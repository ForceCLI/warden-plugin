package lifecycle

import (
	"errors"
	"sort"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

const diffQueryBatchSize = 100

// DiffField compares the current and the intended value of the profile
// or the role of a user.  A nil Current or Intended is written as null.
// Compared is false for a user that was not compared; Current and
// Intended are then left out.
type DiffField struct {
	Compared bool
	Current  *string
	Intended *string
	Matches  bool
}

func (f DiffField) MarshalJSON() ([]byte, error) {
	var object jsonObject
	if f.Compared {
		object.field("current", f.Current)
		object.field("intended", f.Intended)
	}
	object.field("matches", f.Matches)
	return object.bytes()
}

// UserDiff is the comparison of one user.  Status is "compared",
// "would-create" or "failed".
type UserDiff struct {
	Key string
	// ID, UserName and Username are nil when they are not known.
	ID       *string
	UserName *string
	Username *string
	// Personas is nil for a comparison of two users.
	Personas []string
	// MatchedBy is nil when the user names no match field.
	MatchedBy   *string
	Status      string
	Profile     DiffField
	Role        DiffField
	Assignments provisioning.AssignmentDelta
	Errors      []string
}

func (u *UserDiff) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("key", u.Key)
	if u.ID != nil {
		object.field("id", *u.ID)
	}
	if u.UserName != nil {
		object.field("userName", *u.UserName)
	}
	if u.Username != nil {
		object.field("username", *u.Username)
	}
	if u.Personas != nil {
		object.field("personas", u.Personas)
	}
	object.field("matchedBy", u.MatchedBy)
	object.field("status", u.Status)
	object.field("profile", u.Profile)
	object.field("role", u.Role)
	object.field("assignments", u.Assignments)
	object.field("errors", nonNil(u.Errors))
	return object.bytes()
}

// UserDiffRow is one line of the CSV form of a diff.  Kind is "add",
// "remove", "inBoth", "onlyInOrg", "profile", "role" or "error".
type UserDiffRow struct {
	UserKey  string
	UserID   string
	UserName string
	Username string
	Category string
	Kind     string
	Value    string
	// Mode is "" for a row without an assignment mode.
	Mode string
	// ValueBefore and ValueAfter are set for profile and role rows.
	ValueBefore *string
	ValueAfter  *string
}

func (r UserDiffRow) MarshalJSON() ([]byte, error) {
	var object jsonObject
	if r.Kind == "error" {
		object.field("userName", r.UserName)
		object.field("username", r.Username)
		object.field("userKey", r.UserKey)
		object.field("userId", r.UserID)
	} else {
		object.field("userKey", r.UserKey)
		object.field("userId", r.UserID)
		object.field("userName", r.UserName)
		object.field("username", r.Username)
	}
	object.field("category", r.Category)
	object.field("kind", r.Kind)
	object.field("value", r.Value)
	if r.Mode != "" {
		object.field("mode", r.Mode)
	}
	if r.ValueBefore != nil {
		object.field("valueBefore", *r.ValueBefore)
	}
	if r.ValueAfter != nil {
		object.field("valueAfter", *r.ValueAfter)
	}
	return object.bytes()
}

// DiffSummary counts the users of a diff.  Changed counts the users that
// did not fail and differ in profile, role or an assignment to add or
// remove.
type DiffSummary struct {
	Total       int `json:"total"`
	Compared    int `json:"compared"`
	WouldCreate int `json:"wouldCreate"`
	Failed      int `json:"failed"`
	Changed     int `json:"changed"`
}

// UserDiffResult is the outcome of a diff.  Labels maps the Id of each
// record the diff names to its names.
type UserDiffResult struct {
	Summary  DiffSummary   `json:"summary"`
	Users    []*UserDiff   `json:"users"`
	Rows     []UserDiffRow `json:"rows"`
	Warnings []string      `json:"warnings"`
	Labels   *LabelMap     `json:"labels"`
}

func emptyCategory() provisioning.AssignmentCategoryDelta {
	return provisioning.AssignmentCategoryDelta{Adds: []string{}, Removes: []string{}, InBoth: []string{}, OnlyInOrg: []string{}}
}

func emptyAssignments() provisioning.AssignmentDelta {
	return provisioning.AssignmentDelta{
		PermissionSets:      emptyCategory(),
		PermissionSetGroups: emptyCategory(),
		PublicGroups:        emptyCategory(),
		Queues:              emptyCategory(),
	}
}

type namedDelta struct {
	name  string
	delta provisioning.AssignmentCategoryDelta
}

func assignmentCategories(assignments provisioning.AssignmentDelta) []namedDelta {
	return []namedDelta{
		{"permissionSets", assignments.PermissionSets},
		{"permissionSetGroups", assignments.PermissionSetGroups},
		{"publicGroups", assignments.PublicGroups},
		{"queues", assignments.Queues},
	}
}

func hasChanges(user *UserDiff) bool {
	if user.Status == "failed" {
		return false
	}
	if !user.Profile.Matches || !user.Role.Matches {
		return true
	}
	for _, category := range assignmentCategories(user.Assignments) {
		if len(category.delta.Adds) > 0 || len(category.delta.Removes) > 0 {
			return true
		}
	}
	return false
}

func summarizeDiff(users []*UserDiff) DiffSummary {
	summary := DiffSummary{Total: len(users)}
	for _, user := range users {
		switch user.Status {
		case "compared":
			summary.Compared++
		case "would-create":
			summary.WouldCreate++
		case "failed":
			summary.Failed++
		}
		if hasChanges(user) {
			summary.Changed++
		}
	}
	return summary
}

func rowsForUser(user *UserDiff) []UserDiffRow {
	rows := []UserDiffRow{}
	base := UserDiffRow{UserKey: user.Key, UserID: stringOr(user.ID, ""), UserName: stringOr(user.UserName, ""), Username: stringOr(user.Username, "")}
	for _, category := range assignmentCategories(user.Assignments) {
		for _, kind := range []struct {
			name   string
			values []string
		}{{"add", category.delta.Adds}, {"remove", category.delta.Removes}, {"inBoth", category.delta.InBoth}, {"onlyInOrg", category.delta.OnlyInOrg}} {
			for _, value := range kind.values {
				row := base
				row.Category, row.Kind, row.Value, row.Mode = category.name, kind.name, value, category.delta.Mode
				rows = append(rows, row)
			}
		}
	}
	for _, field := range []struct {
		name  string
		field DiffField
	}{{"profile", user.Profile}, {"role", user.Role}} {
		if field.field.Matches {
			continue
		}
		current, intended := stringOr(field.field.Current, ""), stringOr(field.field.Intended, "")
		row := base
		row.Category, row.Kind, row.Value = field.name, field.name, current+" -> "+intended
		row.ValueBefore, row.ValueAfter = &current, &intended
		rows = append(rows, row)
	}
	for _, text := range user.Errors {
		row := base
		row.Category, row.Kind, row.Value = "error", "error", text
		rows = append(rows, row)
	}
	return rows
}

func buildDiffResult(users []*UserDiff, warnings []string, labels *LabelMap) *UserDiffResult {
	rows := []UserDiffRow{}
	for _, user := range users {
		rows = append(rows, rowsForUser(user)...)
	}
	return &UserDiffResult{Summary: summarizeDiff(users), Users: users, Rows: rows, Warnings: nonNil(warnings), Labels: labels}
}

// validationErrorsFor returns the validation errors of a user as texts.
// The lifecycle message table has no entry for the provisioning codes,
// so each text is the code itself, as upstream.
//
// The upstream code puts the file and line of a CSV row before an error
// when the user carries them.  Its diff validates the users document
// with a parser that returns copies without that position, so a diff
// never has it; the errors here are therefore written without it.
func validationErrorsFor(user *provisioning.CanonicalUser) []string {
	texts := make([]string, 0, len(user.ValidationErrors))
	for _, validationError := range user.ValidationErrors {
		texts = append(texts, core.LifecycleMessage(validationError.Code, validationError.MessageArgs...))
	}
	return texts
}

func definitionUserName(fields *core.Object) *string {
	var parts []string
	for _, key := range []string{"FirstName", "LastName"} {
		if part, _ := fields.String(key); part != "" {
			parts = append(parts, part)
		}
	}
	return stringPointer(strings.Join(parts, " "))
}

// userRoleState is the profile and the role of a user in the org.  A nil
// Id is a null or absent value.
type userRoleState struct {
	profileID, userRoleID *string
	profileName, roleName string
}

func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func equalText(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func makeProfileRole(currentProfile, currentRole *string, target *core.Object) (profile, role DiffField) {
	intendedProfile := optionalText(target, "ProfileId")
	intendedRole := optionalText(target, "UserRoleId")
	profile = DiffField{Compared: true, Current: currentProfile, Intended: intendedProfile, Matches: intendedProfile == nil || equalText(currentProfile, intendedProfile)}
	role = DiffField{Compared: true, Current: currentRole, Intended: intendedRole, Matches: intendedRole == nil || equalText(currentRole, intendedRole)}
	return profile, role
}

// displayName is the name of a related record, or its Id, or "".
func displayName(id *string, relationship map[string]any) string {
	if name, ok := relationship["Name"].(string); ok {
		return name
	}
	return stringOr(id, "")
}

type userRoleStates struct {
	order []string
	byID  map[string]userRoleState
}

func buildLabelMap(refs *provisioning.ResolvedRefs, psaRows, groupRows []sfapi.Record, profileRoles *userRoleStates) *LabelMap {
	labels := newLabelMap()
	if refs != nil {
		for _, id := range refs.Labels.Keys() {
			labels.addReference(refs.Labels.Get(id))
		}
	}
	for _, row := range psaRows {
		if bundle := provisioning.PermissionSetAssignmentLabel(row); bundle != nil {
			labels.add(*bundle, assignmentLabelKeys)
		}
	}
	for _, row := range groupRows {
		labels.add(provisioning.GroupMemberLabel(row), assignmentLabelKeys)
	}
	for _, id := range profileRoles.order {
		state := profileRoles.byID[id]
		if state.profileID != nil {
			labels.add(LabelBundle{ID: *state.profileID, Label: state.profileName, Type: "Profile"}, profileRoleLabelKeys)
		}
		if state.userRoleID != nil {
			labels.add(LabelBundle{ID: *state.userRoleID, Label: state.roleName, Type: "UserRole"}, profileRoleLabelKeys)
		}
	}
	return labels
}

func uniqueOrdered(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func queryUserRoles(conn sfapi.Conn, ids []string) (*userRoleStates, error) {
	states := &userRoleStates{byID: map[string]userRoleState{}}
	if len(ids) == 0 {
		return states, nil
	}
	for _, idBatch := range core.Batch(uniqueOrdered(ids), diffQueryBatchSize) {
		page, err := conn.Query("SELECT Id, ProfileId, Profile.Name, UserRoleId, UserRole.Name FROM User WHERE Id IN (" + core.SoqlIn(idBatch) + ")")
		if err != nil {
			return nil, err
		}
		for _, row := range page.Records {
			id := text(row, "Id")
			state := userRoleState{}
			if profileID := text(row, "ProfileId"); profileID != "" {
				state.profileID = &profileID
			}
			if userRoleID := text(row, "UserRoleId"); userRoleID != "" {
				state.userRoleID = &userRoleID
			}
			state.profileName = displayName(state.profileID, nested(row, "Profile"))
			state.roleName = displayName(state.userRoleID, nested(row, "UserRole"))
			if _, seen := states.byID[id]; !seen {
				states.order = append(states.order, id)
			}
			states.byID[id] = state
		}
	}
	return states, nil
}

// pageRecorder keeps the rows of every query in the order the queries
// were made.
type pageRecorder struct {
	sfapi.Conn
	pages [][]sfapi.Record
}

func (r *pageRecorder) Query(soql string) (*sfapi.QueryResult, error) {
	page, err := r.Conn.Query(soql)
	if err == nil {
		r.pages = append(r.pages, page.Records)
	}
	return page, err
}

// groupByKey returns the rows grouped by the value of keyField, the
// groups in the order their first row appears.
func groupByKey(pages [][]sfapi.Record, keyField string) []sfapi.Record {
	var order []string
	byKey := map[string][]sfapi.Record{}
	for _, page := range pages {
		for _, row := range page {
			key := ""
			if value := row[keyField]; value != nil {
				key = core.JSString(value)
			}
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = append(byKey[key], row)
		}
	}
	var rows []sfapi.Record
	for _, key := range order {
		rows = append(rows, byKey[key]...)
	}
	return rows
}

// loadDiffAssignmentState loads the permission set assignments and group
// memberships of the users.  provisioning.AssignmentState keeps the rows
// in maps, which have no order, and the order of the labels of a diff
// follows the order of the rows.  The rows are therefore also returned in
// the order the upstream maps hold them: grouped by user, users in the
// order their first row was returned.  LoadAssignmentState queries the
// assignments of every batch of 100 users and then the memberships of
// every batch.
func loadDiffAssignmentState(conn sfapi.Conn, userIDs []string) (state *provisioning.AssignmentState, psaRows, groupRows []sfapi.Record, err error) {
	recorder := &pageRecorder{Conn: conn}
	state, err = provisioning.LoadAssignmentState(recorder, userIDs, &provisioning.AssignmentStateOptions{PermissionSetAssignments: true, GroupMemberships: true})
	if err != nil {
		return nil, nil, nil, err
	}
	batches := len(recorder.pages) / 2
	return state, groupByKey(recorder.pages[:batches], "AssigneeId"), groupByKey(recorder.pages[batches:], "UserOrGroupId"), nil
}

// PersonaDiffRequest names the definitions a persona diff compares the
// org with.
type PersonaDiffRequest struct {
	UsersDoc         *core.Object
	PersonasDoc      *core.Object
	UsersPath        string
	PersonasPath     string
	InputFormat      string
	CsvListDelimiter string
	ExternalID       string
	// PersonasSupplied overrides whether a personas definition counts as
	// supplied; nil means it is when a document or a path is given.
	PersonasSupplied *bool
}

func diffDefinitionMessages() core.DefinitionMessages {
	return core.DefinitionMessages{
		PersonasWithoutDefinition: func(userKey string) string {
			return core.LifecycleMessage("errorPersonasWithoutDefinition", userKey)
		},
		InvalidJSON: func(path, detail string) string { return core.LifecycleMessage("errorInvalidJson", path, detail) },
	}
}

type existingRequest struct {
	field, value string
	fuzzy        bool
}

// orderedExistingIDs returns the Ids of the users the definition entries
// matched, in the order the upstream resolution holds them: by match
// field in the order the fields are first used, exact matches before
// fuzzy username matches, and by value in the order of the entries.
// core.Resolution keeps the matches in maps, which have no order.
func orderedExistingIDs(users []*provisioning.CanonicalUser, defaultExternalIDField string, resolution *core.Resolution) []string {
	var fieldOrder []string
	valuesByField := map[string][]string{}
	addValue := func(field, value string) {
		if _, seen := valuesByField[field]; !seen {
			fieldOrder = append(fieldOrder, field)
		}
		valuesByField[field] = append(valuesByField[field], value)
	}
	var fuzzyValues []string
	for _, user := range users {
		field := user.MatchField
		if field == "" {
			field = defaultExternalIDField
		}
		if field == "" {
			continue
		}
		raw, _ := user.Fields.Get(field)
		value, usable := core.UsableMatchValue(raw)
		if !usable {
			continue
		}
		if field == "Username" && user.FuzzyUsername != nil && *user.FuzzyUsername {
			fuzzyValues = append(fuzzyValues, value)
			continue
		}
		addValue(field, value)
	}
	for _, value := range fuzzyValues {
		addValue("Username", value)
	}
	ids := []string{}
	for _, field := range fieldOrder {
		for _, value := range uniqueOrdered(valuesByField[field]) {
			if existing, found := resolution.ExistingByField[field][value]; found {
				ids = append(ids, existing.ID)
			}
		}
	}
	return ids
}

// ExecutePersonaDiff compares each user of a users definition with what
// its personas intend: profile, role, permission sets, permission set
// groups, public groups and queues.  It changes no record.
func ExecutePersonaDiff(conn sfapi.Conn, request PersonaDiffRequest) (*UserDiffResult, error) {
	source := core.DefinitionSource{
		UsersDoc:         request.UsersDoc,
		PersonasDoc:      request.PersonasDoc,
		UsersPath:        request.UsersPath,
		PersonasPath:     request.PersonasPath,
		InputFormat:      request.InputFormat,
		CsvListDelimiter: request.CsvListDelimiter,
		PersonasSupplied: request.PersonasSupplied,
	}
	var fieldMap *core.FieldMap
	var definitions *core.Definitions
	var err error
	if request.UsersDoc != nil {
		if definitions, err = core.LoadValidatedDefinitions(source, core.BuildFieldMap(nil), diffDefinitionMessages()); err != nil {
			return nil, err
		}
		if fieldMap, err = core.DescribeUserFields(conn, nil); err != nil {
			return nil, err
		}
	} else {
		if fieldMap, err = core.DescribeUserFields(conn, nil); err != nil {
			return nil, err
		}
		if definitions, err = core.LoadValidatedDefinitions(source, fieldMap, diffDefinitionMessages()); err != nil {
			return nil, err
		}
	}
	personasSupplied := definitions.PersonasSupplied
	if request.PersonasSupplied != nil {
		personasSupplied = *request.PersonasSupplied
	}
	personas := definitions.PersonasDoc.Object("personas")
	if err := provisioning.ValidatePersonaModes(personas); err != nil {
		return nil, err
	}
	if _, err := provisioning.ValidateExternalIDFieldForFlag(request.ExternalID, fieldMap); err != nil {
		return nil, err
	}
	rawUsers, _ := definitions.UsersDoc.Get("users")
	users, err := provisioning.ValidateAndCanonicalizeUsers(rawUsers, personas, fieldMap, personasSupplied, nil)
	if err != nil {
		return nil, err
	}
	defaultExternalIDField := ""
	if request.ExternalID != "" {
		if meta, ok := fieldMap.Get(request.ExternalID); ok {
			defaultExternalIDField = meta.Name
		}
	}
	var validUsers []*provisioning.CanonicalUser
	for _, user := range users {
		if len(user.ValidationErrors) == 0 {
			validUsers = append(validUsers, user)
		}
	}
	refs, err := provisioning.ResolveReferences(conn, personas, validUsers)
	if err != nil {
		return nil, err
	}
	existingResolution, err := provisioning.GetExistingUsers(conn, validUsers, provisioning.ExistingUsersOptions{DefaultExternalIDField: defaultExternalIDField, FieldMap: fieldMap})
	if err != nil {
		return nil, err
	}
	uniqueExistingIDs := uniqueOrdered(orderedExistingIDs(validUsers, defaultExternalIDField, existingResolution))
	existingProfileRoles, err := queryUserRoles(conn, uniqueExistingIDs)
	if err != nil {
		return nil, err
	}
	assignmentState, psaRows, groupRows, err := loadDiffAssignmentState(conn, uniqueExistingIDs)
	if err != nil {
		return nil, err
	}
	results := make([]*UserDiff, 0, len(users))
	for _, user := range users {
		personaNames := nonNil(user.Personas)
		if validationErrors := validationErrorsFor(user); len(validationErrors) > 0 {
			var matchedBy *string
			if user.MatchField != "" {
				matchedBy = stringPointer(user.MatchField)
			}
			results = append(results, &UserDiff{
				Key:         user.InputKey,
				UserName:    definitionUserName(user.Fields),
				Username:    optionalText(user.Fields, "Username"),
				Personas:    personaNames,
				MatchedBy:   matchedBy,
				Status:      "failed",
				Profile:     DiffField{Matches: true},
				Role:        DiffField{Matches: true},
				Assignments: emptyAssignments(),
				Errors:      validationErrors,
			})
			continue
		}
		userErrors := []string{}
		target := provisioning.BuildTarget(user, refs, &userErrors)
		var matchedBy *string
		if user.MatchField != "" {
			matchedBy = stringPointer(user.MatchField)
		} else if defaultExternalIDField != "" {
			matchedBy = stringPointer(defaultExternalIDField)
		}
		var existing *core.ExistingUser
		matchValue, hasMatchValue := "", false
		if matchedBy != nil {
			matchValue, hasMatchValue = target.String(*matchedBy)
			if hasMatchValue {
				if match, found := existingResolution.ExistingByField[*matchedBy][matchValue]; found {
					existing = &match
				}
			}
		}
		var currentProfile, currentRole *string
		if existing != nil {
			currentProfile, currentRole = nonEmpty(existing.ProfileID), nonEmpty(existing.UserRoleID)
			if profileRole, found := existingProfileRoles.byID[existing.ID]; found {
				currentProfile, currentRole = profileRole.profileID, profileRole.userRoleID
			}
		}
		if matchedBy != nil && hasMatchValue && existingResolution.Duplicates[core.MatchKey(*matchedBy, matchValue)] {
			userErrors = append(userErrors, core.LifecycleMessage("errorDuplicateExternalIdMatch", *matchedBy, matchValue))
		}
		profile, role := makeProfileRole(currentProfile, currentRole, target)
		if len(userErrors) > 0 {
			failed := &UserDiff{
				Key:         user.InputKey,
				Personas:    personaNames,
				MatchedBy:   matchedBy,
				Status:      "failed",
				Profile:     profile,
				Role:        role,
				Assignments: emptyAssignments(),
				Errors:      userErrors,
			}
			if existing != nil {
				failed.ID, failed.UserName, failed.Username = stringPointer(existing.ID), existing.Name, existing.Username
			}
			results = append(results, failed)
			continue
		}
		if existing == nil {
			assignments, err := provisioning.ComputeAssignmentDeltaFromState(&user.EffectivePersona, refs, nil, nil)
			if err != nil {
				return nil, err
			}
			results = append(results, &UserDiff{
				Key:         user.InputKey,
				UserName:    definitionUserName(user.Fields),
				Username:    optionalText(user.Fields, "Username"),
				Personas:    personaNames,
				MatchedBy:   matchedBy,
				Status:      "would-create",
				Profile:     profile,
				Role:        role,
				Assignments: assignments,
				Errors:      []string{},
			})
			continue
		}
		assignments, err := provisioning.ComputeAssignmentDeltaFromState(&user.EffectivePersona, refs, assignmentState.PsaByUserID[existing.ID], assignmentState.GroupByUserID[existing.ID])
		if err != nil {
			return nil, err
		}
		results = append(results, &UserDiff{
			Key:         user.InputKey,
			ID:          stringPointer(existing.ID),
			UserName:    existing.Name,
			Username:    existing.Username,
			Personas:    personaNames,
			MatchedBy:   matchedBy,
			Status:      "compared",
			Profile:     profile,
			Role:        role,
			Assignments: assignments,
			Errors:      []string{},
		})
	}
	return buildDiffResult(results, refs.Warnings, buildLabelMap(refs, psaRows, groupRows, existingProfileRoles)), nil
}

func sortedByLocale(ids []string) []string {
	sorted := append([]string{}, ids...)
	sort.SliceStable(sorted, func(i, j int) bool { return localeCompare(sorted[i], sorted[j]) < 0 })
	return sorted
}

// partitionIDs compares the Ids of two users.  Every Id only the first
// user has is a removal, so OnlyInOrg is empty.
func partitionIDs(currentIDs, intendedIDs []string) provisioning.AssignmentCategoryDelta {
	current, intended := uniqueOrdered(currentIDs), uniqueOrdered(intendedIDs)
	delta := emptyCategory()
	var extras, adds, inBoth []string
	for _, id := range current {
		if !contains(intended, id) {
			extras = append(extras, id)
		}
	}
	for _, id := range intended {
		if contains(current, id) {
			inBoth = append(inBoth, id)
		} else {
			adds = append(adds, id)
		}
	}
	delta.Adds = sortedByLocale(adds)
	delta.Removes = sortedByLocale(extras)
	delta.InBoth = sortedByLocale(inBoth)
	return delta
}

func parseUserFlagOrFail(raw, code string) (field, value string, err error) {
	field, value, err = core.ParseUserFlag(raw)
	if err != nil {
		return "", "", core.NewLifecycleError(code, core.LifecycleMessage(code, raw), map[string]any{"raw": raw})
	}
	return field, value, nil
}

// ExecuteUserToUserDiff compares the profile, role and assignments of one
// user with those of another.  user and against are written
// "field:value".  It changes no record.
func ExecuteUserToUserDiff(conn sfapi.Conn, user, against string) (*UserDiffResult, error) {
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	parsedUserField, userValue, err := parseUserFlagOrFail(user, "errorInvalidUserValue")
	if err != nil {
		return nil, err
	}
	parsedAgainstField, againstValue, err := parseUserFlagOrFail(against, "errorInvalidAgainstValue")
	if err != nil {
		return nil, err
	}
	userField, userFieldKnown := core.ResolveTargetField(parsedUserField, fieldMap)
	againstField, againstFieldKnown := core.ResolveTargetField(parsedAgainstField, fieldMap)
	if !userFieldKnown {
		return nil, core.NewLifecycleError("errorInvalidUserMatchField", core.LifecycleMessage("errorInvalidUserMatchField", parsedUserField), nil)
	}
	if !againstFieldKnown {
		return nil, core.NewLifecycleError("errorInvalidAgainstMatchField", core.LifecycleMessage("errorInvalidAgainstMatchField", parsedAgainstField), nil)
	}
	targets, targetErrors, err := core.ResolveTargets(conn, []core.MatchRequest{
		{Key: userField + ":" + userValue, Field: userField, Value: userValue, Order: 0},
		{Key: againstField + ":" + againstValue, Field: againstField, Value: againstValue, Order: 1},
	}, fieldMap)
	if err != nil {
		return nil, err
	}
	if len(targetErrors) > 0 {
		failed := make([]*UserDiff, 0, len(targetErrors))
		for _, targetError := range targetErrors {
			failed = append(failed, &UserDiff{
				Key:         targetError.Key,
				UserName:    stringPointer(""),
				Username:    stringPointer(""),
				MatchedBy:   stringPointer(targetError.Field),
				Status:      "failed",
				Profile:     DiffField{Matches: true},
				Role:        DiffField{Matches: true},
				Assignments: emptyAssignments(),
				Errors:      []string{targetError.Message},
			})
		}
		return buildDiffResult(failed, nil, newLabelMap()), nil
	}
	if len(targets) < 2 {
		return nil, errors.New("User resolution failed.")
	}
	target, other := targets[0], targets[1]
	state, psaRows, groupRows, err := loadDiffAssignmentState(conn, []string{target.ID, other.ID})
	if err != nil {
		return nil, err
	}
	profilesByID, err := queryUserRoles(conn, []string{target.ID, other.ID})
	if err != nil {
		return nil, err
	}
	targetAssignments := provisioning.ExtractCurrentIDs(state.PsaByUserID[target.ID], state.GroupByUserID[target.ID])
	againstAssignments := provisioning.ExtractCurrentIDs(state.PsaByUserID[other.ID], state.GroupByUserID[other.ID])
	userProfile := profilesByID.byID[target.ID]
	againstProfile := profilesByID.byID[other.ID]
	result := &UserDiff{
		Key:       target.Key,
		ID:        stringPointer(target.ID),
		UserName:  target.Name,
		Username:  target.Username,
		MatchedBy: stringPointer(target.Field),
		Status:    "compared",
		Profile: DiffField{
			Compared: true,
			Current:  userProfile.profileID,
			Intended: againstProfile.profileID,
			Matches:  equalText(userProfile.profileID, againstProfile.profileID),
		},
		Role: DiffField{
			Compared: true,
			Current:  userProfile.userRoleID,
			Intended: againstProfile.userRoleID,
			Matches:  equalText(userProfile.userRoleID, againstProfile.userRoleID),
		},
		Assignments: provisioning.AssignmentDelta{
			PermissionSets:      partitionIDs(targetAssignments.PermissionSets, againstAssignments.PermissionSets),
			PermissionSetGroups: partitionIDs(targetAssignments.PermissionSetGroups, againstAssignments.PermissionSetGroups),
			PublicGroups:        partitionIDs(targetAssignments.PublicGroups, againstAssignments.PublicGroups),
			Queues:              partitionIDs(targetAssignments.Queues, againstAssignments.Queues),
		},
		Errors: []string{},
	}
	return buildDiffResult([]*UserDiff{result}, nil, buildLabelMap(nil, psaRows, groupRows, profilesByID)), nil
}

// DiffOptions are the options of a diff.  Mode "user" compares User with
// Against; mode "persona" compares the users of a users definition with
// their personas, and with Verify returns a verdict for each user.
type DiffOptions struct {
	Mode             string
	Verify           bool
	User             string
	Against          string
	UsersPath        string
	UsersDoc         *core.Object
	PersonasPath     string
	PersonasDoc      *core.Object
	ExternalID       string
	InputFormat      string
	CsvListDelimiter string
}

// DiffRun is the outcome of RunDiff: Result for a diff, Verdicts for a
// verification.  It is written to JSON as the one that is set.
type DiffRun struct {
	Result   *UserDiffResult
	Verdicts []UserConformanceVerdict
}

func (r *DiffRun) MarshalJSON() ([]byte, error) {
	if r.Result != nil {
		return encodeJSON(r.Result)
	}
	verdicts := r.Verdicts
	if verdicts == nil {
		verdicts = []UserConformanceVerdict{}
	}
	return encodeJSON(verdicts)
}

// RunDiff runs a diff in the mode of the options.
func RunDiff(conn sfapi.Conn, options DiffOptions) (*DiffRun, error) {
	if options.Mode == "user" {
		if options.User == "" || options.Against == "" {
			return nil, errors.New("Both user and against are required in user mode.")
		}
		if options.Verify {
			return nil, core.NewLifecycleError("errorVerifyUserMode", "Verification is only available in persona mode.", nil)
		}
		result, err := ExecuteUserToUserDiff(conn, options.User, options.Against)
		if err != nil {
			return nil, err
		}
		return &DiffRun{Result: result}, nil
	}
	request := PersonaDiffRequest{
		InputFormat:      options.InputFormat,
		CsvListDelimiter: options.CsvListDelimiter,
		ExternalID:       options.ExternalID,
	}
	if options.UsersDoc != nil {
		personasSupplied := options.PersonasDoc != nil
		request.UsersDoc, request.PersonasDoc, request.PersonasSupplied = options.UsersDoc, options.PersonasDoc, &personasSupplied
	} else {
		request.UsersPath, request.PersonasPath = options.UsersPath, options.PersonasPath
	}
	result, err := ExecutePersonaDiff(conn, request)
	if err != nil {
		return nil, err
	}
	if !options.Verify {
		return &DiffRun{Result: result}, nil
	}
	return &DiffRun{Verdicts: VerifyUserDiff(result, conformanceMessage)}, nil
}
