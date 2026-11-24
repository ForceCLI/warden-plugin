package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/lifecycle"
	"github.com/ForceCLI/warden-plugin/orgdata"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

func init() {
	register(&command{
		id:        "warden strip",
		bundle:    "warden.strip",
		className: "UserStrip",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			defineTargetFlags(flags)
			flags.Bool("no-prompt", false, sharedMessages.Get("flags.no-prompt.summary"))
			flags.Bool("dry-run", false, sharedMessages.Get("flags.dry-run.summary"))
			flags.StringArray("persona", nil, messages.Get("flags.persona.summary"))
			flags.Bool("all-personas", false, messages.Get("flags.all-personas.summary"))
			flags.String("personas-def", "", messages.Get("flags.personas-def.summary"))
			flags.String("persona-source", "", messages.Get("flags.persona-source.summary"))
			for _, name := range []string{"freeze", "deactivate", "no-freeze", "no-deactivate", "keep-permsets", "keep-permset-groups", "keep-licenses", "keep-public-groups", "keep-queues"} {
				flags.Bool(name, false, messages.Get("flags."+name+".summary"))
			}
			flags.String("snapshot", "", messages.Get("flags.snapshot.summary"))
			defineOutputFlags(flags)
			defineInteractiveFlag(flags)
		},
		run: runStrip,
	})
}

// plannedStrip is a strip plan with what the bookkeeping after the strip
// needs.
type plannedStrip struct {
	plan             *lifecycle.StripPlan
	personaMode      bool
	heldAssignments  []orgdata.HeldPersonaAssignment
	ledgerProvenance []orgdata.LedgerProvenance
	// removedPersonasByUser maps a user Id to the personas the strip
	// removes from the user.
	removedPersonasByUser map[string][]string
}

// heldPersonasForTarget returns the personas the users definition lists
// for the entry that matches the target.
func heldPersonasForTarget(usersDoc *core.Object, target core.Target) []string {
	users, _ := usersDoc.List("users")
	for _, item := range users {
		user, ok := item.(*core.Object)
		if !ok {
			continue
		}
		match := target.Field
		if text, ok := user.String("match"); ok && text != "" {
			match = text
		}
		if value, _ := user.GetFold(match); value == any(target.Value) {
			return user.Strings("personas")
		}
	}
	return nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func planStrip(run *invocation, conn sfapi.Conn, messages *Messages) (*plannedStrip, error) {
	personas, _ := run.flags.GetStringArray("persona")
	allPersonas := run.boolean("all-personas")
	personaSource := run.str("persona-source")
	if personaSource == "" {
		personaSource = "file"
	}
	personaMode := len(personas) > 0 || allPersonas
	if !personaMode || personaSource != "file" {
		if err := run.requireExactlyOne("user", "users-def"); err != nil {
			return nil, err
		}
	}
	if err := validateUserTarget(conn, run.str("user"), messages); err != nil {
		return nil, err
	}
	base := lifecycle.StripOptions{
		TargetOptions:     targetOptions(run),
		NoFreeze:          run.boolean("no-freeze"),
		NoDeactivate:      run.boolean("no-deactivate"),
		KeepPermsets:      run.boolean("keep-permsets"),
		KeepPermsetGroups: run.boolean("keep-permset-groups"),
		KeepPublicGroups:  run.boolean("keep-public-groups"),
		KeepQueues:        run.boolean("keep-queues"),
		KeepLicenses:      run.boolean("keep-licenses"),
	}
	options := base
	options.Personas = personas
	options.AllPersonas = allPersonas
	options.PersonasPath = run.str("personas-def")
	options.PersonaSource = personaSource
	options.Freeze = run.boolean("freeze")
	options.Deactivate = run.boolean("deactivate")

	planned := &plannedStrip{personaMode: personaMode, heldAssignments: []orgdata.HeldPersonaAssignment{}, removedPersonasByUser: map[string][]string{}}
	var selected []*lifecycle.StripTargetState
	haveSelected := false
	if personaMode && personaSource == "org" {
		if !orgdata.PersonaPackageDetected(conn) {
			return nil, fmt.Errorf("--persona-source org requires the Warden package with persona objects installed.")
		}
		selection, err := lifecycle.PlanStrip(conn, base)
		if err != nil {
			return nil, err
		}
		selected, haveSelected = selection.States, true
		orgPersonas, err := orgdata.ReadOrgPersonas(conn, true)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(selected))
		for index, state := range selected {
			ids[index] = state.Target.ID
		}
		if planned.heldAssignments, err = orgdata.ReadHeldPersonas(conn, ids); err != nil {
			return nil, err
		}
		heldByUser := map[string][]any{}
		for _, assignment := range planned.heldAssignments {
			heldByUser[assignment.UserID] = append(heldByUser[assignment.UserID], assignment.Persona)
		}
		users := []any{}
		for _, state := range selected {
			entry := core.NewObject()
			entry.Set("match", state.Target.Field)
			entry.Set(state.Target.Field, state.Target.Value)
			held := heldByUser[state.Target.ID]
			if held == nil {
				held = []any{}
			}
			entry.Set("personas", held)
			users = append(users, entry)
		}
		usersDoc := core.NewObject()
		usersDoc.Set("users", users)
		options = base
		options.TargetOptions = lifecycle.TargetOptions{UsersDoc: usersDoc}
		options.PersonasDoc = orgPersonas.Document
		options.Personas = personas
		options.AllPersonas = allPersonas
		options.PersonaSource = "file"
		options.Freeze = run.boolean("freeze")
		options.Deactivate = run.boolean("deactivate")
	}
	if personaMode {
		ledgerFields := orgdata.DescribeObjectFields(conn, orgdata.WardenLedgerObject)
		if !haveSelected {
			selection, err := lifecycle.PlanStrip(conn, base)
			if err != nil {
				return nil, err
			}
			selected = selection.States
		}
		targetIDs := make([]string, len(selected))
		for index, state := range selected {
			targetIDs[index] = state.Target.ID
		}
		if personaSource == "file" && orgdata.PersonaPackageDetected(conn) {
			held, err := orgdata.ReadHeldPersonas(conn, targetIDs)
			if err != nil {
				run.warn("Could not read Warden persona assignments: " + err.Error())
			} else {
				planned.heldAssignments = held
			}
		}
		if ledgerFields != nil {
			userIDs := targetIDs
			if personaSource == "org" {
				seen := map[string]bool{}
				userIDs = nil
				for _, assignment := range planned.heldAssignments {
					if !seen[assignment.UserID] {
						seen[assignment.UserID] = true
						userIDs = append(userIDs, assignment.UserID)
					}
				}
			}
			provenance, err := orgdata.ReadLedgerProvenance(conn, userIDs, ledgerFields)
			if err != nil {
				run.warn("Could not read Warden ledger provenance: " + err.Error())
			} else {
				planned.ledgerProvenance = provenance
				if ledgerFields.Has("wdn_Persona__c") {
					options.LedgerTargets = []lifecycle.LedgerTarget{}
					for _, row := range provenance {
						if row.Persona != nil {
							options.LedgerTargets = append(options.LedgerTargets, lifecycle.LedgerTarget{UserID: row.UserID, TargetID: row.TargetID, Persona: *row.Persona})
						}
					}
				}
			}
		}
	}
	if err := lifecycle.ValidateStripOptions(options); err != nil {
		return nil, err
	}
	plan, err := lifecycle.PlanStrip(conn, options)
	if err != nil {
		return nil, err
	}
	planned.plan = plan

	add := func(userID, persona string) {
		if !contains(planned.removedPersonasByUser[userID], persona) {
			planned.removedPersonasByUser[userID] = append(planned.removedPersonasByUser[userID], persona)
		}
	}
	switch {
	case !personaMode:
	case personaSource == "org":
		for _, assignment := range planned.heldAssignments {
			if allPersonas || contains(personas, assignment.Persona) {
				add(assignment.UserID, assignment.Persona)
			}
		}
	case run.str("users-def") != "":
		source := core.DefinitionSource{UsersPath: run.str("users-def"), PersonasPath: run.str("personas-def")}
		var fieldMap *core.FieldMap
		if core.DetectInputFormat(source.UsersPath, run.str("input-format")) == "csv" {
			source.InputFormat = "csv"
			source.CsvListDelimiter = run.str("csv-list-delimiter")
			if fieldMap, err = core.DescribeUserFields(conn, nil); err != nil {
				return nil, err
			}
		}
		definitions, err := core.ResolveDefinitions(source, fieldMap, core.DefaultDefinitionMessages())
		if err != nil {
			return nil, err
		}
		for _, state := range plan.States {
			for _, persona := range heldPersonasForTarget(definitions.UsersDoc, state.Target) {
				if allPersonas || contains(personas, persona) {
					add(state.Target.ID, persona)
				}
			}
		}
	}
	return planned, nil
}

// userIdentifier is implemented by a connection that can tell which user
// it is authenticated as.
type userIdentifier interface {
	UserID() (string, error)
}

// writePersonaRevocations records, in the objects of the warden-base
// package, the persona assignments and ledger rows the strip revoked.  A
// missing object or field is a warning.
func writePersonaRevocations(conn sfapi.Conn, output *lifecycle.Result, planned *plannedStrip, warn func(string)) {
	assignmentFields := orgdata.DescribeObjectFields(conn, orgdata.PersonaAssignmentObject)
	ledgerFields := orgdata.DescribeObjectFields(conn, orgdata.WardenLedgerObject)
	runFields := orgdata.DescribeObjectFields(conn, orgdata.WardenRunObject)
	assignmentsAvailable := assignmentFields != nil && assignmentFields.Has(orgdata.PersonaAssignmentStatusField) && assignmentFields.Has("wdn_Revoked_On__c") && assignmentFields.Has("wdn_Revoked_By__c")
	if !assignmentsAvailable {
		warn("Warden persona revocation fields are unavailable; skipped persona-assignment revocation bookkeeping.")
	}
	successful := map[string]bool{}
	for _, user := range output.Users {
		if user.Status != "failed" && user.ID != nil && *user.ID != "" {
			successful[*user.ID] = true
		}
	}
	runningUser := ""
	if identifier, ok := conn.(userIdentifier); ok {
		id, err := identifier.UserID()
		if err != nil {
			warn("Could not determine the running user for persona revocation: " + err.Error())
		}
		runningUser = id
	}
	stamp := now().UTC().Format("2006-01-02T15:04:05.000Z")
	runID := ""
	if runFields != nil && runFields.Has("wdn_Mode__c") && runFields.Has("wdn_Source_System__c") {
		runID = orgdata.SaveRun(conn, sfapi.Record{"wdn_Mode__c": "Revoke", "wdn_Source_System__c": "Warden CLI"}, func(string) {})
	}
	if assignmentsAvailable {
		records := []sfapi.Record{}
		for _, assignment := range planned.heldAssignments {
			if !successful[assignment.UserID] || !contains(planned.removedPersonasByUser[assignment.UserID], assignment.Persona) {
				continue
			}
			record := sfapi.Record{"Id": assignment.ID, orgdata.PersonaAssignmentStatusField: "Revoked", "wdn_Revoked_On__c": stamp}
			if runningUser != "" {
				record["wdn_Revoked_By__c"] = runningUser
			}
			records = append(records, record)
		}
		orgdata.UpdateBatch(conn, orgdata.PersonaAssignmentObject, records, warn, "persona assignment revocation")
	}
	if runID == "" {
		warn("Revoke run could not be saved; skipped ledger revocation bookkeeping (the ledger guard requires wdn_Revoked_By_Run__c).")
		return
	}
	unscoped := 0
	for _, row := range planned.ledgerProvenance {
		if row.Persona == nil {
			unscoped++
		}
	}
	if unscoped > 0 {
		reason := ""
		if ledgerFields != nil && !ledgerFields.Has("wdn_Persona__c") {
			reason = " (package lacks Grant.wdn_Persona__c)"
		}
		warn(fmt.Sprintf("%d ledger row(s) have no persona%s; not revoked.", unscoped, reason))
	}
	removedTargets := map[string]map[string]bool{}
	for _, user := range output.Users {
		if user.ID == nil || *user.ID == "" {
			continue
		}
		targets := map[string]bool{}
		for _, action := range user.Actions {
			if !strings.HasPrefix(action.Key, "removed") {
				continue
			}
			for _, item := range action.Items {
				targets[item.ID] = true
			}
		}
		removedTargets[*user.ID] = targets
	}
	if ledgerFields == nil || !ledgerFields.Has("wdn_Revoked_On__c") || !ledgerFields.Has("wdn_Revoked_By_Run__c") {
		warn("Warden ledger revocation fields are unavailable; skipped ledger revocation bookkeeping.")
		return
	}
	records := []sfapi.Record{}
	for _, row := range planned.ledgerProvenance {
		if row.Persona != nil && removedTargets[row.UserID][row.TargetID] && contains(planned.removedPersonasByUser[row.UserID], *row.Persona) {
			records = append(records, sfapi.Record{"Id": row.ID, "wdn_Revoked_On__c": stamp, "wdn_Revoked_By_Run__c": runID})
		}
	}
	orgdata.UpdateBatch(conn, orgdata.WardenLedgerObject, records, warn, "ledger revocation")
}

func runStrip(run *invocation) (any, error) {
	messages := loadMessages("warden.strip")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	for _, check := range []error{targetChecks(run), run.checkOption("persona-source", "file", "org"), run.existingFile("personas-def")} {
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
	planned, err := planStrip(run, conn, messages)
	if err != nil {
		return nil, err
	}
	plan := planned.plan
	personas, _ := run.flags.GetStringArray("persona")
	yesNo := func(value bool) string {
		if value {
			return "yes"
		}
		return "no"
	}
	if planned.personaMode && !context.jsonOutput {
		selection := strings.Join(personas, ", ")
		if run.boolean("all-personas") {
			selection = "all active personas"
		}
		run.log(messages.Get("info.personaMode", selection, fmt.Sprint(len(plan.States)), yesNo(run.boolean("freeze")), yesNo(run.boolean("deactivate"))))
	}
	dryRun := run.boolean("dry-run")
	hasDml := false
	for _, state := range plan.States {
		hasDml = hasDml || state.HasDml
	}
	if err := confirmLifecycleWrites(run, !dryRun && hasDml && !run.boolean("no-prompt") && context.interactive, messages); err != nil {
		return nil, err
	}
	if snapshotPath := run.str("snapshot"); snapshotPath != "" {
		// The snapshot is taken before the strip changes anything.
		targets := make([]core.Target, len(plan.States))
		ids := make([]string, len(plan.States))
		for index, state := range plan.States {
			targets[index], ids[index] = state.Target, state.Target.ID
		}
		state, err := provisioning.LoadAssignmentState(conn, ids, nil)
		if err != nil {
			return nil, err
		}
		file, err := lifecycle.BuildSnapshotFile(conn, targets, state, orgLabel())
		if err != nil {
			return nil, err
		}
		if err := lifecycle.WriteSnapshotFile(snapshotPath, file); err != nil {
			return nil, err
		}
		var users []*lifecycle.UserResult
		if dryRun {
			users = plan.Preview.Users
		} else {
			for _, state := range plan.States {
				users = append(users, state.Result)
			}
		}
		for _, user := range users {
			if user.Status != "failed" {
				user.Actions = append(user.Actions, lifecycle.MakeNotice("snapshotWritten", nil, nil, nil))
			}
		}
	}
	output := plan.Preview
	if !dryRun {
		if output, err = lifecycle.ApplyStrip(conn, plan); err != nil {
			return nil, err
		}
		if planned.personaMode {
			writePersonaRevocations(conn, output, planned, run.warn)
		}
	}
	return finishLifecycle(run, context, output, lifecycle.RenderStripCsv(output), messages)
}
