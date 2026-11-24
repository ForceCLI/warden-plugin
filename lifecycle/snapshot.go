package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Now returns the time BuildSnapshotFile records as capturedAt.  Tests
// replace it.
var Now = time.Now

// uniqueSorted drops empty and repeated values and sorts the rest by
// UTF-16 code units.
func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return compareText(result[i], result[j]) < 0 })
	return result
}

type nameReference struct {
	id, name string
}

func resolveMissingNames(conn sfapi.Conn, table string, references []nameReference, nameField string) (map[string]string, error) {
	names := map[string]string{}
	var withoutName []string
	for _, reference := range references {
		if reference.id != "" && reference.name != "" {
			names[reference.id] = reference.name
		}
		if reference.id != "" && reference.name == "" {
			withoutName = append(withoutName, reference.id)
		}
	}
	missingIDs := uniqueSorted(withoutName)
	if len(missingIDs) == 0 {
		return names, nil
	}
	page, err := conn.Query("SELECT Id, " + nameField + " FROM " + table + " WHERE Id IN (" + core.SoqlIn(missingIDs) + ")")
	if err != nil {
		return nil, err
	}
	for _, row := range page.Records {
		if name := text(row, nameField); name != "" {
			names[text(row, "Id")] = name
		}
	}
	return names, nil
}

// flattenRows returns the rows of every user, users in Id order.
func flattenRows(byUserID map[string][]sfapi.Record) []sfapi.Record {
	userIDs := make([]string, 0, len(byUserID))
	for userID := range byUserID {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	var rows []sfapi.Record
	for _, userID := range userIDs {
		rows = append(rows, byUserID[userID]...)
	}
	return rows
}

func isDirectPermissionSet(row sfapi.Record) bool {
	return row["PermissionSetGroupId"] == nil && !isProfileOwned(row)
}

func isGroupAssignment(row sfapi.Record) bool {
	return row["PermissionSetGroupId"] != nil
}

func mappedNames(rows []sfapi.Record, keep func(row sfapi.Record) bool, idField string, idToName map[string]string) []any {
	var names []string
	for _, row := range rows {
		if keep == nil || keep(row) {
			names = append(names, idToName[text(row, idField)])
		}
	}
	return toAnyList(uniqueSorted(names))
}

func toAnyList(values []string) []any {
	list := make([]any, len(values))
	for i, value := range values {
		list[i] = value
	}
	return list
}

func hasGroupType(groupType string) func(row sfapi.Record) bool {
	return func(row sfapi.Record) bool { return text(nested(row, "Group"), "Type") == groupType }
}

// BuildSnapshotFile returns the snapshot document of the targets: their
// identity, whether they are active and frozen, and the names of their
// permission sets, permission set groups, public groups, queues and
// permission set licenses.  Permission sets a profile owns are left out.
// A name the assignment rows lack is queried by Id.  An empty org is left
// out.
func BuildSnapshotFile(conn sfapi.Conn, targets []core.Target, state *provisioning.AssignmentState, org string) (*core.Object, error) {
	psaRows := flattenRows(state.PsaByUserID)
	groupRows := flattenRows(state.GroupByUserID)
	pslRows := flattenRows(state.PslByUserID)
	var permissionSetReferences, permissionSetGroupReferences, groupReferences, licenseReferences []nameReference
	for _, row := range psaRows {
		if isDirectPermissionSet(row) {
			permissionSetReferences = append(permissionSetReferences, nameReference{text(row, "PermissionSetId"), text(nested(row, "PermissionSet"), "Name")})
		}
	}
	for _, row := range psaRows {
		if isGroupAssignment(row) {
			permissionSetGroupReferences = append(permissionSetGroupReferences, nameReference{text(row, "PermissionSetGroupId"), text(nested(row, "PermissionSetGroup"), "DeveloperName")})
		}
	}
	for _, row := range groupRows {
		groupReferences = append(groupReferences, nameReference{text(row, "GroupId"), text(nested(row, "Group"), "DeveloperName")})
	}
	for _, row := range pslRows {
		licenseReferences = append(licenseReferences, nameReference{text(row, "PermissionSetLicenseId"), text(nested(row, "PermissionSetLicense"), "DeveloperName")})
	}
	permissionSetNames, err := resolveMissingNames(conn, "PermissionSet", permissionSetReferences, "Name")
	if err != nil {
		return nil, err
	}
	permissionSetGroupNames, err := resolveMissingNames(conn, "PermissionSetGroup", permissionSetGroupReferences, "DeveloperName")
	if err != nil {
		return nil, err
	}
	groupNames, err := resolveMissingNames(conn, "Group", groupReferences, "DeveloperName")
	if err != nil {
		return nil, err
	}
	permissionSetLicenseNames, err := resolveMissingNames(conn, "PermissionSetLicense", licenseReferences, "DeveloperName")
	if err != nil {
		return nil, err
	}
	users := []any{}
	for _, target := range targets {
		targetPsaRows := state.PsaByUserID[target.ID]
		targetGroupRows := state.GroupByUserID[target.ID]
		targetPslRows := state.PslByUserID[target.ID]
		entry := core.NewObject()
		entry.Set("match", target.Field)
		entry.Set("matchValue", target.Value)
		entry.Set("userId", target.ID)
		for _, optional := range []struct {
			key   string
			value *string
		}{{"name", target.Name}, {"username", target.Username}, {"email", target.Email}, {"profile", target.Profile}, {"role", target.Role}} {
			if optional.value != nil {
				entry.Set(optional.key, *optional.value)
			}
		}
		entry.Set("IsActive", target.IsActive)
		isFrozen := false
		if frozen := loginIsFrozen(firstRow(state.UserLoginByUserID[target.ID])); frozen != nil {
			isFrozen = *frozen
		}
		entry.Set("IsFrozen", isFrozen)
		entry.Set("permissionSets", mappedNames(targetPsaRows, isDirectPermissionSet, "PermissionSetId", permissionSetNames))
		entry.Set("permissionSetGroups", mappedNames(targetPsaRows, isGroupAssignment, "PermissionSetGroupId", permissionSetGroupNames))
		entry.Set("publicGroups", mappedNames(targetGroupRows, hasGroupType("Regular"), "GroupId", groupNames))
		entry.Set("queues", mappedNames(targetGroupRows, hasGroupType("Queue"), "GroupId", groupNames))
		entry.Set("permissionSetLicenses", mappedNames(targetPslRows, nil, "PermissionSetLicenseId", permissionSetLicenseNames))
		users = append(users, entry)
	}
	snapshot := core.NewObject()
	snapshot.Set("snapshotVersion", float64(1))
	snapshot.Set("capturedAt", Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if org != "" {
		snapshot.Set("org", org)
	}
	snapshot.Set("users", users)
	return snapshot, nil
}

// SnapshotCsvColumns are the columns of a snapshot CSV file.
var SnapshotCsvColumns = []string{
	"snapshotVersion",
	"capturedAt",
	"org",
	"match",
	"matchValue",
	"userId",
	"userName",
	"username",
	"email",
	"profile",
	"role",
	"isActive",
	"isFrozen",
	"category",
	"name",
}

// snapshotAssignmentCategories pairs each list of a snapshot entry with
// its category in the CSV form.
var snapshotAssignmentCategories = [][2]string{
	{"permissionSets", "permissionSet"},
	{"permissionSetGroups", "permissionSetGroup"},
	{"publicGroups", "publicGroup"},
	{"queues", "queue"},
	{"permissionSetLicenses", "permissionSetLicense"},
}

const emptySnapshotCategory = "emptySnapshot"

// snapshotEntry is the typed form of one user of a snapshot document.
type snapshotEntry struct {
	match, matchValue, userID            string
	name, username, email, profile, role *string
	isActive, isFrozen                   bool
	lists                                map[string][]string
}

func optionalText(object *core.Object, key string) *string {
	if value, ok := object.String(key); ok {
		return &value
	}
	return nil
}

func readSnapshotEntry(entry *core.Object) snapshotEntry {
	read := snapshotEntry{lists: map[string][]string{}}
	read.match, _ = entry.String("match")
	read.matchValue, _ = entry.String("matchValue")
	read.userID, _ = entry.String("userId")
	read.name = optionalText(entry, "name")
	read.username = optionalText(entry, "username")
	read.email = optionalText(entry, "email")
	read.profile = optionalText(entry, "profile")
	read.role = optionalText(entry, "role")
	isActive, _ := entry.Get("IsActive")
	read.isActive = isActive == true
	isFrozen, _ := entry.Get("IsFrozen")
	read.isFrozen = isFrozen == true
	for _, category := range snapshotAssignmentCategories {
		read.lists[category[0]] = nonNil(entry.Strings(category[0]))
	}
	return read
}

func snapshotEntries(snapshot *core.Object) []snapshotEntry {
	users, _ := snapshot.List("users")
	entries := make([]snapshotEntry, 0, len(users))
	for _, item := range users {
		entry, _ := item.(*core.Object)
		entries = append(entries, readSnapshotEntry(entry))
	}
	return entries
}

// encodeSnapshotCsvValue doubles an apostrophe at the start of a value.
// One apostrophe at the start is the prefix the CSV writer puts before a
// value a spreadsheet would read as a formula.
func encodeSnapshotCsvValue(value string) string {
	if strings.HasPrefix(value, "'") {
		return "'" + value
	}
	return value
}

// SerializeSnapshotCsv writes a snapshot as CSV: one row for each
// assignment of each user, a "none" row for a user without assignments,
// and one "emptySnapshot" row for a snapshot without users.  Every row
// repeats the version, the capture time and the org.
func SerializeSnapshotCsv(snapshot *core.Object) string {
	version, _ := snapshot.Get("snapshotVersion")
	capturedAt, _ := snapshot.String("capturedAt")
	org, _ := snapshot.String("org")
	entries := snapshotEntries(snapshot)
	if len(entries) == 0 {
		return core.SerializeCsv([]map[string]any{{
			"snapshotVersion": version,
			"capturedAt":      encodeSnapshotCsvValue(capturedAt),
			"org":             encodeSnapshotCsvValue(org),
			"match":           "",
			"matchValue":      "",
			"userId":          "",
			"userName":        "",
			"username":        "",
			"email":           "",
			"profile":         "",
			"role":            "",
			"isActive":        "",
			"isFrozen":        "",
			"category":        emptySnapshotCategory,
			"name":            "",
		}}, SnapshotCsvColumns)
	}
	rows := []map[string]any{}
	for _, user := range entries {
		type assignment struct{ category, name string }
		var assignments []assignment
		for _, category := range snapshotAssignmentCategories {
			for _, name := range uniqueSorted(user.lists[category[0]]) {
				assignments = append(assignments, assignment{category[1], name})
			}
		}
		if len(assignments) == 0 {
			assignments = []assignment{{"none", ""}}
		}
		for _, item := range assignments {
			rows = append(rows, map[string]any{
				"snapshotVersion": version,
				"capturedAt":      encodeSnapshotCsvValue(capturedAt),
				"org":             encodeSnapshotCsvValue(org),
				"match":           encodeSnapshotCsvValue(user.match),
				"matchValue":      encodeSnapshotCsvValue(user.matchValue),
				"userId":          encodeSnapshotCsvValue(user.userID),
				"userName":        encodeSnapshotCsvValue(stringOr(user.name, "")),
				"username":        encodeSnapshotCsvValue(stringOr(user.username, "")),
				"email":           encodeSnapshotCsvValue(stringOr(user.email, "")),
				"profile":         encodeSnapshotCsvValue(stringOr(user.profile, "")),
				"role":            encodeSnapshotCsvValue(stringOr(user.role, "")),
				"isActive":        user.isActive,
				"isFrozen":        user.isFrozen,
				"category":        encodeSnapshotCsvValue(item.category),
				"name":            encodeSnapshotCsvValue(item.name),
			})
		}
	}
	return core.SerializeCsv(rows, SnapshotCsvColumns)
}

func snapshotCsvValue(cells []string, indexes map[string]int, column string) string {
	value := ""
	if index, ok := indexes[column]; ok && index < len(cells) {
		value = cells[index]
	}
	if strings.HasPrefix(value, "''") {
		return value[1:]
	}
	return core.RestoreCsvFormula(value)
}

func parseSnapshotBoolean(value string, rowLine int, field string) (bool, error) {
	switch value {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("Snapshot CSV row %d must contain %s as true or false.", rowLine, field)
}

// isVersionOne reports whether a cell is a number equal to 1 by the
// number conversion of JavaScript.
func isVersionOne(cell string) bool {
	trimmed := strings.TrimSpace(cell)
	if strings.ContainsAny(trimmed, "_pPiInN") {
		return false
	}
	if len(trimmed) > 2 && (strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") || strings.HasPrefix(trimmed, "0b") || strings.HasPrefix(trimmed, "0B") || strings.HasPrefix(trimmed, "0o") || strings.HasPrefix(trimmed, "0O")) {
		number, err := strconv.ParseInt(trimmed, 0, 64)
		return err == nil && number == 1
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	return err == nil && number == 1
}

type csvUserState struct {
	entry                  *core.Object
	isActive, isFrozen     bool
	hasNone, hasAssignment bool
	rowLine                int
}

func deserializeSnapshotCsvUnchecked(source, path string) (*core.Object, error) {
	rows, err := core.ReadCsvRows(source, path, ',')
	if err != nil {
		return nil, err
	}
	columnCount := len(SnapshotCsvColumns)
	if len(rows) == 0 {
		return nil, fmt.Errorf("Snapshot CSV %s must contain a header and at least one user row.", path)
	}
	header := rows[0]
	if len(header.Cells) != columnCount {
		return nil, fmt.Errorf("Snapshot CSV %s:%d must contain %d columns in the snapshot format.", path, header.Line, columnCount)
	}
	indexes := map[string]int{}
	for index, cell := range header.Cells {
		column := strings.TrimSpace(cell)
		if !contains(SnapshotCsvColumns, column) {
			return nil, fmt.Errorf(`Snapshot CSV %s:%d has unknown column "%s".`, path, header.Line, cell)
		}
		if _, repeated := indexes[column]; repeated {
			return nil, fmt.Errorf(`Snapshot CSV %s:%d repeats column "%s".`, path, header.Line, cell)
		}
		indexes[column] = index
	}
	for _, column := range SnapshotCsvColumns {
		if _, present := indexes[column]; !present {
			return nil, fmt.Errorf(`Snapshot CSV %s:%d is missing column "%s".`, path, header.Line, column)
		}
	}
	if len(rows) == 1 {
		return nil, fmt.Errorf("Snapshot CSV %s must contain an empty-snapshot row with metadata.", path)
	}
	type metadata struct {
		capturedAt, org string
		rowLine         int
	}
	var first *metadata
	var userKeys []string
	users := map[string]*csvUserState{}
	for _, row := range rows[1:] {
		if len(row.Cells) != columnCount {
			return nil, fmt.Errorf("Snapshot CSV %s:%d must contain %d cells but found %d.", path, row.Line, columnCount, len(row.Cells))
		}
		value := func(column string) string { return snapshotCsvValue(row.Cells, indexes, column) }
		capturedAt, org := value("capturedAt"), value("org")
		if !isVersionOne(value("snapshotVersion")) {
			return nil, fmt.Errorf("Snapshot CSV %s:%d must contain snapshotVersion 1.", path, row.Line)
		}
		if first == nil {
			first = &metadata{capturedAt: capturedAt, org: org, rowLine: row.Line}
		} else {
			if first.capturedAt != capturedAt {
				return nil, fmt.Errorf("Snapshot CSV %s:%d conflicts with metadata from row %d for capturedAt.", path, row.Line, first.rowLine)
			}
			if first.org != org {
				return nil, fmt.Errorf("Snapshot CSV %s:%d conflicts with metadata from row %d for org.", path, row.Line, first.rowLine)
			}
		}
		category, name := value("category"), value("name")
		if category == emptySnapshotCategory {
			if len(rows) != 2 {
				return nil, fmt.Errorf("Snapshot CSV %s:%d empty-snapshot row must be the only data row.", path, row.Line)
			}
			for _, column := range []string{"match", "matchValue", "userId", "userName", "username", "email", "profile", "role", "isActive", "isFrozen", "name"} {
				if value(column) != "" {
					return nil, fmt.Errorf("Snapshot CSV %s:%d empty-snapshot row must leave %s empty.", path, row.Line, column)
				}
			}
			continue
		}
		match, matchValue, userID := value("match"), value("matchValue"), value("userId")
		isActive, err := parseSnapshotBoolean(value("isActive"), row.Line, "isActive")
		if err != nil {
			return nil, err
		}
		isFrozen, err := parseSnapshotBoolean(value("isFrozen"), row.Line, "isFrozen")
		if err != nil {
			return nil, err
		}
		if match == "" || matchValue == "" || userID == "" {
			return nil, fmt.Errorf("Snapshot CSV %s:%d must include match, matchValue, and userId.", path, row.Line)
		}
		assignmentField := ""
		for _, pair := range snapshotAssignmentCategories {
			if pair[1] == category {
				assignmentField = pair[0]
				break
			}
		}
		if category != "none" && assignmentField == "" {
			return nil, fmt.Errorf(`Snapshot CSV %s:%d has unknown category "%s".`, path, row.Line, category)
		}
		if category == "none" && name != "" {
			return nil, fmt.Errorf("Snapshot CSV %s:%d must leave name empty for category none.", path, row.Line)
		}
		if assignmentField != "" && name == "" {
			return nil, fmt.Errorf("Snapshot CSV %s:%d must include name for category %s.", path, row.Line, category)
		}
		keyBytes, _ := encodeJSON([]string{match, matchValue, userID})
		key := string(keyBytes)
		state := users[key]
		if state == nil {
			entry := core.NewObject()
			entry.Set("match", match)
			entry.Set("matchValue", matchValue)
			entry.Set("userId", userID)
			for _, identity := range [][2]string{{"name", "userName"}, {"username", "username"}, {"email", "email"}, {"profile", "profile"}, {"role", "role"}} {
				if text := value(identity[1]); text != "" {
					entry.Set(identity[0], text)
				}
			}
			entry.Set("IsActive", isActive)
			entry.Set("IsFrozen", isFrozen)
			for _, pair := range snapshotAssignmentCategories {
				entry.Set(pair[0], []any{})
			}
			state = &csvUserState{entry: entry, isActive: isActive, isFrozen: isFrozen, rowLine: row.Line}
			users[key] = state
			userKeys = append(userKeys, key)
		}
		if state.isActive != isActive || state.isFrozen != isFrozen {
			return nil, fmt.Errorf("Snapshot CSV %s:%d conflicts with lifecycle state from row %d.", path, row.Line, state.rowLine)
		}
		for _, field := range []string{"userName", "username", "email", "profile", "role"} {
			text := value(field)
			entryField := field
			if field == "userName" {
				entryField = "name"
			}
			existing, _ := state.entry.String(entryField)
			if text != "" && existing != "" && text != existing {
				return nil, fmt.Errorf("Snapshot CSV %s:%d conflicts with %s from row %d.", path, row.Line, field, state.rowLine)
			}
			if text != "" && existing == "" {
				state.entry.Set(entryField, text)
			}
		}
		if category == "none" {
			if state.hasAssignment {
				return nil, fmt.Errorf("Snapshot CSV %s:%d mixes none with assignments.", path, row.Line)
			}
			state.hasNone = true
		} else if assignmentField != "" {
			if state.hasNone {
				return nil, fmt.Errorf("Snapshot CSV %s:%d mixes assignments with none.", path, row.Line)
			}
			state.hasAssignment = true
			state.entry.Set(assignmentField, toAnyList(uniqueSorted(append(state.entry.Strings(assignmentField), name))))
		}
	}
	snapshot := core.NewObject()
	snapshot.Set("snapshotVersion", float64(1))
	if first != nil && first.capturedAt != "" {
		snapshot.Set("capturedAt", first.capturedAt)
	}
	if first != nil && first.org != "" {
		snapshot.Set("org", first.org)
	}
	entries := []any{}
	for _, key := range userKeys {
		entries = append(entries, users[key].entry)
	}
	snapshot.Set("users", entries)
	return AssertSnapshotFile(snapshot)
}

func isLifecycleError(err error) bool {
	wardenError, ok := core.AsWardenError(err)
	return ok && wardenError.Name == "LifecycleError"
}

func snapshotValidationError(cause error, path string) error {
	data := map[string]any{"cause": cause}
	if path != "" {
		data = map[string]any{"path": path, "cause": cause}
	}
	return core.NewLifecycleError("errorInvalidSnapshot", cause.Error(), data)
}

// DeserializeSnapshotCsv reads a snapshot from CSV text.  path names the
// source in messages; an empty path is written "<snapshot>".  A problem
// is a LifecycleError with the code errorInvalidSnapshot.
func DeserializeSnapshotCsv(source, path string) (*core.Object, error) {
	if path == "" {
		path = "<snapshot>"
	}
	snapshot, err := deserializeSnapshotCsvUnchecked(source, path)
	if err != nil {
		if isLifecycleError(err) {
			return nil, err
		}
		return nil, snapshotValidationError(err, path)
	}
	return snapshot, nil
}

// MarshalSnapshot writes a snapshot as JSON indented by two spaces, with
// a line break at the end.
func MarshalSnapshot(snapshot *core.Object) ([]byte, error) {
	compact, err := encodeJSON(snapshot)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// WriteSnapshotFile writes a snapshot to path, as CSV when the path ends
// in .csv or .tsv and as JSON otherwise.  It creates the directory of the
// path.
func WriteSnapshotFile(path string, snapshot *core.Object) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	var contents []byte
	if core.DetectInputFormat(path, "") == "csv" {
		contents = []byte(SerializeSnapshotCsv(snapshot) + "\n")
	} else {
		var err error
		if contents, err = MarshalSnapshot(snapshot); err != nil {
			return err
		}
	}
	return os.WriteFile(path, contents, 0o666)
}

// ReadSnapshotFile reads a snapshot from a JSON file, or from a CSV file
// when the path ends in .csv or .tsv.  Content that is not a snapshot is
// a LifecycleError with the code errorInvalidSnapshot.
func ReadSnapshotFile(path string) (*core.Object, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if core.DetectInputFormat(path, "") == "csv" {
		return DeserializeSnapshotCsv(string(source), path)
	}
	value, err := core.ParseJSON(source)
	if err != nil {
		return nil, snapshotValidationError(err, path)
	}
	return AssertSnapshotFile(value)
}

var snapshotFileKeys = []string{"snapshotVersion", "capturedAt", "org", "users"}

var snapshotEntryKeys = []string{"match", "matchValue", "userId", "name", "username", "email", "profile", "role", "IsActive", "IsFrozen", "permissionSets", "permissionSetGroups", "publicGroups", "queues", "permissionSetLicenses"}

// schemaOrder returns a copy of an object with the known keys first, in
// the order of the snapshot schema, and the other keys after them.  The
// upstream parser returns its objects in that order.
func schemaOrder(object *core.Object, known []string) *core.Object {
	ordered := core.NewObject()
	for _, key := range known {
		if value, present := object.Get(key); present {
			ordered.Set(key, value)
		}
	}
	for _, key := range object.Keys {
		if !contains(known, key) {
			ordered.Set(key, object.Values[key])
		}
	}
	ordered.Source = object.Source
	return ordered
}

// ParseSnapshotDocument checks a snapshot document and returns it with
// its keys in schema order.
func ParseSnapshotDocument(value any) (*core.Object, error) {
	parsed, err := core.ParseSnapshot(value)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("snapshot must be an object")
	}
	snapshot := schemaOrder(parsed, snapshotFileKeys)
	users, _ := snapshot.List("users")
	entries := make([]any, len(users))
	for i, item := range users {
		entries[i] = item
		if entry, ok := item.(*core.Object); ok {
			entries[i] = schemaOrder(entry, snapshotEntryKeys)
		}
	}
	snapshot.Set("users", entries)
	return snapshot, nil
}

// AssertSnapshotFile checks a snapshot document.  A document that is not
// a snapshot is a LifecycleError with the code errorInvalidSnapshot.
func AssertSnapshotFile(value any) (*core.Object, error) {
	snapshot, err := ParseSnapshotDocument(value)
	if err != nil {
		if isLifecycleError(err) {
			return nil, err
		}
		return nil, snapshotValidationError(err, "")
	}
	return snapshot, nil
}

// SnapshotToLifecycleResult returns the command result of a snapshot that
// was written: each user unchanged with the notice "snapshotWritten".
func SnapshotToLifecycleResult(file *core.Object) *Result {
	users := []*UserResult{}
	for _, entry := range snapshotEntries(file) {
		id, isActive, isFrozen := entry.userID, entry.isActive, entry.isFrozen
		users = append(users, &UserResult{
			Key:      entry.match + ":" + entry.matchValue,
			ID:       &id,
			Name:     entry.name,
			Username: entry.username,
			IsActive: &isActive,
			IsFrozen: &isFrozen,
			Status:   "unchanged",
			Actions:  []Notice{MakeNotice("snapshotWritten", nil, nil, nil)},
			Skipped:  []Notice{},
			Warnings: []string{},
			Errors:   []string{},
		})
	}
	return &Result{Summary: SummarizeLifecycle(users), Users: users}
}

// SnapshotOptions are the options of a snapshot.  Org is recorded in the
// snapshot when it is not empty.
type SnapshotOptions struct {
	TargetOptions
	Org string
}

// RunSnapshot selects the users and returns their snapshot document.  A
// user that cannot be selected is an error.
func RunSnapshot(conn sfapi.Conn, options SnapshotOptions) (*core.Object, error) {
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	targets, selectionErrors, err := resolveTargetSelection(conn, options.TargetOptions, fieldMap)
	if err != nil {
		return nil, err
	}
	if len(selectionErrors) > 0 {
		messages := make([]string, len(selectionErrors))
		for i, selectionError := range selectionErrors {
			messages[i] = selectionError.Message
		}
		return nil, errors.New(strings.Join(messages, "; "))
	}
	targetIDs := make([]string, len(targets))
	for i, target := range targets {
		targetIDs[i] = target.ID
	}
	state, err := provisioning.LoadAssignmentState(conn, targetIDs, nil)
	if err != nil {
		return nil, err
	}
	return BuildSnapshotFile(conn, targets, state, options.Org)
}
