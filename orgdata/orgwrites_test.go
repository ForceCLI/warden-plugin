package orgdata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func createdUser(key string, personas []string, actions ...string) ProvisionUser {
	return ProvisionUser{ID: "005-user", Key: key, Personas: personas, Status: "created", Actions: actions, Errors: []string{}}
}

func TestDetectConnectedPackageDescribesTheObjectsWithoutANamespace(t *testing.T) {
	expectEqual(t, WardenLedgerObject, "wdn_Provisioned_Grant__c")
	expectEqual(t, WardenRunObject, "wdn_Reconciliation_Run__c")
	expectEqual(t, WardenItemObject, "wdn_Reconciliation_Item__c")
	conn := sfapitest.New().DescribeFields(WardenLedgerObject)

	detected := DetectConnectedPackage(conn)

	expectEqual(t, detected, ConnectedPackage{LedgerAvailable: true, RunAvailable: false})
	expectEqual(t, mustJSON(t, detected), `{"ledgerAvailable":true,"runAvailable":false}`)
}

func TestReadLedgerProvenanceDescribesTheLedgerAndReadsThePersona(t *testing.T) {
	conn := &describeCountingConn{Conn: sfapitest.New().DescribeFields(WardenLedgerObject, sfapitest.Field("wdn_Persona__c", "reference"))}
	conn.Answer("FROM "+WardenLedgerObject, sfapi.Record{
		"Id":               "a-ledger",
		"wdn_User__c":      "005-user",
		"wdn_Target_Id__c": "0PS-target",
		"wdn_Persona__r":   map[string]any{"wdn_Api_Name__c": "support"},
	})

	provenance, err := ReadLedgerProvenance(conn, []string{"005-user"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, conn.described, []string{WardenLedgerObject})
	expectEqual(t, mustJSON(t, provenance), `[{"id":"a-ledger","userId":"005-user","targetId":"0PS-target","persona":"support"}]`)
	expectEqual(t, conn.Queries, []string{
		"SELECT Id, wdn_User__c, wdn_Target_Id__c, wdn_Persona__r.wdn_Api_Name__c FROM wdn_Provisioned_Grant__c WHERE wdn_User__c IN ('005-user') AND wdn_Revoked_On__c = NULL",
	})
}

func TestReadLedgerProvenanceOmitsThePersonaOnOlderPackages(t *testing.T) {
	conn := &describeCountingConn{Conn: sfapitest.New().DescribeFields(WardenLedgerObject, sfapitest.Field("wdn_Revoked_On__c", "datetime"))}
	conn.Answer("FROM "+WardenLedgerObject, sfapi.Record{
		"Id":               "a-ledger",
		"wdn_User__c":      "005-user",
		"wdn_Target_Id__c": "0PS-target",
		"wdn_Persona__r":   map[string]any{"wdn_Api_Name__c": "ignored"},
	})

	fields := DescribeObjectFields(conn, WardenLedgerObject)
	provenance, err := ReadLedgerProvenance(conn, []string{"005-user"}, fields)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, len(conn.described), 1)
	expectEqual(t, conn.Queries, []string{
		"SELECT Id, wdn_User__c, wdn_Target_Id__c FROM " + WardenLedgerObject + " WHERE wdn_User__c IN ('005-user') AND wdn_Revoked_On__c = NULL",
	})
	expectEqual(t, provenance, []LedgerProvenance{{ID: "a-ledger", UserID: "005-user", TargetID: "0PS-target"}})
	expectEqual(t, mustJSON(t, provenance), `[{"id":"a-ledger","userId":"005-user","targetId":"0PS-target"}]`)
}

func TestReadLedgerProvenanceExcludesRevokedRows(t *testing.T) {
	conn := sfapitest.New()
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if strings.Contains(soql, "wdn_Revoked_On__c = NULL") {
			return nil, true, nil
		}
		return []sfapi.Record{{
			"Id":               "a-revoked-ledger",
			"wdn_User__c":      "005-user",
			"wdn_Target_Id__c": "0PS-target",
			"wdn_Persona__r":   map[string]any{"wdn_Api_Name__c": "support"},
		}}, true, nil
	}

	provenance, err := ReadLedgerProvenance(conn, []string{"005-user"}, NewFieldSet("wdn_Persona__c"))
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, mustJSON(t, provenance), `[]`)
}

func TestDescribeObjectFieldsIsNilForAnObjectThatIsAbsent(t *testing.T) {
	conn := sfapitest.New().DescribeFields(WardenRunObject)
	if DescribeObjectFields(conn, WardenLedgerObject) != nil {
		t.Fatal("expected nil for an object that cannot be described")
	}
	if fields := DescribeObjectFields(conn, WardenRunObject); fields == nil || fields.Has("wdn_Mode__c") {
		t.Fatalf("expected an empty field set, got %v", fields)
	}
}

func TestUpdateBatch(t *testing.T) {
	cases := []struct {
		name     string
		records  []sfapi.Record
		results  []sfapi.SaveResult
		warnings []string
	}{
		{
			name:     "updates revocation rows without warnings",
			records:  []sfapi.Record{{"Id": "a1", "wdn_Revoked_On__c": "2026-10-17T00:00:00.000Z"}},
			results:  []sfapi.SaveResult{{Success: true, ID: "a1"}},
			warnings: []string{},
		},
		{
			name:    "warns when some records failed",
			records: []sfapi.Record{{"Id": "a1"}, {"Id": "a2"}},
			results: []sfapi.SaveResult{
				{Success: true, ID: "a1"},
				{Success: false, Errors: []sfapi.SaveError{{Message: "denied"}}},
			},
			warnings: []string{"Warden org ledger revocation batch 1 returned 1 failed record(s)."},
		},
		{
			name:     "warns when the number of results differs from the number of records",
			records:  []sfapi.Record{{"Id": "a1"}, {"Id": "a2"}},
			results:  []sfapi.SaveResult{{Success: true, ID: "a1"}},
			warnings: []string{"Warden org ledger revocation batch 1 returned 1 result(s) for 2 record(s)."},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			conn := sfapitest.New()
			conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return test.results, nil }
			warnings, warn := collectWarnings()

			results := UpdateBatch(conn, WardenLedgerObject, test.records, warn, "ledger revocation")

			expectEqual(t, conn.Changes, []sfapitest.Change{{Operation: "update", Sobject: WardenLedgerObject, Records: test.records}})
			expectEqual(t, results, test.results)
			expectEqual(t, *warnings, test.warnings)
		})
	}
}

func TestBuildProvenanceUsesTheGivenEnvironmentAndGitValues(t *testing.T) {
	env := map[string]string{"GITHUB_JOB": "provision", "CI": "true"}
	provenance := BuildProvenance(ProvenanceOptions{
		DefinitionPath: "./personas.json",
		Env:            func(name string) (string, bool) { value, found := env[name]; return value, found },
		CLIVersion:     "0.6.2",
		InvokingUser:   "runner",
		GitSha:         func(string) string { return "abc123" },
	})

	expectEqual(t, provenance.SourceSystem, "warden-cli")
	expectEqual(t, provenance.CLIVersion, "0.6.2")
	expectEqual(t, provenance.InvokingUser, "runner")
	expectEqual(t, provenance.CIJob, "provision")
	expectEqual(t, provenance.DefinitionSha, "abc123")
	if !strings.HasPrefix(provenance.DefinitionPath, "/") || !strings.HasSuffix(provenance.DefinitionPath, "personas.json") {
		t.Fatalf("expected an absolute path to personas.json, got %q", provenance.DefinitionPath)
	}
}

func TestBuildProvenanceCIJob(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"uses the workflow when there is no job", map[string]string{"GITHUB_WORKFLOW": "deploy", "CI": "true"}, "deploy"},
		{"uses CI when only CI is set", map[string]string{"CI": "true"}, "CI"},
		{"is empty outside CI", map[string]string{}, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provenance := BuildProvenance(ProvenanceOptions{
				Env:          func(name string) (string, bool) { value, found := test.env[name]; return value, found },
				InvokingUser: "runner",
			})
			expectEqual(t, provenance.CIJob, test.want)
			expectEqual(t, provenance.DefinitionSha, "")
		})
	}
}

func TestCaptureKeepsTheQueueTypeOfGroupRowsThatHaveOnlyADeveloperName(t *testing.T) {
	conn := sfapitest.New()
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if strings.Contains(soql, "Type = 'Queue'") {
			return []sfapi.Record{{"Id": "00G-queue", "DeveloperName": "Case_Queue"}}, true, nil
		}
		return []sfapi.Record{{"Id": "00G-public", "DeveloperName": "Internal_Users"}}, true, nil
	}
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) {
		return []sfapi.SaveResult{{Success: true, ID: "0GM-public"}, {Success: true, ID: "0GM-queue"}}, nil
	}
	captured := CaptureProvisioningWrites(conn, nil)
	for _, soql := range []string{
		"SELECT Id, DeveloperName FROM Group WHERE DeveloperName IN ('Internal_Users') AND Type = 'Regular'",
		"SELECT Id, DeveloperName FROM Group WHERE DeveloperName IN ('Case_Queue') AND Type = 'Queue'",
	} {
		if _, err := captured.Conn.Query(soql); err != nil {
			t.Fatal(err)
		}
	}
	_, err := captured.Conn.Create("GroupMember", []sfapi.Record{
		{"UserOrGroupId": "005-user", "GroupId": "00G-public"},
		{"UserOrGroupId": "005-user", "GroupId": "00G-queue"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, captured.State.Grants, []CapturedGrant{
		{UserID: "005-user", Type: GrantPublicGroup, TargetID: "00G-public"},
		{UserID: "005-user", Type: GrantQueue, TargetID: "00G-queue"},
	})
	expectEqual(t, mustJSON(t, captured.State.Grants[1]), `{"userId":"005-user","type":"Queue","targetId":"00G-queue"}`)
	rows := BuildLedgerRows(LedgerRowsOptions{
		Grants:             []CapturedGrant{captured.State.Grants[1]},
		Result:             ProvisionResult{Users: []ProvisionUser{createdUser("FederationIdentifier:ONE", []string{"first", "second"}, "assignedQueue")}},
		PersonasDoc:        mustDocument(t, `{"personas":{"first":{"queues":["Case_Queue"]},"second":{"queues":["Case_Queue"]}}}`),
		ReferenceIDsByType: captured.State.ReferenceIDsByType,
		LabelsByTypeAndID:  captured.State.LabelsByTypeAndID,
	})
	expectEqual(t, len(rows), 2)
	expectEqual(t, rows[0].Type, GrantQueue)
}

func TestCaptureRecordsOnlyTheAssignmentsThatWereSaved(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(change sfapitest.Change) ([]sfapi.SaveResult, error) {
		if change.Sobject == "Account" {
			return nil, errors.New("create failed")
		}
		return []sfapi.SaveResult{{Success: true, ID: "0Pa-1"}, {Success: false}, {Success: true, ID: "0Pa-3"}}, nil
	}
	captured := CaptureProvisioningWrites(conn, nil)

	results, err := captured.Conn.Create("PermissionSetAssignment", []sfapi.Record{
		{"AssigneeId": "005-one", "PermissionSetId": "0PS-set"},
		{"AssigneeId": "005-two", "PermissionSetId": "0PS-set"},
		{"AssigneeId": "005-three", "PermissionSetGroupId": "0PG-group"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captured.Conn.Create("Account", []sfapi.Record{{"Name": "Acme"}}, false); err == nil {
		t.Fatal("expected the error of the wrapped connection")
	}

	expectEqual(t, len(results), 3)
	expectEqual(t, captured.State.Grants, []CapturedGrant{
		{UserID: "005-one", Type: GrantPermissionSet, TargetID: "0PS-set"},
		{UserID: "005-three", Type: GrantPermissionSetGroup, TargetID: "0PG-group"},
	})
}

func TestCaptureRecordsLaterPagesOfAQuery(t *testing.T) {
	conn := sfapitest.New()
	conn.Results = []sfapitest.QueryAnswer{
		{Contains: "FROM PermissionSet", Records: []sfapi.Record{{"Id": "0PS-one", "Name": "One"}}, Next: "/next"},
	}
	conn.More["/next"] = &sfapi.QueryResult{Done: true, Records: []sfapi.Record{{"Id": "0PS-two", "Name": "Two"}}}
	captured := CaptureProvisioningWrites(conn, nil)

	page, err := captured.Conn.Query("SELECT Id, Name FROM PermissionSet")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captured.Conn.QueryMore(page.NextRecordsURL); err != nil {
		t.Fatal(err)
	}

	expectEqual(t, mustJSON(t, SerializeCaptureState(captured.State)), `{"labelsByTypeAndId":[["Permission Set",[["0PS-one","One"],["0PS-two","Two"]]]],"referenceIdsByType":[["Permission Set",[["One","0PS-one"],["Two","0PS-two"]]]],"groupTypesById":[]}`)
}

func TestSerializedCaptureStateSeedsALaterCaptureWithoutGrants(t *testing.T) {
	conn := sfapitest.New()
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if strings.Contains(soql, "FROM Group") {
			return []sfapi.Record{{"Id": "00G-queue", "Name": "Cases", "DeveloperName": "Case_Queue", "Type": "Queue"}}, true, nil
		}
		return []sfapi.Record{{"Id": "0PS-support", "Name": "Support"}}, true, nil
	}
	planned := CaptureProvisioningWrites(conn, nil)
	for _, soql := range []string{"SELECT Id, Name FROM PermissionSet WHERE Name = 'Support'", "SELECT Id, Name, DeveloperName, Type FROM Group"} {
		if _, err := planned.Conn.Query(soql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := planned.Conn.Create("PermissionSetAssignment", []sfapi.Record{{"AssigneeId": "005-user", "PermissionSetId": "0PS-support"}}, false); err != nil {
		t.Fatal(err)
	}

	encoded := mustJSON(t, SerializeCaptureState(planned.State))
	expectEqual(t, encoded, `{"labelsByTypeAndId":[["Permission Set",[["0PS-support","Support"]]],["Queue",[["00G-queue","Cases"]]]],"referenceIdsByType":[["Permission Set",[["Support","0PS-support"]]],["Queue",[["Cases","00G-queue"],["Case_Queue","00G-queue"]]]],"groupTypesById":[["00G-queue","Queue"]]}`)
	var capture SerializedCaptureState
	if err := json.Unmarshal([]byte(encoded), &capture); err != nil {
		t.Fatal(err)
	}
	applied := CaptureProvisioningWrites(conn, &capture)

	expectEqual(t, SerializeCaptureState(applied.State), SerializeCaptureState(planned.State))
	expectEqual(t, len(planned.State.Grants), 1)
	expectEqual(t, applied.State.Grants, []CapturedGrant{})
}

func TestBuildLedgerRowsWritesOneRowForEachPersonaThatListsTheGrant(t *testing.T) {
	rows := BuildLedgerRows(LedgerRowsOptions{
		Grants:             []CapturedGrant{{UserID: "005-user", Type: GrantPermissionSet, TargetID: "0PS-target"}},
		Result:             ProvisionResult{Users: []ProvisionUser{createdUser("FederationIdentifier:ONE", []string{"first", "second"}, "assignedPermissionSet")}},
		PersonasDoc:        mustDocument(t, `{"personas":{"first":{"permissionSets":["Shared"]},"second":{"permissionSets":["Shared"]}}}`),
		ReferenceIDsByType: nested(GrantPermissionSet, "Shared", "0PS-target"),
		LabelsByTypeAndID:  nested(GrantPermissionSet, "0PS-target", "Shared"),
		PersonaIDsByName:   stringMap("first", "a-persona-first", "second", "a-persona-second"),
		RunID:              "a-run",
		GrantedOn:          "2026-09-26T00:00:00.000Z",
	})

	expectEqual(t, mustJSON(t, rows), `[`+
		`{"wdn_User__c":"005-user","wdn_Persona__c":"a-persona-first","wdn_Type__c":"Permission Set","wdn_Target_Id__c":"0PS-target","wdn_Target_Label__c":"Shared","wdn_Granted_On__c":"2026-09-26T00:00:00.000Z","wdn_Granted_By_Run__c":"a-run"},`+
		`{"wdn_User__c":"005-user","wdn_Persona__c":"a-persona-second","wdn_Type__c":"Permission Set","wdn_Target_Id__c":"0PS-target","wdn_Target_Label__c":"Shared","wdn_Granted_On__c":"2026-09-26T00:00:00.000Z","wdn_Granted_By_Run__c":"a-run"}]`)
	expectEqual(t, rows[0].Record(), sfapi.Record{
		"wdn_User__c":           "005-user",
		"wdn_Persona__c":        "a-persona-first",
		"wdn_Type__c":           "Permission Set",
		"wdn_Target_Id__c":      "0PS-target",
		"wdn_Target_Label__c":   "Shared",
		"wdn_Granted_On__c":     "2026-09-26T00:00:00.000Z",
		"wdn_Granted_By_Run__c": "a-run",
	})
}

func TestBuildLedgerRowsKeepsAGrantThatNoPersonaLists(t *testing.T) {
	rows := BuildLedgerRows(LedgerRowsOptions{
		Grants:    []CapturedGrant{{UserID: "005-other", Type: GrantPublicGroup, TargetID: "00G-target"}},
		Result:    ProvisionResult{Users: []ProvisionUser{createdUser("Username:person@example.test", []string{"support"})}},
		GrantedOn: "2026-09-26T00:00:00.000Z",
	})

	expectEqual(t, mustJSON(t, rows), `[{"wdn_User__c":"005-other","wdn_Type__c":"Public Group","wdn_Target_Id__c":"00G-target","wdn_Target_Label__c":"00G-target","wdn_Granted_On__c":"2026-09-26T00:00:00.000Z"}]`)
	expectEqual(t, len(rows[0].Record()), 5)
}

func TestLedgerRowsKeepThePersonaForALaterRevokeLookup(t *testing.T) {
	rows := BuildLedgerRows(LedgerRowsOptions{
		Grants:             []CapturedGrant{{UserID: "005-user", Type: GrantPermissionSet, TargetID: "0PS-target"}},
		Result:             ProvisionResult{Users: []ProvisionUser{createdUser("Username:person@example.test", []string{"support"}, "assignedPermissionSet")}},
		PersonasDoc:        mustDocument(t, `{"personas":{"support":{"permissionSets":["Support"]}}}`),
		ReferenceIDsByType: nested(GrantPermissionSet, "Support", "0PS-target"),
		PersonaIDsByName:   stringMap("support", "a-persona-support"),
	})
	conn := sfapitest.New()
	conn.OnQuery = func(string) ([]sfapi.Record, bool, error) {
		var records []sfapi.Record
		for index, row := range rows {
			records = append(records, sfapi.Record{
				"Id":               "a-ledger-" + string(rune('0'+index)),
				"wdn_User__c":      row.User,
				"wdn_Target_Id__c": row.TargetID,
				"wdn_Persona__r":   map[string]any{"wdn_Api_Name__c": "support"},
			})
		}
		return records, true, nil
	}

	provenance, err := ReadLedgerProvenance(conn, []string{"005-user"}, NewFieldSet("wdn_Persona__c"))
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, rows[0].Persona, "a-persona-support")
	if len(rows[0].GrantedOn) != len("2026-09-26T00:00:00.000Z") || !strings.HasSuffix(rows[0].GrantedOn, "Z") {
		t.Fatalf("expected a UTC time with milliseconds, got %q", rows[0].GrantedOn)
	}
	expectEqual(t, mustJSON(t, provenance), `[{"id":"a-ledger-0","userId":"005-user","targetId":"0PS-target","persona":"support"}]`)
}

func TestBestEffortOrgWriteWarnsAndReturnsNoResultWhenTheWriteFails(t *testing.T) {
	warnings, warn := collectWarnings()

	result, ok := BestEffortOrgWrite(func() (string, error) { return "written", errors.New("audit unavailable") }, warn, "ledger")

	expectEqual(t, ok, false)
	expectEqual(t, result, "")
	expectEqual(t, *warnings, []string{"Warden org ledger write failed: audit unavailable"})
}

func numberedRecords(count int) []sfapi.Record {
	records := make([]sfapi.Record, count)
	for index := range records {
		records[index] = sfapi.Record{"index": index}
	}
	return records
}

func TestWriteBatchSendsCollectionsOf200Records(t *testing.T) {
	conn := sfapitest.New()
	warnings, warn := collectWarnings()

	results := WriteBatch(conn, WardenLedgerObject, numberedRecords(401), warn, "ledger")

	var sizes []int
	for _, change := range conn.Changes {
		expectEqual(t, change.Operation, "create")
		expectEqual(t, change.Sobject, WardenLedgerObject)
		expectEqual(t, change.AllOrNone, false)
		sizes = append(sizes, len(change.Records))
	}
	expectEqual(t, sizes, []int{200, 200, 1})
	expectEqual(t, len(results), 401)
	expectEqual(t, *warnings, []string{})
}

func TestWriteBatchContinuesAfterABatchFails(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) {
		if len(conn.Changes) == 1 {
			return nil, errors.New("request too large")
		}
		return []sfapi.SaveResult{{Success: true, ID: "audit-id"}}, nil
	}
	warnings, warn := collectWarnings()

	results := WriteBatch(conn, WardenItemObject, numberedRecords(201), warn, "item")

	expectEqual(t, len(conn.Changes), 2)
	expectEqual(t, results, []sfapi.SaveResult{{Success: true, ID: "audit-id"}})
	expectEqual(t, *warnings, []string{"Warden org item batch 1/2 write failed: request too large"})
}

func TestWriteBatchWarnsWhenSomeRecordsFailed(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) {
		return []sfapi.SaveResult{{Success: true, ID: "audit-id"}, {Success: false, Errors: []sfapi.SaveError{{Message: "invalid"}}}}, nil
	}
	warnings, warn := collectWarnings()

	results := WriteBatch(conn, WardenItemObject, numberedRecords(2), warn, "item")

	expectEqual(t, len(results), 2)
	expectEqual(t, *warnings, []string{"Warden org item batch 1/1 returned 1 failed record(s)."})
}

func TestWriteBatchResultTellsNoRecordsFromNoResults(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return nil, errors.New("unavailable") }
	_, warn := collectWarnings()

	if results := WriteBatch(conn, WardenItemObject, nil, warn, "item"); results == nil || len(results) != 0 {
		t.Fatalf("expected an empty list for no records, got %#v", results)
	}
	if results := WriteBatch(conn, WardenItemObject, numberedRecords(1), warn, "item"); results != nil {
		t.Fatalf("expected nil when no batch returned results, got %#v", results)
	}
	expectEqual(t, len(conn.Changes), 1)
}

func TestSaveRun(t *testing.T) {
	cases := []struct {
		name     string
		results  []sfapi.SaveResult
		err      error
		want     string
		warnings []string
	}{
		{"returns the id of the created run", []sfapi.SaveResult{{Success: true, ID: "a-run"}}, nil, "a-run", []string{}},
		{"warns once when the create fails", nil, errors.New("no access"), "", []string{"Warden org run batch 1/1 write failed: no access"}},
		{
			"warns when the record failed",
			[]sfapi.SaveResult{{Success: false, Errors: []sfapi.SaveError{{Message: "required field"}}}},
			nil,
			"",
			[]string{"Warden org run batch 1/1 returned 1 failed record(s).", "Warden org run write did not return a record id."},
		},
		{"warns when the result has no id", []sfapi.SaveResult{{Success: true}}, nil, "", []string{"Warden org run write did not return a record id."}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			conn := sfapitest.New()
			conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return test.results, test.err }
			warnings, warn := collectWarnings()

			runID := SaveRun(conn, sfapi.Record{"wdn_Mode__c": "Apply"}, warn)

			expectEqual(t, runID, test.want)
			expectEqual(t, *warnings, test.warnings)
			expectEqual(t, conn.Changes, []sfapitest.Change{{Operation: "create", Sobject: WardenRunObject, Records: []sfapi.Record{{"wdn_Mode__c": "Apply"}}}})
		})
	}
}

func TestUpdateRunWarnsForEachFailedResult(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) {
		return []sfapi.SaveResult{
			{Success: false, Errors: []sfapi.SaveError{{Message: "run is locked"}}},
			{Success: true, ID: "run-id"},
			{Success: false},
		}, nil
	}
	warnings, warn := collectWarnings()

	UpdateRun(conn, "run-id", sfapi.Record{"wdn_Error_Count__c": 1}, warn)

	expectEqual(t, conn.Changes, []sfapitest.Change{{
		Operation: "update",
		Sobject:   WardenRunObject,
		Records:   []sfapi.Record{{"Id": "run-id", "wdn_Error_Count__c": 1}},
		AllOrNone: false,
	}})
	expectEqual(t, *warnings, []string{
		"Warden org run summary update result 1/3 failed: run is locked",
		"Warden org run summary update result 3/3 failed.",
	})
}

func TestUpdateRunWarnsWhenTheUpdateFails(t *testing.T) {
	conn := sfapitest.New()
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return nil, errors.New("timeout") }
	warnings, warn := collectWarnings()

	UpdateRun(conn, "run-id", sfapi.Record{}, warn)

	expectEqual(t, *warnings, []string{"Warden org run summary write failed: timeout"})
}

func TestBuildReconciliationItems(t *testing.T) {
	result := ProvisionResult{Users: []ProvisionUser{
		{
			ID:      "005-one",
			Key:     "Username:one@example.test",
			Status:  "created",
			Actions: []string{"createdUser", "assignedPermissionSetGroup", "assignedPermissionSet", "assignedPublicGroup", "assignedQueue"},
			RelatedRecords: []RelatedRecord{
				{Relationship: "contact", Phase: "before", Sobject: "Contact", Action: "created"},
				{Relationship: "territory", Phase: "after", Sobject: "UserTerritory2Association", Action: "skipped", Error: "no territory"},
			},
		},
		{Key: "Username:two@example.test", Status: "failed", Errors: []string{"Profile not found"}},
		{ID: "005-three", Key: "Username:three@example.test", Status: "planned"},
	}}

	items := BuildReconciliationItems(result, "a-run")

	item := func(key, id, status, category, action, detail, itemError string) ReconciliationItem {
		return ReconciliationItem{Run: "a-run", UserKey: key, UserID: id, Status: status, Category: category, Action: action, Detail: detail, Error: itemError}
	}
	one := "Username:one@example.test"
	expectEqual(t, items, []ReconciliationItem{
		item(one, "005-one", "Applied", "User", "createdUser", "", ""),
		item(one, "005-one", "Applied", "Permission Set Group", "assignedPermissionSetGroup", "", ""),
		item(one, "005-one", "Applied", "Permission Set", "assignedPermissionSet", "", ""),
		item(one, "005-one", "Applied", "Public Group", "assignedPublicGroup", "", ""),
		item(one, "005-one", "Applied", "Queue", "assignedQueue", "", ""),
		item(one, "005-one", "Applied", "Contact", "related", "contact before Contact created", ""),
		item(one, "005-one", "Applied", "UserTerritory2Association", "related", "territory after UserTerritory2Association skipped", "no territory"),
		item("Username:two@example.test", "", "Failed", "User", "", "", "Profile not found"),
		item("Username:three@example.test", "005-three", "Planned", "User", "", "", ""),
	})
	expectEqual(t, mustJSON(t, items[6]), `{"wdn_Reconciliation_Run__c":"a-run","wdn_User_Key__c":"Username:one@example.test","wdn_User_Id__c":"005-one","wdn_Status__c":"Applied","wdn_Category__c":"UserTerritory2Association","wdn_Action__c":"related","wdn_Detail__c":"territory after UserTerritory2Association skipped","wdn_Error__c":"no territory"}`)
	expectEqual(t, ItemRecords(items[7:8]), []sfapi.Record{{
		"wdn_Reconciliation_Run__c": "a-run",
		"wdn_User_Key__c":           "Username:two@example.test",
		"wdn_User_Id__c":            "",
		"wdn_Status__c":             "Failed",
		"wdn_Category__c":           "User",
		"wdn_Action__c":             "",
		"wdn_Detail__c":             "",
		"wdn_Error__c":              "Profile not found",
	}})
}

func TestWriteProvisionLedger(t *testing.T) {
	output := ProvisionResult{Users: []ProvisionUser{
		createdUser("Username:person@example.test", []string{"support", "unknown"}, "assignedPermissionSet"),
		{ID: "005-other", Key: "Username:other@example.test", Personas: []string{"support"}, Status: "updated"},
	}}
	personasDoc := `{"personas":{"support":{"permissionSets":["Support"]},"unknown":{}}}`
	newState := func() *CaptureState {
		state := captureStateFrom(nil)
		state.Grants = []CapturedGrant{{UserID: "005-user", Type: GrantPermissionSet, TargetID: "0PS-target"}}
		state.ReferenceIDsByType = nested(GrantPermissionSet, "Support", "0PS-target")
		state.LabelsByTypeAndID = nested(GrantPermissionSet, "0PS-target", "Support Label")
		return state
	}

	t.Run("stores the persona id when the ledger has the persona field", func(t *testing.T) {
		conn := sfapitest.New().DescribeFields(WardenLedgerObject, sfapitest.Field("wdn_Persona__c", "reference"))
		conn.Answer("FROM "+PersonaObject, sfapi.Record{"Id": "a-persona-support", PersonaAPINameField: "support"})
		warnings, warn := collectWarnings()

		err := WriteProvisionLedger(conn, ProvisionLedgerOptions{State: newState(), Output: output, PersonasDoc: mustDocument(t, personasDoc), RunID: "a-run", Warn: warn})
		if err != nil {
			t.Fatal(err)
		}

		expectEqual(t, conn.Queries, []string{"SELECT Id, wdn_Api_Name__c FROM wdn_Persona__c WHERE wdn_Api_Name__c IN ('support','unknown')"})
		expectEqual(t, *warnings, []string{"Warden org ledger provenance is unavailable for persona(s): unknown."})
		expectEqual(t, len(conn.Changes), 1)
		expectEqual(t, conn.Changes[0].Sobject, WardenLedgerObject)
		expectEqual(t, len(conn.Changes[0].Records), 1)
		record := conn.Changes[0].Records[0]
		expectEqual(t, record["wdn_Persona__c"], any("a-persona-support"))
		expectEqual(t, record["wdn_Target_Label__c"], any("Support Label"))
		expectEqual(t, record["wdn_Granted_By_Run__c"], any("a-run"))
	})

	t.Run("does not query personas when the ledger lacks the persona field", func(t *testing.T) {
		conn := sfapitest.New().DescribeFields(WardenLedgerObject)
		warnings, warn := collectWarnings()

		err := WriteProvisionLedger(conn, ProvisionLedgerOptions{State: newState(), Output: output, PersonasDoc: mustDocument(t, personasDoc), Warn: warn})
		if err != nil {
			t.Fatal(err)
		}

		expectEqual(t, len(conn.Queries), 0)
		expectEqual(t, *warnings, []string{})
		record := conn.Changes[0].Records[0]
		if _, present := record["wdn_Persona__c"]; present {
			t.Fatal("expected no persona id")
		}
		if _, present := record["wdn_Granted_By_Run__c"]; present {
			t.Fatal("expected no run id")
		}
	})

	t.Run("warns when the rows cannot be saved", func(t *testing.T) {
		conn := sfapitest.New().DescribeFields(WardenLedgerObject)
		conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return nil, errors.New("no access") }
		warnings, warn := collectWarnings()

		err := WriteProvisionLedger(conn, ProvisionLedgerOptions{State: newState(), Output: output, Warn: warn})
		if err != nil {
			t.Fatal(err)
		}

		expectEqual(t, *warnings, []string{"Warden org ledger batch 1/1 write failed: no access"})
	})
}
