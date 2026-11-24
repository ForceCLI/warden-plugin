package orgdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

const (
	PersonaObject                 = "wdn_Persona__c"
	PersonaComponentObject        = "wdn_Persona_Component__c"
	PersonaAssignmentObject       = "wdn_Persona_Assignment__c"
	PersonaAssignmentUserField    = "wdn_User__c"
	PersonaAssignmentPersonaField = "wdn_Persona__c"
	PersonaAssignmentStatusField  = "wdn_Status__c"
	PersonaNameField              = "Name"
	PersonaAPINameField           = "wdn_Api_Name__c"
	PersonaActiveField            = "wdn_Active__c"
	ComponentPersonaField         = "wdn_Persona__c"
	ComponentTypeField            = "wdn_Type__c"
	ComponentReferenceField       = "wdn_Reference__c"
)

const collectionBatchSize = 200

// A persona document is a *core.Object of the shape of personas.json:
// {"personas": {"<api name>": {"permissionSets": [...], ...}}}.  It stands
// for PersonaDefinitionsFile of warden-core.

// OrgPersonaRead is the persona document read from an org and the number
// of personas in it.
type OrgPersonaRead struct {
	Document *core.Object `json:"document"`
	Count    int          `json:"count"`
}

// HeldPersonaAssignment is one active persona assignment of a user.
type HeldPersonaAssignment struct {
	ID      string `json:"id"`
	UserID  string `json:"userId"`
	Persona string `json:"persona"`
}

// PersonaPromotionResult counts what an import wrote.  PrunedComponents is
// nil unless the import was asked to prune.
type PersonaPromotionResult struct {
	Personas         int  `json:"personas"`
	Components       int  `json:"components"`
	PrunedComponents *int `json:"prunedComponents,omitempty"`
}

// Upserter is implemented by a connection that can insert or update
// records by an external id field, with one result for each record in
// order.  ImportOrgPersonas uses it when the connection has it.
type Upserter interface {
	Upsert(sobject string, records []sfapi.Record, externalIDField string, allOrNone bool) ([]sfapi.SaveResult, error)
}

// PersonaPackageDetected reports whether the org has the persona object.
func PersonaPackageDetected(conn sfapi.Conn) bool {
	return DescribeObject(conn, PersonaObject)
}

type listField struct {
	componentType string
	field         string
}

// listFields pairs each org component type with the persona property that
// lists its references.
var listFields = []listField{
	{"Permission Set", "permissionSets"},
	{"Permission Set Group", "permissionSetGroups"},
	{"Public Group", "publicGroups"},
	{"Queue", "queues"},
}

// text returns the text under field, or "" when it is absent or not text.
func text(record sfapi.Record, field string) string {
	value, _ := record[field].(string)
	return value
}

// queryRecords returns the records of every page of a query.
func queryRecords(conn sfapi.Conn, soql string) ([]sfapi.Record, error) {
	result, err := conn.Query(soql)
	if err != nil {
		return nil, err
	}
	records := append([]sfapi.Record{}, result.Records...)
	for !result.Done && result.NextRecordsURL != "" {
		if result, err = conn.QueryMore(result.NextRecordsURL); err != nil {
			return nil, err
		}
		records = append(records, result.Records...)
	}
	return records, nil
}

func jsonText(value any) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprint(value)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

type saveErrorJSON struct {
	StatusCode string   `json:"statusCode,omitempty"`
	Message    string   `json:"message,omitempty"`
	Fields     []string `json:"fields,omitempty"`
}

type saveResultJSON struct {
	ID      string `json:"id,omitempty"`
	Success bool   `json:"success"`
}

// assertSuccessful returns an error that carries the errors of the results
// that failed.  A failed result without an error list is reported itself.
func assertSuccessful(operation string, results []sfapi.SaveResult) error {
	failures := []any{}
	for _, item := range results {
		if item.Success {
			continue
		}
		if item.Errors == nil {
			failures = append(failures, saveResultJSON{ID: item.ID, Success: item.Success})
			continue
		}
		for _, saveError := range item.Errors {
			failures = append(failures, saveErrorJSON{StatusCode: saveError.StatusCode, Message: saveError.Message, Fields: saveError.Fields})
		}
	}
	if len(failures) > 0 {
		return errors.New(operation + " failed: " + jsonText(failures))
	}
	return nil
}

func queryRecordsInBatches(conn sfapi.Conn, values []string, query func(batch []string) string) ([]sfapi.Record, error) {
	records := []sfapi.Record{}
	for _, batch := range core.Batch(values, collectionBatchSize) {
		page, err := queryRecords(conn, query(batch))
		if err != nil {
			return nil, err
		}
		records = append(records, page...)
	}
	return records, nil
}

func saveInBatches[T any](records []T, save func(batch []T) ([]sfapi.SaveResult, error), operation string) error {
	for _, batch := range core.Batch(records, collectionBatchSize) {
		results, err := save(batch)
		if err != nil {
			return err
		}
		if err := assertSuccessful(operation, results); err != nil {
			return err
		}
	}
	return nil
}

func emptyPersona() *core.Object {
	persona := core.NewObject()
	for _, list := range listFields {
		persona.Set(list.field, []any{})
	}
	return persona
}

func addComponent(persona *core.Object, componentType, reference string) {
	for _, list := range listFields {
		if list.componentType != componentType {
			continue
		}
		values, _ := persona.List(list.field)
		if slices.Contains(values, any(reference)) {
			return
		}
		persona.Set(list.field, append(append([]any{}, values...), reference))
		return
	}
}

func componentKey(personaID, componentType, reference string) string {
	return personaID + "|" + componentType + "|" + reference
}

func componentQuery(batch []string) string {
	return "SELECT Id, " + ComponentPersonaField + ", " + ComponentTypeField + ", " + ComponentReferenceField + " FROM " + PersonaComponentObject + " WHERE " + ComponentPersonaField + " IN (" + core.SoqlIn(batch) + ")"
}

// ReadOrgPersonas reads the personas of the org and their components into
// a persona document.  With activeOnly, inactive personas are left out.
func ReadOrgPersonas(conn sfapi.Conn, activeOnly bool) (*OrgPersonaRead, error) {
	activeClause := ""
	if activeOnly {
		activeClause = " WHERE " + PersonaActiveField + " = true"
	}
	personas, err := queryRecords(conn, "SELECT Id, "+PersonaAPINameField+", "+PersonaActiveField+" FROM "+PersonaObject+activeClause)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, persona := range personas {
		if id := text(persona, "Id"); id != "" {
			ids = append(ids, id)
		}
	}
	components, err := queryRecordsInBatches(conn, ids, componentQuery)
	if err != nil {
		return nil, err
	}
	type orgPersona struct {
		name       string
		definition *core.Object
	}
	personaByID := NewOrderedMap[*orgPersona]()
	for _, persona := range personas {
		id := text(persona, "Id")
		name := text(persona, PersonaAPINameField)
		if id != "" && name != "" {
			personaByID.Set(id, &orgPersona{name: name, definition: emptyPersona()})
		}
	}
	for _, component := range components {
		parentID := text(component, ComponentPersonaField)
		componentType := text(component, ComponentTypeField)
		reference := text(component, ComponentReferenceField)
		persona, found := personaByID.Get(parentID)
		if found && componentType != "" && reference != "" {
			addComponent(persona.definition, componentType, reference)
		}
	}
	definitions := core.NewObject()
	for _, id := range personaByID.Keys() {
		persona, _ := personaByID.Get(id)
		definitions.Set(persona.name, persona.definition)
	}
	document := core.NewObject()
	document.Set("personas", definitions)
	return &OrgPersonaRead{Document: document, Count: len(definitions.Keys)}, nil
}

// ReadHeldPersonas reads the active persona assignments of users.  Revoked
// assignments are not read because they grant no access.
func ReadHeldPersonas(conn sfapi.Conn, userIDs []string) ([]HeldPersonaAssignment, error) {
	held := []HeldPersonaAssignment{}
	if len(userIDs) == 0 {
		return held, nil
	}
	records, err := queryRecordsInBatches(conn, userIDs, func(batch []string) string {
		return "SELECT Id, " + PersonaAssignmentUserField + ", " + PersonaAssignmentPersonaField + ", wdn_Persona__r." + PersonaAPINameField + " FROM " + PersonaAssignmentObject + " WHERE " + PersonaAssignmentUserField + " IN (" + core.SoqlIn(batch) + ") AND " + PersonaAssignmentStatusField + " = 'Active'"
	})
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		id := text(record, "Id")
		userID := text(record, PersonaAssignmentUserField)
		persona := ""
		if relationship, ok := record["wdn_Persona__r"].(map[string]any); ok {
			persona = text(relationship, PersonaAPINameField)
		}
		if id != "" && userID != "" && persona != "" {
			held = append(held, HeldPersonaAssignment{ID: id, UserID: userID, Persona: persona})
		}
	}
	return held, nil
}

// ReadPersonaFile reads and checks a persona document file.
func ReadPersonaFile(path string) (*core.Object, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, err := core.ParseJSON(content)
	if err != nil {
		return nil, err
	}
	return core.ParsePersonaDefinitions(value)
}

func objectKeys(object *core.Object) []string {
	if object == nil {
		return nil
	}
	return object.Keys
}

type componentRecord struct {
	apiName       string
	componentType string
	reference     string
}

func componentRecords(document *core.Object) []componentRecord {
	entries := []componentRecord{}
	personas := document.Object("personas")
	for _, apiName := range objectKeys(personas) {
		persona := personas.Object(apiName)
		for _, list := range listFields {
			for _, reference := range persona.Strings(list.field) {
				entries = append(entries, componentRecord{apiName: apiName, componentType: list.componentType, reference: reference})
			}
		}
	}
	return entries
}

// ReadPersonaIDs returns the record id of each persona that exists in the
// org, by API name, in the order the org returned them.
func ReadPersonaIDs(conn sfapi.Conn, names []string) (*OrderedMap[string], error) {
	ids := NewOrderedMap[string]()
	if len(names) == 0 {
		return ids, nil
	}
	records, err := queryRecordsInBatches(conn, names, func(batch []string) string {
		return "SELECT Id, " + PersonaAPINameField + " FROM " + PersonaObject + " WHERE " + PersonaAPINameField + " IN (" + core.SoqlIn(batch) + ")"
	})
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		id := text(record, "Id")
		name := text(record, PersonaAPINameField)
		if id != "" && name != "" {
			ids.Set(name, id)
		}
	}
	return ids, nil
}

// upsertPersonas inserts or updates persona records by API name.  A
// connection that is not an Upserter gets the same outcome in three
// requests: a query for the personas that exist, an update of those and a
// create of the others.
func upsertPersonas(conn sfapi.Conn, batch []sfapi.Record) ([]sfapi.SaveResult, error) {
	if upserter, ok := conn.(Upserter); ok {
		return upserter.Upsert(PersonaObject, batch, PersonaAPINameField, false)
	}
	names := make([]string, len(batch))
	for i, record := range batch {
		names[i] = text(record, PersonaAPINameField)
	}
	existing, err := ReadPersonaIDs(conn, names)
	if err != nil {
		return nil, err
	}
	var updates, creates []sfapi.Record
	var updateIndexes, createIndexes []int
	for i, record := range batch {
		if id, found := existing.Get(names[i]); found {
			update := sfapi.Record{"Id": id}
			for key, value := range record {
				update[key] = value
			}
			updates = append(updates, update)
			updateIndexes = append(updateIndexes, i)
		} else {
			creates = append(creates, record)
			createIndexes = append(createIndexes, i)
		}
	}
	results := make([]sfapi.SaveResult, len(batch))
	place := func(indexes []int, saved []sfapi.SaveResult) error {
		if len(saved) != len(indexes) {
			return fmt.Errorf("Persona upsert returned %d result(s) for %d record(s).", len(saved), len(indexes))
		}
		for position, index := range indexes {
			results[index] = saved[position]
		}
		return nil
	}
	if len(updates) > 0 {
		saved, err := conn.Update(PersonaObject, updates, false)
		if err != nil {
			return nil, err
		}
		if err := place(updateIndexes, saved); err != nil {
			return nil, err
		}
	}
	if len(creates) > 0 {
		saved, err := conn.Create(PersonaObject, creates, false)
		if err != nil {
			return nil, err
		}
		if err := place(createIndexes, saved); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// ImportOrgPersonas writes a persona document to the org.  It upserts each
// persona by API name and sets it active, then creates the components the
// org lacks.  With prune it also deletes the components of those personas
// that the document does not list.
func ImportOrgPersonas(conn sfapi.Conn, document *core.Object, prune bool) (*PersonaPromotionResult, error) {
	names := objectKeys(document.Object("personas"))
	parentRecords := make([]sfapi.Record, len(names))
	for i, apiName := range names {
		parentRecords[i] = sfapi.Record{
			PersonaNameField:    apiName,
			PersonaAPINameField: apiName,
			PersonaActiveField:  true,
		}
	}
	err := saveInBatches(parentRecords, func(batch []sfapi.Record) ([]sfapi.SaveResult, error) {
		return upsertPersonas(conn, batch)
	}, "Persona upsert")
	if err != nil {
		return nil, err
	}
	personaIDs, err := ReadPersonaIDs(conn, names)
	if err != nil {
		return nil, err
	}
	type desiredComponent struct {
		personaID     string
		componentType string
		reference     string
	}
	desired := []desiredComponent{}
	for _, component := range componentRecords(document) {
		if personaID, _ := personaIDs.Get(component.apiName); personaID != "" {
			desired = append(desired, desiredComponent{personaID: personaID, componentType: component.componentType, reference: component.reference})
		}
	}
	parentIDs := make([]string, 0, personaIDs.Len())
	for _, name := range personaIDs.Keys() {
		id, _ := personaIDs.Get(name)
		parentIDs = append(parentIDs, id)
	}
	existing, err := queryRecordsInBatches(conn, parentIDs, componentQuery)
	if err != nil {
		return nil, err
	}
	// existingKey returns the key of a component record, or "" when the
	// record lacks one of the values the key is made of.
	existingKey := func(component sfapi.Record) (id, key string) {
		id = text(component, "Id")
		personaID := text(component, ComponentPersonaField)
		componentType := text(component, ComponentTypeField)
		reference := text(component, ComponentReferenceField)
		if id == "" || personaID == "" || componentType == "" || reference == "" {
			return id, ""
		}
		return id, componentKey(personaID, componentType, reference)
	}
	existingKeys := map[string]bool{}
	for _, component := range existing {
		if _, key := existingKey(component); key != "" {
			existingKeys[key] = true
		}
	}
	desiredKeys := map[string]bool{}
	toCreate := []sfapi.Record{}
	for _, component := range desired {
		key := componentKey(component.personaID, component.componentType, component.reference)
		desiredKeys[key] = true
		if !existingKeys[key] {
			toCreate = append(toCreate, sfapi.Record{
				ComponentPersonaField:   component.personaID,
				ComponentTypeField:      component.componentType,
				ComponentReferenceField: component.reference,
			})
		}
	}
	if len(toCreate) > 0 {
		err := saveInBatches(toCreate, func(batch []sfapi.Record) ([]sfapi.SaveResult, error) {
			return conn.Create(PersonaComponentObject, batch, false)
		}, "Persona component insert")
		if err != nil {
			return nil, err
		}
	}
	result := &PersonaPromotionResult{Personas: len(names), Components: len(desired)}
	if prune {
		prunedComponents := 0
		toDelete := []string{}
		for _, component := range existing {
			if id, key := existingKey(component); key != "" && !desiredKeys[key] {
				toDelete = append(toDelete, id)
			}
		}
		if len(toDelete) > 0 {
			err := saveInBatches(toDelete, func(batch []string) ([]sfapi.SaveResult, error) {
				return conn.Delete(PersonaComponentObject, batch, false)
			}, "Persona component delete")
			if err != nil {
				return nil, err
			}
			prunedComponents = len(toDelete)
		}
		result.PrunedComponents = &prunedComponents
	}
	return result, nil
}

// PersonaChange is a persona that differs between a file and the org.
// Before is the definition in the org and After the one in the file.
type PersonaChange struct {
	Name   string       `json:"name"`
	Before *core.Object `json:"before"`
	After  *core.Object `json:"after"`
}

// PersonaDiff is the comparison of a persona file with the org.
type PersonaDiff struct {
	Added   []string        `json:"added"`
	Removed []string        `json:"removed"`
	Changed []PersonaChange `json:"changed"`
}

// canonical writes a JSON value so that two values that differ only in
// the order of object keys or of list items give the same text.
func canonical(value any) string {
	switch typed := value.(type) {
	case []any:
		items := make([]string, len(typed))
		for i, item := range typed {
			items[i] = canonical(item)
		}
		sort.Strings(items)
		return "[" + strings.Join(items, ",") + "]"
	case *core.Object:
		keys := append([]string{}, objectKeys(typed)...)
		sort.Strings(keys)
		entries := make([]string, len(keys))
		for i, key := range keys {
			entries[i] = jsonText(key) + ":" + canonical(typed.Values[key])
		}
		return "{" + strings.Join(entries, ",") + "}"
	}
	return jsonText(value)
}

// withEmptyLists returns a copy of a persona in which each of the four
// lists is present.  An org read has every list; a file may omit empty
// ones.
func withEmptyLists(persona *core.Object) *core.Object {
	result := persona.Clone()
	for _, list := range listFields {
		if value, _ := persona.Get(list.field); value == nil {
			result.Set(list.field, []any{})
		}
	}
	return result
}

func samePersona(left, right *core.Object) bool {
	return canonical(withEmptyLists(left)) == canonical(withEmptyLists(right))
}

// sortByCodeUnits sorts names by their UTF-16 code units, the order of the
// JavaScript sort without a comparison function.
func sortByCodeUnits(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		return slices.Compare(utf16.Encode([]rune(names[i])), utf16.Encode([]rune(names[j]))) < 0
	})
}

// DiffPersonaDocuments compares a persona file with the personas of an
// org: the personas only in the file, those only in the org, and those in
// both whose definitions differ.
func DiffPersonaDocuments(file, org *core.Object) PersonaDiff {
	filePersonas := file.Object("personas")
	orgPersonas := org.Object("personas")
	has := func(personas *core.Object, name string) bool {
		_, present := personas.Get(name)
		return present
	}
	diff := PersonaDiff{Added: []string{}, Removed: []string{}, Changed: []PersonaChange{}}
	changed := []string{}
	for _, name := range objectKeys(filePersonas) {
		if !has(orgPersonas, name) {
			diff.Added = append(diff.Added, name)
		} else if !samePersona(filePersonas.Object(name), orgPersonas.Object(name)) {
			changed = append(changed, name)
		}
	}
	for _, name := range objectKeys(orgPersonas) {
		if !has(filePersonas, name) {
			diff.Removed = append(diff.Removed, name)
		}
	}
	sortByCodeUnits(diff.Added)
	sortByCodeUnits(diff.Removed)
	sortByCodeUnits(changed)
	for _, name := range changed {
		diff.Changed = append(diff.Changed, PersonaChange{Name: name, Before: orgPersonas.Object(name), After: filePersonas.Object(name)})
	}
	return diff
}

// RenderPersonaDiff writes a diff as text: three counts, then one line for
// each persona.
func RenderPersonaDiff(diff PersonaDiff) string {
	lines := []string{
		fmt.Sprintf("Added: %d", len(diff.Added)),
		fmt.Sprintf("Removed: %d", len(diff.Removed)),
		fmt.Sprintf("Changed: %d", len(diff.Changed)),
	}
	for _, name := range diff.Added {
		lines = append(lines, "  + "+name)
	}
	for _, name := range diff.Removed {
		lines = append(lines, "  - "+name)
	}
	for _, change := range diff.Changed {
		lines = append(lines, "  ~ "+change.Name)
	}
	return strings.Join(lines, "\n")
}
