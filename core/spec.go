package core

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Issue is one problem found in a definition file: the path of the value
// and what is wrong with it.
type Issue struct {
	Path    []string
	Message string
}

func issueText(issues []Issue) string {
	lines := make([]string, len(issues))
	for i, issue := range issues {
		path := "$"
		if len(issue.Path) > 0 {
			path = strings.Join(issue.Path, ".")
		}
		lines[i] = path + ": " + issue.Message
	}
	return strings.Join(lines, "\n")
}

func issuesData(issues []Issue) map[string]any {
	list := make([]any, len(issues))
	for i, issue := range issues {
		path := make([]any, len(issue.Path))
		for j, part := range issue.Path {
			path[j] = part
		}
		list[i] = map[string]any{"path": path, "message": issue.Message}
	}
	return map[string]any{"issues": list}
}

// validator collects the issues of one document.
type validator struct {
	issues []Issue
}

func (v *validator) add(path []string, message string) {
	v.issues = append(v.issues, Issue{Path: append([]string{}, path...), Message: message})
}

func (v *validator) expected(path []string, want string, value any, present bool) {
	v.add(path, fmt.Sprintf("Invalid input: expected %s, received %s", want, JSONTypeOf(value, present)))
}

func child(path []string, key string) []string {
	return append(append([]string{}, path...), key)
}

// object returns the value as an object, or reports that it is not one.
func (v *validator) object(path []string, value any, present bool) *Object {
	object, ok := value.(*Object)
	if !ok {
		v.expected(path, "object", value, present)
		return nil
	}
	return object
}

func (v *validator) optionalString(object *Object, path []string, key string) {
	if value, present := object.Get(key); present {
		if _, ok := value.(string); !ok {
			v.expected(child(path, key), "string", value, true)
		}
	}
}

func (v *validator) requiredString(object *Object, path []string, key string, nonEmpty bool) {
	value, present := object.Get(key)
	text, ok := value.(string)
	if !ok {
		v.expected(child(path, key), "string", value, present)
		return
	}
	if nonEmpty && text == "" {
		v.add(child(path, key), "Too small: expected string to have >=1 characters")
	}
}

func (v *validator) requiredBoolean(object *Object, path []string, key string) {
	value, present := object.Get(key)
	if _, ok := value.(bool); !ok {
		v.expected(child(path, key), "boolean", value, present)
	}
}

func (v *validator) stringList(object *Object, path []string, key string, required bool) {
	value, present := object.Get(key)
	if !present && !required {
		return
	}
	list, ok := value.([]any)
	if !ok {
		v.expected(child(path, key), "array", value, present)
		return
	}
	for index, item := range list {
		if _, ok := item.(string); !ok {
			v.expected(child(child(path, key), fmt.Sprint(index)), "string", item, true)
		}
	}
}

func (v *validator) enum(object *Object, path []string, key string, required bool, allowNull bool, options ...string) {
	value, present := object.Get(key)
	if !present && !required {
		return
	}
	if value == nil && present && allowNull {
		return
	}
	if text, ok := value.(string); ok {
		for _, option := range options {
			if text == option {
				return
			}
		}
	}
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = `"` + option + `"`
	}
	v.add(child(path, key), "Invalid option: expected one of "+strings.Join(quoted, "|"))
}

// schemaVersion accepts an absent version and version 1.
func (v *validator) schemaVersion(object *Object, path []string) {
	value, present := object.Get("schemaVersion")
	if !present {
		return
	}
	if number, ok := value.(float64); !ok || number != 1 {
		v.add(child(path, "schemaVersion"), "Invalid input: expected 1")
	}
}

func (v *validator) result() error {
	if len(v.issues) == 0 {
		return nil
	}
	return NewDefinitionError("schema-invalid", issueText(v.issues), issuesData(v.issues))
}

// unsupportedSchemaVersion reports a document whose schemaVersion is a
// whole number above 1.
func unsupportedSchemaVersion(input any) error {
	object, ok := input.(*Object)
	if !ok {
		return nil
	}
	value, present := object.Get("schemaVersion")
	number, isNumber := value.(float64)
	if !present || !isNumber || number != math.Trunc(number) || number <= 1 {
		return nil
	}
	issues := []Issue{{Path: []string{"schemaVersion"}, Message: "Unsupported schemaVersion; this reader supports version 1."}}
	return NewDefinitionError("schema-version-unsupported", issueText(issues), issuesData(issues))
}

func (v *validator) persona(persona *Object, path []string) {
	v.optionalString(persona, path, "profile")
	v.optionalString(persona, path, "role")
	for _, key := range []string{"permissionSetMode", "permissionSetGroupMode", "publicGroupMode", "queueMode"} {
		v.enum(persona, path, key, false, false, "additive", "sync")
	}
	for _, key := range []string{"permissionSets", "permissionSetGroups", "publicGroups", "queues"} {
		v.stringList(persona, path, key, false)
	}
	if value, present := persona.Get("userAttributes"); present {
		v.object(child(path, "userAttributes"), value, true)
	}
}

// ParsePersonaDefinitions checks a persona definitions document: an object
// with "personas", an object of personas by name.  Unknown keys are kept.
func ParsePersonaDefinitions(input any) (*Object, error) {
	if err := unsupportedSchemaVersion(input); err != nil {
		return nil, err
	}
	v := &validator{}
	doc := v.object(nil, input, true)
	if doc != nil {
		v.schemaVersion(doc, nil)
		value, present := doc.Get("personas")
		if personas := v.object([]string{"personas"}, value, present); personas != nil {
			for _, name := range personas.Keys {
				if persona := v.object([]string{"personas", name}, personas.Values[name], true); persona != nil {
					v.persona(persona, []string{"personas", name})
				}
			}
		}
	}
	return doc, v.result()
}

func (v *validator) sourceExpr(value any, present bool, path []string) {
	expr, ok := value.(*Object)
	if !ok || len(expr.Keys) != 1 {
		v.add(path, "Invalid input")
		return
	}
	if from, isFrom := expr.Get("from"); isFrom {
		if _, isText := from.(string); !isText {
			v.add(path, "Invalid input")
		}
		return
	}
	if _, isValue := expr.Get("value"); !isValue {
		v.add(path, "Invalid input")
	}
}

// ParseUsersDefinition checks a users definition document: an object with
// "users", a list of objects of User fields and the optional keys
// personas, related and relatedContext.
func ParseUsersDefinition(input any) (*Object, error) {
	if err := unsupportedSchemaVersion(input); err != nil {
		return nil, err
	}
	v := &validator{}
	doc := v.object(nil, input, true)
	if doc != nil {
		v.schemaVersion(doc, nil)
		value, present := doc.Get("users")
		users, ok := value.([]any)
		if !ok {
			v.expected([]string{"users"}, "array", value, present)
		}
		for index, item := range users {
			path := []string{"users", fmt.Sprint(index)}
			user := v.object(path, item, true)
			if user == nil {
				continue
			}
			v.stringList(user, path, "related", false)
			v.stringList(user, path, "personas", false)
			if context, present := user.Get("relatedContext"); present {
				if contextObject := v.object(child(path, "relatedContext"), context, true); contextObject != nil {
					for _, name := range contextObject.Keys {
						v.relatedContextValue(contextObject.Values[name], append(child(path, "relatedContext"), name))
					}
				}
			}
		}
	}
	return doc, v.result()
}

func (v *validator) relatedContextValue(value any, path []string) {
	switch typed := value.(type) {
	case nil, string, float64, bool:
		return
	case *Object:
		lookup := typed.Object("lookup")
		if len(typed.Keys) == 1 && lookup != nil && len(lookup.Keys) == 3 {
			sobject, _ := lookup.String("sobject")
			field, _ := lookup.String("field")
			expr, hasValue := lookup.Get("value")
			before := len(v.issues)
			v.sourceExpr(expr, hasValue, path)
			if sobject != "" && field != "" && hasValue && len(v.issues) == before {
				return
			}
			v.issues = v.issues[:before]
		}
	}
	v.add(path, "Invalid input")
}

// ParseRelatedCatalog checks a related-record catalog: an object with
// "relationships", an object of relationship definitions by name.
func ParseRelatedCatalog(input any) (*Object, error) {
	if err := unsupportedSchemaVersion(input); err != nil {
		return nil, err
	}
	v := &validator{}
	doc := v.object(nil, input, true)
	if doc != nil {
		v.schemaVersion(doc, nil)
		value, present := doc.Get("relationships")
		if relationships := v.object([]string{"relationships"}, value, present); relationships != nil {
			for _, name := range relationships.Keys {
				path := []string{"relationships", name}
				relationship := v.object(path, relationships.Values[name], true)
				if relationship == nil {
					continue
				}
				v.requiredString(relationship, path, "sobject", false)
				v.enum(relationship, path, "phase", true, false, "before", "after")
				if recordType, present := relationship.Get("recordType"); present && recordType != nil {
					if object := v.object(child(path, "recordType"), recordType, true); object != nil {
						v.requiredString(object, child(path, "recordType"), "developerName", false)
					}
				}
				match, hasMatch := relationship.Get("match")
				if object := v.object(child(path, "match"), match, hasMatch); object != nil {
					v.requiredString(object, child(path, "match"), "field", false)
					v.requiredString(object, child(path, "match"), "from", false)
				}
				fields, hasFields := relationship.Get("fields")
				if object := v.object(child(path, "fields"), fields, hasFields); object != nil {
					for _, field := range object.Keys {
						v.sourceExpr(object.Values[field], true, append(child(path, "fields"), field))
					}
				}
				if linkUser, present := relationship.Get("linkUser"); present {
					if object := v.object(child(path, "linkUser"), linkUser, true); object != nil {
						v.requiredString(object, child(path, "linkUser"), "userField", true)
						v.requiredString(object, child(path, "linkUser"), "fromRelatedField", true)
						for _, key := range object.Keys {
							if key != "userField" && key != "fromRelatedField" {
								v.add(child(path, "linkUser"), fmt.Sprintf("Unrecognized key: %q", key))
							}
						}
					}
				}
				v.enum(relationship, path, "mode", false, true, "setIfEmpty", "sync")
			}
		}
	}
	return doc, v.result()
}

// ParseSnapshot checks a snapshot document: snapshotVersion 1 and a list of
// user entries.
func ParseSnapshot(input any) (*Object, error) {
	v := &validator{}
	doc := v.object(nil, input, true)
	if doc != nil {
		if version, _ := doc.Get("snapshotVersion"); version != float64(1) {
			v.add([]string{"snapshotVersion"}, "Invalid input: expected 1")
		}
		v.optionalString(doc, nil, "capturedAt")
		v.optionalString(doc, nil, "org")
		value, present := doc.Get("users")
		users, ok := value.([]any)
		if !ok {
			v.expected([]string{"users"}, "array", value, present)
		}
		for index, item := range users {
			path := []string{"users", fmt.Sprint(index)}
			entry := v.object(path, item, true)
			if entry == nil {
				continue
			}
			for _, key := range []string{"match", "matchValue", "userId"} {
				v.requiredString(entry, path, key, true)
			}
			for _, key := range []string{"name", "username", "email", "profile", "role"} {
				v.optionalString(entry, path, key)
			}
			v.requiredBoolean(entry, path, "IsActive")
			v.requiredBoolean(entry, path, "IsFrozen")
			for _, key := range []string{"permissionSets", "permissionSetGroups", "publicGroups", "queues", "permissionSetLicenses"} {
				v.stringList(entry, path, key, true)
			}
		}
	}
	return doc, v.result()
}

// ParseText parses JSON text and checks it with parse.  label names the
// kind of document in the message for text that is not JSON.
func ParseText(text, label string, parse func(input any) (*Object, error)) (*Object, error) {
	input, err := ParseJSON([]byte(text))
	if err != nil {
		var syntax *json.SyntaxError
		_ = syntax
		issues := []Issue{{Message: "Invalid JSON: " + err.Error()}}
		return nil, NewDefinitionError("schema-invalid", label+": "+issueText(issues), issuesData(issues))
	}
	return parse(input)
}
