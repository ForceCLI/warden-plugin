package orgdata

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

const (
	WardenLedgerObject = "wdn_Provisioned_Grant__c"
	WardenRunObject    = "wdn_Reconciliation_Run__c"
	WardenItemObject   = "wdn_Reconciliation_Item__c"
)

// CLIVersion is the version BuildProvenance records when its options name
// none.  The program sets it at start; it stands for the version field of
// the package.json of the TypeScript plugin.
var CLIVersion string

// ConnectedPackage says which audit objects the org has.
type ConnectedPackage struct {
	LedgerAvailable bool `json:"ledgerAvailable"`
	RunAvailable    bool `json:"runAvailable"`
}

// LedgerProvenance is one ledger row that is not revoked.  Persona is nil
// when the row names no persona or the ledger has no persona field.
type LedgerProvenance struct {
	ID       string  `json:"id"`
	UserID   string  `json:"userId"`
	TargetID string  `json:"targetId"`
	Persona  *string `json:"persona,omitempty"`
}

// GrantType is the kind of an assignment.
type GrantType string

const (
	GrantPermissionSet      GrantType = "Permission Set"
	GrantPermissionSetGroup GrantType = "Permission Set Group"
	GrantPublicGroup        GrantType = "Public Group"
	GrantQueue              GrantType = "Queue"
)

// CapturedGrant is one assignment that provisioning created.
type CapturedGrant struct {
	UserID   string    `json:"userId"`
	Type     GrantType `json:"type"`
	TargetID string    `json:"targetId"`
}

// Provenance says where a run came from.  An empty field is unknown.
type Provenance struct {
	SourceSystem   string `json:"sourceSystem"`
	CLIVersion     string `json:"cliVersion,omitempty"`
	InvokingUser   string `json:"invokingUser,omitempty"`
	CIJob          string `json:"ciJob,omitempty"`
	DefinitionPath string `json:"definitionPath,omitempty"`
	DefinitionSha  string `json:"definitionSha,omitempty"`
}

// LedgerRow is one record of the grant ledger.
type LedgerRow struct {
	User         string    `json:"wdn_User__c"`
	Persona      string    `json:"wdn_Persona__c,omitempty"`
	Type         GrantType `json:"wdn_Type__c"`
	TargetID     string    `json:"wdn_Target_Id__c"`
	TargetLabel  string    `json:"wdn_Target_Label__c"`
	GrantedOn    string    `json:"wdn_Granted_On__c"`
	GrantedByRun string    `json:"wdn_Granted_By_Run__c,omitempty"`
}

// Record returns the row as a record to create.
func (row LedgerRow) Record() sfapi.Record {
	record := sfapi.Record{
		"wdn_User__c":         row.User,
		"wdn_Type__c":         string(row.Type),
		"wdn_Target_Id__c":    row.TargetID,
		"wdn_Target_Label__c": row.TargetLabel,
		"wdn_Granted_On__c":   row.GrantedOn,
	}
	if row.Persona != "" {
		record["wdn_Persona__c"] = row.Persona
	}
	if row.GrantedByRun != "" {
		record["wdn_Granted_By_Run__c"] = row.GrantedByRun
	}
	return record
}

// ReconciliationItem is one record of the detail log of a run.  Status is
// Planned, Applied, Failed or Unmanaged.
type ReconciliationItem struct {
	Run      string `json:"wdn_Reconciliation_Run__c"`
	UserKey  string `json:"wdn_User_Key__c"`
	UserID   string `json:"wdn_User_Id__c"`
	Status   string `json:"wdn_Status__c"`
	Category string `json:"wdn_Category__c"`
	Action   string `json:"wdn_Action__c"`
	Detail   string `json:"wdn_Detail__c"`
	Error    string `json:"wdn_Error__c"`
}

// Record returns the item as a record to create.
func (item ReconciliationItem) Record() sfapi.Record {
	return sfapi.Record{
		"wdn_Reconciliation_Run__c": item.Run,
		"wdn_User_Key__c":           item.UserKey,
		"wdn_User_Id__c":            item.UserID,
		"wdn_Status__c":             item.Status,
		"wdn_Category__c":           item.Category,
		"wdn_Action__c":             item.Action,
		"wdn_Detail__c":             item.Detail,
		"wdn_Error__c":              item.Error,
	}
}

// ItemRecords returns the items as records to create.
func ItemRecords(items []ReconciliationItem) []sfapi.Record {
	records := make([]sfapi.Record, len(items))
	for i, item := range items {
		records[i] = item.Record()
	}
	return records
}

// LedgerRecords returns the rows as records to create.
func LedgerRecords(rows []LedgerRow) []sfapi.Record {
	records := make([]sfapi.Record, len(rows))
	for i, row := range rows {
		records[i] = row.Record()
	}
	return records
}

// ProvisionResult holds the fields of ProvisionResult of warden-core that
// this package reads.
type ProvisionResult struct {
	Users []ProvisionUser
}

// ProvisionUser holds the fields of UserResult of warden-core that this
// package reads.  Status is created, updated, failed or planned.
type ProvisionUser struct {
	ID             string
	Key            string
	Personas       []string
	Status         string
	Actions        []string
	Errors         []string
	RelatedRecords []RelatedRecord
}

// RelatedRecord holds the fields of RelatedRecordResult of warden-core
// that this package reads.
type RelatedRecord struct {
	Relationship string
	Phase        string
	Sobject      string
	Action       string
	Error        string
}

// CaptureState is what a capturing connection has observed: the grants
// created, and the labels, reference ids and group types of the permission
// sets, permission set groups and groups that queries returned.
type CaptureState struct {
	Grants []CapturedGrant
	// LabelsByTypeAndID maps a grant type to the Name of each record id.
	LabelsByTypeAndID *OrderedMap[*OrderedMap[string]]
	// ReferenceIDsByType maps a grant type to the record id of each Name
	// and DeveloperName.
	ReferenceIDsByType *OrderedMap[*OrderedMap[string]]
	// GroupTypesByID maps a Group id to Public Group or Queue.
	GroupTypesByID *OrderedMap[GrantType]
}

// SerializedPair is a key and a value that are written to JSON as a list
// of two items.
type SerializedPair struct {
	Key   string
	Value string
}

func (pair SerializedPair) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string{pair.Key, pair.Value})
}

func (pair *SerializedPair) UnmarshalJSON(data []byte) error {
	var items []string
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	if len(items) != 2 {
		return fmt.Errorf("expected a list of 2 items, got %d", len(items))
	}
	pair.Key, pair.Value = items[0], items[1]
	return nil
}

// SerializedMap is a key and its entries, written to JSON as a list of the
// key and the list of entries.
type SerializedMap struct {
	Key     string
	Entries []SerializedPair
}

func (entry SerializedMap) MarshalJSON() ([]byte, error) {
	entries := entry.Entries
	if entries == nil {
		entries = []SerializedPair{}
	}
	return json.Marshal([]any{entry.Key, entries})
}

func (entry *SerializedMap) UnmarshalJSON(data []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	if len(items) != 2 {
		return fmt.Errorf("expected a list of 2 items, got %d", len(items))
	}
	if err := json.Unmarshal(items[0], &entry.Key); err != nil {
		return err
	}
	return json.Unmarshal(items[1], &entry.Entries)
}

// SerializedCaptureState is a CaptureState without its grants, in a form
// that can be written to JSON and read back.
type SerializedCaptureState struct {
	LabelsByTypeAndID  []SerializedMap  `json:"labelsByTypeAndId"`
	ReferenceIDsByType []SerializedMap  `json:"referenceIdsByType"`
	GroupTypesByID     []SerializedPair `json:"groupTypesById"`
}

// DescribeObject reports whether the org has an object of this name.  A
// describe that fails counts as an object that is absent.
func DescribeObject(conn sfapi.Conn, objectName string) bool {
	description, err := conn.Describe(objectName)
	return err == nil && description != nil && description.Name == objectName
}

// FieldSet is the set of field names of an object.  A nil FieldSet means
// the object is not available.
type FieldSet map[string]bool

// Has reports whether the set contains field.
func (fields FieldSet) Has(field string) bool {
	return fields[field]
}

// NewFieldSet returns the set of the given names.
func NewFieldSet(names ...string) FieldSet {
	fields := FieldSet{}
	for _, name := range names {
		fields[name] = true
	}
	return fields
}

// DescribeObjectFields returns the field names of an object, or nil when
// the object is not available.  It does not assume a package version.
func DescribeObjectFields(conn sfapi.Conn, objectName string) FieldSet {
	description, err := conn.Describe(objectName)
	if err != nil || description == nil || description.Name != objectName {
		return nil
	}
	fields := FieldSet{}
	for _, field := range description.Fields {
		fields[field.Name] = true
	}
	return fields
}

// ReadLedgerProvenance reads the ledger rows of users that are not
// revoked.  ledgerFields is the field set of the ledger object; when it is
// nil the object is described.  The persona of a row is read only when the
// ledger has the field wdn_Persona__c.
func ReadLedgerProvenance(conn sfapi.Conn, userIDs []string, ledgerFields FieldSet) ([]LedgerProvenance, error) {
	rows := []LedgerProvenance{}
	if len(userIDs) == 0 {
		return rows, nil
	}
	fields := ledgerFields
	if fields == nil {
		fields = DescribeObjectFields(conn, WardenLedgerObject)
	}
	personaSelect := ""
	if fields.Has("wdn_Persona__c") {
		personaSelect = ", wdn_Persona__r.wdn_Api_Name__c"
	}
	for _, batch := range core.Batch(userIDs, 200) {
		records, err := queryRecords(conn, "SELECT Id, wdn_User__c, wdn_Target_Id__c"+personaSelect+" FROM "+WardenLedgerObject+" WHERE wdn_User__c IN ("+core.SoqlIn(batch)+") AND wdn_Revoked_On__c = NULL")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			var relationship map[string]any
			if personaSelect != "" {
				relationship, _ = record["wdn_Persona__r"].(map[string]any)
			}
			id, hasID := record["Id"].(string)
			userID, hasUserID := record["wdn_User__c"].(string)
			targetID, hasTargetID := record["wdn_Target_Id__c"].(string)
			if !hasID || !hasUserID || !hasTargetID {
				continue
			}
			row := LedgerProvenance{ID: id, UserID: userID, TargetID: targetID}
			if persona, ok := relationship["wdn_Api_Name__c"].(string); ok {
				row.Persona = &persona
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// DetectConnectedPackage describes the ledger object and the run object.
// It does not query InstalledSubscriberPackage.
func DetectConnectedPackage(conn sfapi.Conn) ConnectedPackage {
	ledgerAvailable := DescribeObject(conn, WardenLedgerObject)
	runAvailable := DescribeObject(conn, WardenRunObject)
	return ConnectedPackage{LedgerAvailable: ledgerAvailable, RunAvailable: runAvailable}
}

func mapFor(maps *OrderedMap[*OrderedMap[string]], key string) *OrderedMap[string] {
	inner, found := maps.Get(key)
	if !found {
		inner = NewOrderedMap[string]()
		maps.Set(key, inner)
	}
	return inner
}

func rememberReference(state *CaptureState, grantType GrantType, record sfapi.Record) {
	id := text(record, "Id")
	if id == "" {
		return
	}
	label := text(record, "Name")
	developerName := text(record, "DeveloperName")
	references := mapFor(state.ReferenceIDsByType, string(grantType))
	if label != "" {
		references.Set(label, id)
	}
	if developerName != "" {
		references.Set(developerName, id)
	}
	if label != "" {
		mapFor(state.LabelsByTypeAndID, string(grantType)).Set(id, label)
	}
	if grantType == GrantPublicGroup || grantType == GrantQueue {
		state.GroupTypesByID.Set(id, grantType)
	}
}

var (
	fromObjectPattern   = regexp.MustCompile(`(?i)\bFROM\s+([A-Za-z_][A-Za-z0-9_]*)`)
	queueTypePattern    = regexp.MustCompile(`(?i)\bType\s*=\s*'Queue'`)
	regularGroupPattern = regexp.MustCompile(`(?i)\bType\s*=\s*'Regular'`)
)

// rememberQueryRecords records the ids, names and group types of the
// records of a query on PermissionSet, PermissionSetGroup or Group.
func rememberQueryRecords(state *CaptureState, soql string, records []sfapi.Record) {
	objectMatch := fromObjectPattern.FindStringSubmatch(soql)
	if objectMatch == nil {
		return
	}
	objectName := objectMatch[1]
	var grantType GrantType
	switch objectName {
	case "PermissionSet":
		grantType = GrantPermissionSet
	case "PermissionSetGroup":
		grantType = GrantPermissionSetGroup
	}
	var groupTypeFromQuery GrantType
	if queueTypePattern.MatchString(soql) {
		groupTypeFromQuery = GrantQueue
	} else if regularGroupPattern.MatchString(soql) {
		groupTypeFromQuery = GrantPublicGroup
	}
	for _, record := range records {
		if objectName == "Group" {
			groupType := groupTypeFromQuery
			switch record["Type"] {
			case "Queue":
				groupType = GrantQueue
			case "Regular":
				groupType = GrantPublicGroup
			}
			if groupType != "" {
				rememberReference(state, groupType, record)
			}
		} else if grantType != "" {
			rememberReference(state, grantType, record)
		}
	}
}

func captureSuccessfulAssignments(state *CaptureState, objectName string, records []sfapi.Record, results []sfapi.SaveResult) {
	if objectName != "PermissionSetAssignment" && objectName != "GroupMember" {
		return
	}
	for index, record := range records {
		if index >= len(results) || !results[index].Success {
			continue
		}
		var userValue, targetValue any
		if objectName == "PermissionSetAssignment" {
			userValue = record["AssigneeId"]
			if targetValue = record["PermissionSetId"]; targetValue == nil {
				targetValue = record["PermissionSetGroupId"]
			}
		} else {
			userValue = record["UserOrGroupId"]
			targetValue = record["GroupId"]
		}
		userID, hasUserID := userValue.(string)
		targetID, hasTargetID := targetValue.(string)
		if !hasUserID || !hasTargetID {
			continue
		}
		var grantType GrantType
		if objectName == "GroupMember" {
			var found bool
			if grantType, found = state.GroupTypesByID.Get(targetID); !found {
				grantType = GrantPublicGroup
			}
		} else if _, isGroup := record["PermissionSetGroupId"].(string); isGroup {
			grantType = GrantPermissionSetGroup
		} else {
			grantType = GrantPermissionSet
		}
		state.Grants = append(state.Grants, CapturedGrant{UserID: userID, Type: grantType, TargetID: targetID})
	}
}

func serializeNested(maps *OrderedMap[*OrderedMap[string]]) []SerializedMap {
	serialized := []SerializedMap{}
	for _, key := range maps.Keys() {
		inner, _ := maps.Get(key)
		entries := []SerializedPair{}
		for _, innerKey := range inner.Keys() {
			value, _ := inner.Get(innerKey)
			entries = append(entries, SerializedPair{Key: innerKey, Value: value})
		}
		serialized = append(serialized, SerializedMap{Key: key, Entries: entries})
	}
	return serialized
}

// SerializeCaptureState returns the labels, reference ids and group types
// of a state.  The grants are not included.
func SerializeCaptureState(state *CaptureState) SerializedCaptureState {
	groupTypes := []SerializedPair{}
	for _, id := range state.GroupTypesByID.Keys() {
		groupType, _ := state.GroupTypesByID.Get(id)
		groupTypes = append(groupTypes, SerializedPair{Key: id, Value: string(groupType)})
	}
	return SerializedCaptureState{
		LabelsByTypeAndID:  serializeNested(state.LabelsByTypeAndID),
		ReferenceIDsByType: serializeNested(state.ReferenceIDsByType),
		GroupTypesByID:     groupTypes,
	}
}

func nestedFrom(serialized []SerializedMap) *OrderedMap[*OrderedMap[string]] {
	maps := NewOrderedMap[*OrderedMap[string]]()
	for _, entry := range serialized {
		inner := NewOrderedMap[string]()
		for _, pair := range entry.Entries {
			inner.Set(pair.Key, pair.Value)
		}
		maps.Set(entry.Key, inner)
	}
	return maps
}

func captureStateFrom(seed *SerializedCaptureState) *CaptureState {
	if seed == nil {
		seed = &SerializedCaptureState{}
	}
	groupTypes := NewOrderedMap[GrantType]()
	for _, pair := range seed.GroupTypesByID {
		groupTypes.Set(pair.Key, GrantType(pair.Value))
	}
	return &CaptureState{
		Grants:             []CapturedGrant{},
		LabelsByTypeAndID:  nestedFrom(seed.LabelsByTypeAndID),
		ReferenceIDsByType: nestedFrom(seed.ReferenceIDsByType),
		GroupTypesByID:     groupTypes,
	}
}

// captureConn passes every call to the connection it wraps and records
// the query results and created assignments that the ledger needs.
type captureConn struct {
	sfapi.Conn
	state *CaptureState
	// pageQueries maps the nextRecordsUrl of a page to the query it
	// belongs to, so that later pages are recorded like the first.
	pageQueries map[string]string
}

func (c *captureConn) remember(soql string, result *sfapi.QueryResult) {
	if result == nil {
		return
	}
	rememberQueryRecords(c.state, soql, result.Records)
	if !result.Done && result.NextRecordsURL != "" {
		c.pageQueries[result.NextRecordsURL] = soql
	}
}

func (c *captureConn) Query(soql string) (*sfapi.QueryResult, error) {
	result, err := c.Conn.Query(soql)
	if err != nil {
		return nil, err
	}
	c.remember(soql, result)
	return result, nil
}

func (c *captureConn) QueryMore(nextRecordsURL string) (*sfapi.QueryResult, error) {
	result, err := c.Conn.QueryMore(nextRecordsURL)
	if err != nil {
		return nil, err
	}
	if soql, found := c.pageQueries[nextRecordsURL]; found {
		delete(c.pageQueries, nextRecordsURL)
		c.remember(soql, result)
	}
	return result, nil
}

func (c *captureConn) Create(sobject string, records []sfapi.Record, allOrNone bool) ([]sfapi.SaveResult, error) {
	results, err := c.Conn.Create(sobject, records, allOrNone)
	if err != nil {
		return nil, err
	}
	captureSuccessfulAssignments(c.state, sobject, records, results)
	return results, nil
}

// CapturedWrites is a capturing connection and the state it fills.
type CapturedWrites struct {
	Conn  sfapi.Conn
	State *CaptureState
}

// CaptureProvisioningWrites wraps a connection.  The wrapper behaves like
// the connection and records each PermissionSetAssignment and GroupMember
// that a Create call saved, and the records of queries on PermissionSet,
// PermissionSetGroup and Group.  seed, when not nil, is the state of an
// earlier capture without its grants.
func CaptureProvisioningWrites(conn sfapi.Conn, seed *SerializedCaptureState) CapturedWrites {
	state := captureStateFrom(seed)
	return CapturedWrites{Conn: &captureConn{Conn: conn, state: state, pageQueries: map[string]string{}}, State: state}
}

func refsForType(persona *core.Object, grantType GrantType) []string {
	key := "queues"
	switch grantType {
	case GrantPermissionSet:
		key = "permissionSets"
	case GrantPermissionSetGroup:
		key = "permissionSetGroups"
	case GrantPublicGroup:
		key = "publicGroups"
	}
	return persona.Strings(key)
}

func matchingPersonas(userPersonas []string, personas *core.Object, grant CapturedGrant, referenceIDsByType *OrderedMap[*OrderedMap[string]]) []string {
	referenceIDs, _ := referenceIDsByType.Get(string(grant.Type))
	var matching []string
	for _, personaName := range userPersonas {
		for _, ref := range refsForType(personas.Object(personaName), grant.Type) {
			id, found := referenceIDs.Get(ref)
			if !found {
				id = ref
			}
			if id == grant.TargetID {
				matching = append(matching, personaName)
				break
			}
		}
	}
	return matching
}

// LedgerRowsOptions are the inputs of BuildLedgerRows.
type LedgerRowsOptions struct {
	Grants []CapturedGrant
	Result ProvisionResult
	// PersonasDoc is the persona document provisioning ran from, or nil.
	PersonasDoc        *core.Object
	LabelsByTypeAndID  *OrderedMap[*OrderedMap[string]]
	ReferenceIDsByType *OrderedMap[*OrderedMap[string]]
	// PersonaIDsByName maps a persona API name to its record id.
	PersonaIDsByName *OrderedMap[string]
	RunID            string
	// GrantedOn is the time written to each row; when it is empty the
	// current time is used.
	GrantedOn string
}

func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// BuildLedgerRows returns one ledger row for each persona of the user that
// lists the target of a grant, and one row without a persona for a grant
// that no persona of the user lists.
func BuildLedgerRows(options LedgerRowsOptions) []LedgerRow {
	usersByID := map[string]ProvisionUser{}
	for _, user := range options.Result.Users {
		if user.ID != "" {
			usersByID[user.ID] = user
		}
	}
	rows := []LedgerRow{}
	for _, grant := range options.Grants {
		user := usersByID[grant.UserID]
		contributingPersonas := matchingPersonas(user.Personas, options.PersonasDoc.Object("personas"), grant, options.ReferenceIDsByType)
		if len(contributingPersonas) == 0 {
			contributingPersonas = []string{""}
		}
		label := grant.TargetID
		if labels, found := options.LabelsByTypeAndID.Get(string(grant.Type)); found {
			if known, found := labels.Get(grant.TargetID); found {
				label = known
			}
		}
		for _, persona := range contributingPersonas {
			row := LedgerRow{
				User:         grant.UserID,
				Type:         grant.Type,
				TargetID:     grant.TargetID,
				TargetLabel:  label,
				GrantedOn:    options.GrantedOn,
				GrantedByRun: options.RunID,
			}
			if persona != "" {
				row.Persona, _ = options.PersonaIDsByName.Get(persona)
			}
			if row.GrantedOn == "" {
				row.GrantedOn = isoNow()
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func actionCategory(action string) string {
	switch {
	case strings.Contains(action, "PermissionSetGroup"):
		return "Permission Set Group"
	case strings.Contains(action, "PermissionSet"):
		return "Permission Set"
	case strings.Contains(action, "PublicGroup"):
		return "Public Group"
	case strings.Contains(action, "Queue"):
		return "Queue"
	}
	return "User"
}

func itemStatus(status string) string {
	switch status {
	case "planned":
		return "Planned"
	case "failed":
		return "Failed"
	}
	return "Applied"
}

// BuildReconciliationItems returns one item for each action, related
// record and error of each user, and one item for a user that has none.
// The expansion matches the rows of the provisioning CSV output.
func BuildReconciliationItems(result ProvisionResult, runID string) []ReconciliationItem {
	items := []ReconciliationItem{}
	for _, user := range result.Users {
		base := ReconciliationItem{
			Run:     runID,
			UserKey: user.Key,
			UserID:  user.ID,
			Status:  itemStatus(user.Status),
		}
		for _, action := range user.Actions {
			item := base
			item.Category = actionCategory(action)
			item.Action = action
			items = append(items, item)
		}
		for _, related := range user.RelatedRecords {
			item := base
			item.Category = related.Sobject
			item.Action = "related"
			item.Detail = related.Relationship + " " + related.Phase + " " + related.Sobject + " " + related.Action
			item.Error = related.Error
			items = append(items, item)
		}
		for _, userError := range user.Errors {
			item := base
			item.Category = "User"
			item.Error = userError
			items = append(items, item)
		}
		if len(user.Actions) == 0 && len(user.RelatedRecords) == 0 && len(user.Errors) == 0 {
			item := base
			item.Category = "User"
			items = append(items, item)
		}
	}
	return items
}

// ProvenanceOptions are the inputs of BuildProvenance.  Each has a default
// that reads the process environment.
type ProvenanceOptions struct {
	DefinitionPath string
	// Env looks up an environment variable; nil means os.LookupEnv.
	Env func(name string) (string, bool)
	// CLIVersion defaults to the package variable CLIVersion.
	CLIVersion string
	// InvokingUser defaults to the name of the OS user.
	InvokingUser string
	// GitSha returns the commit of the repository that holds path, or
	// "".  nil means running git.
	GitSha func(path string) string
}

func defaultGitSha(path string) string {
	output, err := exec.Command("git", "-C", filepath.Dir(path), "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// BuildProvenance collects where a run comes from: the CLI version, the OS
// user, the CI job, and the path and repository commit of the definition
// file.
func BuildProvenance(options ProvenanceOptions) Provenance {
	env := options.Env
	if env == nil {
		env = os.LookupEnv
	}
	definitionPath := ""
	if options.DefinitionPath != "" {
		if absolute, err := filepath.Abs(options.DefinitionPath); err == nil {
			definitionPath = absolute
		} else {
			definitionPath = options.DefinitionPath
		}
	}
	invokingUser := options.InvokingUser
	if invokingUser == "" {
		if current, err := user.Current(); err == nil {
			invokingUser = current.Username
		}
	}
	ciJob, found := env("GITHUB_JOB")
	if !found {
		if ciJob, found = env("GITHUB_WORKFLOW"); !found {
			if ci, _ := env("CI"); ci != "" {
				ciJob = "CI"
			}
		}
	}
	cliVersion := options.CLIVersion
	if cliVersion == "" {
		cliVersion = CLIVersion
	}
	definitionSha := ""
	if definitionPath != "" {
		gitSha := options.GitSha
		if gitSha == nil {
			gitSha = defaultGitSha
		}
		definitionSha = gitSha(definitionPath)
	}
	return Provenance{
		SourceSystem:   "warden-cli",
		CLIVersion:     cliVersion,
		InvokingUser:   invokingUser,
		CIJob:          ciJob,
		DefinitionPath: definitionPath,
		DefinitionSha:  definitionSha,
	}
}

// BestEffortOrgWrite runs an operation.  When it fails, the error goes to
// warn and the second result is false.
func BestEffortOrgWrite[T any](operation func() (T, error), warn func(message string), label string) (T, bool) {
	result, err := operation()
	if err != nil {
		warn("Warden org " + label + " write failed: " + err.Error())
		var zero T
		return zero, false
	}
	return result, true
}

func failedCount(results []sfapi.SaveResult) int {
	failures := 0
	for _, result := range results {
		if !result.Success {
			failures++
		}
	}
	return failures
}

// saveBatches sends records in batches of 200, one after the other, so
// that a batch that fails does not stop the later ones.  batchLabel names a
// batch in warnings.  The result is nil when no batch returned results.
func saveBatches(records []sfapi.Record, save func(batch []sfapi.Record) ([]sfapi.SaveResult, error), warn func(message string), batchLabel func(batchNumber int) string) []sfapi.SaveResult {
	if len(records) == 0 {
		return []sfapi.SaveResult{}
	}
	var allResults []sfapi.SaveResult
	for index, batch := range core.Batch(records, 200) {
		label := batchLabel(index + 1)
		saveResults, ok := BestEffortOrgWrite(func() ([]sfapi.SaveResult, error) { return save(batch) }, warn, label)
		if !ok {
			continue
		}
		if len(saveResults) != len(batch) {
			warn(fmt.Sprintf("Warden org %s returned %d result(s) for %d record(s).", label, len(saveResults), len(batch)))
		}
		if failures := failedCount(saveResults); failures > 0 {
			warn(fmt.Sprintf("Warden org %s returned %d failed record(s).", label, failures))
		}
		allResults = append(allResults, saveResults...)
	}
	return allResults
}

// WriteBatch creates records in batches of 200 without allOrNone.  A
// batch that fails, a batch with failed records and a batch with the wrong
// number of results each produce a warning and nothing else.  The result
// is empty when there are no records and nil when no batch returned
// results.
func WriteBatch(conn sfapi.Conn, objectName string, records []sfapi.Record, warn func(message string), label string) []sfapi.SaveResult {
	batchCount := (len(records) + 199) / 200
	return saveBatches(records, func(batch []sfapi.Record) ([]sfapi.SaveResult, error) {
		return conn.Create(objectName, batch, false)
	}, warn, func(batchNumber int) string {
		return fmt.Sprintf("%s batch %d/%d", label, batchNumber, batchCount)
	})
}

// UpdateBatch updates records in batches of 200 without allOrNone, with
// the warnings and results of WriteBatch.  A failure never undoes the
// access removal that the update describes.
func UpdateBatch(conn sfapi.Conn, objectName string, records []sfapi.Record, warn func(message string), label string) []sfapi.SaveResult {
	return saveBatches(records, func(batch []sfapi.Record) ([]sfapi.SaveResult, error) {
		return conn.Update(objectName, batch, false)
	}, warn, func(batchNumber int) string {
		return fmt.Sprintf("%s batch %d", label, batchNumber)
	})
}

// SaveRun creates a run record and returns its id, or "" when it could not
// be created.
func SaveRun(conn sfapi.Conn, fields sfapi.Record, warn func(message string)) string {
	results := WriteBatch(conn, WardenRunObject, []sfapi.Record{fields}, warn, "run")
	if results == nil {
		return ""
	}
	if len(results) == 0 || !results[0].Success || results[0].ID == "" {
		warn("Warden org run write did not return a record id.")
		return ""
	}
	return results[0].ID
}

// UpdateRun updates the fields of a run record.  A failure produces a
// warning.
func UpdateRun(conn sfapi.Conn, runID string, fields sfapi.Record, warn func(message string)) {
	record := sfapi.Record{"Id": runID}
	for key, value := range fields {
		record[key] = value
	}
	results, ok := BestEffortOrgWrite(func() ([]sfapi.SaveResult, error) {
		return conn.Update(WardenRunObject, []sfapi.Record{record}, false)
	}, warn, "run summary")
	if !ok {
		return
	}
	for index, saveResult := range results {
		if saveResult.Success {
			continue
		}
		detail := "."
		if len(saveResult.Errors) > 0 {
			messages := make([]string, len(saveResult.Errors))
			for i, saveError := range saveResult.Errors {
				messages[i] = saveError.Message
			}
			detail = ": " + strings.Join(messages, "; ")
		}
		warn(fmt.Sprintf("Warden org run summary update result %d/%d failed%s", index+1, len(results), detail))
	}
}
