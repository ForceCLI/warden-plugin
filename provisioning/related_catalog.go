package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

// SourceExpr is where the value of a related-record field comes from:
// From names a source such as "user.Department", "user.Id" or
// "context.account"; otherwise Value is a literal.
type SourceExpr struct {
	HasFrom bool
	From    any
	Value   any
}

// RecordTypeRef names a record type.
type RecordTypeRef struct {
	DeveloperName string `json:"developerName"`
}

// MatchDef names the field of the related sObject that identifies a
// record and the source of its value.
type MatchDef struct {
	Field string
	From  string
}

// LinkUser copies a field of a related record to a field of the User
// before the User is saved.
type LinkUser struct {
	UserField        string `json:"userField"`
	FromRelatedField string `json:"fromRelatedField"`
}

// RelationshipDef is one relationship of a related-record catalog.
type RelationshipDef struct {
	Sobject string
	// Phase is "before" or "after" the User save.
	Phase      string
	RecordType *RecordTypeRef
	Match      MatchDef
	// FieldNames holds the configured fields in catalog order.
	FieldNames []string
	Fields     map[string]SourceExpr
	LinkUser   *LinkUser
	// Mode is "setIfEmpty" or "sync".
	Mode string
}

// Catalog is a checked related-record catalog.
type Catalog struct {
	// SchemaVersion is the schemaVersion of the document, or nil.
	SchemaVersion any
	// Names holds the relationship names in catalog order.
	Names         []string
	Relationships map[string]*RelationshipDef
}

// unwritableRelatedFields cannot be written through the Account API.
var unwritableRelatedFields = map[string]bool{"personcontactid": true}

func isNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func relatedFailure(message MessageFunc, key string, args ...string) error {
	return core.NewRelatedRecordsError(key, message(key, args...), nil)
}

type userSourceOptions struct {
	allowUserID  bool
	allowContext bool
}

func assertUserSource(from any, name, fieldName string, userFieldMap *core.FieldMap, message MessageFunc, options userSourceOptions) error {
	parsed := ParseSource(from)
	if parsed.Kind == "context" && !options.allowContext {
		return relatedFailure(message, "errorRelatedContextUnsupported", name, fieldName)
	}
	if parsed.Kind == "invalid" {
		return relatedFailure(message, "errorRelationshipInvalidFrom", name, fieldName, core.JSString(from))
	}
	if parsed.Kind == "userId" && !options.allowUserID {
		return relatedFailure(message, "errorRelationshipMatchFromUserId", name)
	}
	if parsed.Kind == "userField" {
		if _, known := userFieldMap.Get(parsed.Field); !known {
			return relatedFailure(message, "errorRelationshipUnknownUserField", name, fieldName, parsed.Field)
		}
	}
	return nil
}

func sourceExprOf(value any) (SourceExpr, bool) {
	entry, ok := value.(*core.Object)
	if !ok || entry == nil || len(entry.Keys) != 1 {
		return SourceExpr{}, false
	}
	if from, hasFrom := entry.Get("from"); hasFrom {
		return SourceExpr{HasFrom: true, From: from}, true
	}
	if literal, hasValue := entry.Get("value"); hasValue {
		return SourceExpr{Value: literal}, true
	}
	return SourceExpr{}, false
}

func assertValidFields(raw any, name string, userFieldMap *core.FieldMap, message MessageFunc, phase string, def *RelationshipDef) error {
	fields, ok := raw.(*core.Object)
	if !ok || fields == nil || len(fields.Keys) == 0 {
		return relatedFailure(message, "errorRelationshipInvalidFields", name)
	}
	def.Fields = map[string]SourceExpr{}
	for _, fieldName := range fields.Keys {
		if unwritableRelatedFields[strings.ToLower(fieldName)] {
			return relatedFailure(message, "errorRelationshipUnwritableField", name, fieldName)
		}
		source, valid := sourceExprOf(fields.Values[fieldName])
		if !valid {
			return relatedFailure(message, "errorRelationshipInvalidSource", name, fieldName)
		}
		if source.HasFrom {
			if err := assertUserSource(source.From, name, fieldName, userFieldMap, message, userSourceOptions{allowUserID: phase == "after", allowContext: true}); err != nil {
				return err
			}
		}
		def.FieldNames = append(def.FieldNames, fieldName)
		def.Fields[fieldName] = source
	}
	return nil
}

func assertValidRelationship(name string, raw any, userFieldMap *core.FieldMap, message MessageFunc) (*RelationshipDef, error) {
	source, ok := raw.(*core.Object)
	if !ok || source == nil {
		return nil, relatedFailure(message, "errorRelationshipInvalidDefinition", name)
	}
	sobject, _ := source.Get("sobject")
	if !isNonEmptyString(sobject) {
		return nil, relatedFailure(message, "errorRelationshipInvalidSobject", name)
	}
	def := &RelationshipDef{Sobject: sobject.(string)}
	phase, _ := source.Get("phase")
	if phase == nil {
		return nil, relatedFailure(message, "errorRelationshipMissingPhase", name)
	}
	if phase != "before" && phase != "after" {
		return nil, relatedFailure(message, "errorRelationshipInvalidPhase", name, core.JSString(phase))
	}
	def.Phase = phase.(string)
	linkUser, hasLinkUser := source.Get("linkUser")
	if hasLinkUser && def.Phase == "after" {
		return nil, relatedFailure(message, "errorLinkUserUnsupported", name)
	}
	if truthy(linkUser) {
		link, isObject := linkUser.(*core.Object)
		userField, _ := link.Get("userField")
		fromRelatedField, _ := link.Get("fromRelatedField")
		if !isObject || !isNonEmptyString(userField) || !isNonEmptyString(fromRelatedField) {
			return nil, relatedFailure(message, "errorRelationshipInvalidLinkUser", name)
		}
		def.LinkUser = &LinkUser{UserField: userField.(string), FromRelatedField: fromRelatedField.(string)}
	}
	match := source.Object("match")
	matchField, _ := match.Get("field")
	matchFrom, _ := match.Get("from")
	if match == nil || !isNonEmptyString(matchField) || !isNonEmptyString(matchFrom) {
		return nil, relatedFailure(message, "errorRelationshipInvalidMatch", name)
	}
	if err := assertUserSource(matchFrom, name, "match.from", userFieldMap, message, userSourceOptions{}); err != nil {
		return nil, err
	}
	def.Match = MatchDef{Field: matchField.(string), From: matchFrom.(string)}
	if recordType, _ := source.Get("recordType"); recordType != nil {
		developerName, _ := source.Object("recordType").Get("developerName")
		if !isNonEmptyString(developerName) {
			return nil, relatedFailure(message, "errorRelationshipInvalidRecordType", name)
		}
		def.RecordType = &RecordTypeRef{DeveloperName: developerName.(string)}
	}
	fields, _ := source.Get("fields")
	if err := assertValidFields(fields, name, userFieldMap, message, def.Phase, def); err != nil {
		return nil, err
	}
	def.Mode = "setIfEmpty"
	if mode, _ := source.Get("mode"); mode != nil {
		if mode != "setIfEmpty" && mode != "sync" {
			return nil, relatedFailure(message, "errorRelationshipInvalidMode", name, core.JSString(mode))
		}
		def.Mode = mode.(string)
	}
	return def, nil
}

// AssertValidRelatedCatalog checks a related-record catalog document.
// Every problem is an error of the whole run: a DefinitionError for a
// document that does not have the form of a catalog, a
// RelatedRecordsError for a relationship that cannot be used.  A nil
// message means Message.
func AssertValidRelatedCatalog(doc *core.Object, userFieldMap *core.FieldMap, message MessageFunc) (*Catalog, error) {
	if message == nil {
		message = Message
	}
	parsed, err := core.ParseRelatedCatalog(doc)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{Relationships: map[string]*RelationshipDef{}}
	catalog.SchemaVersion, _ = parsed.Get("schemaVersion")
	relationships := parsed.Object("relationships")
	if relationships == nil {
		return catalog, nil
	}
	for _, name := range relationships.Keys {
		def, err := assertValidRelationship(name, relationships.Values[name], userFieldMap, message)
		if err != nil {
			return nil, err
		}
		catalog.Names = append(catalog.Names, name)
		catalog.Relationships[name] = def
	}
	return catalog, nil
}
