// Package cmd is the command line of the warden plugin: the flags of each
// "warden" command, its messages, its output in the format of the
// Salesforce CLI and its exit status.  It is a port of the commands of the
// warden sf plugin 0.11.3.
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/force/sfplugin"
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Version is the version of the plugin.  It follows the version of the
// warden sf plugin whose behavior it reproduces.
var Version = "0.11.3"

// invocation is one run of a command.
type invocation struct {
	flags    *pflag.FlagSet
	out      io.Writer
	errOut   io.Writer
	json     bool
	warnings []string
	// exitCode is the status the process ends with when the command
	// returns a result.  A command sets it to 1 when a user failed.
	exitCode int
}

// warn reports a warning: on stderr, or in the envelope with --json.
func (i *invocation) warn(message string) {
	i.warnings = append(i.warnings, message)
	if !i.json {
		fmt.Fprintf(i.errOut, "Warning: %s\n", message)
	}
}

func (i *invocation) log(message string) {
	if !i.json {
		fmt.Fprintln(i.out, message)
	}
}

func (i *invocation) str(name string) string {
	value, _ := i.flags.GetString(name)
	return value
}

func (i *invocation) boolean(name string) bool {
	value, _ := i.flags.GetBool(name)
	return value
}

func (i *invocation) given(name string) bool {
	return i.flags.Changed(name)
}

// command is one command of the plugin.
type command struct {
	// id is the words of the command, such as "warden persona export".
	id string
	// bundle names the message file of the command.
	bundle string
	// className is the name sf reports as the command name in errors.
	className string
	define    func(flags *pflag.FlagSet, messages *Messages)
	run       func(run *invocation) (any, error)
}

var commands []*command

func register(c *command) {
	commands = append(commands, c)
}

// connect returns the connection to the target org.  Tests replace it.
var connect = func() (sfapi.Conn, error) {
	org, ok := sfplugin.OrgFromEnv()
	if !ok {
		return nil, sfError("No default environment found. Use -o or --target-org to specify an environment.")
	}
	return sfapi.NewClient(org.InstanceURL, org.AccessToken, org.APIVersion), nil
}

// orgLabel returns the username of the target org, for provenance.
var orgLabel = func() string {
	org, _ := sfplugin.OrgFromEnv()
	return org.Username
}

// Manifest returns the plugin manifest.
func Manifest() sfplugin.Manifest {
	manifest := sfplugin.Manifest{
		Name:     "warden",
		Version:  Version,
		Protocol: sfplugin.ProtocolVersion,
		Topics: []sfplugin.Topic{
			{ID: "warden", Summary: "User provisioning and lifecycle commands."},
			{ID: "warden persona", Summary: "Commands that move persona definitions between files and an org."},
		},
	}
	sorted := append([]*command{}, commands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })
	for _, c := range sorted {
		manifest.Commands = append(manifest.Commands, sfplugin.Command{ID: c.id, Summary: loadMessages(c.bundle).Get("summary"), Org: sfplugin.OrgRequired})
	}
	return manifest
}

// findCommand returns the command whose id is the longest prefix of the
// arguments, written with spaces or with colons, and the remaining
// arguments.
func findCommand(args []string) (*command, []string) {
	if len(args) > 0 && strings.Contains(args[0], ":") && !strings.HasPrefix(args[0], "-") {
		args = append(strings.Split(args[0], ":"), args[1:]...)
	}
	var best *command
	bestWords := 0
	for _, c := range commands {
		words := strings.Fields(c.id)
		if len(words) > len(args) || len(words) <= bestWords {
			continue
		}
		if strings.Join(args[:len(words)], " ") == c.id {
			best, bestWords = c, len(words)
		}
	}
	if best == nil {
		return nil, args
	}
	return best, args[bestWords:]
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
}

func usage(c *command, flags *pflag.FlagSet) string {
	messages := loadMessages(c.bundle)
	binName := sfplugin.BinName()
	var out strings.Builder
	out.WriteString(messages.Get("summary") + "\n\n")
	out.WriteString("USAGE\n  $ " + binName + " " + c.id + " [flags]\n\n")
	out.WriteString("FLAGS\n" + flags.FlagUsagesWrapped(120) + "\n")
	if messages.Has("description") {
		out.WriteString("DESCRIPTION\n")
		for _, line := range strings.Split(messages.Get("description"), "\n") {
			out.WriteString("  " + line + "\n")
		}
		out.WriteString("\n")
	}
	if examples := messages.List("examples"); len(examples) > 0 {
		out.WriteString("EXAMPLES\n")
		for _, example := range examples {
			example = strings.ReplaceAll(example, "<%= config.bin %>", binName)
			example = strings.ReplaceAll(example, "<%= command.id %>", c.id)
			for _, line := range strings.Split(example, "\n") {
				out.WriteString("  " + strings.TrimRight(line, " ") + "\n")
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

// errorName returns the name sf reports for an error, and its message.
// The library's errors keep the names the commands reported before they
// used the library: "SfError" for the codes below, "Error" otherwise.
func describeError(err error) (name, message string, exitCode int) {
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		return commandErr.name, commandErr.message, commandErr.exitCode
	}
	if warden, ok := core.AsWardenError(err); ok {
		switch warden.Code {
		case "errorInvalidJson", "errorPersonasWithoutDefinition", "errorPromptDeclined", "errorInvalidUserMatchField", "errorInvalidAgainstMatchField", "errorInvalidAgainstValue":
			return "SfError", warden.Message, 1
		}
		return "Error", warden.Message, 1
	}
	return "Error", err.Error(), 1
}

// Run executes the command the arguments name and returns the exit status:
// 0 when the command ran and no user failed, 1 when a user failed or the
// command reported an error, 2 for an unknown or malformed flag.
func Run(args []string, out, errOut io.Writer) int {
	c, rest := findCommand(args)
	jsonOutput := hasJSONFlag(args)
	fail := func(className, name, message string, exitCode int, warnings []string) int {
		if jsonOutput {
			sfplugin.WriteError(out, name, message, exitCode, className, warnings)
		} else {
			label := name
			if label == "" || label == "Error" || label == "SfError" {
				label = fmt.Sprint(exitCode)
			}
			fmt.Fprintf(errOut, "Error (%s): %s\n", label, message)
		}
		return exitCode
	}
	if c == nil {
		words := strings.Join(args, " ")
		if index := strings.Index(words, " -"); index >= 0 {
			words = words[:index]
		}
		return fail("", "Error", fmt.Sprintf("%q is not a command of the warden plugin.", words), 2, nil)
	}
	messages := loadMessages(c.bundle)
	flags := pflag.NewFlagSet(c.id, pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.SortFlags = false
	c.define(flags, messages)
	flags.Bool("json", false, "Format output as json.")
	help := flags.BoolP("help", "h", false, "Show help for the command.")
	if err := flags.Parse(rest); err != nil {
		text := err.Error()
		if name, ok := strings.CutPrefix(text, "unknown flag: "); ok {
			text = "Nonexistent flag: " + name
		} else if name, ok := strings.CutPrefix(text, "unknown shorthand flag: "); ok {
			text = "Nonexistent flag: " + name
		}
		return fail(c.className, "Error", text+"\nSee more help with --help", 2, nil)
	}
	if *help {
		fmt.Fprint(out, usage(c, flags))
		return 0
	}
	if extra := flags.Args(); len(extra) > 0 {
		return fail(c.className, "Error", fmt.Sprintf("Unexpected argument: %s\nSee more help with --help", extra[0]), 2, nil)
	}
	run := &invocation{flags: flags, out: out, errOut: errOut, json: jsonOutput}
	result, err := c.run(run)
	if err != nil {
		name, message, exitCode := describeError(err)
		return fail(c.className, name, message, exitCode, run.warnings)
	}
	if jsonOutput {
		// The envelope of a result has status 0 also when a user failed;
		// the exit status of the process reports the failure.
		sfplugin.WriteResult(out, 0, result, run.warnings)
	}
	return run.exitCode
}

// Flag helpers shared by the commands.

var sharedMessages = loadMessages("warden.shared")

func defineOrgFlags(flags *pflag.FlagSet) {
	flags.StringP("target-org", "o", "", sharedMessages.Get("flags.target-org.summary"))
	flags.String("api-version", "", sharedMessages.Get("flags.api-version.summary"))
}

func defineOutputFlags(flags *pflag.FlagSet) {
	flags.String("output", "human", sharedMessages.Get("flags.output.summary"))
	flags.String("output-file", "", sharedMessages.Get("flags.output-file.summary"))
}

func defineInteractiveFlag(flags *pflag.FlagSet) {
	flags.BoolP("interactive", "i", false, sharedMessages.Get("flags.interactive.summary"))
}

func defineTargetFlags(flags *pflag.FlagSet) {
	flags.String("user", "", sharedMessages.Get("flags.user.summary"))
	flags.String("users-def", "", sharedMessages.Get("flags.users-def.summary"))
	flags.String("external-id", "", sharedMessages.Get("flags.external-id.summary"))
	flags.String("input-format", "", sharedMessages.Get("flags.input-format.summary"))
	flags.String("csv-list-delimiter", "", sharedMessages.Get("flags.csv-list-delimiter.summary"))
}

// outputContextOf reads --output and --output-file and checks them.
func (i *invocation) outputContext() (outputContext, error) {
	output := i.str("output")
	if output != "human" && output != "csv" && output != "json" {
		return outputContext{}, &commandError{name: "Error", message: fmt.Sprintf("Expected --output=%s to be one of: human, csv, json\nSee more help with --help", output), exitCode: 2}
	}
	return resolveOutputContext(output, i.str("output-file"), i.json)
}

// checkOption returns the error sf reports for a flag value outside its
// options.
func (i *invocation) checkOption(name string, options ...string) error {
	if !i.given(name) {
		return nil
	}
	value := i.str(name)
	for _, option := range options {
		if value == option {
			return nil
		}
	}
	return &commandError{name: "Error", message: fmt.Sprintf("Expected --%s=%s to be one of: %s\nSee more help with --help", name, value, strings.Join(options, ", ")), exitCode: 2}
}

// existingFile returns the error sf reports for a file flag whose file
// does not exist.
func (i *invocation) existingFile(name string) error {
	if !i.given(name) {
		return nil
	}
	path := i.str(name)
	info, err := os.Stat(path)
	if err != nil {
		return sfErrorf("No file found at %s", path)
	}
	if info.IsDir() {
		return sfErrorf("%s exists but is not a file", path)
	}
	return nil
}

// requireExactlyOne is the check of two flags of which one must be given.
func (i *invocation) requireExactlyOne(first, second string) error {
	if i.given(first) && i.given(second) {
		return sfErrorf("--%s cannot also be provided when using --%s", second, first)
	}
	if !i.given(first) && !i.given(second) {
		return sfErrorf("Exactly one of the following must be provided: --%s, --%s", first, second)
	}
	return nil
}

// requireDependencies is the check of a flag that needs other flags.
func (i *invocation) requireDependencies(name string, dependencies ...string) error {
	if !i.given(name) {
		return nil
	}
	for _, dependency := range dependencies {
		if !i.given(dependency) {
			names := make([]string, len(dependencies))
			for index, each := range dependencies {
				names[index] = "--" + each
			}
			return sfErrorf("All of the following must be provided when using --%s: %s", name, strings.Join(names, ", "))
		}
	}
	return nil
}

// interactiveGuard refuses --interactive.  The prompts of the sf plugin
// are not ported yet.
func (i *invocation) interactiveGuard() error {
	if !i.boolean("interactive") {
		return nil
	}
	if i.json {
		return sfError(sharedMessages.Get("errorInteractiveGuard"))
	}
	return sfError("Interactive mode is not available in this port of warden. Pass the values as flags.")
}
