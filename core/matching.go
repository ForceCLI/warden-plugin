package core

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/warden-plugin/sfapi"
)

const matchQueryMaxLength = 18_000

// MatchKey is the key of a match on a field value.
func MatchKey(field, value string) string {
	return field + ":" + value
}

// UsableMatchValue returns the value a match request is issued under: a
// non-empty text.  White space is not trimmed.
func UsableMatchValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != ""
}

// ValidateMatchField returns the field when it exists and can be used in a
// WHERE clause.
func ValidateMatchField(field string, fieldMap *FieldMap) (FieldMeta, error) {
	meta, ok := fieldMap.Get(field)
	if !ok || !meta.Filterable {
		return FieldMeta{}, fmt.Errorf("Invalid match field %q.", field)
	}
	return meta, nil
}

// EscapeSoqlLike escapes a value for a LIKE pattern.
func EscapeSoqlLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return strings.ReplaceAll(value, `'`, `\'`)
}

func batchesByQueryLength(values []string, render func(string) string, prefixLength, suffixLength int) [][]string {
	var batches [][]string
	var current []string
	currentLength := prefixLength + suffixLength
	for _, value := range values {
		renderedLength := len([]rune(render(value)))
		separator := 0
		if len(current) > 0 {
			separator = 1
		}
		if len(current) > 0 && currentLength+separator+renderedLength > matchQueryMaxLength {
			batches = append(batches, current)
			current = nil
			currentLength = prefixLength + suffixLength
		}
		current = append(current, value)
		if len(current) > 1 {
			currentLength++
		}
		currentLength += renderedLength
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// MatchRequest asks for the users whose field has a value.
type MatchRequest struct {
	// Key identifies the request; empty means MatchKey(Field, Value).
	Key   string
	Field string
	Value string
	// Fuzzy, for Username, also matches usernames that continue with a
	// dot and more text, such as a sandbox suffix.
	Fuzzy  bool
	Order  int
	Source *RowInfo
}

func (r MatchRequest) key() string {
	if r.Key != "" {
		return r.Key
	}
	return MatchKey(r.Field, r.Value)
}

// ExistingUser is a User row found by a match.
type ExistingUser struct {
	ID       string
	IsActive *bool
	Name     *string
	Username *string
	Email    *string
	// ProfileID and UserRoleID are nil when the row lacks the field, and
	// point to "" for a null value.
	ProfileID  *string
	Profile    sfapi.Record
	HasProfile bool
	UserRoleID *string
	UserRole   sfapi.Record
	HasRole    bool
	// Row is the whole record.
	Row sfapi.Record
}

func optionalString(row sfapi.Record, key string) *string {
	value, present := row[key]
	if !present {
		return nil
	}
	text, _ := value.(string)
	return &text
}

func toExistingUser(row sfapi.Record) ExistingUser {
	user := ExistingUser{Row: row}
	user.ID, _ = row["Id"].(string)
	if active, ok := row["IsActive"].(bool); ok {
		user.IsActive = &active
	}
	user.Name = optionalString(row, "Name")
	user.Username = optionalString(row, "Username")
	user.Email = optionalString(row, "Email")
	user.ProfileID = optionalString(row, "ProfileId")
	user.UserRoleID = optionalString(row, "UserRoleId")
	if profile, present := row["Profile"]; present {
		user.HasProfile = true
		user.Profile, _ = profile.(sfapi.Record)
	}
	if role, present := row["UserRole"]; present {
		user.HasRole = true
		user.UserRole, _ = role.(sfapi.Record)
	}
	return user
}

func distinctUsers(users []ExistingUser) []ExistingUser {
	seen := map[string]bool{}
	var result []ExistingUser
	for _, user := range users {
		if !seen[user.ID] {
			seen[user.ID] = true
			result = append(result, user)
		}
	}
	return result
}

func selectUserFields(matchField string) []string {
	fields := []string{"Id", "IsActive", "Name", "Username", "Email", "ProfileId", "Profile.Name", "UserRoleId", "UserRole.Name"}
	for _, field := range fields {
		if strings.EqualFold(field, matchField) {
			return fields
		}
	}
	return append(fields, matchField)
}

// Resolution is the outcome of matching users.
type Resolution struct {
	// ExistingByField maps a field and a value to the one user it matched.
	ExistingByField map[string]map[string]ExistingUser
	// Duplicates holds the MatchKey of each value that matched more than
	// one user.
	Duplicates map[string]bool
	// MatchesByRequest maps a request key to every user it matched.
	MatchesByRequest map[string][]ExistingUser
}

func fieldValue(row sfapi.Record, field string) string {
	value, present := row[field]
	if !present || value == nil {
		return ""
	}
	return JSString(value)
}

// ResolveExistingUsers finds the users for the requests, with one query
// for each field and one for the fuzzy username requests.  Values are
// compared without regard to letter case.
func ResolveExistingUsers(conn sfapi.Conn, requests []MatchRequest, fieldMap *FieldMap) (*Resolution, error) {
	resolution := &Resolution{ExistingByField: map[string]map[string]ExistingUser{}, Duplicates: map[string]bool{}, MatchesByRequest: map[string][]ExistingUser{}}
	var fieldOrder []string
	exactByField := map[string][]MatchRequest{}
	var fuzzyRequests []MatchRequest
	for _, request := range requests {
		if request.Field != "Id" {
			if _, err := ValidateMatchField(request.Field, fieldMap); err != nil {
				return nil, err
			}
		}
		if request.Fuzzy && request.Field == "Username" {
			fuzzyRequests = append(fuzzyRequests, request)
			continue
		}
		if _, seen := exactByField[request.Field]; !seen {
			fieldOrder = append(fieldOrder, request.Field)
		}
		exactByField[request.Field] = append(exactByField[request.Field], request)
	}
	setMatch := func(field, value string, user ExistingUser) {
		if resolution.ExistingByField[field] == nil {
			resolution.ExistingByField[field] = map[string]ExistingUser{}
		}
		resolution.ExistingByField[field][value] = user
	}
	distinctValues := func(list []MatchRequest) []string {
		seen := map[string]bool{}
		var values []string
		for _, request := range list {
			if !seen[request.Value] {
				seen[request.Value] = true
				values = append(values, request.Value)
			}
		}
		return values
	}
	for _, field := range fieldOrder {
		fieldRequests := exactByField[field]
		values := distinctValues(fieldRequests)
		prefix := "SELECT " + strings.Join(selectUserFields(field), ", ") + " FROM User WHERE " + field + " IN ("
		var rows []sfapi.Record
		for _, chunk := range batchesByQueryLength(values, func(value string) string { return "'" + Esc(value) + "'" }, len(prefix), 1) {
			page, err := conn.Query(prefix + SoqlIn(chunk) + ")")
			if err != nil {
				return nil, err
			}
			rows = append(rows, page.Records...)
		}
		if resolution.ExistingByField[field] == nil {
			resolution.ExistingByField[field] = map[string]ExistingUser{}
		}
		for _, request := range fieldRequests {
			var matches []ExistingUser
			for _, row := range rows {
				if strings.EqualFold(fieldValue(row, field), request.Value) {
					matches = append(matches, toExistingUser(row))
				}
			}
			matches = distinctUsers(matches)
			resolution.MatchesByRequest[request.key()] = matches
			if len(matches) > 1 {
				resolution.Duplicates[MatchKey(field, request.Value)] = true
			} else if len(matches) == 1 {
				setMatch(field, request.Value, matches[0])
			}
		}
	}
	if len(fuzzyRequests) > 0 {
		bases := distinctValues(fuzzyRequests)
		renderClause := func(base string) string {
			return "(Username = '" + Esc(base) + "' OR Username LIKE '" + EscapeSoqlLike(base) + ".%')"
		}
		prefix := "SELECT " + strings.Join(selectUserFields("Username"), ", ") + " FROM User WHERE "
		var rows []sfapi.Record
		for _, chunk := range batchesByQueryLength(bases, renderClause, len(prefix), 0) {
			clauses := make([]string, len(chunk))
			for i, base := range chunk {
				clauses[i] = renderClause(base)
			}
			page, err := conn.Query(prefix + strings.Join(clauses, " OR "))
			if err != nil {
				return nil, err
			}
			rows = append(rows, page.Records...)
		}
		rowsByBase := map[string][]ExistingUser{}
		basesByRowID := map[string]map[string]bool{}
		for _, base := range bases {
			normalized := strings.ToLower(base)
			var matches []ExistingUser
			for _, row := range rows {
				username := strings.ToLower(fieldValue(row, "Username"))
				if username == normalized || strings.HasPrefix(username, normalized+".") {
					matches = append(matches, toExistingUser(row))
				}
			}
			matches = distinctUsers(matches)
			rowsByBase[base] = matches
			for _, match := range matches {
				if basesByRowID[match.ID] == nil {
					basesByRowID[match.ID] = map[string]bool{}
				}
				basesByRowID[match.ID][base] = true
			}
		}
		if resolution.ExistingByField["Username"] == nil {
			resolution.ExistingByField["Username"] = map[string]ExistingUser{}
		}
		for _, base := range bases {
			matches := rowsByBase[base]
			for _, request := range fuzzyRequests {
				if request.Value == base {
					resolution.MatchesByRequest[request.key()] = matches
				}
			}
			matchesAnotherBase := false
			for _, match := range matches {
				matchesAnotherBase = matchesAnotherBase || len(basesByRowID[match.ID]) > 1
			}
			if len(matches) > 1 || matchesAnotherBase {
				resolution.Duplicates[MatchKey("Username", base)] = true
			} else if len(matches) == 1 {
				setMatch("Username", base, matches[0])
			}
		}
	}
	return resolution, nil
}
