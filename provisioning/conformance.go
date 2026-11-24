package provisioning

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// FixtureUser is a user of a conformance fixture.
type FixtureUser struct {
	UserKey  string
	UserID   string
	Personas []string
}

// SetupReference is a permission set, permission set group, public group
// or queue of the simulated org.  Type is "Regular" or "Queue" for a
// group and "" otherwise.
type SetupReference struct {
	ID            string
	Name          string
	DeveloperName string
	Label         string
	Type          string
}

// FixtureAssignment is a simulated PermissionSetAssignment row.  An Id
// that is absent or null is nil.
type FixtureAssignment struct {
	ID                   string
	UserID               string
	PermissionSetID      *string
	PermissionSetGroupID *string
}

// FixtureGroupMember is a simulated GroupMember row.
type FixtureGroupMember struct {
	ID      string
	UserID  string
	GroupID string
}

// OrgState is the simulated org of a conformance fixture.
type OrgState struct {
	PermissionSets           []SetupReference
	PermissionSetGroups      []SetupReference
	Groups                   []SetupReference
	PermissionSetAssignments []FixtureAssignment
	GroupMembers             []FixtureGroupMember
}

// ConformanceDefinitions are the definitions of a conformance fixture.
type ConformanceDefinitions struct {
	// Personas is the personas document: an object with "personas".
	Personas *core.Object
	Users    []FixtureUser
}

// ConformanceFixture is a reconciliation fixture: definitions, a
// simulated org and the plan PlanFromState must give for them.
type ConformanceFixture struct {
	Definitions  ConformanceDefinitions
	OrgState     OrgState
	ExpectedPlan []PlanRow
}

// RevocationFixture is a persona revocation fixture: definitions, a
// simulated org, the personas to remove and the plan
// PlanRevocationFromState must give for them.
type RevocationFixture struct {
	Definitions ConformanceDefinitions
	OrgState    OrgState
	// RemovePersonas names the personas to remove; RemoveAll is true when
	// the fixture says "all".
	RemovePersonas []string
	RemoveAll      bool
	ExpectedPlan   []PlanRow
}

type fixtureValidator struct {
	issues []core.Issue
}

func (v *fixtureValidator) add(path []string, message string) {
	v.issues = append(v.issues, core.Issue{Path: append([]string{}, path...), Message: message})
}

func (v *fixtureValidator) expected(path []string, want string, value any, present bool) {
	v.add(path, fmt.Sprintf("Invalid input: expected %s, received %s", want, core.JSONTypeOf(value, present)))
}

func at(path []string, keys ...string) []string {
	return append(append([]string{}, path...), keys...)
}

func (v *fixtureValidator) object(path []string, value any, present bool) *core.Object {
	object, ok := value.(*core.Object)
	if !ok || object == nil {
		v.expected(path, "object", value, present)
		return nil
	}
	return object
}

func (v *fixtureValidator) list(parent *core.Object, path []string, key string) []any {
	value, present := parent.Get(key)
	list, ok := value.([]any)
	if !ok {
		v.expected(at(path, key), "array", value, present)
	}
	return list
}

// str reads a text of at least one character.  With optional set, an
// absent key is valid; with nullable set, so is null.
func (v *fixtureValidator) str(parent *core.Object, path []string, key string, optional, nullable bool) *string {
	value, present := parent.Get(key)
	if (!present && optional) || (present && value == nil && nullable) {
		return nil
	}
	result, ok := value.(string)
	if !ok {
		v.expected(at(path, key), "string", value, present)
		return nil
	}
	if result == "" {
		v.add(at(path, key), "Too small: expected string to have >=1 characters")
	}
	return &result
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (v *fixtureValidator) enum(parent *core.Object, path []string, key string, options ...string) string {
	value, _ := parent.Get(key)
	if result, ok := value.(string); ok && contains(options, result) {
		return result
	}
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = `"` + option + `"`
	}
	v.add(at(path, key), "Invalid option: expected one of "+strings.Join(quoted, "|"))
	return ""
}

// nestedDocument checks a personas or users document inside a fixture and
// adds its issues under path.
func (v *fixtureValidator) nestedDocument(path []string, value any, present bool, parse func(input any) (*core.Object, error)) *core.Object {
	if v.object(path, value, present) == nil {
		return nil
	}
	doc, err := parse(value)
	if err == nil {
		return doc
	}
	failure, ok := core.AsWardenError(err)
	if !ok {
		v.add(path, err.Error())
		return nil
	}
	if failure.Code == "schema-version-unsupported" {
		v.add(at(path, "schemaVersion"), "Invalid input: expected 1")
		return nil
	}
	issues, _ := failure.Data["issues"].([]any)
	for _, item := range issues {
		issue, _ := item.(map[string]any)
		parts, _ := issue["path"].([]any)
		issuePath := append([]string{}, path...)
		for _, part := range parts {
			issuePath = append(issuePath, fmt.Sprint(part))
		}
		message, _ := issue["message"].(string)
		v.add(issuePath, message)
	}
	return nil
}

func (v *fixtureValidator) definitions(root *core.Object) ConformanceDefinitions {
	var definitions ConformanceDefinitions
	value, present := root.Get("definitions")
	object := v.object([]string{"definitions"}, value, present)
	if object == nil {
		return definitions
	}
	personas, hasPersonas := object.Get("personas")
	definitions.Personas = v.nestedDocument([]string{"definitions", "personas"}, personas, hasPersonas, core.ParsePersonaDefinitions)
	users, hasUsers := object.Get("users")
	usersDoc := v.nestedDocument([]string{"definitions", "users"}, users, hasUsers, core.ParseUsersDefinition)
	if usersDoc == nil {
		return definitions
	}
	list, _ := usersDoc.List("users")
	for index, item := range list {
		path := []string{"definitions", "users", "users", fmt.Sprint(index)}
		user, _ := item.(*core.Object)
		definitions.Users = append(definitions.Users, FixtureUser{
			UserKey:  deref(v.str(user, path, "userKey", false, false)),
			UserID:   deref(v.str(user, path, "userId", false, false)),
			Personas: user.Strings("personas"),
		})
	}
	return definitions
}

func (v *fixtureValidator) setupReferences(parent *core.Object, path []string, key string, isGroup bool) []SetupReference {
	var references []SetupReference
	for index, item := range v.list(parent, path, key) {
		itemPath := at(path, key, fmt.Sprint(index))
		object := v.object(itemPath, item, true)
		if object == nil {
			continue
		}
		reference := SetupReference{
			ID:            deref(v.str(object, itemPath, "id", false, false)),
			Name:          deref(v.str(object, itemPath, "name", true, false)),
			DeveloperName: deref(v.str(object, itemPath, "developerName", true, false)),
			Label:         deref(v.str(object, itemPath, "label", true, false)),
		}
		if isGroup {
			reference.Type = v.enum(object, itemPath, "type", "Regular", "Queue")
		}
		references = append(references, reference)
	}
	return references
}

func (v *fixtureValidator) orgState(root *core.Object) OrgState {
	var state OrgState
	value, present := root.Get("orgState")
	path := []string{"orgState"}
	object := v.object(path, value, present)
	if object == nil {
		return state
	}
	state.PermissionSets = v.setupReferences(object, path, "permissionSets", false)
	state.PermissionSetGroups = v.setupReferences(object, path, "permissionSetGroups", false)
	state.Groups = v.setupReferences(object, path, "groups", true)
	for index, item := range v.list(object, path, "permissionSetAssignments") {
		itemPath := at(path, "permissionSetAssignments", fmt.Sprint(index))
		row := v.object(itemPath, item, true)
		if row == nil {
			continue
		}
		state.PermissionSetAssignments = append(state.PermissionSetAssignments, FixtureAssignment{
			ID:                   deref(v.str(row, itemPath, "id", false, false)),
			UserID:               deref(v.str(row, itemPath, "userId", false, false)),
			PermissionSetID:      v.str(row, itemPath, "permissionSetId", true, true),
			PermissionSetGroupID: v.str(row, itemPath, "permissionSetGroupId", true, true),
		})
	}
	for index, item := range v.list(object, path, "groupMembers") {
		itemPath := at(path, "groupMembers", fmt.Sprint(index))
		row := v.object(itemPath, item, true)
		if row == nil {
			continue
		}
		state.GroupMembers = append(state.GroupMembers, FixtureGroupMember{
			ID:      deref(v.str(row, itemPath, "id", false, false)),
			UserID:  deref(v.str(row, itemPath, "userId", false, false)),
			GroupID: deref(v.str(row, itemPath, "groupId", false, false)),
		})
	}
	return state
}

func (v *fixtureValidator) planRows(root *core.Object, actions, statuses []string) []PlanRow {
	rows := []PlanRow{}
	for index, item := range v.list(root, nil, "expectedPlan") {
		path := []string{"expectedPlan", fmt.Sprint(index)}
		object := v.object(path, item, true)
		if object == nil {
			continue
		}
		row := PlanRow{
			UserKey:  deref(v.str(object, path, "userKey", false, false)),
			UserID:   deref(v.str(object, path, "userId", false, false)),
			Category: v.enum(object, path, "category", CategoryOrder...),
			Action:   v.enum(object, path, "action", actions...),
			Status:   v.enum(object, path, "status", statuses...),
		}
		for _, key := range []string{"detail", "error"} {
			value, present := object.Get(key)
			result, ok := value.(string)
			if !ok {
				v.expected(at(path, key), "string", value, present)
			}
			if key == "detail" {
				row.Detail = result
			} else {
				row.Error = result
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (v *fixtureValidator) root(input any) (*core.Object, error) {
	if object, ok := input.(*core.Object); ok && object != nil {
		if version, present := object.Get("schemaVersion"); present {
			if number, isNumber := version.(float64); isNumber && number == float64(int64(number)) && number > 1 {
				issue := core.Issue{Path: []string{"schemaVersion"}, Message: "Unsupported schemaVersion; this reader supports version 1."}
				return nil, fixtureError("schema-version-unsupported", []core.Issue{issue})
			}
		}
	}
	root := v.object(nil, input, true)
	if root != nil {
		if version, _ := root.Get("schemaVersion"); version != float64(1) {
			v.add([]string{"schemaVersion"}, "Invalid input: expected 1")
		}
	}
	return root, nil
}

func fixtureError(code string, issues []core.Issue) error {
	lines := make([]string, len(issues))
	list := make([]any, len(issues))
	for i, issue := range issues {
		path := "$"
		if len(issue.Path) > 0 {
			path = strings.Join(issue.Path, ".")
		}
		lines[i] = path + ": " + issue.Message
		parts := make([]any, len(issue.Path))
		for j, part := range issue.Path {
			parts[j] = part
		}
		list[i] = map[string]any{"path": parts, "message": issue.Message}
	}
	return core.NewDefinitionError(code, strings.Join(lines, "\n"), map[string]any{"issues": list})
}

func (v *fixtureValidator) result() error {
	if len(v.issues) == 0 {
		return nil
	}
	return fixtureError("schema-invalid", v.issues)
}

// ParseConformanceFixture checks and reads a reconciliation fixture that
// was decoded with core.ParseJSON.
func ParseConformanceFixture(input any) (*ConformanceFixture, error) {
	v := &fixtureValidator{}
	root, err := v.root(input)
	if err != nil {
		return nil, err
	}
	fixture := &ConformanceFixture{}
	if root != nil {
		fixture.Definitions = v.definitions(root)
		fixture.OrgState = v.orgState(root)
		fixture.ExpectedPlan = v.planRows(root, []string{"wouldAssign", "unresolved"}, []string{"planned", "unmanaged"})
	}
	if err := v.result(); err != nil {
		return nil, err
	}
	return fixture, nil
}

// ParseRevocationFixture checks and reads a persona revocation fixture
// that was decoded with core.ParseJSON.
func ParseRevocationFixture(input any) (*RevocationFixture, error) {
	v := &fixtureValidator{}
	root, err := v.root(input)
	if err != nil {
		return nil, err
	}
	fixture := &RevocationFixture{}
	if root != nil {
		fixture.Definitions = v.definitions(root)
		fixture.OrgState = v.orgState(root)
		value, present := root.Get("revocation")
		if revocation := v.object([]string{"revocation"}, value, present); revocation != nil {
			remove, _ := revocation.Get("removePersonas")
			list, isList := remove.([]any)
			for _, item := range list {
				name, isText := item.(string)
				isList = isList && isText
				fixture.RemovePersonas = append(fixture.RemovePersonas, name)
			}
			fixture.RemoveAll = remove == "all"
			if !isList && !fixture.RemoveAll {
				v.add([]string{"revocation", "removePersonas"}, "Invalid input")
			}
		}
		fixture.ExpectedPlan = v.planRows(root, []string{"wouldRemove", "keptByOtherPersona", "alreadyAbsent", "unresolved"}, []string{"planned", "kept", "unchanged", "unmanaged"})
	}
	if err := v.result(); err != nil {
		return nil, err
	}
	return fixture, nil
}

func referenceLabel(reference SetupReference) string {
	for _, candidate := range []string{reference.Label, reference.DeveloperName, reference.Name} {
		if candidate != "" {
			return candidate
		}
	}
	return reference.ID
}

// referencesFor resolves references from the simulated org: a permission
// set by its Id or name, the other categories by Id or developer name.
func referencesFor(state OrgState) (*ResolvedRefs, *OrderedMap[string]) {
	labels := &OrderedMap[string]{}
	refs := NewResolvedRefs()
	add := func(target *RefMap, reference SetupReference, lookupKey string) {
		target.Set(reference.ID, reference.ID)
		if lookupKey != "" {
			target.Set(lookupKey, reference.ID)
		}
		labels.Set(reference.ID, referenceLabel(reference))
	}
	for _, reference := range state.PermissionSets {
		add(refs.PermissionSetIDsByRef, reference, reference.Name)
	}
	for _, reference := range state.PermissionSetGroups {
		add(refs.PermissionSetGroupIDsByRef, reference, reference.DeveloperName)
	}
	for _, reference := range state.Groups {
		if reference.Type == "Queue" {
			add(refs.QueueIDsByRef, reference, reference.DeveloperName)
		} else {
			add(refs.PublicGroupIDsByRef, reference, reference.DeveloperName)
		}
	}
	return refs, labels
}

func optionalID(id *string) any {
	if id == nil {
		return nil
	}
	return *id
}

func assignmentRowsForUser(state OrgState, userID string) []sfapi.Record {
	rows := []sfapi.Record{}
	for _, row := range state.PermissionSetAssignments {
		if row.UserID != userID {
			continue
		}
		rows = append(rows, sfapi.Record{
			"Id":                   row.ID,
			"AssigneeId":           row.UserID,
			"PermissionSetId":      optionalID(row.PermissionSetID),
			"PermissionSetGroupId": optionalID(row.PermissionSetGroupID),
			"PermissionSet":        sfapi.Record{"IsOwnedByProfile": false},
		})
	}
	return rows
}

func groupMembersForUser(state OrgState, userID string) []sfapi.Record {
	rows := []sfapi.Record{}
	for _, row := range state.GroupMembers {
		if row.UserID != userID {
			continue
		}
		record := sfapi.Record{"Id": row.ID, "GroupId": row.GroupID, "UserOrGroupId": row.UserID}
		for _, group := range state.Groups {
			if group.ID == row.GroupID {
				record["Group"] = sfapi.Record{"Type": group.Type, "DeveloperName": group.DeveloperName, "Name": group.Name}
				break
			}
		}
		rows = append(rows, record)
	}
	return rows
}

func plannedRowsForCategory(user FixtureUser, category string, persona *Persona, refs *ResolvedRefs, labels *OrderedMap[string], assignmentRows, membershipRows []sfapi.Record) ([]PlanRow, error) {
	var rows []PlanRow
	byRef := referenceMap(refs, category)
	for _, reference := range persona.List(categoryListKey[category]) {
		if !byRef.Has(reference) {
			rows = append(rows, PlanRow{UserKey: user.UserKey, UserID: user.UserID, Category: category, Action: "unresolved", Status: "unmanaged", Detail: reference, Error: `Unresolved ` + category + ` reference "` + reference + `".`})
		}
	}
	delta, err := ComputeAssignmentDeltaFromState(persona, refs, assignmentRows, membershipRows)
	if err != nil {
		return nil, err
	}
	adds := delta.PermissionSets.Adds
	switch category {
	case "PermissionSetGroup":
		adds = delta.PermissionSetGroups.Adds
	case "PublicGroup":
		adds = delta.PublicGroups.Adds
	case "Queue":
		adds = delta.Queues.Adds
	}
	for _, id := range adds {
		detail, ok := labels.Lookup(id)
		if !ok {
			detail = id
		}
		rows = append(rows, PlanRow{UserKey: user.UserKey, UserID: user.UserID, Category: category, Action: "wouldAssign", Status: "planned", Detail: detail})
	}
	return rows, nil
}

// PlanFromState computes the additive reconciliation plan of the fixture
// users without a connection.  References resolve against the simulated
// org, and the rows are ordered by user key, category, detail and action.
func PlanFromState(definitions ConformanceDefinitions, orgState OrgState) ([]PlanRow, error) {
	refs, labels := referencesFor(orgState)
	rows := []PlanRow{}
	personas := definitions.Personas.Object("personas")
	for _, user := range definitions.Users {
		effective, validationErrors := MergePersonas(user.Personas, personas, core.NewObject(), nil, false, false)
		if len(validationErrors) > 0 {
			codes := make([]string, len(validationErrors))
			for i, validationError := range validationErrors {
				codes[i] = validationError.Code
			}
			rows = append(rows, PlanRow{UserKey: user.UserKey, UserID: user.UserID, Category: "PermissionSet", Action: "unresolved", Status: "unmanaged", Detail: strings.Join(codes, ", "), Error: "Unable to merge persona definitions."})
			continue
		}
		assignmentRows := assignmentRowsForUser(orgState, user.UserID)
		membershipRows := groupMembersForUser(orgState, user.UserID)
		for _, category := range CategoryOrder {
			categoryRows, err := plannedRowsForCategory(user, category, &effective, refs, labels, assignmentRows, membershipRows)
			if err != nil {
				return nil, err
			}
			rows = append(rows, categoryRows...)
		}
	}
	sortPlanRows(rows)
	return rows, nil
}

// PlanRevocationFromState computes the persona revocation plan of a
// fixture without a connection.
func PlanRevocationFromState(fixture *RevocationFixture) []PlanRow {
	refs, labels := referencesFor(fixture.OrgState)
	refs.Labels = NewLabelMap()
	for _, id := range labels.Keys() {
		refs.Labels.Set(id, ItemLabel{ID: id, Label: labels.Get(id)})
	}
	rows := []PlanRow{}
	for _, user := range fixture.Definitions.Users {
		plan := PlanPersonaRevocation(PersonaRevocationInput{
			UserKey:        user.UserKey,
			UserID:         user.UserID,
			Personas:       fixture.Definitions.Personas.Object("personas"),
			HeldPersonas:   user.Personas,
			RemovePersonas: fixture.RemovePersonas,
			RemoveAll:      fixture.RemoveAll,
			Refs:           refs,
			AssignmentRows: assignmentRowsForUser(fixture.OrgState, user.UserID),
			MembershipRows: groupMembersForUser(fixture.OrgState, user.UserID),
		})
		rows = append(rows, plan.Rows...)
	}
	sortPlanRows(rows)
	return rows
}
