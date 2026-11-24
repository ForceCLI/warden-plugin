package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ForceCLI/warden-plugin/sfapi"
)

// TargetError is a user that could not be selected.
type TargetError struct {
	Key     string
	Field   string
	Value   string
	Message string
	Order   int
	Source  *RowInfo
}

// Target is a user a lifecycle command acts on.
type Target struct {
	Key      string
	ID       string
	IsActive bool
	// Name, Username, Email, Profile and Role are nil when the User row
	// lacks the value.
	Name     *string
	Username *string
	Email    *string
	Profile  *string
	Role     *string
	Field    string
	Value    string
	Order    int
}

// ParseUserFlag splits "field:value" at the first colon.
func ParseUserFlag(raw string) (field, value string, err error) {
	colon := strings.Index(raw, ":")
	if colon <= 0 || colon == len(raw)-1 {
		return "", "", NewLifecycleError("errorInvalidUserValue", LifecycleMessage("errorInvalidUserValue", raw), map[string]any{"raw": raw})
	}
	return strings.TrimSpace(raw[:colon]), raw[colon+1:], nil
}

// ResolveTargetField returns the name of a User field as the org writes
// it, "Id" for the record id, or false for an unknown field.
func ResolveTargetField(field string, fieldMap *FieldMap) (string, bool) {
	if strings.EqualFold(field, "id") {
		return "Id", true
	}
	meta, ok := fieldMap.Get(field)
	return meta.Name, ok
}

// TargetFlags are the flags that select users.
type TargetFlags struct {
	User             string
	UsersDef         string
	ExternalID       string
	InputFormat      string
	CsvListDelimiter string
}

// TargetMessages are the texts of the errors of target selection.
type TargetMessages struct {
	InvalidUserMatchField func(field string) string
	InvalidJSON           func(path, detail string) string
}

// DefaultTargetMessages uses the lifecycle texts.
func DefaultTargetMessages() TargetMessages {
	return TargetMessages{
		InvalidUserMatchField: func(field string) string { return LifecycleMessage("errorInvalidUserMatchField", field) },
		InvalidJSON:           func(path, detail string) string { return LifecycleMessage("errorInvalidJson", path, detail) },
	}
}

// BuildTargetRequests returns the match requests for --user, or for each
// entry of the users definition file.
func BuildTargetRequests(flags TargetFlags, fieldMap *FieldMap, messages TargetMessages) ([]MatchRequest, []TargetError, error) {
	if flags.User != "" {
		field, value, err := ParseUserFlag(flags.User)
		if err != nil {
			return nil, nil, err
		}
		matchField, ok := ResolveTargetField(field, fieldMap)
		if !ok {
			return nil, nil, NewLifecycleError("errorInvalidUserMatchField", messages.InvalidUserMatchField(field), map[string]any{"field": field})
		}
		return []MatchRequest{{Key: matchField + ":" + value, Field: matchField, Value: value}}, nil, nil
	}
	inputFormat := ""
	if flags.InputFormat == "json" || flags.InputFormat == "csv" {
		inputFormat = flags.InputFormat
	}
	usersDoc, err := ReadUsersDefinition(flags.UsersDef, ReadOptions{FieldMap: fieldMap, InputFormat: inputFormat, CsvListDelimiter: flags.CsvListDelimiter}, messages.InvalidJSON)
	if err != nil {
		return nil, nil, err
	}
	requests, targetErrors := ExtractDefTargets(usersDoc, flags.ExternalID, fieldMap)
	return requests, targetErrors, nil
}

// ExtractDefTargets returns a match request for each entry of a users
// definition, and an error for each entry that names no usable match.  An
// entry matches on its "match" field, or on defaultExternalID.
func ExtractDefTargets(doc *Object, defaultExternalID string, fieldMap *FieldMap) ([]MatchRequest, []TargetError) {
	users, ok := doc.List("users")
	if doc == nil || !ok {
		return nil, []TargetError{{Key: "user-def", Field: "users", Message: "users-def.json must contain a users array."}}
	}
	var requests []MatchRequest
	var targetErrors []TargetError
	for index, item := range users {
		user, isObject := item.(*Object)
		key := fmt.Sprintf("user[%d]", index+1)
		var rowInfo *RowInfo
		if isObject && user.Source != nil {
			rowInfo = user.Source
			key = fmt.Sprintf("%s:%d", rowInfo.Path, rowInfo.Line)
		}
		withRowContext := func(text string) string {
			if rowInfo != nil {
				return key + " — " + text
			}
			return text
		}
		if !isObject {
			targetErrors = append(targetErrors, TargetError{Key: key, Field: "users", Message: withRowContext("Each users-def entry must be an object."), Order: index})
			continue
		}
		rawField := defaultExternalID
		if matchRaw, _ := user.GetFold("match"); matchRaw != nil {
			if text, ok := matchRaw.(string); ok && strings.TrimSpace(text) != "" {
				rawField = text
			}
		}
		if strings.TrimSpace(rawField) == "" {
			targetErrors = append(targetErrors, TargetError{Key: key, Field: "match", Message: withRowContext("Each users-def entry must specify a match field or the --external-id flag must be set."), Order: index})
			continue
		}
		matchField := "Id"
		if !strings.EqualFold(rawField, "id") {
			meta, err := ValidateMatchField(rawField, fieldMap)
			if err != nil {
				targetErrors = append(targetErrors, TargetError{Key: key, Field: "match", Value: rawField, Message: withRowContext(err.Error()), Order: index, Source: rowInfo})
				continue
			}
			matchField = meta.Name
		}
		matchValue, _ := user.GetFold(matchField)
		value, isText := matchValue.(string)
		if !isText || value == "" {
			targetErrors = append(targetErrors, TargetError{Key: key, Field: matchField, Message: withRowContext(matchField + " must be populated on the user-def entry."), Order: index})
			continue
		}
		request := MatchRequest{Key: matchField + ":" + value, Field: matchField, Value: value, Order: index, Source: rowInfo}
		if fuzzy, _ := user.GetFold("fuzzyUsername"); matchField == "Username" && fuzzy == true {
			request.Fuzzy = true
		}
		requests = append(requests, request)
	}
	return requests, targetErrors
}

func relationshipName(id *string, relationship sfapi.Record) *string {
	if name, ok := relationship["Name"].(string); ok {
		return &name
	}
	if id != nil && *id != "" {
		return id
	}
	return nil
}

// ResolveTargets finds the user of each request.  A request that matches
// no user, or more than one, is an error.  Both results are in request
// order.
func ResolveTargets(conn sfapi.Conn, requests []MatchRequest, fieldMap *FieldMap) ([]Target, []TargetError, error) {
	resolution, err := ResolveExistingUsers(conn, requests, fieldMap)
	if err != nil {
		return nil, nil, err
	}
	var targets []Target
	var targetErrors []TargetError
	for _, request := range requests {
		matches := resolution.MatchesByRequest[request.key()]
		prefix := ""
		if request.Source != nil {
			prefix = fmt.Sprintf("%s:%d — ", request.Source.Path, request.Source.Line)
		}
		fail := func(outcome string) {
			targetErrors = append(targetErrors, TargetError{
				Key: request.key(), Field: request.Field, Value: request.Value,
				Message: fmt.Sprintf("%s%s=%q %s", prefix, request.Field, request.Value, outcome),
				Order:   request.Order, Source: request.Source,
			})
		}
		if len(matches) == 0 {
			fail("matched no user")
			continue
		}
		if len(matches) > 1 || resolution.Duplicates[MatchKey(request.Field, request.Value)] {
			fail("matched multiple users")
			continue
		}
		row := matches[0]
		targets = append(targets, Target{
			Key: request.key(), ID: row.ID, IsActive: row.IsActive != nil && *row.IsActive,
			Name: row.Name, Username: row.Username, Email: row.Email,
			Profile: relationshipName(row.ProfileID, row.Profile), Role: relationshipName(row.UserRoleID, row.UserRole),
			Field: request.Field, Value: request.Value, Order: request.Order,
		})
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Order < targets[j].Order })
	sort.SliceStable(targetErrors, func(i, j int) bool { return targetErrors[i].Order < targetErrors[j].Order })
	return targets, targetErrors, nil
}
