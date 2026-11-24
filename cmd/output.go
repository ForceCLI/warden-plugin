package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// commandError is an error printed in the format of sf.  Name is "SfError"
// or "Error", as the warden plugin reports it with --json.
type commandError struct {
	name     string
	message  string
	exitCode int
}

func (e *commandError) Error() string {
	return e.message
}

func sfError(message string) error {
	return &commandError{name: "SfError", message: message, exitCode: 1}
}

func sfErrorf(format string, args ...any) error {
	return sfError(fmt.Sprintf(format, args...))
}

// outputContext is how a command prints its result.
type outputContext struct {
	// format is "human", "csv" or "json".
	format     string
	outputFile string
	jsonOutput bool
	// interactive is true when stdout is read by a person, so prompts and
	// warnings fit.
	interactive bool
}

func resolveOutputFormat(value string) string {
	if value == "csv" || value == "json" {
		return value
	}
	return "human"
}

// resolveOutputContext checks that --output and --json do not both write
// to stdout.
func resolveOutputContext(output, outputFile string, jsonOutput bool) (outputContext, error) {
	format := resolveOutputFormat(output)
	if jsonOutput && format != "human" && outputFile == "" {
		return outputContext{}, sfError(loadMessages("warden.shared").Get("errorOutputJsonConflict"))
	}
	return outputContext{format: format, outputFile: outputFile, jsonOutput: jsonOutput, interactive: !jsonOutput}, nil
}

// toJSON writes a value as JSON indented by two spaces, without escaping
// <, > and &.
func toJSON(value any) (string, error) {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// emitResult writes the result to the output file in the chosen format and
// prints it.  With --json nothing is printed here: the caller prints the
// envelope.  With an output file in csv or json format, the human form is
// printed.
func emitResult(out io.Writer, context outputContext, result any, csv, human string) error {
	resultJSON, err := toJSON(result)
	if err != nil {
		return err
	}
	payload, hasPayload := "", false
	switch {
	case context.format == "csv":
		payload, hasPayload = csv, true
	case context.format == "json":
		payload, hasPayload = resultJSON, true
	case context.jsonOutput:
		envelope, err := toJSON(struct {
			Status   int      `json:"status"`
			Result   any      `json:"result"`
			Warnings []string `json:"warnings"`
		}{Result: result, Warnings: []string{}})
		if err != nil {
			return err
		}
		payload, hasPayload = envelope, true
	}
	if context.outputFile != "" && hasPayload {
		if err := os.WriteFile(context.outputFile, []byte(payload+"\n"), 0o644); err != nil {
			return err
		}
	}
	if context.jsonOutput {
		return nil
	}
	switch {
	case context.outputFile != "" && context.format != "human":
		fmt.Fprintln(out, human)
	case context.format == "csv":
		fmt.Fprintln(out, csv)
	case context.format == "json":
		fmt.Fprintln(out, resultJSON)
	default:
		fmt.Fprintln(out, human)
	}
	return nil
}
