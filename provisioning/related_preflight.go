package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// PreflightResult says which selected relationships can be used in this
// org.
type PreflightResult struct {
	Eligible map[string]bool
	// Ineligible maps a relationship to the reason it is skipped.
	Ineligible *OrderedMap[string]
	// Warnings has one line for each ineligible relationship.
	Warnings []string
	// FieldsBySobject maps an sObject name in lower case to its fields.
	FieldsBySobject            map[string]*core.FieldMap
	RecordTypeIDByRelationship map[string]string
}

// EmptyPreflightResult returns a result without relationships.
func EmptyPreflightResult() *PreflightResult {
	return &PreflightResult{
		Eligible:                   map[string]bool{},
		Ineligible:                 &OrderedMap[string]{},
		Warnings:                   []string{},
		FieldsBySobject:            map[string]*core.FieldMap{},
		RecordTypeIDByRelationship: map[string]string{},
	}
}

// sobjectAliases returns the values RecordType.SobjectType can have for
// an sObject.  A person account record type has read "Account" in the
// orgs that were checked; "PersonAccount" is accepted as well.
func sobjectAliases(sobject string) []string {
	if strings.ToLower(sobject) == "account" {
		return []string{"Account", "PersonAccount"}
	}
	return []string{sobject}
}

type recordTypeRequest struct {
	sobject       string
	developerName string
}

// queryRecordTypes selects IsPersonType only when person accounts are
// enabled, because the column does not exist otherwise and the query
// would fail.
func queryRecordTypes(conn sfapi.Conn, requests []recordTypeRequest, personAccountsEnabled bool) ([]sfapi.Record, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	var sobjects, developerNames []string
	for _, request := range requests {
		sobjects = append(sobjects, sobjectAliases(request.sobject)...)
		developerNames = append(developerNames, request.developerName)
	}
	columns := []string{"Id", "DeveloperName", "SobjectType", "IsActive"}
	if personAccountsEnabled {
		columns = append(columns, "IsPersonType")
	}
	soql := "SELECT " + strings.Join(columns, ", ") + " FROM RecordType " +
		"WHERE SobjectType IN (" + core.SoqlIn(unique(sobjects)) + ") AND DeveloperName IN (" + core.SoqlIn(unique(developerNames)) + ")"
	page, err := conn.Query(soql)
	if err != nil {
		return nil, err
	}
	return page.Records, nil
}

// hasPersonAccounts reports whether the Account describe has the field
// IsPersonAccount, which exists only when person accounts are enabled.
func hasPersonAccounts(described *core.SobjectInfo) bool {
	if described == nil {
		return false
	}
	_, ok := described.Fields.Get("ispersonaccount")
	return ok
}

func configuredFields(def *RelationshipDef) []string {
	return append([]string{def.Match.Field}, def.FieldNames...)
}

func isFalse(flag *bool) bool {
	return flag != nil && !*flag
}

func isTrue(flag *bool) bool {
	return flag != nil && *flag
}

func isUniqueFilterableField(meta core.FieldMeta, known bool) bool {
	return known && meta.Filterable && (meta.ExternalID || isTrue(meta.Unique))
}

// IsEligibleLookupField reports whether a field can identify one record
// in a context lookup: readable, filterable and an External ID or unique.
func IsEligibleLookupField(meta core.FieldMeta, known bool) bool {
	return isUniqueFilterableField(meta, known) && !isFalse(meta.Readable)
}

func checkFieldMetadata(def *RelationshipDef, fieldMap *core.FieldMap, message MessageFunc) string {
	var missing, unreadable, unwritable []string
	for _, field := range configuredFields(def) {
		meta, known := fieldMap.Get(field)
		if !known {
			missing = append(missing, field)
		} else if isFalse(meta.Readable) {
			unreadable = append(unreadable, field)
		}
	}
	for _, field := range def.FieldNames {
		if meta, known := fieldMap.Get(field); known && !meta.Createable && !meta.Updateable {
			unwritable = append(unwritable, field)
		}
	}
	if len(missing) > 0 {
		return message("errorRelatedUnknownFields", def.Sobject, strings.Join(missing, ", "))
	}
	if matchMeta, known := fieldMap.Get(def.Match.Field); !isUniqueFilterableField(matchMeta, known) {
		return message("errorRelatedMatchFieldNotUnique", def.Match.Field, def.Sobject)
	}
	if len(unreadable) > 0 {
		return message("errorRelatedFieldsNotReadable", def.Sobject, strings.Join(unreadable, ", "))
	}
	if len(unwritable) > 0 {
		return message("errorRelatedFieldsNotWritable", def.Sobject, strings.Join(unwritable, ", "))
	}
	return ""
}

// PreflightOptions is what RunRelatedPreflight checks.
type PreflightOptions struct {
	Conn    sfapi.Conn
	Catalog *Catalog
	// Selected holds the relationship names the users select.
	Selected     []string
	Cache        core.DescribeCache
	UserFieldMap *core.FieldMap
	// Message is the text lookup; nil means Message.
	Message MessageFunc
}

// RunRelatedPreflight checks each selected relationship once: its sObject
// can be described and queried, its fields exist and can be read and
// written, its match field identifies one record, and its record type is
// available.  A relationship that fails a check is skipped and adds one
// warning; it is not an error.
func RunRelatedPreflight(options PreflightOptions) (*PreflightResult, error) {
	message := options.Message
	if message == nil {
		message = Message
	}
	catalog := options.Catalog
	result := EmptyPreflightResult()
	var names []string
	for _, name := range unique(options.Selected) {
		if catalog.Relationships[name] != nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return result, nil
	}
	describedByName := map[string]*core.SobjectInfo{}
	personAccountsEnabled := false
	var recordTypeRequests []recordTypeRequest
	for _, name := range names {
		def := catalog.Relationships[name]
		// A describe that fails makes the relationship ineligible below.
		described, err := core.DescribeSobject(options.Conn, def.Sobject, options.Cache)
		if err != nil {
			described = nil
		}
		describedByName[name] = described
		if def.RecordType != nil {
			recordTypeRequests = append(recordTypeRequests, recordTypeRequest{sobject: def.Sobject, developerName: def.RecordType.DeveloperName})
		}
		if strings.ToLower(def.Sobject) == "account" && hasPersonAccounts(described) {
			personAccountsEnabled = true
		}
	}
	recordTypeRows, err := queryRecordTypes(options.Conn, recordTypeRequests, personAccountsEnabled)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		def := catalog.Relationships[name]
		described := describedByName[name]
		if described == nil {
			result.Ineligible.Set(name, message("errorRelatedSobjectUnavailable", def.Sobject))
			continue
		}
		result.FieldsBySobject[strings.ToLower(def.Sobject)] = described.Fields
		if !described.Queryable {
			result.Ineligible.Set(name, message("errorRelatedSobjectNotQueryable", def.Sobject))
			continue
		}
		if def.LinkUser != nil {
			userField, userFieldKnown := options.UserFieldMap.Get(def.LinkUser.UserField)
			sourceField, sourceFieldKnown := described.Fields.Get(def.LinkUser.FromRelatedField)
			if !userFieldKnown || !userField.Createable || !userField.Updateable || !sourceFieldKnown || isFalse(sourceField.Readable) {
				result.Ineligible.Set(name, "Cannot link "+def.Sobject+"."+def.LinkUser.FromRelatedField+" to User."+def.LinkUser.UserField+": field access is unavailable.")
				continue
			}
		}
		if fieldProblem := checkFieldMetadata(def, described.Fields, message); fieldProblem != "" {
			result.Ineligible.Set(name, fieldProblem)
			continue
		}
		isAccount := strings.ToLower(def.Sobject) == "account"
		if isAccount && def.RecordType == nil {
			result.Ineligible.Set(name, message("errorRelatedPersonAccountRecordTypeRequired", def.Sobject))
			continue
		}
		if def.RecordType != nil {
			var row sfapi.Record
			for _, candidate := range recordTypeRows {
				sobjectType := "undefined"
				if value, present := candidate["SobjectType"]; present {
					sobjectType = core.JSString(value)
				}
				allowed := false
				for _, alias := range sobjectAliases(def.Sobject) {
					allowed = allowed || strings.ToLower(alias) == strings.ToLower(sobjectType)
				}
				if text(candidate, "DeveloperName") == def.RecordType.DeveloperName && allowed && candidate["IsActive"] != false {
					row = candidate
					break
				}
			}
			unavailable := row == nil
			if !unavailable && isAccount {
				// The REST describe does not carry isPersonType for
				// record types, so the queried IsPersonType decides and
				// the describe flag is used only when it is present.
				recordType, known := described.RecordTypeInfos[text(row, "Id")]
				unavailable = !known || !recordType.Available || (row["IsPersonType"] != true && !recordType.IsPersonType)
			}
			if unavailable {
				result.Ineligible.Set(name, message("errorRelatedRecordTypeUnavailable", def.RecordType.DeveloperName, def.Sobject))
				continue
			}
			result.RecordTypeIDByRelationship[name] = text(row, "Id")
		}
		result.Eligible[name] = true
	}
	for _, name := range result.Ineligible.Keys() {
		result.Warnings = append(result.Warnings, message("warningRelationshipSkipped", name, result.Ineligible.Get(name)))
	}
	return result, nil
}
