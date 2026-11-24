package core

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/sfapi"
)

func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}

// FieldMeta is what the commands need to know of a field.
type FieldMeta struct {
	Name       string
	Createable bool
	Updateable bool
	Filterable bool
	// Readable is nil for a describe response without the flag.
	Readable      *bool
	ExternalID    bool
	Unique        *bool
	CaseSensitive *bool
	IsBoolean     bool
}

// FieldMap holds the fields of an sObject by lower-case name.
type FieldMap struct {
	order []string
	byKey map[string]FieldMeta
}

// BuildFieldMap indexes fields by lower-case name.
func BuildFieldMap(fields []FieldMeta) *FieldMap {
	result := &FieldMap{byKey: map[string]FieldMeta{}}
	for _, field := range fields {
		key := strings.ToLower(field.Name)
		if _, exists := result.byKey[key]; !exists {
			result.order = append(result.order, key)
		}
		result.byKey[key] = field
	}
	return result
}

// Get returns the field with the name, without regard to letter case.
func (m *FieldMap) Get(name string) (FieldMeta, bool) {
	if m == nil {
		return FieldMeta{}, false
	}
	field, ok := m.byKey[strings.ToLower(name)]
	return field, ok
}

// Fields returns the fields in describe order.
func (m *FieldMap) Fields() []FieldMeta {
	if m == nil {
		return nil
	}
	fields := make([]FieldMeta, 0, len(m.order))
	for _, key := range m.order {
		fields = append(fields, m.byKey[key])
	}
	return fields
}

// RecordTypeAvailability is what a describe says of a record type.
type RecordTypeAvailability struct {
	Available    bool
	IsPersonType bool
}

// SobjectInfo is the description of an sObject.
type SobjectInfo struct {
	Name            string
	Queryable       bool
	Fields          *FieldMap
	RecordTypeInfos map[string]RecordTypeAvailability
}

// DescribeCache holds the descriptions read during one run, by lower-case
// sObject name.
type DescribeCache map[string]*SobjectInfo

// DescribeSobject describes an sObject, from the cache when it is there.
func DescribeSobject(conn sfapi.Conn, sobject string, cache DescribeCache) (*SobjectInfo, error) {
	key := strings.ToLower(sobject)
	if cached, ok := cache[key]; ok {
		return cached, nil
	}
	described, err := conn.Describe(sobject)
	if err != nil {
		return nil, err
	}
	fields := make([]FieldMeta, 0, len(described.Fields))
	for _, field := range described.Fields {
		fields = append(fields, FieldMeta{
			Name:          field.Name,
			Createable:    field.Createable,
			Updateable:    field.Updateable,
			Filterable:    field.Filterable,
			Readable:      field.Readable,
			ExternalID:    field.ExternalID,
			Unique:        field.Unique,
			CaseSensitive: field.CaseSensitive,
			IsBoolean:     field.Type == "boolean",
		})
	}
	info := &SobjectInfo{
		Name:            described.Name,
		Queryable:       described.Queryable == nil || *described.Queryable,
		Fields:          BuildFieldMap(fields),
		RecordTypeInfos: map[string]RecordTypeAvailability{},
	}
	if info.Name == "" {
		info.Name = sobject
	}
	for _, recordType := range described.RecordTypeInfos {
		info.RecordTypeInfos[recordType.RecordTypeID] = RecordTypeAvailability{
			Available:    recordType.Available == nil || *recordType.Available,
			IsPersonType: recordType.IsPersonType,
		}
	}
	if cache != nil {
		cache[key] = info
	}
	return info, nil
}

// DescribeUserFields returns the fields of User.
func DescribeUserFields(conn sfapi.Conn, cache DescribeCache) (*FieldMap, error) {
	info, err := DescribeSobject(conn, "User", cache)
	if err != nil {
		return nil, err
	}
	return info.Fields, nil
}

// Esc escapes a value for a SOQL string literal.
func Esc(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `'`, `\'`)
}

// SoqlIn writes values as the list of an IN clause.
func SoqlIn(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + Esc(value) + "'"
	}
	return strings.Join(quoted, ",")
}

// Batch splits items into groups of size.
func Batch[T any](items []T, size int) [][]T {
	var groups [][]T
	for start := 0; start < len(items); start += size {
		groups = append(groups, items[start:min(start+size, len(items))])
	}
	return groups
}

// FormatSaveError writes an error of a record change as
// "STATUS_CODE: message (fields: a, b)".
func FormatSaveError(saveError sfapi.SaveError) string {
	suffix := ""
	if len(saveError.Fields) > 0 {
		suffix = " (fields: " + strings.Join(saveError.Fields, ", ") + ")"
	}
	if saveError.StatusCode != "" {
		return saveError.StatusCode + ": " + saveError.Message + suffix
	}
	return saveError.Message
}

// SaveErrors returns the formatted errors of the results that failed.
func SaveErrors(results []sfapi.SaveResult) []string {
	var errors []string
	for _, result := range results {
		if result.Success {
			continue
		}
		for _, saveError := range result.Errors {
			errors = append(errors, FormatSaveError(saveError))
		}
	}
	return errors
}
