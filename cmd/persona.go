package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/warden-plugin/orgdata"
)

func init() {
	register(&command{
		id:        "warden persona export",
		bundle:    "warden.persona.export",
		className: "PersonaExport",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("output", "", messages.Get("flags.output.summary"))
		},
		run: runPersonaExport,
	})
	register(&command{
		id:        "warden persona import",
		bundle:    "warden.persona.import",
		className: "PersonaImport",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("input", "", messages.Get("flags.input.summary"))
			flags.Bool("prune", false, messages.Get("flags.prune.summary"))
			flags.Bool("dry-run", false, sharedMessages.Get("flags.dry-run.summary"))
		},
		run: runPersonaImport,
	})
	register(&command{
		id:        "warden persona diff",
		bundle:    "warden.persona.diff",
		className: "PersonaDiffCommand",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("input", "", messages.Get("flags.input.summary"))
		},
		run: runPersonaDiff,
	})
}

func missingFlag(name string) error {
	return &commandError{name: "Error", message: fmt.Sprintf("The following error occurred:\n  Missing required flag %s\nSee more help with --help", name), exitCode: 2}
}

// personaExportResult is the result of "warden persona export".
type personaExportResult struct {
	Personas int    `json:"personas"`
	Output   string `json:"output"`
}

func runPersonaExport(run *invocation) (any, error) {
	if !run.given("output") {
		return nil, missingFlag("output")
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	output := run.str("output")
	read, err := orgdata.ReadOrgPersonas(conn, true)
	if err != nil {
		return nil, err
	}
	document, err := toJSON(read.Document)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(output, []byte(document+"\n"), 0o644); err != nil {
		return nil, err
	}
	run.log(fmt.Sprintf("Exported %d personas to %s", read.Count, output))
	return personaExportResult{Personas: read.Count, Output: output}, nil
}

func runPersonaImport(run *invocation) (any, error) {
	if !run.given("input") {
		return nil, missingFlag("input")
	}
	if err := run.existingFile("input"); err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	prune := run.boolean("prune")
	plan, err := orgdata.PlanPersonaImportRequest(conn, orgdata.PersonaImportRequestInput{Input: run.str("input"), Prune: prune})
	if err != nil {
		return nil, err
	}
	// A dry run returns the differences and prints nothing without --json,
	// as the sf plugin does.
	if run.boolean("dry-run") {
		return plan.Diff, nil
	}
	result, err := orgdata.ApplyPersonaImportRequest(conn, plan)
	if err != nil {
		return nil, err
	}
	pruneMessage := ""
	if prune {
		pruned := 0
		if result.PrunedComponents != nil {
			pruned = *result.PrunedComponents
		}
		pruneMessage = fmt.Sprintf(", pruned %d components", pruned)
	}
	run.log(fmt.Sprintf("Imported %d personas and %d components%s", result.Personas, result.Components, pruneMessage))
	return result, nil
}

func runPersonaDiff(run *invocation) (any, error) {
	if !run.given("input") {
		return nil, missingFlag("input")
	}
	if err := run.existingFile("input"); err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	file, err := orgdata.ReadPersonaFile(run.str("input"))
	if err != nil {
		return nil, err
	}
	org, err := orgdata.ReadOrgPersonas(conn, true)
	if err != nil {
		return nil, err
	}
	result := orgdata.DiffPersonaDocuments(file, org.Document)
	run.log(orgdata.RenderPersonaDiff(result))
	return result, nil
}
