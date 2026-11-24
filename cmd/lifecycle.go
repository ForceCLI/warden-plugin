package cmd

import (
	"regexp"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/lifecycle"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

func init() {
	for _, direction := range []string{"freeze", "unfreeze"} {
		direction := direction
		className := "UserFreeze"
		if direction == "unfreeze" {
			className = "UserUnfreeze"
		}
		register(&command{
			id:        "warden " + direction,
			bundle:    "warden." + direction,
			className: className,
			define: func(flags *pflag.FlagSet, messages *Messages) {
				defineOrgFlags(flags)
				defineTargetFlags(flags)
				defineOutputFlags(flags)
				flags.Bool("no-prompt", false, sharedMessages.Get("flags.no-prompt.summary"))
				flags.Bool("dry-run", false, sharedMessages.Get("flags.dry-run.summary"))
				defineInteractiveFlag(flags)
			},
			run: func(run *invocation) (any, error) { return runFreeze(run, direction) },
		})
	}
	register(&command{
		id:        "warden snapshot",
		bundle:    "warden.snapshot",
		className: "UserSnapshot",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			defineTargetFlags(flags)
			flags.String("out", "", messages.Get("flags.out.summary"))
			defineOutputFlags(flags)
			defineInteractiveFlag(flags)
		},
		run: runSnapshot,
	})
	register(&command{
		id:        "warden restore",
		bundle:    "warden.restore",
		className: "UserRestore",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("snapshot", "", messages.Get("flags.snapshot.summary"))
			defineOutputFlags(flags)
			flags.Bool("no-prompt", false, sharedMessages.Get("flags.no-prompt.summary"))
			flags.Bool("dry-run", false, sharedMessages.Get("flags.dry-run.summary"))
			defineInteractiveFlag(flags)
		},
		run: runRestore,
	})
	register(&command{
		id:        "warden diff",
		bundle:    "warden.diff",
		className: "UserDiff",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("user", "", messages.Get("flags.user.summary"))
			flags.String("against", "", messages.Get("flags.against.summary"))
			flags.String("users-def", "", sharedMessages.Get("flags.users-def.summary"))
			flags.String("personas-def", "", messages.Get("flags.personas-def.summary"))
			flags.String("external-id", "", sharedMessages.Get("flags.external-id.summary"))
			flags.String("input-format", "", sharedMessages.Get("flags.input-format.summary"))
			flags.String("csv-list-delimiter", "", sharedMessages.Get("flags.csv-list-delimiter.summary"))
			defineOutputFlags(flags)
			flags.Bool("verbose", false, messages.Get("flags.verbose.summary"))
			flags.Bool("fail-on-drift", false, messages.Get("flags.fail-on-drift.summary"))
			flags.Bool("verify", false, messages.Get("flags.verify.summary"))
			defineInteractiveFlag(flags)
		},
		run: runDiff,
	})
}

// targetChecks are the checks every command with the target flags makes
// before it contacts the org.
func targetChecks(run *invocation) error {
	for _, check := range []error{run.checkOption("input-format", "json", "csv"), run.existingFile("users-def")} {
		if check != nil {
			return check
		}
	}
	return nil
}

func targetOptions(run *invocation) lifecycle.TargetOptions {
	return lifecycle.TargetOptions{
		User:             run.str("user"),
		UsersPath:        run.str("users-def"),
		ExternalID:       run.str("external-id"),
		InputFormat:      run.str("input-format"),
		CsvListDelimiter: run.str("csv-list-delimiter"),
	}
}

// validateUserTarget reports a --user value whose field is not a User
// field, with the text of the command.
func validateUserTarget(conn sfapi.Conn, user string, messages *Messages) error {
	if user == "" {
		return nil
	}
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return err
	}
	field, _, err := core.ParseUserFlag(user)
	if err != nil {
		return err
	}
	if _, ok := core.ResolveTargetField(field, fieldMap); !ok {
		return core.NewLifecycleError("errorInvalidUserMatchField", messages.Get("errorInvalidUserMatchField", field), map[string]any{"field": field})
	}
	return nil
}

// finishLifecycle prints a lifecycle result and sets the exit status to 1
// when a user failed.
func finishLifecycle(run *invocation, context outputContext, output *lifecycle.Result, csv string, messages *Messages) (any, error) {
	if err := emitResult(run.out, context, output, csv, lifecycle.RenderLifecycleResult(output, messages.Get)); err != nil {
		return nil, err
	}
	if output.Summary.Failed > 0 {
		run.exitCode = 1
	}
	return output, nil
}

func runFreeze(run *invocation, direction string) (any, error) {
	messages := loadMessages("warden." + direction)
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	if err := targetChecks(run); err != nil {
		return nil, err
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	if err := run.requireExactlyOne("user", "users-def"); err != nil {
		return nil, err
	}
	if err := validateUserTarget(conn, run.str("user"), messages); err != nil {
		return nil, err
	}
	plan, apply := lifecycle.PlanFreeze, lifecycle.ApplyFreeze
	if direction == "unfreeze" {
		plan, apply = lifecycle.PlanUnfreeze, lifecycle.ApplyUnfreeze
	}
	planned, err := plan(conn, targetOptions(run))
	if err != nil {
		return nil, err
	}
	dryRun := run.boolean("dry-run")
	if err := confirmLifecycleWrites(run, !dryRun && len(planned.Updates) > 0 && !run.boolean("no-prompt") && context.interactive, messages); err != nil {
		return nil, err
	}
	output := &lifecycle.Result{Summary: lifecycle.SummarizeLifecycle(planned.Users), Users: planned.Users}
	if !dryRun {
		if output, err = apply(conn, planned); err != nil {
			return nil, err
		}
	}
	return finishLifecycle(run, context, output, lifecycle.RenderLifecycleCsv(output), messages)
}

// now is the clock of the commands.  Tests replace it.
var now = time.Now

func defaultSnapshotOut() string {
	stamp := now().UTC().Format("2006-01-02T15:04:05.000Z")
	return "user-snapshot-" + strings.NewReplacer(":", "-", ".", "-").Replace(stamp) + ".json"
}

func runSnapshot(run *invocation) (any, error) {
	messages := loadMessages("warden.snapshot")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	if err := targetChecks(run); err != nil {
		return nil, err
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	out := run.str("out")
	if out == "" {
		out = defaultSnapshotOut()
	}
	if err := run.requireExactlyOne("user", "users-def"); err != nil {
		return nil, err
	}
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	requests, requestErrors, err := core.BuildTargetRequests(core.TargetFlags{
		User: run.str("user"), UsersDef: run.str("users-def"), ExternalID: run.str("external-id"),
		InputFormat: run.str("input-format"), CsvListDelimiter: run.str("csv-list-delimiter"),
	}, fieldMap, core.TargetMessages{
		InvalidUserMatchField: func(field string) string { return messages.Get("errorInvalidUserMatchField", field) },
		InvalidJSON:           func(path, detail string) string { return messages.Get("errorInvalidJson", path, detail) },
	})
	if err != nil {
		return nil, err
	}
	targets, resolutionErrors, err := core.ResolveTargets(conn, requests, fieldMap)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(targets))
	users := []any{}
	for index, target := range targets {
		ids[index] = target.ID
		entry := core.NewObject()
		entry.Set("match", target.Field)
		entry.Set(target.Field, target.Value)
		users = append(users, entry)
	}
	state, err := provisioning.LoadAssignmentState(conn, ids, nil)
	if err != nil {
		return nil, err
	}
	usersDoc := core.NewObject()
	usersDoc.Set("users", users)
	snapshot, err := lifecycle.RunSnapshot(conn, lifecycle.SnapshotOptions{TargetOptions: lifecycle.TargetOptions{UsersDoc: usersDoc}, Org: orgLabel()})
	if err != nil {
		return nil, err
	}
	if err := lifecycle.WriteSnapshotFile(out, snapshot); err != nil {
		return nil, err
	}
	var results []*lifecycle.UserResult
	for _, targetError := range append(requestErrors, resolutionErrors...) {
		results = append(results, lifecycle.FailedResult(targetError))
	}
	for _, target := range targets {
		var login sfapi.Record
		if rows := state.UserLoginByUserID[target.ID]; len(rows) > 0 {
			login = rows[0]
		}
		result := lifecycle.ResolvedTargetResult(target, login)
		result.Actions = []lifecycle.Notice{lifecycle.MakeNotice("snapshotWritten", nil, nil, nil)}
		results = append(results, result)
	}
	if results == nil {
		results = []*lifecycle.UserResult{}
	}
	output := &lifecycle.Result{Summary: lifecycle.SummarizeLifecycle(results), Users: results}
	return finishLifecycle(run, context, output, lifecycle.RenderSnapshotCsv(output), messages)
}

var missingReference = regexp.MustCompile(`^(\w+) reference "(.*)" was not found\.$`)

func runRestore(run *invocation) (any, error) {
	messages := loadMessages("warden.restore")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	if !run.given("snapshot") {
		return nil, sfError("Missing required flag snapshot")
	}
	snapshotPath := run.str("snapshot")
	snapshot, err := lifecycle.ReadSnapshotFile(snapshotPath)
	if err != nil {
		return nil, sfError(messages.Get("errorInvalidJson", snapshotPath, err.Error()))
	}
	plan, err := lifecycle.PlanRestore(conn, lifecycle.RestoreOptions{SnapshotDoc: snapshot.Clone()})
	if err != nil {
		return nil, err
	}
	// A missing reference is reported with the text of this command.
	for _, user := range plan.Users {
		warnings := make([]string, len(user.Result.Warnings))
		for index, warning := range user.Result.Warnings {
			warnings[index] = warning
			if match := missingReference.FindStringSubmatch(warning); match != nil {
				warnings[index] = messages.Get("warningReferenceMissing", match[1], match[2])
			}
		}
		user.Result.Warnings = warnings
		user.Preview.Warnings = warnings
	}
	dryRun := run.boolean("dry-run")
	hasPlanned := false
	for _, user := range plan.Preview.Users {
		hasPlanned = hasPlanned || user.Status == "planned"
	}
	if err := confirmLifecycleWrites(run, !dryRun && hasPlanned && !run.boolean("no-prompt") && context.interactive, messages); err != nil {
		return nil, err
	}
	output := plan.Preview
	if !dryRun {
		if output, err = lifecycle.ApplyRestore(conn, plan); err != nil {
			return nil, err
		}
	}
	return finishLifecycle(run, context, output, lifecycle.RenderRestoreCsv(output), messages)
}

func runDiff(run *invocation) (any, error) {
	messages := loadMessages("warden.diff")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	for _, check := range []error{run.checkOption("input-format", "json", "csv"), run.existingFile("users-def"), run.existingFile("personas-def")} {
		if check != nil {
			return nil, check
		}
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	verbose := run.boolean("verbose")
	if output := run.str("output"); verbose && (output == "csv" || output == "json" || run.json) {
		return nil, sfError(messages.Get("errorVerboseNonHuman"))
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	user, against := run.str("user"), run.str("against")
	usersDef, personasDef, externalID := run.str("users-def"), run.str("personas-def"), run.str("external-id")
	verify := run.boolean("verify")
	if err := run.requireExactlyOne("user", "users-def"); err != nil {
		return nil, err
	}
	switch {
	case run.given("user") && externalID != "":
		return nil, sfError(messages.Get("errorExternalIdUserMode"))
	case run.given("user") && personasDef != "":
		return nil, sfError(messages.Get("errorPersonasUserMode"))
	case verify && (run.given("user") || run.given("against")):
		return nil, sfError(messages.Get("errorVerifyUserMode"))
	}
	if err := run.requireDependencies("user", "against"); err != nil {
		return nil, err
	}
	if err := run.requireDependencies("against", "user"); err != nil {
		return nil, err
	}

	options := lifecycle.DiffOptions{Mode: "persona", Verify: verify, CsvListDelimiter: run.str("csv-list-delimiter"), ExternalID: externalID}
	if run.given("user") {
		options = lifecycle.DiffOptions{Mode: "user", User: user, Against: against}
	} else if inputFormat := core.DetectInputFormat(usersDef, run.str("input-format")); inputFormat == "json" {
		definitions, err := core.ResolveDefinitions(core.DefinitionSource{UsersPath: usersDef, PersonasPath: personasDef}, nil, core.DefinitionMessages{
			InvalidJSON:               func(path, detail string) string { return messages.Get("errorInvalidJson", path, detail) },
			PersonasWithoutDefinition: core.DefaultDefinitionMessages().PersonasWithoutDefinition,
		})
		if err != nil {
			return nil, personasSchemaError(err, personasDef, messages.Get("errorInvalidPersonaDefinition"))
		}
		options.UsersDoc = definitions.UsersDoc
		if definitions.PersonasSupplied {
			options.PersonasDoc = definitions.PersonasDoc
		}
	} else {
		options.UsersPath, options.PersonasPath, options.InputFormat = usersDef, personasDef, "csv"
	}
	result, err := lifecycle.RunDiff(conn, options)
	if err != nil {
		// In user mode an invalid --user or --against value is reported
		// under the name SfError.
		if warden, ok := core.AsWardenError(err); ok && options.Mode == "user" && warden.Code == "errorInvalidUserValue" {
			return nil, sfError(warden.Message)
		}
		return nil, err
	}
	if result.Result == nil {
		verdicts := result.Verdicts
		if verdicts == nil {
			verdicts = []lifecycle.UserConformanceVerdict{}
		}
		if err := emitResult(run.out, context, verdicts, lifecycle.RenderUserConformanceCsv(verdicts), lifecycle.RenderUserConformanceHuman(verdicts, messages.Get)); err != nil {
			return nil, err
		}
		for _, verdict := range verdicts {
			if !verdict.Conformant {
				run.exitCode = 1
			}
		}
		return verdicts, nil
	}
	comparison := result.Result
	if err := emitResult(run.out, context, comparison, lifecycle.RenderUserDiffCsv(comparison), lifecycle.RenderUserDiffHuman(comparison, messages.Get, verbose)); err != nil {
		return nil, err
	}
	if comparison.Summary.Failed > 0 || (run.boolean("fail-on-drift") && comparison.Summary.Changed > 0) {
		run.exitCode = 1
	}
	return comparison, nil
}
