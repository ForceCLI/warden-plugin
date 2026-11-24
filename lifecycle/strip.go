package lifecycle

import (
	"errors"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// StripFlags are the flags of a strip by name: "dry-run", "no-freeze",
// "no-deactivate", "keep-permsets", "keep-permset-groups",
// "keep-public-groups", "keep-queues", "keep-licenses" and "all-personas".
type StripFlags map[string]bool

// StepRow is the row of an update step: IsFrozen for a UserLogin row,
// IsActive for a User row.
type StepRow struct {
	ID       string `json:"Id"`
	IsFrozen *bool  `json:"IsFrozen,omitempty"`
	IsActive *bool  `json:"IsActive,omitempty"`
}

func (r StepRow) record() sfapi.Record {
	record := sfapi.Record{"Id": r.ID}
	if r.IsFrozen != nil {
		record["IsFrozen"] = *r.IsFrozen
	}
	if r.IsActive != nil {
		record["IsActive"] = *r.IsActive
	}
	return record
}

// StripStep is one record change of a strip.  Kind "update" changes Row
// of Sobject; kind "delete" deletes the rows IDs of Sobject, which Items
// name.
type StripStep struct {
	Kind      string
	Sobject   string
	Row       *StepRow
	IDs       []string
	ActionKey string
	Items     []LabelBundle
}

func (s StripStep) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("kind", s.Kind)
	object.field("sobject", s.Sobject)
	if s.Kind == "update" {
		object.field("row", s.Row)
		object.field("actionKey", s.ActionKey)
		return object.bytes()
	}
	object.field("ids", nonNil(s.IDs))
	object.field("actionKey", s.ActionKey)
	items := s.Items
	if items == nil {
		items = []LabelBundle{}
	}
	object.field("items", items)
	return object.bytes()
}

// StripTargetState is the plan of a strip for one user.  Applying the
// plan changes Result.
type StripTargetState struct {
	Target core.Target
	Result *UserResult
	Steps  []StripStep
	HasDml bool
	// RevocationRows is the persona revocation plan of the user; nil when
	// the strip does not remove personas.
	RevocationRows []provisioning.PlanRow
}

func (s *StripTargetState) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("target", targetJSON(s.Target))
	object.field("result", s.Result)
	object.field("steps", s.Steps)
	object.field("hasDml", s.HasDml)
	if s.RevocationRows != nil {
		object.field("revocationRows", s.RevocationRows)
	}
	return object.bytes()
}

// TargetAssignmentRows are the assignment rows of one user.
type TargetAssignmentRows struct {
	Psa   []sfapi.Record
	Group []sfapi.Record
	Psl   []sfapi.Record
}

func addAction(result *UserResult, key string, count *int, dryRun bool, items []LabelBundle) {
	result.Status = "changed"
	if dryRun {
		result.Status = "planned"
	}
	result.Actions = append(result.Actions, MakeNotice(key, count, items, nil))
}

func addSkipped(result *UserResult, key string, count int, items []LabelBundle) {
	if count > 0 {
		result.Skipped = append(result.Skipped, MakeNotice(key, Count(count), items, nil))
	}
}

func isProfileOwned(row sfapi.Record) bool {
	return nested(row, "PermissionSet")["IsOwnedByProfile"] == true
}

func filterRows(rows []sfapi.Record, keep func(row sfapi.Record) bool) []sfapi.Record {
	kept := []sfapi.Record{}
	for _, row := range rows {
		if keep(row) {
			kept = append(kept, row)
		}
	}
	return kept
}

type stripCategory struct {
	selectRows func(rows TargetAssignmentRows) []sfapi.Record
	sobject    string
	skipFlag   string
	skipKey    string
	dryRunKey  string
	actionKey  string
	toItem     func(row sfapi.Record) *LabelBundle
}

func groupItem(row sfapi.Record) *LabelBundle {
	item := provisioning.GroupMemberLabel(row)
	return &item
}

func licenseItem(row sfapi.Record) *LabelBundle {
	item := provisioning.PermissionSetLicenseLabel(row)
	return &item
}

// stripCategories is in the order the rows are deleted: assignments
// before the permission set licenses they may need.
var stripCategories = []stripCategory{
	{
		selectRows: func(rows TargetAssignmentRows) []sfapi.Record {
			return filterRows(rows.Psa, func(row sfapi.Record) bool {
				return row["PermissionSetGroupId"] == nil && !isProfileOwned(row)
			})
		},
		sobject:   "PermissionSetAssignment",
		skipFlag:  "keep-permsets",
		skipKey:   "skippedPermissionSets",
		dryRunKey: "wouldRemovePermissionSet",
		actionKey: "removedPermissionSet",
		toItem:    provisioning.PermissionSetAssignmentLabel,
	},
	{
		selectRows: func(rows TargetAssignmentRows) []sfapi.Record {
			return filterRows(rows.Psa, func(row sfapi.Record) bool { return row["PermissionSetGroupId"] != nil })
		},
		sobject:   "PermissionSetAssignment",
		skipFlag:  "keep-permset-groups",
		skipKey:   "skippedPermissionSetGroups",
		dryRunKey: "wouldRemovePermissionSetGroup",
		actionKey: "removedPermissionSetGroup",
		toItem:    provisioning.PermissionSetAssignmentLabel,
	},
	{
		selectRows: func(rows TargetAssignmentRows) []sfapi.Record {
			return filterRows(rows.Group, func(row sfapi.Record) bool { return text(nested(row, "Group"), "Type") == "Regular" })
		},
		sobject:   "GroupMember",
		skipFlag:  "keep-public-groups",
		skipKey:   "skippedPublicGroups",
		dryRunKey: "wouldRemovePublicGroupMember",
		actionKey: "removedPublicGroupMember",
		toItem:    groupItem,
	},
	{
		selectRows: func(rows TargetAssignmentRows) []sfapi.Record {
			return filterRows(rows.Group, func(row sfapi.Record) bool { return text(nested(row, "Group"), "Type") == "Queue" })
		},
		sobject:   "GroupMember",
		skipFlag:  "keep-queues",
		skipKey:   "skippedQueues",
		dryRunKey: "wouldRemoveQueueMember",
		actionKey: "removedQueueMember",
		toItem:    groupItem,
	},
	{
		selectRows: func(rows TargetAssignmentRows) []sfapi.Record { return rows.Psl },
		sobject:    "PermissionSetLicenseAssign",
		skipFlag:   "keep-licenses",
		skipKey:    "skippedPermissionSetLicenses",
		dryRunKey:  "wouldRemovePermissionSetLicense",
		actionKey:  "removedPermissionSetLicense",
		toItem:     licenseItem,
	},
}

// PlanFreezeState returns the step that freezes the login of a user, or
// records in the result why there is none.  loginRow is the UserLogin row
// of the user, or nil.  A nil message uses core.LifecycleMessage.
func PlanFreezeState(result *UserResult, loginRow sfapi.Record, flags StripFlags, message MessageLookup) []StripStep {
	if message == nil {
		message = core.LifecycleMessage
	}
	if flags["no-freeze"] {
		result.Skipped = append(result.Skipped, MakeNotice("skippedFreeze", nil, nil, nil))
		return nil
	}
	if loginRow == nil {
		result.Warnings = append(result.Warnings, message("warningMissingUserLogin"))
		return nil
	}
	if loginRow["IsFrozen"] == true {
		result.Actions = append(result.Actions, MakeNotice("alreadyFrozen", nil, nil, nil))
		return nil
	}
	if flags["dry-run"] {
		addAction(result, "wouldFreeze", nil, true, nil)
		return nil
	}
	frozen := true
	return []StripStep{{Kind: "update", Sobject: "UserLogin", Row: &StepRow{ID: text(loginRow, "Id"), IsFrozen: &frozen}, ActionKey: "frozen"}}
}

func planCategory(result *UserResult, rows []sfapi.Record, category stripCategory, flags StripFlags) []StripStep {
	ids := []string{}
	items := []LabelBundle{}
	for _, row := range rows {
		if item := category.toItem(row); item != nil {
			ids = append(ids, text(row, "Id"))
			items = append(items, *item)
		}
	}
	if flags[category.skipFlag] {
		addSkipped(result, category.skipKey, len(items), items)
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	if flags["dry-run"] {
		addAction(result, category.dryRunKey, Count(len(items)), true, items)
		return nil
	}
	return []StripStep{{Kind: "delete", Sobject: category.sobject, IDs: ids, ActionKey: category.actionKey, Items: items}}
}

// PlanDeactivateState returns the step that deactivates a user, or
// records in the result why there is none.
func PlanDeactivateState(result *UserResult, target core.Target, flags StripFlags) []StripStep {
	if flags["no-deactivate"] {
		result.Skipped = append(result.Skipped, MakeNotice("skippedDeactivate", nil, nil, nil))
		return nil
	}
	if !target.IsActive {
		result.Actions = append(result.Actions, MakeNotice("alreadyInactive", nil, nil, nil))
		return nil
	}
	if flags["dry-run"] {
		addAction(result, "wouldDeactivate", nil, true, nil)
		return nil
	}
	active := false
	return []StripStep{{Kind: "update", Sobject: "User", Row: &StepRow{ID: target.ID, IsActive: &active}, ActionKey: "deactivated"}}
}

func firstRow(rows []sfapi.Record) sfapi.Record {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// BuildTargetState plans a full strip of one user: freeze, delete every
// assignment row of each category the flags do not keep, deactivate.
// Permission set assignments a profile owns are never deleted.  With the
// "dry-run" flag the result reports what would happen and there are no
// steps.
func BuildTargetState(target core.Target, loginRows []sfapi.Record, rows TargetAssignmentRows, flags StripFlags, message MessageLookup) *StripTargetState {
	result := ResolvedTargetResult(target, firstRow(loginRows))
	profileOwnedRows := filterRows(rows.Psa, func(row sfapi.Record) bool {
		return row["PermissionSetGroupId"] == nil && isProfileOwned(row)
	})
	if len(profileOwnedRows) > 0 {
		result.Skipped = append(result.Skipped, MakeNotice("skippedProfileOwnedPermissionSets", Count(len(profileOwnedRows)), nil, nil))
	}
	steps := []StripStep{}
	steps = append(steps, PlanFreezeState(result, firstRow(loginRows), flags, message)...)
	for _, category := range stripCategories {
		steps = append(steps, planCategory(result, category.selectRows(rows), category, flags)...)
	}
	steps = append(steps, PlanDeactivateState(result, target, flags)...)
	return &StripTargetState{Target: target, Result: result, Steps: steps, HasDml: len(steps) > 0}
}

func runStripStep(conn sfapi.Conn, result *UserResult, step StripStep) error {
	if step.Kind == "update" {
		return RunRecordUpdate(conn, result, step.Sobject, []sfapi.Record{step.Row.record()}, step.ActionKey)
	}
	return RunAssignmentDeletes(conn, result, step.Sobject, step.IDs, step.ActionKey, step.Items)
}

// ApplyStripState carries out the steps of one user in order and records
// each outcome in the result of the state.
func ApplyStripState(conn sfapi.Conn, state *StripTargetState) error {
	for _, step := range state.Steps {
		if err := runStripStep(conn, state.Result, step); err != nil {
			return err
		}
	}
	return nil
}

// LedgerTarget says that the ledger records TargetID as granted to
// UserID by Persona.
type LedgerTarget struct {
	UserID   string `json:"userId"`
	Persona  string `json:"persona"`
	TargetID string `json:"targetId"`
}

// StripOptions are the options of a strip.  With Personas or AllPersonas
// the strip removes only what those personas grant, and freezes or
// deactivates only when Freeze or Deactivate is set; otherwise it removes
// every assignment and freezes and deactivates unless NoFreeze or
// NoDeactivate is set.
type StripOptions struct {
	TargetOptions
	Personas     []string
	AllPersonas  bool
	PersonasPath string
	PersonasDoc  *core.Object
	// PersonaSource is "file", "org" or "" for "file".
	PersonaSource     string
	Freeze            bool
	Deactivate        bool
	NoFreeze          bool
	NoDeactivate      bool
	KeepPermsets      bool
	KeepPermsetGroups bool
	KeepPublicGroups  bool
	KeepQueues        bool
	KeepLicenses      bool
	// LedgerTargets, when not nil, makes the removal of a target the
	// ledger does not record for the removed persona add a warning.
	LedgerTargets []LedgerTarget
}

// ValidateStripOptions reports the option combination the upstream
// options schema rejects.
func ValidateStripOptions(options StripOptions) error {
	if options.AllPersonas && len(options.Personas) > 0 {
		return errors.New("personas and allPersonas are mutually exclusive.")
	}
	return nil
}

func stripFlags(options StripOptions) StripFlags {
	return StripFlags{
		"no-freeze":           options.NoFreeze,
		"no-deactivate":       options.NoDeactivate,
		"keep-permsets":       options.KeepPermsets,
		"keep-permset-groups": options.KeepPermsetGroups,
		"keep-public-groups":  options.KeepPublicGroups,
		"keep-queues":         options.KeepQueues,
		"keep-licenses":       options.KeepLicenses,
		"all-personas":        options.AllPersonas,
	}
}

func isPersonaMode(options StripOptions) bool {
	return options.AllPersonas || len(options.Personas) > 0
}

func userRowMatchesTarget(row *core.Object, target core.Target) bool {
	matchField := target.Field
	if match, _ := row.String("match"); match != "" {
		matchField = match
	}
	value, _ := row.GetFold(matchField)
	matched, isText := value.(string)
	return isText && matched == target.Value
}

func heldPersonasFromFile(usersDoc *core.Object, target core.Target) []string {
	users, ok := usersDoc.List("users")
	if !ok {
		return []string{}
	}
	for _, candidate := range users {
		row, isObject := candidate.(*core.Object)
		if !isObject || row == nil || !userRowMatchesTarget(row, target) {
			continue
		}
		if _, isList := row.List("personas"); !isList {
			return []string{}
		}
		return nonNil(row.Strings("personas"))
	}
	return []string{}
}

func stripFlagsForPersona(options StripOptions, dryRun bool) StripFlags {
	flags := stripFlags(options)
	flags["no-freeze"] = !options.Freeze || options.NoFreeze
	flags["no-deactivate"] = !options.Deactivate || options.NoDeactivate
	flags["dry-run"] = dryRun
	return flags
}

func actionKeyForCategory(category string, dryRun bool) string {
	removed, would := "removedQueueMember", "wouldRemoveQueueMember"
	switch category {
	case "PermissionSet":
		removed, would = "removedPermissionSet", "wouldRemovePermissionSet"
	case "PermissionSetGroup":
		removed, would = "removedPermissionSetGroup", "wouldRemovePermissionSetGroup"
	case "PublicGroup":
		removed, would = "removedPublicGroupMember", "wouldRemovePublicGroupMember"
	}
	if dryRun {
		return would
	}
	return removed
}

// keepFlagForCategory holds the flag names the upstream code looks up for
// a planned removal.  The flags are stored under their dashed names
// ("keep-permsets"), so the lookup finds none of them and the skip notice
// below it is never added; the revocation plan itself applies the keep
// flags.  The names are kept as they are upstream.
var keepFlagForCategory = map[string]string{
	"PermissionSet":      "keepPermsets",
	"PermissionSetGroup": "keepPermsetGroups",
	"PublicGroup":        "keepPublicGroups",
	"Queue":              "keepQueues",
}

type personaTargetOptions struct {
	target         core.Target
	loginRows      []sfapi.Record
	psaRows        []sfapi.Record
	groupRows      []sfapi.Record
	refs           *provisioning.ResolvedRefs
	personas       *core.Object
	heldPersonas   []string
	removePersonas []string
	removeAll      bool
	flags          StripFlags
	ledgerTargets  map[string]map[string]bool
}

func findLabel(refs *provisioning.ResolvedRefs, planDetail string) (LabelBundle, bool) {
	detail := planDetail
	if index := strings.Index(detail, " (kept by "); index >= 0 {
		detail = detail[:index]
	}
	for _, id := range refs.Labels.Keys() {
		candidate := refs.Labels.Get(id)
		for _, value := range []string{candidate.Label, candidate.APIName, candidate.ID} {
			if value != "" && (value == detail || value == planDetail) {
				return candidate, true
			}
		}
	}
	return LabelBundle{}, false
}

func buildPersonaTargetState(options personaTargetOptions) *StripTargetState {
	target, flags := options.target, options.flags
	result := ResolvedTargetResult(target, firstRow(options.loginRows))
	revocation := provisioning.PlanPersonaRevocation(provisioning.PersonaRevocationInput{
		UserKey:        target.Key,
		UserID:         target.ID,
		Personas:       options.personas,
		HeldPersonas:   options.heldPersonas,
		RemovePersonas: options.removePersonas,
		RemoveAll:      options.removeAll,
		Refs:           options.refs,
		AssignmentRows: options.psaRows,
		MembershipRows: options.groupRows,
		Flags:          flags,
		LedgerTargets:  options.ledgerTargets,
	})
	result.Warnings = append(result.Warnings, revocation.Warnings...)
	if !options.removeAll {
		for _, persona := range options.removePersonas {
			if !contains(options.heldPersonas, persona) {
				result.Warnings = append(result.Warnings, "personaNotHeld: "+persona+" for "+target.Key+".")
			}
		}
	}
	dryRun := flags["dry-run"]
	var noticeCategories []string
	noticeItemsByCategory := map[string][]LabelBundle{}
	for _, planRow := range revocation.Rows {
		if planRow.Action == "unresolved" {
			result.Errors = append(result.Errors, planRow.Error)
			continue
		}
		item, found := findLabel(options.refs, planRow.Detail)
		if planRow.Action == "keptByOtherPersona" {
			result.Skipped = append(result.Skipped, MakeNotice("keptByOtherPersona", nil, nil, stringPointer(planRow.Detail)))
			continue
		}
		if planRow.Action == "alreadyAbsent" {
			result.Skipped = append(result.Skipped, MakeNotice("alreadyAbsent", nil, nil, stringPointer(planRow.Detail)))
			continue
		}
		if flags[keepFlagForCategory[planRow.Category]] {
			skipKey := "skippedQueues"
			switch planRow.Category {
			case "PermissionSet":
				skipKey = "skippedPermissionSets"
			case "PermissionSetGroup":
				skipKey = "skippedPermissionSetGroups"
			case "PublicGroup":
				skipKey = "skippedPublicGroups"
			}
			var items []LabelBundle
			if found {
				items = []LabelBundle{item}
			}
			result.Skipped = append(result.Skipped, MakeNotice(skipKey, Count(1), items, nil))
			continue
		}
		if dryRun {
			result.Status = "planned"
			if _, seen := noticeItemsByCategory[planRow.Category]; !seen {
				noticeCategories = append(noticeCategories, planRow.Category)
				noticeItemsByCategory[planRow.Category] = []LabelBundle{}
			}
			if found {
				noticeItemsByCategory[planRow.Category] = append(noticeItemsByCategory[planRow.Category], item)
			}
		}
	}
	for _, category := range noticeCategories {
		items := noticeItemsByCategory[category]
		result.Actions = append(result.Actions, MakeNotice(actionKeyForCategory(category, true), Count(len(items)), items, nil))
	}
	steps := []StripStep{}
	steps = append(steps, PlanFreezeState(result, firstRow(options.loginRows), flags, nil)...)
	for _, step := range revocation.Steps {
		steps = append(steps, StripStep{Kind: step.Kind, Sobject: step.Sobject, IDs: step.IDs, ActionKey: step.ActionKey, Items: step.Items})
	}
	steps = append(steps, PlanDeactivateState(result, target, flags)...)
	revocationRows := revocation.Rows
	if revocationRows == nil {
		revocationRows = []provisioning.PlanRow{}
	}
	return &StripTargetState{Target: target, Result: result, Steps: steps, HasDml: len(steps) > 0, RevocationRows: revocationRows}
}

func ledgerTargetsForUser(ledgerTargets []LedgerTarget, target core.Target, heldPersonas, removePersonas []string, removeAll bool) map[string]map[string]bool {
	if ledgerTargets == nil {
		return nil
	}
	removed := removePersonas
	if removeAll {
		removed = heldPersonas
	}
	targetsByPersona := map[string]map[string]bool{}
	for _, entry := range ledgerTargets {
		if entry.UserID != target.ID || !contains(removed, entry.Persona) {
			continue
		}
		if targetsByPersona[entry.Persona] == nil {
			targetsByPersona[entry.Persona] = map[string]bool{}
		}
		targetsByPersona[entry.Persona][entry.TargetID] = true
	}
	return targetsByPersona
}

func personaDefinitionsForStrip(options StripOptions, fieldMap *core.FieldMap) (personas, usersDoc *core.Object, err error) {
	personaSource := options.PersonaSource
	if personaSource == "" {
		personaSource = "file"
	}
	if personaSource == "org" {
		return nil, nil, core.NewLifecycleError("errorPersonaSourceUnsupported", core.LifecycleMessage("errorPersonaSourceUnsupported", personaSource), map[string]any{"personaSource": personaSource, "reason": "org-held-persona-loader-is-owned-by-the-caller"})
	}
	if options.UsersDoc == nil && options.UsersPath == "" {
		return nil, nil, core.NewLifecycleError("errorPersonaSourceRequiresUsers", core.LifecycleMessage("errorPersonaSourceRequiresUsers", personaSource), map[string]any{"personaSource": personaSource, "required": "usersDoc-or-usersPath"})
	}
	definitions, err := core.LoadValidatedDefinitions(core.DefinitionSource{
		UsersDoc:         options.UsersDoc,
		PersonasDoc:      options.PersonasDoc,
		UsersPath:        options.UsersPath,
		PersonasPath:     options.PersonasPath,
		InputFormat:      options.InputFormat,
		CsvListDelimiter: options.CsvListDelimiter,
	}, fieldMap, core.DefaultDefinitionMessages())
	if err != nil {
		return nil, nil, err
	}
	return definitions.PersonasDoc.Object("personas"), definitions.UsersDoc, nil
}

// StripPlan is what a strip would do.  States holds the steps and the
// live result of each selected user, Errors the users that could not be
// selected, and Preview the dry-run result.
type StripPlan struct {
	States   []*StripTargetState
	Errors   []core.TargetError
	Preview  *Result
	Warnings []string
	// RevocationRows is nil when the strip does not remove personas.
	RevocationRows []provisioning.PlanRow
}

func (p *StripPlan) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("states", p.States)
	object.field("errors", targetErrorsJSON(p.Errors))
	object.field("preview", p.Preview)
	object.field("warnings", nonNil(p.Warnings))
	if p.RevocationRows != nil {
		object.field("revocationRows", p.RevocationRows)
	}
	return object.bytes()
}

func stripPreview(selectionErrors []core.TargetError, previewStates []*StripTargetState) *Result {
	previewUsers := failedResults(selectionErrors)
	for _, state := range previewStates {
		previewUsers = append(previewUsers, state.Result)
	}
	return &Result{Summary: SummarizeLifecycle(previewUsers), Users: previewUsers}
}

// PlanStrip selects the users, reads their assignments and plans the
// strip.  It changes no record.
func PlanStrip(conn sfapi.Conn, options StripOptions) (*StripPlan, error) {
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	targets, selectionErrors, err := resolveTargetSelection(conn, options.TargetOptions, fieldMap)
	if err != nil {
		return nil, err
	}
	targetIDs := make([]string, len(targets))
	for i, target := range targets {
		targetIDs[i] = target.ID
	}
	stateMaps, err := provisioning.LoadAssignmentState(conn, targetIDs, nil)
	if err != nil {
		return nil, err
	}
	flags := stripFlags(options)
	if isPersonaMode(options) {
		personas, usersDoc, err := personaDefinitionsForStrip(options, fieldMap)
		if err != nil {
			return nil, err
		}
		refs, err := provisioning.ResolveReferences(conn, personas, nil)
		if err != nil {
			return nil, err
		}
		buildStates := func(flags StripFlags) []*StripTargetState {
			states := make([]*StripTargetState, 0, len(targets))
			for _, target := range targets {
				heldPersonas := heldPersonasFromFile(usersDoc, target)
				removePersonas := options.Personas
				if options.AllPersonas {
					removePersonas = nil
				}
				states = append(states, buildPersonaTargetState(personaTargetOptions{
					target:         target,
					loginRows:      stateMaps.UserLoginByUserID[target.ID],
					psaRows:        stateMaps.PsaByUserID[target.ID],
					groupRows:      stateMaps.GroupByUserID[target.ID],
					refs:           refs,
					personas:       personas,
					heldPersonas:   heldPersonas,
					removePersonas: removePersonas,
					removeAll:      options.AllPersonas,
					flags:          flags,
					ledgerTargets:  ledgerTargetsForUser(options.LedgerTargets, target, heldPersonas, removePersonas, options.AllPersonas),
				}))
			}
			return states
		}
		states := buildStates(stripFlagsForPersona(options, false))
		previewStates := buildStates(stripFlagsForPersona(options, true))
		revocationRows := []provisioning.PlanRow{}
		for _, state := range states {
			revocationRows = append(revocationRows, state.RevocationRows...)
		}
		return &StripPlan{
			States:         states,
			Errors:         selectionErrors,
			Preview:        stripPreview(selectionErrors, previewStates),
			Warnings:       nonNil(refs.Warnings),
			RevocationRows: revocationRows,
		}, nil
	}
	buildStates := func(flags StripFlags) []*StripTargetState {
		states := make([]*StripTargetState, 0, len(targets))
		for _, target := range targets {
			states = append(states, BuildTargetState(target, stateMaps.UserLoginByUserID[target.ID], TargetAssignmentRows{
				Psa:   stateMaps.PsaByUserID[target.ID],
				Group: stateMaps.GroupByUserID[target.ID],
				Psl:   stateMaps.PslByUserID[target.ID],
			}, flags, nil))
		}
		return states
	}
	states := buildStates(flags)
	previewFlags := StripFlags{"dry-run": true}
	for name, value := range flags {
		previewFlags[name] = value
	}
	previewStates := buildStates(previewFlags)
	return &StripPlan{
		States:   states,
		Errors:   selectionErrors,
		Preview:  stripPreview(selectionErrors, previewStates),
		Warnings: []string{},
	}, nil
}

// ApplyStrip carries out the steps of each user of a plan, one user after
// the other, and returns the live results.  It changes the Result of each
// state of the plan.
func ApplyStrip(conn sfapi.Conn, plan *StripPlan) (*Result, error) {
	for _, state := range plan.States {
		if err := ApplyStripState(conn, state); err != nil {
			return nil, err
		}
	}
	users := failedResults(plan.Errors)
	for _, state := range plan.States {
		users = append(users, state.Result)
	}
	return &Result{Summary: SummarizeLifecycle(users), Users: users}, nil
}
