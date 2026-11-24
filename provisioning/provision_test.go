package provisioning

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

const insertDefaults = `"LastName":"User","Alias":"usr","TimeZoneSidKey":"America/Los_Angeles","LocaleSidKey":"en_US","EmailEncodingKey":"UTF-8","LanguageLocaleKey":"en_US"`

// userJSON returns a users definition entry with the fields an insert
// needs, followed by the given fields.
func userJSON(fields string) string {
	return "{" + insertDefaults + "," + fields + "}"
}

func usersJSON(entries ...string) string {
	return `{"users":[` + strings.Join(entries, ",") + `]}`
}

func parseDoc(t *testing.T, text string) *core.Object {
	t.Helper()
	if text == "" {
		return nil
	}
	value, err := core.ParseJSON([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return value.(*core.Object)
}

func records(t *testing.T, text string) []sfapi.Record {
	t.Helper()
	var rows []sfapi.Record
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return rows
}

func userDescribeFields() []sfapi.Field {
	var fields []sfapi.Field
	for _, name := range []string{"Username", "LastName", "FirstName", "Email", "Alias", "TimeZoneSidKey", "LocaleSidKey", "EmailEncodingKey", "LanguageLocaleKey", "ProfileId", "UserRoleId", "Title", "Department", "UserPermissionKnowledgeUser"} {
		fields = append(fields, sfapitest.Field(name, "string"))
	}
	federationIdentifier := sfapitest.Field("FederationIdentifier", "string")
	federationIdentifier.ExternalID = true
	return append(fields, federationIdentifier)
}

// answer is the records for the queries that contain a text.  The first
// answer whose text a query contains is used.
type answer struct {
	contains string
	rows     string
}

const adminProfile = `[{"Id":"00exx0000000001AAA","Name":"Admin"}]`

func newConn(t *testing.T, answers []answer) *sfapitest.Conn {
	t.Helper()
	conn := sfapitest.New()
	conn.URL = "https://myorg.my.salesforce.com"
	conn.DescribeFields("User", userDescribeFields()...)
	for _, item := range answers {
		conn.Answer(item.contains, records(t, item.rows)...)
	}
	return conn
}

// failSaves makes the changes of one operation on one sObject return the
// given results, and every other change succeed.
func failSaves(conn *sfapitest.Conn, operation, sobject string, results ...sfapi.SaveResult) {
	created := 0
	conn.OnSave = func(change sfapitest.Change) ([]sfapi.SaveResult, error) {
		if change.Operation == operation && change.Sobject == sobject {
			return results, nil
		}
		count := len(change.Records)
		if change.Operation == "delete" {
			count = len(change.IDs)
		}
		saved := make([]sfapi.SaveResult, count)
		for i := range saved {
			saved[i].Success = true
			switch change.Operation {
			case "create":
				created++
				saved[i].ID = change.Sobject + "-" + itoa(created)
			case "update":
				saved[i].ID, _ = change.Records[i]["Id"].(string)
			case "delete":
				saved[i].ID = change.IDs[i]
			}
		}
		return saved, nil
	}
}

func failure(message string) sfapi.SaveResult {
	return sfapi.SaveResult{Errors: []sfapi.SaveError{{Message: message}}}
}

func changes(conn *sfapitest.Conn, operation, sobject string) []sfapitest.Change {
	var found []sfapitest.Change
	for _, change := range conn.Changes {
		if change.Operation == operation && change.Sobject == sobject {
			found = append(found, change)
		}
	}
	return found
}

func queriesContaining(conn *sfapitest.Conn, text string) []string {
	var found []string
	for _, soql := range conn.Queries {
		if strings.Contains(soql, text) {
			found = append(found, soql)
		}
	}
	return found
}

func toJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func deref2(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

type provisionCase struct {
	name     string
	users    string
	personas string
	related  string
	// noPersonas runs without a personas definition.
	noPersonas bool
	externalID string
	fuzzy      bool
	dryRun     bool
	cleanup    bool
	answers    []answer
	setup      func(conn *sfapitest.Conn)
	check      func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result)
}

func runProvisionCase(t *testing.T, test provisionCase) {
	t.Helper()
	conn := newConn(t, test.answers)
	if test.setup != nil {
		test.setup(conn)
	}
	options := Options{
		UsersDoc:         parseDoc(t, test.users),
		PersonasDoc:      parseDoc(t, test.personas),
		RelatedDoc:       parseDoc(t, test.related),
		ExternalID:       test.externalID,
		FuzzyUsername:    test.fuzzy,
		CleanupOnFailure: test.cleanup,
	}
	if test.noPersonas {
		supplied := false
		options.PersonasDoc = parseDoc(t, `{"personas":{}}`)
		options.PersonasSupplied = &supplied
	}
	plan, err := Plan(conn, options)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(conn.Changes) > 0 {
		t.Fatalf("Plan changed records: %+v", conn.Changes)
	}
	result := plan.Preview
	if !test.dryRun {
		if result, err = Apply(conn, plan); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	test.check(t, conn, plan, result)
}

func statuses(result *Result) []string {
	found := make([]string, len(result.Users))
	for i, user := range result.Users {
		found[i] = user.Status
	}
	return found
}

func expectEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", what, got, want)
	}
}

func expectContains(t *testing.T, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s: %q does not contain %q", what, got, want)
	}
}

const licenseUsers = `{"users":[` +
	`{` + insertDefaults + `,"Username":"admin-one@example.test","FederationIdentifier":"NEW-1","personas":["admin"]},` +
	`{` + insertDefaults + `,"Username":"admin-two@example.test","FederationIdentifier":"NEW-2","personas":["admin"]},` +
	`{` + insertDefaults + `,"Username":"standard@example.test","FederationIdentifier":"NEW-3","personas":["standard"]},` +
	`{` + insertDefaults + `,"Username":"inactive@example.test","FederationIdentifier":"NEW-4","personas":["inactive"]},` +
	`{` + insertDefaults + `,"Username":"existing@example.test","FederationIdentifier":"EXISTING","personas":["admin"]}]}`

const licensePersonas = `{"personas":{"admin":{"profile":"Admin"},"standard":{"profile":"Standard"},"inactive":{"profile":"Inactive"}}}`

func licenseAnswers(adminTotal, inactiveStatus string) []answer {
	return []answer{
		{"FROM Profile", `[
			{"Id":"00eadmin00000001AAA","Name":"Admin","UserLicenseId":"0ULadmin0000001AAA","UserLicense":{"Name":"Salesforce","MasterLabel":"Salesforce"}},
			{"Id":"00estandard00001AAA","Name":"Standard","UserLicenseId":"0ULstandard00001AAA","UserLicense":{"Name":"Platform","MasterLabel":"Platform"}},
			{"Id":"00einactive00001AAA","Name":"Inactive","UserLicenseId":"0ULinactive000001AAA","UserLicense":{"Name":"Inactive","MasterLabel":"Inactive"}}]`},
		{"FROM UserLicense", `[
			{"Id":"0ULadmin0000001AAA","Name":"Salesforce","MasterLabel":"Salesforce","TotalLicenses":` + adminTotal + `,"UsedLicenses":0,"Status":"Active"},
			{"Id":"0ULstandard00001AAA","Name":"Platform","MasterLabel":"Platform","TotalLicenses":-1,"UsedLicenses":100,"Status":"Active"},
			{"Id":"0ULinactive000001AAA","Name":"Inactive","MasterLabel":"Inactive","TotalLicenses":100,"UsedLicenses":0,"Status":"` + inactiveStatus + `"}]`},
		{"FROM User WHERE FederationIdentifier IN", `[{"Id":"005existing00001AAA","IsActive":true,"FederationIdentifier":"EXISTING"}]`},
	}
}

func TestProvisionUsers(t *testing.T) {
	defaultPersona := `{"personas":{"default":{"profile":"Admin"}}}`
	profileAnswer := answer{"FROM Profile", adminProfile}
	cases := []provisionCase{
		{
			name:     "dry run changes no record",
			users:    usersJSON(userJSON(`"Username":"a@example.test","personas":["default"]`), userJSON(`"Username":"b@example.test","personas":["default"]`)),
			personas: defaultPersona,
			dryRun:   true,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "total", result.Summary.Total, 2)
				expectEqual(t, "changes", len(conn.Changes), 0)
				expectEqual(t, "statuses", statuses(result), []string{"planned", "planned"})
				expectEqual(t, "actions", result.Users[0].Actions, []string{"wouldCreate", "wouldActivate"})
			},
		},
		{
			name:       "dry run reports the user licenses the new users need",
			users:      licenseUsers,
			personas:   licensePersonas,
			externalID: "FederationIdentifier",
			dryRun:     true,
			answers:    licenseAnswers("1", "Inactive"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "licenses", toJSON(t, result.Licenses), `[`+
					`{"licenseName":"Salesforce","required":2,"available":1,"unlimited":false,"shortfall":1},`+
					`{"licenseName":"Platform","required":1,"available":null,"unlimited":true,"shortfall":0,"note":"unlimited"},`+
					`{"licenseName":"Inactive","required":1,"available":0,"unlimited":false,"shortfall":1,"note":"status Inactive"}]`)
				expectEqual(t, "permissionSetLicenses", toJSON(t, result.PermissionSetLicenses), `{"evaluated":false,"note":"not evaluated"}`)
				expectEqual(t, "summary", result.Summary, Summary{Total: 5, Warnings: 2})
				expectEqual(t, "existing user", result.Users[4].ID, "005existing00001AAA")
				expectEqual(t, "existing user status", result.Users[4].Status, "planned")
				expectEqual(t, "changes", len(conn.Changes), 0)
				human := RenderHuman(result, "", nil)
				expectContains(t, "human", human, "User license headroom for net-new users:\nSalesforce: required 2, available 1, shortfall 1\nPlatform: required 1, available unlimited, shortfall 0 (unlimited)\nInactive: required 1, available 0, shortfall 1 (status Inactive)\nPermission set license headroom: not evaluated.")
				expectContains(t, "human", human, "Processed 5 users: 0 created, 0 updated, 0 failed.")
			},
		},
		{
			name:       "dry run reports no shortfall when the licenses are sufficient",
			users:      licenseUsers,
			personas:   licensePersonas,
			externalID: "FederationIdentifier",
			dryRun:     true,
			answers:    licenseAnswers("10", "Active"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				for _, license := range result.Licenses {
					expectEqual(t, license.LicenseName+" shortfall", license.Shortfall, 0)
				}
				expectEqual(t, "unlimited available", result.Licenses[1].Available, (*int)(nil))
			},
		},
		{
			name:       "live result has no licenses and counts the license shortfalls of the plan as warnings",
			users:      licenseUsers,
			personas:   licensePersonas,
			externalID: "FederationIdentifier",
			answers:    licenseAnswers("1", "Inactive"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "warnings", result.Summary.Warnings, 2)
				if strings.Contains(toJSON(t, result), "licenses") {
					t.Errorf("live result has licenses: %s", toJSON(t, result))
				}
				expectEqual(t, "user creates", len(changes(conn, "create", "User")), 1)
			},
		},
		{
			name: "new users are saved in one create call and existing users in one update call",
			users: usersJSON(
				userJSON(`"Username":"new.user@example.test","FederationIdentifier":"A001","personas":["default"],"FirstName":"New"`),
				userJSON(`"Username":"existing.user@example.test","FederationIdentifier":"A002","personas":["default"],"FirstName":"Existing"`)),
			personas:   defaultPersona,
			externalID: "FederationIdentifier",
			answers:    []answer{profileAnswer, {"FROM User WHERE FederationIdentifier IN", `[{"Id":"005xx0000000002AAA","IsActive":true,"FederationIdentifier":"A002"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				creates, updates := changes(conn, "create", "User"), changes(conn, "update", "User")
				if len(creates) != 1 || len(creates[0].Records) != 1 || creates[0].AllOrNone {
					t.Errorf("user creates: %+v", creates)
				}
				if len(updates) != 1 || len(updates[0].Records) != 1 || updates[0].AllOrNone {
					t.Errorf("user updates: %+v", updates)
				}
				expectEqual(t, "created record", creates[0].Records[0], sfapi.Record{
					"LastName": "User", "Alias": "usr", "TimeZoneSidKey": "America/Los_Angeles", "LocaleSidKey": "en_US", "EmailEncodingKey": "UTF-8", "LanguageLocaleKey": "en_US",
					"Username": "new.user@example.test", "FederationIdentifier": "A001", "FirstName": "New", "IsActive": true, "ProfileId": "00exx0000000001AAA",
				})
				expectEqual(t, "updated id", updates[0].Records[0]["Id"], "005xx0000000002AAA")
				expectEqual(t, "summary", result.Summary, Summary{Total: 2, Created: 1, Updated: 1})
				expectEqual(t, "created actions", result.Users[0].Actions, []string{"created", "activated"})
				expectEqual(t, "updated actions", result.Users[1].Actions, []string{"updated"})
				expectEqual(t, "user query", queriesContaining(conn, "FROM User WHERE"), []string{
					"SELECT Id, IsActive, Name, Username, Email, ProfileId, Profile.Name, UserRoleId, UserRole.Name, FederationIdentifier FROM User WHERE FederationIdentifier IN ('A001','A002')",
				})
				expectEqual(t, "json", toJSON(t, result.Users[1]), `{"key":"A002","id":"005xx0000000002AAA","userName":"Existing User","username":"existing.user@example.test","personas":["default"],"matchedBy":"FederationIdentifier","matchValue":"A002","matched":true,"status":"updated","actions":["updated"],"errors":[]}`)
			},
		},
		{
			name: "users with different match fields are looked up by one query for each field",
			users: usersJSON(
				userJSON(`"match":"FederationIdentifier","FederationIdentifier":"A101","personas":["default"]`),
				userJSON(`"match":"Username","Username":"two@example.test","personas":["default"]`)),
			personas: defaultPersona,
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN ('A101')", `[{"Id":"005xx0000000001AAA","IsActive":true,"FederationIdentifier":"A101"}]`},
				{"FROM User WHERE Username IN ('two@example.test')", `[{"Id":"005xx0000000002AAA","IsActive":true,"Username":"two@example.test"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "user queries", len(queriesContaining(conn, "FROM User WHERE")), 2)
				expectEqual(t, "matchedBy", []string{deref2(result.Users[0].MatchedBy), deref2(result.Users[1].MatchedBy)}, []string{"FederationIdentifier", "Username"})
				expectEqual(t, "updated", result.Summary.Updated, 2)
			},
		},
		{
			name: "the same value in two match fields is not a duplicate",
			users: usersJSON(
				userJSON(`"match":"FederationIdentifier","FederationIdentifier":"A200","personas":["default"]`),
				userJSON(`"match":"Username","Username":"A200","personas":["default"]`)),
			personas: defaultPersona,
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN ('A200')", `[{"Id":"005xx0000000001AAA","IsActive":true,"FederationIdentifier":"A200"}]`},
				{"FROM User WHERE Username IN ('A200')", `[{"Id":"005xx0000000002AAA","IsActive":true,"Username":"A200"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"updated", "updated"})
			},
		},
		{
			name: "an empty match field fails that user and the next user is created",
			users: usersJSON(
				userJSON(`"match":"FederationIdentifier","FederationIdentifier":"","personas":["default"]`),
				userJSON(`"Username":"insert-only@example.test","personas":["default"]`)),
			personas: defaultPersona,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"failed", "created"})
				expectEqual(t, "matchedBy", deref2(result.Users[0].MatchedBy), "FederationIdentifier")
				expectEqual(t, "matchValue", result.Users[0].MatchValue, (*string)(nil))
				expectEqual(t, "errors", result.Users[0].Errors, []string{"match field FederationIdentifier must be populated on the user."})
				expectEqual(t, "second matchedBy", result.Users[1].MatchedBy, (*string)(nil))
				expectContains(t, "json", toJSON(t, result.Users[1]), `"matchedBy":null,"matchValue":null,"matched":false`)
			},
		},
		{
			name: "a match key of a user replaces the external id option for that user",
			users: usersJSON(
				userJSON(`"match":"Username","Username":"override@example.test","FederationIdentifier":"FLAG-IGNORED","personas":["default"]`),
				userJSON(`"FederationIdentifier":"FLAG-001","personas":["default"]`)),
			personas:   defaultPersona,
			externalID: "FederationIdentifier",
			answers: []answer{profileAnswer,
				{"FROM User WHERE Username IN ('override@example.test')", `[{"Id":"005xx0000000001AAA","IsActive":true,"Username":"override@example.test"}]`},
				{"FROM User WHERE FederationIdentifier IN ('FLAG-001')", `[{"Id":"005xx0000000002AAA","IsActive":true,"FederationIdentifier":"FLAG-001"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "matchedBy", []string{deref2(result.Users[0].MatchedBy), deref2(result.Users[1].MatchedBy)}, []string{"Username", "FederationIdentifier"})
				expectEqual(t, "updated", result.Summary.Updated, 2)
			},
		},
		{
			name:     "references that are not found are counted as warnings",
			users:    usersJSON(userJSON(`"Username":"a@example.test","personas":["default"]`)),
			personas: `{"personas":{"default":{"profile":"Missing","role":"NoRole","permissionSets":["NoPerm"],"permissionSetGroups":["NoPsg"],"publicGroups":["NoGroup"],"queues":["NoQueue"]}}}`,
			dryRun:   true,
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "warnings", plan.Warnings, []string{
					`Profile reference "Missing" was not found.`,
					`UserRole reference "NoRole" was not found.`,
					`PermissionSet reference "NoPerm" was not found.`,
					`PermissionSetGroup reference "NoPsg" was not found.`,
					`Group reference "NoGroup" was not found.`,
					`Group reference "NoQueue" was not found.`,
				})
				expectEqual(t, "summary warnings", result.Summary.Warnings, 6)
				expectEqual(t, "reference queries", conn.Queries[:6], []string{
					"SELECT Id, Name FROM Profile WHERE Name IN ('Missing')",
					"SELECT Id, DeveloperName, Name FROM UserRole WHERE DeveloperName IN ('NoRole') OR Name IN ('NoRole')",
					"SELECT Id, Name, Label FROM PermissionSet WHERE Name IN ('NoPerm')",
					"SELECT Id, DeveloperName, MasterLabel FROM PermissionSetGroup WHERE DeveloperName IN ('NoPsg')",
					"SELECT Id, DeveloperName, Name FROM Group WHERE DeveloperName IN ('NoGroup') AND Type = 'Regular'",
					"SELECT Id, DeveloperName, Name FROM Group WHERE DeveloperName IN ('NoQueue') AND Type = 'Queue'",
				})
				expectEqual(t, "errors", result.Users[0].Errors, []string{
					`Required Profile reference "Missing" was not found.`,
					`Required UserRole reference "NoRole" was not found.`,
				})
			},
		},
		{
			name:       "public group sync removes other public groups and keeps queue memberships",
			users:      usersJSON(userJSON(`"Username":"existing.user@example.test","FederationIdentifier":"A002","personas":["default"]`)),
			personas:   `{"personas":{"default":{"profile":"Admin","publicGroupMode":"sync","publicGroups":["Pub1"]}}}`,
			externalID: "FederationIdentifier",
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN", `[{"Id":"005xx0000000002AAA","IsActive":true,"FederationIdentifier":"A002"}]`},
				{"FROM Group WHERE DeveloperName IN ('Pub1') AND Type = 'Regular'", `[{"Id":"00Gpub000000001AAA","DeveloperName":"Pub1"}]`},
				{"FROM GroupMember", `[
					{"Id":"0GMpub","GroupId":"00Gold000000001AAA","UserOrGroupId":"005xx0000000002AAA","Group":{"Type":"Regular"}},
					{"Id":"0GMqueue","GroupId":"00Gqueue0000001AAA","UserOrGroupId":"005xx0000000002AAA","Group":{"Type":"Queue"}}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				deletes := changes(conn, "delete", "GroupMember")
				if len(deletes) != 1 || !reflect.DeepEqual(deletes[0].IDs, []string{"0GMpub"}) {
					t.Errorf("group member deletes: %+v", deletes)
				}
				expectEqual(t, "group member creates", changes(conn, "create", "GroupMember")[0].Records, []sfapi.Record{{"GroupId": "00Gpub000000001AAA", "UserOrGroupId": "005xx0000000002AAA"}})
				expectEqual(t, "actions", result.Users[0].Actions, []string{"updated", "addedPublicGroupMember", "removedPublicGroupMember"})
				stateQueries := []string{
					"SELECT Id FROM UserLogin WHERE UserId = '005xx0000000002AAA' AND IsFrozen = true",
					"SELECT Id, AssigneeId, PermissionSetId, PermissionSetGroupId, PermissionSet.IsOwnedByProfile, PermissionSet.Name, PermissionSet.Label, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel FROM PermissionSetAssignment WHERE AssigneeId IN ('005xx0000000002AAA')",
					"SELECT Id, GroupId, UserOrGroupId, Group.Type, Group.DeveloperName, Group.Name FROM GroupMember WHERE UserOrGroupId IN ('005xx0000000002AAA') AND Group.Type IN ('Regular', 'Queue')",
				}
				// The preview of the plan and the live run each read the state.
				expectEqual(t, "state queries", queriesContaining(conn, "005xx0000000002AAA"), append(append([]string{}, stateQueries...), stateQueries...))
			},
		},
		{
			name:       "permission set sync keeps assignments owned by a profile or made by a permission set group",
			users:      usersJSON(userJSON(`"Username":"existing.user@example.test","FederationIdentifier":"A003","personas":["default"]`)),
			personas:   `{"personas":{"default":{"profile":"Admin","permissionSetMode":"sync","permissionSets":["KeepPerm"]}}}`,
			externalID: "FederationIdentifier",
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN", `[{"Id":"005xx0000000003AAA","IsActive":true,"FederationIdentifier":"A003"}]`},
				{"FROM PermissionSet WHERE Name IN ('KeepPerm')", `[{"Id":"0PSkeep00000001AAA","Name":"KeepPerm"}]`},
				{"FROM PermissionSetAssignment", `[
					{"Id":"0PaKeep","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSkeep00000001AAA","PermissionSetGroupId":null,"PermissionSet":{"IsOwnedByProfile":false}},
					{"Id":"0PaProfileOwned","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSprofileOwned","PermissionSetGroupId":null,"PermissionSet":{"IsOwnedByProfile":true}},
					{"Id":"0PaPsgBacked","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSpsgBacked","PermissionSetGroupId":"0PGsourceGroup","PermissionSet":{"IsOwnedByProfile":false}},
					{"Id":"0PaExtra","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSextra","PermissionSetGroupId":null,"PermissionSet":{"IsOwnedByProfile":false}}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				deletes := changes(conn, "delete", "PermissionSetAssignment")
				if len(deletes) != 1 || !reflect.DeepEqual(deletes[0].IDs, []string{"0PaExtra"}) {
					t.Errorf("assignment deletes: %+v", deletes)
				}
				expectEqual(t, "assignment creates", len(changes(conn, "create", "PermissionSetAssignment")), 0)
				expectEqual(t, "actions", result.Users[0].Actions, []string{"updated", "removedPermissionSet"})
			},
		},
		{
			name:       "additive mode adds the missing permission set and removes nothing",
			users:      usersJSON(userJSON(`"Username":"existing.user@example.test","FederationIdentifier":"A003","personas":["default"]`)),
			personas:   `{"personas":{"default":{"profile":"Admin","permissionSets":["KeepPerm","NewPerm"]}}}`,
			externalID: "FederationIdentifier",
			dryRun:     true,
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN", `[{"Id":"005xx0000000003AAA","IsActive":false,"FederationIdentifier":"A003"}]`},
				{"FROM PermissionSet WHERE Name IN", `[{"Id":"0PSkeep00000001AAA","Name":"KeepPerm"},{"Id":"0PSnew000000001AAA","Name":"NewPerm"}]`},
				{"FROM UserLogin", `[{"Id":"0LL1"}]`},
				{"FROM PermissionSetAssignment", `[
					{"Id":"0PaKeep","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSkeep00000001AAA","PermissionSetGroupId":null,"PermissionSet":{"IsOwnedByProfile":false}},
					{"Id":"0PaExtra","AssigneeId":"005xx0000000003AAA","PermissionSetId":"0PSextra","PermissionSetGroupId":null,"PermissionSet":{"IsOwnedByProfile":false}}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "actions", result.Users[0].Actions, []string{"wouldUpdate", "wouldActivate", "wouldUnfreeze", "wouldAssignPermissionSet"})
				expectEqual(t, "plan actions", plan.Plans[0].Actions, []string{"updated", "activated"})
			},
		},
		{
			name:       "an existing user that is inactive and frozen is activated and its login is unfrozen",
			users:      usersJSON(userJSON(`"FederationIdentifier":"A300","personas":["default"]`)),
			personas:   defaultPersona,
			externalID: "FederationIdentifier",
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN", `[{"Id":"005frozen","IsActive":false,"FederationIdentifier":"A300"}]`},
				{"FROM UserLogin", `[{"Id":"0LLfrozen"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "actions", result.Users[0].Actions, []string{"updated", "activated", "unfrozen"})
				expectEqual(t, "preview actions", plan.Preview.Users[0].Actions, []string{"wouldUpdate", "wouldActivate", "wouldUnfreeze"})
				logins := changes(conn, "update", "UserLogin")
				if len(logins) != 1 || logins[0].AllOrNone {
					t.Fatalf("login updates: %+v", logins)
				}
				expectEqual(t, "login update", logins[0].Records, []sfapi.Record{{"Id": "0LLfrozen", "IsFrozen": false}})
				expectEqual(t, "IsActive", changes(conn, "update", "User")[0].Records[0]["IsActive"], true)
			},
		},
		{
			name:       "a match value that two users have fails that user",
			users:      usersJSON(userJSON(`"Username":"dup@example.test","FederationIdentifier":"A100","personas":["default"]`)),
			personas:   defaultPersona,
			externalID: "FederationIdentifier",
			answers: []answer{profileAnswer,
				{"FROM User WHERE FederationIdentifier IN", `[{"Id":"0051","IsActive":true,"FederationIdentifier":"A100"},{"Id":"0052","IsActive":true,"FederationIdentifier":"A100"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "matchedBy", deref2(result.Users[0].MatchedBy), "FederationIdentifier")
				expectEqual(t, "errors", result.Users[0].Errors, []string{`Multiple users matched FederationIdentifier="A100".`})
				expectEqual(t, "changes", len(conn.Changes), 0)
			},
		},
		{
			name:     "a fuzzy username matches a username with a suffix",
			users:    usersJSON(userJSON(`"match":"Username","Username":"fuzzy@example.test","personas":["default"]`)),
			personas: defaultPersona,
			fuzzy:    true,
			answers: []answer{profileAnswer,
				{"FROM User WHERE (Username = 'fuzzy@example.test' OR Username LIKE 'fuzzy@example.test.%')", `[{"Id":"005fuzzy","IsActive":true,"Username":"fuzzy@example.test.sandbox"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "updated")
				expectEqual(t, "id", result.Users[0].ID, "005fuzzy")
				expectEqual(t, "username", deref2(result.Users[0].Username), "fuzzy@example.test.sandbox")
				expectEqual(t, "matchValue", deref2(result.Users[0].MatchValue), "fuzzy@example.test")
			},
		},
		{
			name: "fuzzyUsername false on a user turns the fuzzy option off for that user",
			users: usersJSON(
				userJSON(`"match":"Username","fuzzyUsername":false,"Username":"exact@example.test","personas":["default"]`),
				userJSON(`"match":"Username","fuzzyUsername":"yes","Username":"bad@example.test","personas":["default"]`)),
			personas: defaultPersona,
			fuzzy:    true,
			dryRun:   true,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "user queries", queriesContaining(conn, "FROM User WHERE"), []string{
					"SELECT Id, IsActive, Name, Username, Email, ProfileId, Profile.Name, UserRoleId, UserRole.Name FROM User WHERE Username IN ('exact@example.test')",
				})
				expectEqual(t, "errors", result.Users[1].Errors, []string{"fuzzyUsername must be a boolean. Got: yes."})
			},
		},
		{
			name:     "an assignment that fails is reported in the errors of the user",
			users:    usersJSON(userJSON(`"Username":"u@example.test","personas":["default"]`)),
			personas: `{"personas":{"default":{"profile":"Admin","permissionSets":["PermA"]}}}`,
			answers:  []answer{profileAnswer, {"FROM PermissionSet WHERE Name IN ('PermA')", `[{"Id":"0PSx","Name":"PermA"}]`}},
			setup: func(conn *sfapitest.Conn) {
				failSaves(conn, "create", "PermissionSetAssignment", failure("PSA failed"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "errors", result.Users[0].Errors, []string{"PSA failed"})
				expectEqual(t, "actions", result.Users[0].Actions, []string{"created", "activated"})
				expectEqual(t, "id", result.Users[0].ID, "User-1")
				expectEqual(t, "csv", RenderCsv(result), "userKey,userId,userName,username,personas,matchedBy,status,action,detail,error\n"+
					"u@example.test,User-1,User,u@example.test,default,,failed,created,,\n"+
					"u@example.test,User-1,User,u@example.test,default,,failed,activated,,\n"+
					"u@example.test,User-1,User,u@example.test,default,,failed,,,PSA failed")
			},
		},
		{
			name:     "an assignment call that partly succeeds reports the action and the error",
			users:    usersJSON(userJSON(`"Username":"partial@example.test","personas":["default"]`)),
			personas: `{"personas":{"default":{"profile":"Admin","permissionSets":["PermA","PermB"]}}}`,
			answers:  []answer{profileAnswer, {"FROM PermissionSet WHERE Name IN", `[{"Id":"0PSA","Name":"PermA"},{"Id":"0PSB","Name":"PermB"}]`}},
			setup: func(conn *sfapitest.Conn) {
				failSaves(conn, "create", "PermissionSetAssignment", sfapi.SaveResult{Success: true}, failure("second PSA failed"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "actions", result.Users[0].Actions, []string{"created", "activated", "assignedPermissionSet"})
				expectEqual(t, "errors", result.Users[0].Errors, []string{"second PSA failed"})
				expectEqual(t, "assignment records", changes(conn, "create", "PermissionSetAssignment")[0].Records, []sfapi.Record{
					{"AssigneeId": "User-1", "PermissionSetId": "0PSA"}, {"AssigneeId": "User-1", "PermissionSetId": "0PSB"},
				})
			},
		},
		{
			name:       "a field that cannot be updated fails the existing user",
			users:      usersJSON(userJSON(`"FederationIdentifier":"A200","personas":["default"]`)),
			personas:   defaultPersona,
			externalID: "FederationIdentifier",
			answers:    []answer{profileAnswer, {"FROM User WHERE FederationIdentifier IN", `[{"Id":"005x","IsActive":true,"FederationIdentifier":"A200"}]`}},
			setup: func(conn *sfapitest.Conn) {
				fields := userDescribeFields()
				for i := range fields {
					if fields[i].Name == "Alias" {
						fields[i].Updateable = false
					}
				}
				conn.DescribeFields("User", fields...)
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "errors", result.Users[0].Errors, []string{"Field Alias is not updateable."})
				if strings.Contains(toJSON(t, result.Users[0]), `"id"`) {
					t.Errorf("failed user has an id: %s", toJSON(t, result.Users[0]))
				}
			},
		},
		{
			name:     "personas are merged: assignment lists are joined",
			users:    usersJSON(userJSON(`"Username":"multi@example.test","personas":["base","support"]`)),
			personas: `{"personas":{"base":{"profile":"Admin","permissionSets":["BasePS"]},"support":{"permissionSets":["SupportPS","BasePS"]}}}`,
			answers:  []answer{profileAnswer, {"FROM PermissionSet WHERE Name IN", `[{"Id":"0PSbase000000001AAA","Name":"BasePS"},{"Id":"0PSsupport00001AAA","Name":"SupportPS"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "created")
				expectEqual(t, "personas", result.Users[0].Personas, []string{"base", "support"})
				expectEqual(t, "assignment records", changes(conn, "create", "PermissionSetAssignment")[0].Records, []sfapi.Record{
					{"AssigneeId": "User-1", "PermissionSetId": "0PSbase000000001AAA"}, {"AssigneeId": "User-1", "PermissionSetId": "0PSsupport00001AAA"},
				})
				expectEqual(t, "permission set query", queriesContaining(conn, "FROM PermissionSet WHERE"), []string{"SELECT Id, Name, Label FROM PermissionSet WHERE Name IN ('BasePS','SupportPS')"})
				expectEqual(t, "effective persona", toJSON(t, plan.Plans[0].EffectivePersona), `{"permissionSets":["BasePS","SupportPS"],"profile":"Admin"}`)
				expectEqual(t, "plan id", plan.Plans[0].PlanID, "0:multi@example.test:base+support")
			},
		},
		{
			name: "personas that name different profiles fail that user and the next user is created",
			users: usersJSON(
				userJSON(`"Username":"conflict@example.test","personas":["a","b"]`),
				userJSON(`"Username":"good@example.test","personas":["a"]`)),
			personas: `{"personas":{"a":{"profile":"Admin"},"b":{"profile":"Standard"}}}`,
			answers:  []answer{{"FROM Profile", `[{"Id":"00exx0000000001AAA","Name":"Admin"},{"Id":"00exx0000000002AAA","Name":"Standard"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"failed", "created"})
				expectEqual(t, "errors", result.Users[0].Errors, []string{"Personas conflict on profile: Admin, Standard."})
				expectEqual(t, "validation plan id", plan.ValidationResults[0].PlanID, "0:conflict@example.test:a+b:validation")
			},
		},
		{
			name: "a profile key or a ProfileId of the user replaces the persona profile and stops the conflict",
			users: usersJSON(
				userJSON(`"Username":"byname@example.test","profile":"Standard","personas":["a","b"]`),
				userJSON(`"Username":"byid@example.test","ProfileId":"00eRAW000000001AAA","personas":["a","b"]`),
				userJSON(`"Username":"both@example.test","profile":"Standard","ProfileId":"00eRAW000000001AAA","personas":["a"]`),
				userJSON(`"Username":"unknown@example.test","profile":"Nope","personas":["a"]`),
				userJSON(`"Username":"number@example.test","profile":7,"personas":["a"]`)),
			personas: `{"personas":{"a":{"profile":"Admin"},"b":{"profile":"Other"}}}`,
			answers:  []answer{{"FROM Profile", `[{"Id":"00exx0000000001AAA","Name":"Admin"},{"Id":"00exx0000000002AAA","Name":"Standard"},{"Id":"00exx0000000003AAA","Name":"Other"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"created", "created", "failed", "failed", "failed"})
				created := changes(conn, "create", "User")[0].Records
				expectEqual(t, "profile by name", created[0]["ProfileId"], "00exx0000000002AAA")
				expectEqual(t, "raw profile id", created[1]["ProfileId"], "00eRAW000000001AAA")
				if _, sent := created[0]["profile"]; sent {
					t.Errorf("the profile key was sent to the org: %+v", created[0])
				}
				expectEqual(t, "both", result.Users[2].Errors, []string{`Set either "profile" or "ProfileId" on a user, not both.`})
				expectEqual(t, "unknown", result.Users[3].Errors, []string{`Required Profile reference "Nope" was not found.`})
				expectEqual(t, "number", result.Users[4].Errors, []string{`user "profile" must be a string (a profile name or Id). Got: 7`})
				expectEqual(t, "profile query", queriesContaining(conn, "FROM Profile")[0], "SELECT Id, Name FROM Profile WHERE Name IN ('Admin','Other','Standard','Nope')")
			},
		},
		{
			name:     "a role key of the user replaces the persona role",
			users:    usersJSON(userJSON(`"Username":"role@example.test","role":"CEO","personas":["default"]`)),
			personas: `{"personas":{"default":{"profile":"Admin","role":"Sales"}}}`,
			answers:  []answer{profileAnswer, {"FROM UserRole", `[{"Id":"00Exx0000000001AAA","Name":"Chief","DeveloperName":"CEO"},{"Id":"00Exx0000000002AAA","Name":"Sales","DeveloperName":"SalesDev"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "role id", changes(conn, "create", "User")[0].Records[0]["UserRoleId"], "00Exx0000000001AAA")
				expectEqual(t, "role query", queriesContaining(conn, "FROM UserRole"), []string{"SELECT Id, DeveloperName, Name FROM UserRole WHERE DeveloperName IN ('Sales','CEO') OR Name IN ('Sales','CEO')"})
				expectEqual(t, "labels", toJSON(t, plan.Refs.Labels), `{"00exx0000000001AAA":{"id":"00exx0000000001AAA","apiName":"Admin","label":"Admin","type":"Profile"},"00Exx0000000001AAA":{"id":"00Exx0000000001AAA","apiName":"CEO","label":"Chief","type":"UserRole"},"00Exx0000000002AAA":{"id":"00Exx0000000002AAA","apiName":"SalesDev","label":"Sales","type":"UserRole"}}`)
				expectEqual(t, "roles", toJSON(t, plan.Refs.RolesByRef), `{"CEO":"00Exx0000000001AAA","Sales":"00Exx0000000002AAA"}`)
			},
		},
		{
			name: "an insert without Username and Alias gets them from the email, the org domain and the names",
			users: `{"users":[{"Email":"jane.doe@example.test","FirstName":"Jane","LastName":"Doe","TimeZoneSidKey":"America/Los_Angeles","LocaleSidKey":"en_US","EmailEncodingKey":"UTF-8","LanguageLocaleKey":"en_US","personas":["default"]},` +
				userJSON(`"Username":"explicit@example.test","Alias":"keepme","Email":"explicit@example.test","FirstName":"Ex","personas":["default"]`) + `]}`,
			personas: defaultPersona,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				created := changes(conn, "create", "User")[0].Records
				expectEqual(t, "username", created[0]["Username"], "jane.doe@example.test.myorg")
				expectEqual(t, "alias", created[0]["Alias"], "jandoe")
				expectEqual(t, "explicit username", created[1]["Username"], "explicit@example.test")
				expectEqual(t, "explicit alias", created[1]["Alias"], "keepme")
				expectEqual(t, "user name", deref2(result.Users[0].UserName), "Jane Doe")
			},
		},
		{
			name:       "an update gets no default Username or Alias",
			users:      `{"users":[{"Email":"jane.doe@example.test","FirstName":"Jane","LastName":"Doe","FederationIdentifier":"A9","personas":["default"]}]}`,
			personas:   defaultPersona,
			externalID: "federationidentifier",
			answers:    []answer{profileAnswer, {"FROM User WHERE FederationIdentifier IN", `[{"Id":"005upd","IsActive":true,"Name":"Jane D","Username":"jd@example.test","FederationIdentifier":"a9"}]`}},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				updated := changes(conn, "update", "User")[0].Records[0]
				expectEqual(t, "record", updated, sfapi.Record{"Email": "jane.doe@example.test", "FirstName": "Jane", "LastName": "Doe", "FederationIdentifier": "A9", "IsActive": true, "ProfileId": "00exx0000000001AAA", "Id": "005upd"})
				expectEqual(t, "user name", deref2(result.Users[0].UserName), "Jane D")
				expectEqual(t, "username", deref2(result.Users[0].Username), "jd@example.test")
			},
		},
		{
			name:     "persona user attributes are applied and a field of the user replaces them",
			users:    usersJSON(userJSON(`"Username":"kb@example.test","userpermissionknowledgeuser":false,"personas":["default"]`), userJSON(`"Username":"kb2@example.test","personas":["default"]`)),
			personas: `{"personas":{"default":{"profile":"Admin","userAttributes":{"UserPermissionKnowledgeUser":true,"department":"Support"}}}}`,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				created := changes(conn, "create", "User")[0].Records
				expectEqual(t, "user value", created[0]["UserPermissionKnowledgeUser"], false)
				expectEqual(t, "persona value", created[1]["UserPermissionKnowledgeUser"], true)
				expectEqual(t, "department", created[0]["Department"], "Support")
				expectEqual(t, "target order", plan.Plans[0].Target.Keys[:2], []string{"UserPermissionKnowledgeUser", "Department"})
			},
		},
		{
			name: "validation problems of a user are reported for that user",
			users: usersJSON(
				userJSON(`"Username":"legacy@example.test","persona":"default"`),
				userJSON(`"Username":"none@example.test"`),
				userJSON(`"Username":"unknown@example.test","personas":["ghost"]`),
				userJSON(`"Username":"badmatch@example.test","match":"NotAField","personas":["default"]`),
				userJSON(`"Username":"related@example.test","related":["contact"],"personas":["default"]`)),
			personas: defaultPersona,
			dryRun:   true,
			answers:  []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"failed", "failed", "failed", "failed", "failed"})
				expectEqual(t, "legacy", result.Users[0].Errors, []string{`"persona" is no longer supported; use "personas": [ ... ].`})
				expectEqual(t, "none", result.Users[1].Errors, []string{"Each user must include a non-empty personas array."})
				expectEqual(t, "unknown", result.Users[2].Errors, []string{`Unknown persona "ghost".`})
				expectEqual(t, "bad match", result.Users[3].Errors, []string{"match must name a valid filterable User field: NotAField."})
				expectEqual(t, "related", result.Users[4].Errors, []string{"related was supplied but no --related-def catalog was provided."})
				expectEqual(t, "plans", len(plan.Plans), 0)
				expectEqual(t, "json", toJSON(t, result.Users[0]), `{"key":"legacy@example.test","userName":"User","username":"legacy@example.test","personas":[],"matchedBy":null,"matchValue":null,"matched":false,"status":"failed","actions":[],"errors":["\"persona\" is no longer supported; use \"personas\": [ ... ]."]}`)
			},
		},
		{
			name:       "without a personas definition a user is provisioned from its profile key",
			users:      usersJSON(userJSON(`"Username":"profile-only@example.test","profile":"Admin"`), `{"profile":"Admin","Email":"x@example.test"}`, userJSON(`"Username":"missing@example.test","profile":"Missing"`), userJSON(`"Username":"noprofile@example.test"`)),
			noPersonas: true,
			dryRun:     true,
			answers:    []answer{profileAnswer},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"planned", "failed", "failed", "failed"})
				expectEqual(t, "personas", result.Users[0].Personas, []string{})
				expectEqual(t, "missing fields", result.Users[1].Errors, []string{"Missing required fields for insert: LastName, Alias, TimeZoneSidKey, LocaleSidKey, EmailEncodingKey, LanguageLocaleKey."})
				expectEqual(t, "missing profile", result.Users[2].Errors, []string{`Required Profile reference "Missing" was not found.`})
				expectEqual(t, "no profile", result.Users[3].Errors, []string{"Missing required fields for insert: ProfileId."})
				expectContains(t, "human", RenderHuman(result, "file (no --personas-def) [default]", nil), "Persona source: file (no --personas-def) [default]\n\nprofile-only@example.test · planned\n  unmatched · personas: (none)\n  action: wouldCreate\n  action: wouldActivate\n")
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { runProvisionCase(t, test) })
	}
}
