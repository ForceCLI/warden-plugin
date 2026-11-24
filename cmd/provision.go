package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/orgdata"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

func init() {
	register(&command{
		id:        "warden provision",
		bundle:    "warden.provision",
		className: "UserProvision",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("users-def", "", messages.Get("flags.users-def.summary"))
			flags.String("personas-def", "", messages.Get("flags.personas-def.summary"))
			flags.String("persona-source", "", messages.Get("flags.persona-source.summary"))
			flags.String("related-def", "", messages.Get("flags.related-def.summary"))
			flags.String("external-id", "", messages.Get("flags.external-id.summary"))
			flags.String("match-field", "", "")
			flags.MarkHidden("match-field")
			flags.String("input-format", "", sharedMessages.Get("flags.input-format.summary"))
			flags.String("csv-list-delimiter", "", sharedMessages.Get("flags.csv-list-delimiter.summary"))
			flags.Bool("fuzzy-username", false, messages.Get("flags.fuzzy-username.summary"))
			flags.Bool("no-prompt", false, messages.Get("flags.no-prompt.summary"))
			flags.Bool("dry-run", false, sharedMessages.Get("flags.dry-run.summary"))
			flags.Bool("cleanup-on-failure", false, messages.Get("flags.cleanup-on-failure.summary"))
			flags.Bool("fail-on-insufficient-license", false, messages.Get("flags.fail-on-insufficient-license.summary"))
			flags.Bool("log-to-org", false, messages.Get("flags.log-to-org.summary"))
			flags.Bool("no-log-to-org", false, "")
			flags.MarkHidden("no-log-to-org")
			flags.String("log-detail", "summary", messages.Get("flags.log-detail.summary"))
			defineOutputFlags(flags)
			defineInteractiveFlag(flags)
		},
		run: runProvision,
	})
}

// provisionOutput is the result of "warden provision": the provisioning
// result and where the personas came from.
type provisionOutput struct {
	Summary               provisioning.Summary                   `json:"summary"`
	Users                 []provisioning.PublicUserResult        `json:"users"`
	Licenses              []provisioning.LicenseUsage            `json:"licenses,omitzero"`
	PermissionSetLicenses *provisioning.PermissionSetLicenseNote `json:"permissionSetLicenses,omitempty"`
	PersonaSource         string                                 `json:"personaSource"`
}

// effectiveInputFormat is the format of the users definition: the
// --input-format value, or the one its extension names, or json.
func effectiveInputFormat(run *invocation) string {
	override := ""
	if format := run.str("input-format"); format == "json" || format == "csv" {
		override = format
	}
	return core.DetectInputFormat(run.str("users-def"), override)
}

// personasSchemaError replaces the schema error of a personas file without
// a "personas" key by the text of the command.
func personasSchemaError(err error, personasPath, text string) error {
	warden, ok := core.AsWardenError(err)
	if !ok || warden.Code != "schema-invalid" || personasPath == "" {
		return err
	}
	issues, _ := warden.Data["issues"].([]any)
	for _, item := range issues {
		issue, _ := item.(map[string]any)
		if path, _ := issue["path"].([]any); len(path) == 1 && path[0] == "personas" {
			return sfError(text)
		}
	}
	return err
}

// applyProvision applies the plan and adds what the sf plugin reports on
// top of the library result: the errors of failed related records as
// errors of the user, the Id fields of the target after a cross-reference
// error, and a summary whose warnings are those of the plan.
func applyProvision(conn sfapi.Conn, plan *provisioning.PlanResult, messages *Messages) (*provisioning.Result, error) {
	output, err := provisioning.Apply(conn, plan)
	if err != nil {
		return nil, err
	}
	type ordered struct {
		order  int
		target *core.Object
	}
	var entries []ordered
	for _, userPlan := range plan.Plans {
		entries = append(entries, ordered{order: userPlan.Order, target: userPlan.Target})
	}
	for _, validation := range plan.ValidationResults {
		entries = append(entries, ordered{order: validation.Order})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	summary := provisioning.Summary{Total: len(output.Users), Warnings: len(plan.Warnings)}
	for index := range output.Users {
		user := &output.Users[index]
		var relatedErrors []string
		for _, related := range user.RelatedRecords {
			if related.Status != "failed" || related.Error == "" {
				continue
			}
			known := false
			for _, existing := range user.Errors {
				known = known || existing == related.Error
			}
			if !known {
				relatedErrors = append(relatedErrors, related.Error)
			}
		}
		for _, related := range user.RelatedRecords {
			if related.Status == "failed" && related.Error != "" {
				user.Errors = append(relatedErrors, user.Errors...)
				user.Status = "failed"
				break
			}
		}
		if index < len(entries) && entries[index].target != nil && user.ID == "" {
			hasCrossReference := false
			for _, text := range user.Errors {
				hasCrossReference = hasCrossReference || strings.Contains(text, "INVALID_CROSS_REFERENCE_KEY")
			}
			if hasCrossReference {
				var candidates []string
				target := entries[index].target
				for _, field := range target.Keys {
					if strings.HasSuffix(field, "Id") && field != "Id" {
						candidates = append(candidates, field+"="+core.JSString(target.Values[field]))
					}
				}
				if len(candidates) > 0 {
					user.Errors = append(user.Errors, messages.Get("errorCrossReferenceCandidates", strings.Join(candidates, ", ")))
				}
			}
		}
		switch user.Status {
		case "created":
			summary.Created++
		case "updated":
			summary.Updated++
		case "failed":
			summary.Failed++
		}
	}
	output.Summary = summary
	return output, nil
}

func toLedgerResult(output *provisioning.Result) orgdata.ProvisionResult {
	var result orgdata.ProvisionResult
	for _, user := range output.Users {
		converted := orgdata.ProvisionUser{ID: user.ID, Key: user.Key, Personas: user.Personas, Status: user.Status, Actions: user.Actions, Errors: user.Errors}
		for _, related := range user.RelatedRecords {
			converted.RelatedRecords = append(converted.RelatedRecords, orgdata.RelatedRecord{Relationship: related.Relationship, Phase: related.Phase, Sobject: related.Sobject, Action: related.Action, Error: related.Error})
		}
		result.Users = append(result.Users, converted)
	}
	return result
}

func runProvision(run *invocation) (any, error) {
	messages := loadMessages("warden.provision")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	for _, check := range []error{
		run.checkOption("persona-source", "file", "org"),
		run.checkOption("input-format", "json", "csv"),
		run.checkOption("log-detail", "summary", "full"),
		run.existingFile("users-def"),
		run.existingFile("personas-def"),
		run.existingFile("related-def"),
	} {
		if check != nil {
			return nil, check
		}
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	if !run.given("users-def") {
		return nil, sfError("Missing required flag users-def")
	}
	usersPath, personasPath, relatedPath := run.str("users-def"), run.str("personas-def"), run.str("related-def")
	externalID := run.str("external-id")
	if externalID == "" {
		externalID = run.str("match-field")
	}
	inputFormat := effectiveInputFormat(run)
	if relatedPath != "" && inputFormat == "csv" {
		return nil, sfError(messages.Get("errorRelatedRequiresJson"))
	}
	dryRun := run.boolean("dry-run")
	logToOrg := run.boolean("log-to-org") && !run.boolean("no-log-to-org")
	logDetail := run.str("log-detail")

	var connected orgdata.ConnectedPackage
	var captured *orgdata.CapturedWrites
	provisioningConn := conn
	if !dryRun {
		connected = orgdata.DetectConnectedPackage(conn)
		if connected.LedgerAvailable {
			writes := orgdata.CaptureProvisioningWrites(conn, nil)
			captured = &writes
			provisioningConn = writes.Conn
		}
	}
	if dryRun && run.boolean("cleanup-on-failure") {
		run.warn(messages.Get("warning.cleanupIgnoredInDryRun"))
	}

	personaSource := run.str("persona-source")
	if personaSource == "" {
		personaSource = "file"
		if personasPath == "" && orgdata.PersonaPackageDetected(provisioningConn) {
			personaSource = "org"
		}
	}
	var orgPersonas *orgdata.OrgPersonaRead
	if personaSource == "org" {
		if orgPersonas, err = orgdata.ReadOrgPersonas(provisioningConn, true); err != nil {
			return nil, err
		}
	}
	source := core.DefinitionSource{UsersPath: usersPath, RelatedPath: relatedPath}
	if personaSource == "file" {
		source.PersonasPath = personasPath
	}
	var fieldMap *core.FieldMap
	if inputFormat == "csv" {
		source.InputFormat = "csv"
		source.CsvListDelimiter = run.str("csv-list-delimiter")
		if fieldMap, err = core.DescribeUserFields(provisioningConn, nil); err != nil {
			return nil, err
		}
	}
	definitionMessages := core.DefinitionMessages{
		InvalidJSON:               func(path, detail string) string { return messages.Get("errorInvalidJson", path, detail) },
		PersonasWithoutDefinition: func(identity string) string { return messages.Get("errorPersonasWithoutDefinition", identity) },
	}
	definitions, err := core.ResolveDefinitions(source, fieldMap, definitionMessages)
	if err != nil {
		return nil, personasSchemaError(err, personasPath, messages.Get("errorInvalidPersonaDefinition"))
	}
	if personaSource == "file" && !definitions.PersonasSupplied {
		if _, err := core.ParseUsersDefinition(definitions.UsersDoc); err != nil {
			return nil, err
		}
		users, _ := definitions.UsersDoc.List("users")
		if identity, found := core.FirstUserWithPersonas(users); found {
			return nil, core.NewDefinitionError("errorPersonasWithoutDefinition", definitionMessages.PersonasWithoutDefinition(identity), nil)
		}
	}
	personasDoc := definitions.PersonasDoc
	personasSupplied := definitions.PersonasSupplied
	if orgPersonas != nil {
		personasDoc = orgPersonas.Document
		personasSupplied = true
	}
	plan, err := provisioning.Plan(provisioningConn, provisioning.Options{
		UsersDoc:         definitions.UsersDoc,
		PersonasDoc:      personasDoc,
		PersonasSupplied: &personasSupplied,
		RelatedDoc:       definitions.RelatedDoc,
		CsvListDelimiter: run.str("csv-list-delimiter"),
		ExternalID:       externalID,
		FuzzyUsername:    run.boolean("fuzzy-username"),
		CleanupOnFailure: run.boolean("cleanup-on-failure"),
	})
	if err != nil {
		return nil, err
	}
	personaSourceLabel := ""
	if personaSource == "org" {
		personaSourceLabel = fmt.Sprintf("org (%s, %d personas)", orgdata.PersonaObject, orgPersonas.Count)
	} else {
		personaSourceLabel = "file (no --personas-def)"
		if personasPath != "" {
			personaSourceLabel = "file (" + personasPath + ")"
		}
		if !run.given("persona-source") {
			personaSourceLabel += " [default]"
		}
	}

	if len(plan.Warnings) > 0 && context.interactive {
		for _, warning := range plan.Warnings {
			run.warn(warning)
		}
		if !run.boolean("no-prompt") && !confirm(run.errOut, messages.Get("promptWarningsContinue")) {
			run.warn(messages.Get("warningPromptTimeout"))
			return nil, sfError(messages.Get("errorPromptDeclined"))
		}
	}

	runID := ""
	if logToOrg && connected.RunAvailable {
		definitionPath := personasPath
		if definitionPath == "" {
			definitionPath = usersPath
		}
		provenance := orgdata.BuildProvenance(orgdata.ProvenanceOptions{DefinitionPath: definitionPath, CLIVersion: Version})
		mode := "Apply"
		if dryRun {
			mode = "Preview"
		}
		scope := usersPath
		if personasPath != "" {
			scope += ", " + personasPath
		}
		fields := sfapi.Record{
			"wdn_Mode__c":            mode,
			"wdn_Scope__c":           scope,
			"wdn_Planned_Count__c":   0,
			"wdn_Applied_Count__c":   0,
			"wdn_Error_Count__c":     0,
			"wdn_Unmanaged_Count__c": 0,
			"wdn_Source_System__c":   provenance.SourceSystem,
		}
		for field, value := range map[string]string{
			"wdn_Cli_Version__c":     provenance.CLIVersion,
			"wdn_Invoking_User__c":   provenance.InvokingUser,
			"wdn_Ci_Job__c":          provenance.CIJob,
			"wdn_Definition_Path__c": provenance.DefinitionPath,
			"wdn_Definition_Sha__c":  provenance.DefinitionSha,
		} {
			if value != "" {
				fields[field] = value
			}
		}
		runID = orgdata.SaveRun(provisioningConn, fields, run.warn)
	}

	output := plan.Preview
	if !dryRun {
		if output, err = applyProvision(provisioningConn, plan, messages); err != nil {
			return nil, err
		}
	}

	// The records of the warden-base package are written after the users;
	// a failure there is a warning.
	ledgerResult := toLedgerResult(output)
	if !dryRun && connected.LedgerAvailable && captured != nil {
		if err := orgdata.WriteProvisionLedger(provisioningConn, orgdata.ProvisionLedgerOptions{State: captured.State, Output: ledgerResult, PersonasDoc: personasDoc, RunID: runID, Warn: run.warn}); err != nil {
			run.warn("Warden org audit write failed: " + err.Error())
		}
	}
	if runID != "" {
		var items []orgdata.ReconciliationItem
		if logDetail == "full" {
			items = orgdata.BuildReconciliationItems(ledgerResult, runID)
		}
		if len(items) > 0 {
			orgdata.BestEffortOrgWrite(func() ([]sfapi.SaveResult, error) {
				return orgdata.WriteBatch(provisioningConn, orgdata.WardenItemObject, orgdata.ItemRecords(items), run.warn, "item"), nil
			}, run.warn, "item")
		}
		planned := output.Summary.Total
		if logDetail == "full" {
			planned = len(items)
		}
		applied := 0
		for _, user := range output.Users {
			if user.Status == "created" || user.Status == "updated" {
				applied++
			}
		}
		orgdata.UpdateRun(provisioningConn, runID, sfapi.Record{
			"wdn_Planned_Count__c":   planned,
			"wdn_Applied_Count__c":   applied,
			"wdn_Error_Count__c":     output.Summary.Failed,
			"wdn_Unmanaged_Count__c": 0,
		}, run.warn)
	}

	result := provisionOutput{Summary: output.Summary, Users: output.Users, Licenses: output.Licenses, PermissionSetLicenses: output.PermissionSetLicenses, PersonaSource: personaSourceLabel}
	csv := provisioning.RenderCsv(output)
	if !context.jsonOutput {
		for _, user := range output.Users {
			if len(user.Errors) > 0 {
				run.warn(messages.Get("warningUserFailed", user.Key, strings.Join(user.Errors, "; ")))
			}
		}
		for _, license := range output.Licenses {
			if license.Shortfall > 0 {
				run.warn(messages.Get("warningInsufficientLicense", license.LicenseName, fmt.Sprint(license.Shortfall)))
			}
		}
	}
	human := provisioning.RenderHuman(output, personaSourceLabel, messages.Get)
	if err := emitResult(run.out, context, result, csv, human); err != nil {
		return nil, err
	}
	if output.Summary.Failed > 0 {
		run.exitCode = 1
	}
	if run.boolean("fail-on-insufficient-license") {
		var shortfalls []string
		for _, license := range output.Licenses {
			if license.Shortfall > 0 {
				shortfalls = append(shortfalls, fmt.Sprintf("%s (%d)", license.LicenseName, license.Shortfall))
			}
		}
		if len(shortfalls) > 0 {
			return nil, sfError(messages.Get("errorInsufficientLicense", strings.Join(shortfalls, ", ")))
		}
	}
	return result, nil
}
