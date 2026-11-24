package provisioning

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func TestBuildDefaultAlias(t *testing.T) {
	cases := []struct {
		first, last any
		want        string
	}{
		{"Jane", "Doe", "jandoe"},
		{"Al", "Fitzgerald", "alfitz"},
		{"Christopher", "Li", "chrili"},
		{"", "O'Neil-Smith", "oneils"},
		{"J.", nil, "j"},
		{nil, nil, ""},
		{"!!", "??", ""},
		{float64(7), "Ng", "ng"},
	}
	for _, test := range cases {
		if got := BuildDefaultAlias(test.first, test.last); got != test.want {
			t.Errorf("BuildDefaultAlias(%v, %v) = %q, want %q", test.first, test.last, got, test.want)
		}
	}
}

func TestDeriveMyDomain(t *testing.T) {
	cases := map[string]string{
		"https://myorg.my.salesforce.com":               "myorg",
		"https://MyOrg--dev.sandbox.my.salesforce.com/": "myorg--dev",
		"not a url": "",
		"":          "",
	}
	for input, want := range cases {
		if got := DeriveMyDomain(input); got != want {
			t.Errorf("DeriveMyDomain(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidatePersonaModes(t *testing.T) {
	cases := []struct {
		name     string
		personas string
		want     string
	}{
		{"valid", `{"a":{"permissionSetMode":"sync","queueMode":"additive","queues":[]}}`, ""},
		{"unknown mode", `{"a":{"publicGroupMode":"replace"}}`, `Invalid assignment mode "replace". Expected additive or sync.`},
		{"null mode", `{"a":{"queueMode":null}}`, `Invalid assignment mode "null". Expected additive or sync.`},
		{"list that is not a list", `{"a":{"permissionSets":"One"}}`, `Persona "a" has non-array permissionSets.`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePersonaModes(parseDoc(t, test.personas))
			got := ""
			if err != nil {
				got = err.Error()
			}
			expectEqual(t, "error", got, test.want)
		})
	}
}

func TestMergePersonas(t *testing.T) {
	fieldMap := core.BuildFieldMap([]core.FieldMeta{{Name: "Department"}, {Name: "Title"}})
	personas := parseDoc(t, `{
		"a":{"profile":"Admin","role":"CEO","permissionSets":["P1","P2"],"permissionSetMode":"sync","userAttributes":{"department":"Sales","Title":"Rep"}},
		"b":{"profile":"Admin","role":"COO","permissionSets":["P2","P3"],"queues":["Q1"],"permissionSetMode":"additive","userAttributes":{"Department":"Support","Title":"Rep"}},
		"c":{"publicGroups":["G1"]}}`)
	cases := []struct {
		name            string
		names           []string
		overrides       string
		roleOverride    bool
		wantPersona     string
		wantErrorCodes  []string
		wantFirstErrors []string
	}{
		{"one persona", []string{"a"}, `{}`, false, `{"permissionSets":["P1","P2"],"profile":"Admin","role":"CEO","permissionSetMode":"sync","userAttributes":{"Department":"Sales","Title":"Rep"}}`, nil, nil},
		{"lists are joined without repeats", []string{"a", "c"}, `{}`, false, `{"permissionSets":["P1","P2"],"publicGroups":["G1"],"profile":"Admin","role":"CEO","permissionSetMode":"sync","userAttributes":{"Department":"Sales","Title":"Rep"}}`, nil, nil},
		{"conflicts on role, mode and attribute", []string{"a", "b"}, `{}`, false, `{"permissionSets":["P1","P2","P3"],"queues":["Q1"],"profile":"Admin","userAttributes":{"Department":"Sales","Title":"Rep"}}`, []string{"errorPersonaConflictRole", "errorPersonaConflictMode", "errorPersonaConflictUserAttribute"}, []string{"CEO, COO"}},
		{"a field of the user stops the attribute conflict and a role override the role conflict", []string{"a", "b"}, `{"Department":"Mine"}`, true, `{"permissionSets":["P1","P2","P3"],"queues":["Q1"],"profile":"Admin","userAttributes":{"Department":"Sales","Title":"Rep"}}`, []string{"errorPersonaConflictMode"}, []string{"permissionSetMode"}},
		{"unknown persona", []string{"zz", "c"}, `{}`, false, `{"publicGroups":["G1"]}`, []string{"errorUnknownPersona"}, []string{"zz"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			effective, validationErrors := MergePersonas(test.names, personas, parseDoc(t, test.overrides), fieldMap, false, test.roleOverride)
			expectEqual(t, "persona", toJSON(t, effective), test.wantPersona)
			var codes []string
			for _, validationError := range validationErrors {
				codes = append(codes, validationError.Code)
			}
			expectEqual(t, "codes", codes, test.wantErrorCodes)
			if len(validationErrors) > 0 {
				expectEqual(t, "first error arguments", validationErrors[0].MessageArgs, test.wantFirstErrors)
			}
		})
	}
}

func TestCoreUsesTheFullMessageTable(t *testing.T) {
	expectEqual(t, "core message", core.ProvisioningMessage("errorUnknownPersona", "x"), `Unknown persona "x".`)
	expectEqual(t, "unknown code", Message("noSuchCode", "x"), "noSuchCode")
	expectEqual(t, "missing argument", Message("errorFieldNotWritable", "Alias"), "Field Alias is not .")
}

func TestMatchQueryBatches(t *testing.T) {
	values := make([]string, 450)
	for i := range values {
		values[i] = itoa(i)
	}
	batches, err := MatchQueryBatches(values, "SELECT Id FROM X WHERE Y IN (", nil)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "batch sizes", []int{len(batches[0]), len(batches[1]), len(batches[2])}, []int{200, 200, 50})
	long := string(make([]byte, 18_000))
	if _, err := MatchQueryBatches([]string{long}, "SELECT", nil); err == nil || err.Error() != "A lookup value exceeds the 18000-character query budget." {
		t.Errorf("got error %v", err)
	}
	var rejected []string
	batches, err = MatchQueryBatches([]string{"a", long, "b"}, "SELECT", func(value string) { rejected = append(rejected, value) })
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "kept", batches, [][]string{{"a", "b"}})
	expectEqual(t, "rejected", len(rejected), 1)
}

func TestLoadAssignmentStateQueriesOneHundredUsersAtATime(t *testing.T) {
	conn := sfapitest.New()
	conn.Answer("FROM PermissionSetAssignment", sfapi.Record{"Id": "0Pa1", "AssigneeId": "005-0"}, sfapi.Record{"Id": "0Pa2", "AssigneeId": "005-0"})
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "005-" + itoa(i)
	}
	state, err := LoadAssignmentState(conn, ids, &AssignmentStateOptions{PermissionSetAssignments: true})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "queries", len(conn.Queries), 2)
	if !strings.HasSuffix(conn.Queries[1], " FROM PermissionSetAssignment WHERE AssigneeId IN ('005-100')") {
		t.Errorf("second query: %s", conn.Queries[1])
	}
	expectEqual(t, "rows", len(state.PsaByUserID["005-0"]), 4)
	all, err := LoadAssignmentState(sfapitest.New(), nil, nil)
	if err != nil || len(all.PsaByUserID) != 0 {
		t.Errorf("empty target list: %+v %v", all, err)
	}
}

func TestExecuteRunsPlanAndApplyInOneCall(t *testing.T) {
	users := usersJSON(
		userJSON(`"Username":"new.user@example.test","FederationIdentifier":"A001","personas":["default"],"ProfileId":"00eBAD000000001AAA"`),
		userJSON(`"Username":"existing.user@example.test","FederationIdentifier":"A002","personas":["default"],"UserRoleId":"00EBAD000000001AAA"`))
	answers := []answer{{"FROM Profile", adminProfile}, {"FROM User WHERE FederationIdentifier IN", `[{"Id":"005xx0000000002AAA","IsActive":true,"FederationIdentifier":"A002"}]`}}
	t.Run("live run adds the cross reference candidates to a failed save", func(t *testing.T) {
		conn := newConn(t, answers)
		failSaves(conn, "update", "User", sfapi.SaveResult{Errors: []sfapi.SaveError{{StatusCode: "INVALID_CROSS_REFERENCE_KEY", Message: "invalid cross reference id", Fields: []string{"UserRoleId"}}}})
		result, err := Execute(conn, Request{Options: Options{UsersDoc: parseDoc(t, users), PersonasDoc: parseDoc(t, `{"personas":{"default":{"profile":"Admin"}}}`), ExternalID: "FederationIdentifier"}})
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, "statuses", statuses(result), []string{"created", "failed"})
		expectEqual(t, "errors", result.Users[1].Errors, []string{
			"INVALID_CROSS_REFERENCE_KEY: invalid cross reference id (fields: UserRoleId)",
			"Cross-reference update candidates for this user: UserRoleId=00EBAD000000001AAA, ProfileId=00exx0000000001AAA",
		})
		expectEqual(t, "raw profile id is kept", changes(conn, "create", "User")[0].Records[0]["ProfileId"], "00eBAD000000001AAA")
		if result.Licenses != nil || result.PermissionSetLicenses != nil {
			t.Errorf("a live result has license data: %s", toJSON(t, result))
		}
	})
	t.Run("dry run reports licenses and changes no record", func(t *testing.T) {
		conn := newConn(t, answers)
		result, err := Execute(conn, Request{DryRun: true, Options: Options{UsersDoc: parseDoc(t, users), PersonasDoc: parseDoc(t, `{"personas":{"default":{"profile":"Admin"}}}`), ExternalID: "FederationIdentifier"}})
		if err != nil {
			t.Fatal(err)
		}
		expectEqual(t, "statuses", statuses(result), []string{"planned", "planned"})
		expectEqual(t, "changes", len(conn.Changes), 0)
		expectEqual(t, "licenses", toJSON(t, result.Licenses), `[]`)
	})
	t.Run("personas without a personas definition are an error", func(t *testing.T) {
		_, err := Execute(newConn(t, nil), Request{Options: Options{UsersDoc: parseDoc(t, users)}})
		failure, ok := core.AsWardenError(err)
		if !ok {
			t.Fatalf("got error %v, want a WardenError", err)
		}
		expectEqual(t, "error", []string{failure.Name, failure.Code, failure.Message}, []string{"DefinitionError", "errorPersonasWithoutDefinition", `User "A001" lists personas but no --personas-def was supplied.`})
	})
}

func TestPlanReadsDefinitionFilesAndReportsCsvRows(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.csv")
	personasPath := filepath.Join(dir, "personas.json")
	csv := "Username,LastName,Alias,TimeZoneSidKey,LocaleSidKey,EmailEncodingKey,LanguageLocaleKey,personas\n" +
		"one@example.test,One,one,America/Los_Angeles,en_US,UTF-8,en_US,default\n" +
		"two@example.test,Two,two,America/Los_Angeles,en_US,UTF-8,en_US,ghost\n"
	if err := os.WriteFile(usersPath, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(personasPath, []byte(`{"personas":{"default":{"profile":"Admin"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := newConn(t, []answer{{"FROM Profile", adminProfile}})
	plan, err := Plan(conn, Options{UsersPath: usersPath, PersonasPath: personasPath})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "statuses", statuses(plan.Preview), []string{"planned", "failed"})
	expectEqual(t, "errors", plan.Preview.Users[1].Errors, []string{usersPath + `:3 — Unknown persona "ghost".`})
	expectEqual(t, "plan source", *plan.Plans[0].Source, core.RowInfo{Path: usersPath, Line: 2})
	expectContains(t, "plan json", toJSON(t, plan.Plans[0]), `"source":{"path":`+toJSON(t, usersPath)+`,"line":2}`)
}

func TestPlanAndApplyStopWhenCancelled(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	conn := newConn(t, []answer{{"FROM Profile", adminProfile}})
	options := Options{UsersDoc: parseDoc(t, usersJSON(userJSON(`"Username":"a@example.test","personas":["default"]`))), PersonasDoc: parseDoc(t, `{"personas":{"default":{"profile":"Admin"}}}`)}
	var phases []string
	run := &RunContext{Context: cancelled, OnProgress: func(phase string) { phases = append(phases, phase) }}
	plan, err := PlanWithContext(run, conn, options)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = ApplyWithContext(run, conn, plan)
	failure, ok := core.AsWardenError(err)
	if !ok || failure.Code != "cancelled" || failure.Message != "Operation cancelled." {
		t.Fatalf("got error %v, want the cancelled error", err)
	}
	expectEqual(t, "phases", phases, []string{"start", "complete"})
	expectEqual(t, "changes", len(conn.Changes), 0)
}

func TestPlanRejectsDefinitionsThatCannotBeUsed(t *testing.T) {
	persona := `{"personas":{"default":{"profile":"Admin"}}}`
	cases := []struct {
		name     string
		users    string
		personas string
		external string
		want     string
	}{
		{"unknown user field", usersJSON(`{"Nickname":"x","personas":["default"]}`), persona, "", `user personas=default: unknown User field "Nickname".`},
		{"users is not a list", `{"users":{}}`, persona, "", "user-def.json must contain a users array."},
		{"external id field that does not exist", usersJSON(), persona, "Nope__c", `Invalid --external-id field "Nope__c". Must be a filterable User field.`},
		{"invalid mode", usersJSON(), `{"personas":{"default":{"queueMode":"all"}}}`, "", `Invalid assignment mode "all". Expected additive or sync.`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Plan(newConn(t, nil), Options{UsersDoc: parseDoc(t, test.users), PersonasDoc: parseDoc(t, test.personas), ExternalID: test.external})
			if err == nil || err.Error() != test.want {
				t.Errorf("got error %v, want %q", err, test.want)
			}
		})
	}
}
