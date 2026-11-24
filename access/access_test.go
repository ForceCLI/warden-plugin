package access

import (
	"errors"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

type rec = sfapi.Record

type queryFunc func(soql string) ([]sfapi.Record, bool, error)

// answers returns a query function that answers with the records of the
// first rule whose every text the query contains, and with no records
// when no rule matches.
type rule struct {
	contains []string
	records  []sfapi.Record
}

func answers(rules ...rule) queryFunc {
	return func(soql string) ([]sfapi.Record, bool, error) {
		for _, candidate := range rules {
			matched := true
			for _, text := range candidate.contains {
				if !strings.Contains(soql, text) {
					matched = false
					break
				}
			}
			if matched {
				return candidate.records, true, nil
			}
		}
		return []sfapi.Record{}, true, nil
	}
}

func on(records []sfapi.Record, contains ...string) rule {
	return rule{contains: contains, records: records}
}

func regular(id, name string) rec {
	return rec{"Id": id, "Name": name, "IsOwnedByProfile": false, "Type": "Regular"}
}

var janeAssignment = rec{
	"Id":              "0PA1",
	"AssigneeId":      "0051",
	"Assignee":        rec{"Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true},
	"PermissionSetId": "0PS1",
	"PermissionSet":   regular("0PS1", "Account Readers"),
}

var accountReadGrant = rec{
	"ParentId":                    "0PS1",
	"Parent":                      regular("0PS1", "Account Readers"),
	"PermissionsRead":             true,
	"PermissionsCreate":           false,
	"PermissionsEdit":             false,
	"PermissionsDelete":           false,
	"PermissionsViewAllRecords":   false,
	"PermissionsModifyAllRecords": false,
}

// baseRules is the org of the upstream command tests: one permission set
// 0PS1 that grants every kind of target, assigned to Jane Smith.
func baseRules() []rule {
	return []rule{
		on([]rec{{"Id": "01p1", "Name": "MyController"}}, "FROM ApexClass"),
		on([]rec{{"Id": "01pPage", "Name": "MyPage"}}, "FROM ApexPage"),
		on([]rec{{"Id": "0CP1", "DeveloperName": "Can_Edit_Accounts"}}, "FROM CustomPermission"),
		on([]rec{{"DurableId": "Account", "Name": "standard-Account"}}, "FROM TabDefinition"),
		on([]rec{{"ParentId": "0PS1", "Parent": regular("0PS1", "Setup Access")}}, "FROM SetupEntityAccess"),
		on([]rec{{"ParentId": "0PS1", "Visibility": "DefaultOn", "Parent": regular("0PS1", "Tab Access")}}, "FROM PermissionSetTabSetting"),
		on([]rec{accountReadGrant}, "FROM ObjectPermissions"),
		on([]rec{{"ParentId": "0PS1", "Parent": regular("0PS1", "Account Readers"), "PermissionsRead": true, "PermissionsEdit": false}}, "FROM FieldPermissions"),
		on([]rec{}, "FROM PermissionSetGroupComponent"),
		on([]rec{janeAssignment}, "FROM PermissionSetAssignment"),
	}
}

func newConn(query queryFunc) *sfapitest.Conn {
	conn := sfapitest.New()
	conn.DescribeFields("Account", sfapitest.Field("CustomField__c", "string"))
	conn.OnQuery = query
	return conn
}

func baseConn() *sfapitest.Conn {
	return newConn(answers(baseRules()...))
}

func withUser(conn *sfapitest.Conn) *sfapitest.Conn {
	conn.DescribeFields("User", sfapitest.Field("Username", "string"), sfapitest.Field("Name", "string"))
	return conn
}

var janeUser = []rec{{"Id": "0051", "Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true}}

type audit struct {
	result *Result
	csv    string
	human  string
}

// runAudit does what the command does with a request: it runs the audit,
// sorts the rows, and writes the CSV and the text output.
func runAudit(t *testing.T, conn sfapi.Conn, options Options, userLabel string) audit {
	t.Helper()
	result, err := Run(conn, options)
	if err != nil {
		t.Fatalf("Run(%+v): %v", options, err)
	}
	result.Rows = StableOrder(result.Rows)
	columns := ReverseCsvColumns(result.TargetType)
	if userLabel == "" {
		resolver, err := GetResolver(options.Type)
		if err != nil {
			t.Fatal(err)
		}
		columns = resolver.CsvColumns()
	}
	flat := make([]map[string]any, len(result.Rows))
	for i, row := range result.Rows {
		flat[i] = FlattenAccessRow(row, columns)
	}
	human, err := RenderAccessResult(result, userLabel)
	if err != nil {
		t.Fatalf("RenderAccessResult: %v", err)
	}
	return audit{result: result, csv: core.SerializeCsv(flat, columns), human: human}
}

func resultJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := marshalNoEscape(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func expectEqual(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got: %s\nwant: %s", label, got, want)
	}
}

const baseHeader = "userId,userName,username,assignmentType,sourceId,sourceName,viaPermissionSetId,viaPermissionSetName,targetType,targetName,sourceApiName,sourceLabel"

func TestApexClassHumanOutput(t *testing.T) {
	conn := newConn(answers(
		on([]rec{{"Id": "01p1", "Name": "MyController"}}, "FROM ApexClass"),
		on([]rec{{"ParentId": "0PS1", "Parent": regular("0PS1", "Controller Access")}}, "FROM SetupEntityAccess"),
		on([]rec{{
			"Id":              "0PA1",
			"AssigneeId":      "0051",
			"Assignee":        rec{"Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true},
			"PermissionSetId": "0PS1",
		}}, "FROM PermissionSetAssignment"),
	))
	got := runAudit(t, conn, Options{Type: "apex-class", Target: "MyController"}, "")
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Apex Class: MyController",
		"Active users with access: 1",
		"Profiles: 0 | Permission Sets: 1 | Permission Set Groups: 0",
		"",
		"User Name   Username          Enabled  Via",
		"Jane Smith  jane@example.com  yes      Permission Set: Controller Access",
	}, "\n"))
	wantQueries := []string{
		"SELECT Id, Name FROM ApexClass WHERE Name = 'MyController' LIMIT 1",
		"SELECT ParentId, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type FROM SetupEntityAccess WHERE SetupEntityType = 'ApexClass' AND SetupEntityId = '01p1'",
		"SELECT PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel, PermissionSetId, PermissionSet.Name, PermissionSet.Type FROM PermissionSetGroupComponent WHERE PermissionSetId IN ('0PS1')",
		"SELECT Id, AssigneeId, Assignee.Name, Assignee.Username, Assignee.IsActive, PermissionSetId, PermissionSet.Name, PermissionSet.IsOwnedByProfile, PermissionSet.ProfileId, PermissionSet.Profile.Name, PermissionSet.Type, PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel FROM PermissionSetAssignment WHERE Assignee.IsActive = true AND PermissionSetId IN ('0PS1') ORDER BY Assignee.Name, Id",
	}
	expectEqual(t, "queries", strings.Join(conn.Queries, "\n"), strings.Join(wantQueries, "\n"))
}

func TestForwardOutputsByType(t *testing.T) {
	cases := []struct {
		name          string
		options       Options
		humanContains []string
		csv           string
		json          string
	}{
		{
			name:    "apex-class access in csv output",
			options: Options{Type: "apex-class", Target: "MyController"},
			csv: baseHeader + ",enabled\n" +
				"0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Setup Access,,,apex-class,MyController,Setup Access,Setup Access,true",
		},
		{
			name:    "vf-page access in json output",
			options: Options{Type: "vf-page", Target: "MyPage"},
			json: `{"targetType":"vf-page","targetName":"MyPage","rows":[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"vf-page","targetName":"MyPage","assignmentType":"PermissionSet","sourceId":"0PS1","sourceName":"Setup Access","sourceApiName":"Setup Access","sourceLabel":"Setup Access","access":{"kind":"enabled","enabled":true}}],` +
				`"stats":{"totalActiveUsersWithAccess":1,"profileGrants":0,"permissionSetGrants":1,"permissionSetGroupGrants":0},"warnings":[]}`,
		},
		{
			name:    "custom-permission access in csv output",
			options: Options{Type: "custom-permission", Target: "Can_Edit_Accounts"},
			csv: baseHeader + ",enabled\n" +
				"0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Setup Access,,,custom-permission,Can_Edit_Accounts,Setup Access,Setup Access,true",
		},
		{
			name:    "tab access in human output",
			options: Options{Type: "tab", Target: "Account"},
			humanContains: []string{
				"Tab: Account\nActive users with access: 1\n",
				"\n\nWarning: Profile-level tab visibility is not included because it is not exposed as a clean PermissionSetTabSetting data-API grant.\n\n",
				"User Name   Username          Visibility  Via\nJane Smith  jane@example.com  DefaultOn   Permission Set: Tab Access",
			},
			csv: baseHeader + ",visibility\n" +
				"0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Tab Access,,,tab,Account,Tab Access,Tab Access,DefaultOn",
		},
		{
			name:    "object access in human and json output",
			options: Options{Type: "object", Target: "Account"},
			humanContains: []string{
				"Object: Account\n",
				"User Name   Username          R  C  E  D  VA  MA  Via\nJane Smith  jane@example.com  Y  N  N  N  N   N   Permission Set: Account Readers",
			},
			csv: baseHeader + ",read,create,edit,delete,viewAll,modifyAll\n" +
				"0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,object,Account,Account Readers,Account Readers,true,false,false,false,false,false",
			json: `{"targetType":"object","targetName":"Account","sobjectType":"Account","rows":[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"object","targetName":"Account","assignmentType":"PermissionSet","sourceId":"0PS1","sourceName":"Account Readers","sourceApiName":"Account Readers","sourceLabel":"Account Readers","access":{"kind":"object","read":true,"create":false,"edit":false,"delete":false,"viewAll":false,"modifyAll":false}}],` +
				`"stats":{"totalActiveUsersWithAccess":1,"profileGrants":0,"permissionSetGrants":1,"permissionSetGroupGrants":0},"warnings":[]}`,
		},
		{
			name:    "field access in csv and human output",
			options: Options{Type: "field", Target: "account.customfield__c"},
			humanContains: []string{
				"Field: Account.CustomField__c\n",
				"User Name   Username          Read  Edit  Via\nJane Smith  jane@example.com  yes   no    Permission Set: Account Readers",
			},
			csv: baseHeader + ",read,edit\n" +
				"0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,field,Account.CustomField__c,Account Readers,Account Readers,true,false",
			json: `{"targetType":"field","targetName":"Account.CustomField__c","sobjectType":"Account","fieldApiName":"CustomField__c","rows":[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"field","targetName":"Account.CustomField__c","assignmentType":"PermissionSet","sourceId":"0PS1","sourceName":"Account Readers","sourceApiName":"Account Readers","sourceLabel":"Account Readers","access":{"kind":"field","read":true,"edit":false}}],` +
				`"stats":{"totalActiveUsersWithAccess":1,"profileGrants":0,"permissionSetGrants":1,"permissionSetGroupGrants":0},"warnings":[]}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := runAudit(t, baseConn(), test.options, "")
			for _, text := range test.humanContains {
				if !strings.Contains(got.human, text) {
					t.Errorf("human output lacks %q:\n%s", text, got.human)
				}
			}
			if test.csv != "" {
				expectEqual(t, "csv", got.csv, test.csv)
			}
			if test.json != "" {
				expectEqual(t, "json", resultJSON(t, got.result), test.json)
			}
			again := runAudit(t, baseConn(), test.options, "")
			expectEqual(t, "csv of a second run", again.csv, got.csv)
		})
	}
}

func TestUnsupportedType(t *testing.T) {
	_, err := Run(baseConn(), Options{Type: "not-a-type", Target: "MyClass"})
	accessError, ok := AsAccessError(err)
	if !ok {
		t.Fatalf("got %v, want an access error", err)
	}
	if accessError.Code != "errorUnsupportedAccessType" || accessError.Message != "Unsupported access type: not-a-type." {
		t.Errorf("got %s %q", accessError.Code, accessError.Message)
	}
	if args := ErrorArgs(accessError); len(args) != 1 || args[0] != "not-a-type" {
		t.Errorf("args = %v", args)
	}
}

func TestEmptyResultPrintsNoResultsLine(t *testing.T) {
	got := runAudit(t, newConn(answers()), Options{Type: "object", Target: "Account"}, "")
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Object: Account",
		"Active users with access: 0",
		"Profiles: 0 | Permission Sets: 0 | Permission Set Groups: 0",
		"",
		"No active users matched this target.",
	}, "\n"))
	expectEqual(t, "json", resultJSON(t, got.result), `{"targetType":"object","targetName":"Account","sobjectType":"Account","rows":[],"stats":{"totalActiveUsersWithAccess":0,"profileGrants":0,"permissionSetGrants":0,"permissionSetGroupGrants":0},"warnings":[]}`)
}

func TestFieldWithoutGrantsWarns(t *testing.T) {
	got := runAudit(t, newConn(answers()), Options{Type: "field", Target: "Account.CustomField__c"}, "")
	want := "No explicit FieldPermissions records were found for Account.CustomField__c. Standard fields may still be visible through base object/profile behavior. This command reports explicit field-level security grants only."
	if len(got.result.Warnings) != 1 || got.result.Warnings[0] != want {
		t.Errorf("warnings = %q", got.result.Warnings)
	}
	if strings.Contains(got.human, infoNoResults) || !strings.HasSuffix(got.human, "\n\nWarning: "+want) {
		t.Errorf("human output:\n%s", got.human)
	}
}

func TestQueryFailureNamesTypeAndTarget(t *testing.T) {
	conn := newConn(func(string) ([]sfapi.Record, bool, error) {
		return nil, false, errors.New("query failed")
	})
	_, err := Run(conn, Options{Type: "object", Target: "Account"})
	accessError, ok := AsAccessError(err)
	if !ok {
		t.Fatalf("got %v, want an access error", err)
	}
	expectEqual(t, "code", accessError.Code, "errorAccessQueryFailed")
	expectEqual(t, "message", accessError.Message, "Failed to resolve access for object target Account.")
	expectEqual(t, "args", strings.Join(ErrorArgs(accessError), "|"), "object|Account")
	if cause := ErrorCause(accessError); cause == nil || cause.Error() != "query failed" {
		t.Errorf("cause = %v", cause)
	}
}

func assignment(id, userID, name, username, permissionSetID string) rec {
	return rec{
		"Id":              id,
		"AssigneeId":      userID,
		"Assignee":        rec{"Name": name, "Username": username, "IsActive": true},
		"PermissionSetId": permissionSetID,
		"PermissionSet":   regular(permissionSetID, "Account Readers"),
	}
}

func TestHumanRowsAreSortedByUserName(t *testing.T) {
	conn := newConn(answers(
		on([]rec{accountReadGrant}, "FROM ObjectPermissions"),
		on([]rec{
			assignment("0PA2", "0052", "Zed User", "zed@example.com", "0PS1"),
			assignment("0PA1", "0051", "Amy User", "amy@example.com", "0PS1"),
		}, "FROM PermissionSetAssignment"),
	))
	result, err := Run(conn, Options{Type: "object", Target: "Account"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0].UserName != "Zed User" {
		t.Errorf("Run changed the order of the assignment rows: first is %s", result.Rows[0].UserName)
	}
	human, err := RenderAccessResult(result, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(human, "Amy User   amy@example.com  Y  N  N  N  N   N   Permission Set: Account Readers\nZed User   zed@example.com  Y  N  N  N  N   N   Permission Set: Account Readers") {
		t.Errorf("human output:\n%s", human)
	}
	if result.Stats.TotalActiveUsersWithAccess != 2 || result.Stats.PermissionSetGrants != 1 {
		t.Errorf("stats = %+v", result.Stats)
	}
}

func TestReverseFieldAuditOmitsMutedGroupAccess(t *testing.T) {
	conn := withUser(newConn(answers(
		on(janeUser, "FROM User"),
		on([]rec{
			{"PermissionSetId": "0PSDirect", "PermissionSet": rec{"Name": "Account Readers", "Type": "Regular"}},
			{"PermissionSetId": "0PSGroup", "PermissionSet": rec{"Name": "Sales Ops backing set", "Type": "Group"}},
			{"PermissionSetGroupId": "0PG1", "PermissionSetGroup": rec{"MasterLabel": "Sales Ops"}},
		}, "FROM PermissionSetAssignment"),
		on([]rec{{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PSMute", "PermissionSet": rec{"Type": "Muting"}}}, "FROM PermissionSetGroupComponent", "PermissionSet.Type = 'Muting'"),
		on([]rec{{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PSGroup", "PermissionSet": rec{"Name": "Grouped Reader", "Type": "Regular"}}}, "FROM PermissionSetGroupComponent"),
		on([]rec{{"ParentId": "0PSMute", "Field": "Account.CustomField__c", "PermissionsRead": true, "PermissionsEdit": false}}, "FROM FieldPermissions", "Parent.IsOwnedByProfile = false"),
		on([]rec{
			{"ParentId": "0PSDirect", "SobjectType": "Account", "Field": "Account.CustomField__c", "PermissionsRead": true, "PermissionsEdit": false},
			{"ParentId": "0PSGroup", "SobjectType": "Account", "Field": "Account.CustomField__c", "PermissionsRead": true, "PermissionsEdit": false},
		}, "FROM FieldPermissions"),
	)))
	got := runAudit(t, conn, Options{Type: "field", Target: "Account.CustomField__c", User: "Username:jane@example.com"}, "Jane Smith")
	expectEqual(t, "json", resultJSON(t, got.result), `{"targetType":"field","targetName":"Account.CustomField__c","sobjectType":"Account","fieldApiName":"CustomField__c","rows":[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"field","assignmentType":"PermissionSet","sourceId":"0PSDirect","sourceName":"Account Readers","sourceApiName":"Account Readers","sourceLabel":"Account Readers","targetName":"Account.CustomField__c","access":{"kind":"field","read":true,"edit":false}}],`+
		`"stats":{"totalActiveUsersWithAccess":1,"profileGrants":0,"permissionSetGrants":1,"permissionSetGroupGrants":0},"warnings":[]}`)
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Access for Jane Smith: Account.CustomField__c",
		"Accessible grants: 1",
		"Profiles: 0 | Permission Sets: 1 | Permission Set Groups: 0",
		"",
		"User Name   Username          Read  Edit  Via",
		"Jane Smith  jane@example.com  yes   no    Permission Set: Account Readers",
	}, "\n"))
	wantQueries := []string{
		"SELECT PermissionSetId, PermissionSet.Name, PermissionSet.Label, PermissionSet.IsOwnedByProfile, PermissionSet.ProfileId, PermissionSet.Profile.Name, PermissionSet.Type, PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel FROM PermissionSetAssignment WHERE AssigneeId = '0051'",
		"SELECT PermissionSetGroupId, PermissionSetId, PermissionSet.Name, PermissionSet.Label, PermissionSet.Type FROM PermissionSetGroupComponent WHERE PermissionSetGroupId IN ('0PG1')",
		"SELECT PermissionSetGroupId, PermissionSetId FROM PermissionSetGroupComponent WHERE PermissionSetGroupId IN ('0PG1') AND PermissionSet.Type = 'Muting'",
		"SELECT ParentId, Field, PermissionsRead, PermissionsEdit FROM FieldPermissions WHERE Parent.IsOwnedByProfile = false AND ParentId IN ('0PSMute') AND SobjectType = 'Account' AND Field = 'Account.CustomField__c'",
		"SELECT ParentId, SobjectType, Field, PermissionsRead, PermissionsEdit FROM FieldPermissions WHERE ParentId IN ('0PSDirect','0PSGroup') AND SobjectType = 'Account' AND Field = 'Account.CustomField__c' AND Parent.Type != 'Muting'",
	}
	expectEqual(t, "queries after the user lookup", strings.Join(conn.Queries[1:], "\n"), strings.Join(wantQueries, "\n"))
}

func TestReverseObjectAuditScopedBySobject(t *testing.T) {
	conn := withUser(newConn(answers(
		on(janeUser, "FROM User"),
		on([]rec{{"PermissionSetId": "0PSDirect", "PermissionSet": rec{"Name": "Account Readers", "Type": "Regular"}}}, "FROM PermissionSetAssignment"),
		on([]rec{{"ParentId": "0PSDirect", "SobjectType": "Account", "PermissionsRead": true}}, "FROM ObjectPermissions"),
	)))
	got := runAudit(t, conn, Options{Type: "object", Sobject: "Account", User: "Username:jane@example.com"}, "Jane Smith")
	expectEqual(t, "json", resultJSON(t, got.result), `{"targetType":"object","targetName":"Account","sobjectType":"Account","rows":[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"object","assignmentType":"PermissionSet","sourceId":"0PSDirect","sourceName":"Account Readers","sourceApiName":"Account Readers","sourceLabel":"Account Readers","targetName":"Account","access":{"kind":"object","read":true,"create":false,"edit":false,"delete":false,"viewAll":false,"modifyAll":false}}],`+
		`"stats":{"totalActiveUsersWithAccess":1,"profileGrants":0,"permissionSetGrants":1,"permissionSetGroupGrants":0},"warnings":[]}`)
}

func TestReverseFieldAuditScopedBySobject(t *testing.T) {
	conn := withUser(newConn(answers(
		on([]rec{{"Id": "0051", "Name": "Jane Smith", "Username": "jane@example.com"}}, "FROM User"),
		on([]rec{{"PermissionSetId": "0PSDirect", "PermissionSet": rec{"Name": "Account Readers", "Type": "Regular"}}}, "FROM PermissionSetAssignment"),
		on([]rec{
			{"ParentId": "0PSDirect", "SobjectType": "Account", "Field": "Account.B__c", "PermissionsRead": true},
			{"ParentId": "0PSDirect", "SobjectType": "Account", "Field": "Account.A__c", "PermissionsRead": true},
		}, "FROM FieldPermissions"),
	)))
	got := runAudit(t, conn, Options{Type: "field", Sobject: "Account", User: "Username:jane@example.com"}, "Jane Smith")
	if got.result.TargetName != "Account" || got.result.FieldAPIName != "" {
		t.Errorf("target = %q, field = %q", got.result.TargetName, got.result.FieldAPIName)
	}
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Access for Jane Smith: Account",
		"Accessible grants: 2",
		"Profiles: 0 | Permission Sets: 1 | Permission Set Groups: 0",
		"",
		"Target        User Name   Username          Read  Edit  Via",
		"Account.A__c  Jane Smith  jane@example.com  yes   no    Permission Set: Account Readers",
		"Account.B__c  Jane Smith  jane@example.com  yes   no    Permission Set: Account Readers",
	}, "\n"))
	expectEqual(t, "csv", got.csv, baseHeader+",read,edit\n"+
		"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Account Readers,,,field,Account.A__c,Account Readers,Account Readers,true,false\n"+
		"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Account Readers,,,field,Account.B__c,Account Readers,Account Readers,true,false")
	last := conn.Queries[len(conn.Queries)-1]
	expectEqual(t, "grant query", last, "SELECT ParentId, SobjectType, Field, PermissionsRead, PermissionsEdit FROM FieldPermissions WHERE ParentId IN ('0PSDirect') AND SobjectType = 'Account' AND Parent.Type != 'Muting'")
}

func TestReverseGroupRowsWithEqualLabelsAreSortedByID(t *testing.T) {
	conn := withUser(newConn(answers(
		on([]rec{{"Id": "0051", "Name": "Jane Smith", "Username": "jane@example.com"}}, "FROM User"),
		on([]rec{
			{"PermissionSetGroupId": "0PG2", "PermissionSetGroup": rec{"MasterLabel": "Sales Ops"}},
			{"PermissionSetGroupId": "0PG1", "PermissionSetGroup": rec{"MasterLabel": "Sales Ops"}},
		}, "FROM PermissionSetAssignment"),
		on([]rec{}, "FROM PermissionSetGroupComponent", "PermissionSet.Type = 'Muting'"),
		on([]rec{
			{"PermissionSetGroupId": "0PG2", "PermissionSetId": "0PS2", "PermissionSet": rec{"Name": "Account Reader", "Type": "Regular"}},
			{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PS1", "PermissionSet": rec{"Name": "Account Reader", "Type": "Regular"}},
		}, "FROM PermissionSetGroupComponent"),
		on([]rec{
			{"ParentId": "0PS2", "SobjectType": "Account", "PermissionsRead": true},
			{"ParentId": "0PS1", "SobjectType": "Account", "PermissionsRead": true},
		}, "FROM ObjectPermissions"),
	)))
	got := runAudit(t, conn, Options{Type: "object", Target: "Account", User: "Username:jane@example.com"}, "Jane Smith")
	expectEqual(t, "csv", got.csv, baseHeader+",read,create,edit,delete,viewAll,modifyAll\n"+
		"0051,Jane Smith,jane@example.com,PermissionSetGroup,0PG1,Sales Ops,0PS1,Account Reader,object,Account,Sales Ops,Sales Ops,true,false,false,false,false,false\n"+
		"0051,Jane Smith,jane@example.com,PermissionSetGroup,0PG2,Sales Ops,0PS2,Account Reader,object,Account,Sales Ops,Sales Ops,true,false,false,false,false,false")
	expectEqual(t, "first row json", resultJSON(t, got.result.Rows[0]), `{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"object","assignmentType":"PermissionSetGroup","sourceId":"0PG1","sourceName":"Sales Ops","sourceApiName":"Sales Ops","sourceLabel":"Sales Ops","viaPermissionSetId":"0PS1","viaPermissionSetName":"Account Reader","targetName":"Account","access":{"kind":"object","read":true,"create":false,"edit":false,"delete":false,"viewAll":false,"modifyAll":false}}`)
	if !strings.Contains(got.human, "PSG: Sales Ops / PS: Account Reader") {
		t.Errorf("human output:\n%s", got.human)
	}
	if got.result.Stats.PermissionSetGroupGrants != 2 {
		t.Errorf("stats = %+v", got.result.Stats)
	}
}

func TestReverseAuditWithoutAssignmentsPrintsNoUserResults(t *testing.T) {
	conn := withUser(newConn(answers(
		on(janeUser, "FROM User"),
		on([]rec{{"DurableId": "Account", "Name": "standard-Account"}}, "FROM TabDefinition"),
	)))
	got := runAudit(t, conn, Options{Type: "tab", Target: "Account", User: "Username:jane@example.com"}, "Jane Smith")
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Access for Jane Smith: Account",
		"Accessible grants: 0",
		"Profiles: 0 | Permission Sets: 0 | Permission Set Groups: 0",
		"",
		"No access grants matched this user and scope.",
	}, "\n"))
	expectEqual(t, "csv", got.csv, baseHeader+",visibility")
}

func TestScopeErrors(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		want    string
		code    string
	}{
		{"target and sobject together", Options{Type: "field", Target: "Account.Name", Sobject: "Account", User: "Username:jane@example.com"}, "target and sobject are mutually exclusive.", "errorAccessScopesMutuallyExclusive"},
		{"neither target nor user", Options{Type: "object", Sobject: "Account"}, "An access target or user is required.", ""},
		{"user without target or sobject", Options{Type: "field", User: "Username:jane@example.com"}, "SObject scope is only valid for field and object access.", ""},
		{"sobject with a type other than field or object", Options{Type: "record-type", Sobject: "Account", User: "Username:jane@example.com"}, "SObject scope is only valid for field and object access.", ""},
		{"user value without a colon", Options{Type: "object", Target: "Account", User: "jane"}, `Invalid --user value "jane". Expected field:value.`, "errorInvalidUserValue"},
		{"unknown user field", Options{Type: "object", Target: "Account", User: "Nope__c:jane"}, `Invalid user match field "Nope__c".`, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Run(withUser(baseConn()), test.options)
			if err == nil || err.Error() != test.want {
				t.Fatalf("got %v, want %q", err, test.want)
			}
			if warden, ok := core.AsWardenError(err); (ok && warden.Code != test.code) || (!ok && test.code != "") {
				t.Errorf("got %v, want code %q", err, test.code)
			}
		})
	}
}

func TestUserResolutionErrors(t *testing.T) {
	cases := []struct {
		name  string
		users []rec
		want  string
	}{
		{"no user", []rec{}, `Username="jane@example.com" matched no user`},
		{"two users", []rec{
			{"Id": "0051", "Name": "Jane Smith", "Username": "jane@example.com"},
			{"Id": "0052", "Name": "Jane Smith 2", "Username": "jane@example.com"},
		}, `Username="jane@example.com" matched multiple users`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			conn := withUser(newConn(answers(on(test.users, "FROM User"))))
			_, err := Run(conn, Options{Type: "object", Target: "Account", User: "Username:jane@example.com"})
			if err == nil || err.Error() != test.want {
				t.Errorf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateTarget(t *testing.T) {
	recordType := func(rows ...rec) queryFunc { return answers(on(rows, "FROM RecordType")) }
	active := rec{"Id": "0121", "SobjectType": "Account", "DeveloperName": "Business_Account", "IsActive": true}
	cases := []struct {
		name      string
		targetTyp string
		target    string
		query     queryFunc
		want      ValidatedTarget
		code      string
		message   string
		soql      string
	}{
		{name: "object name as the org writes it", targetTyp: "object", target: " account ", want: ValidatedTarget{Type: "object", TargetName: "Account", SobjectType: "Account"}},
		{name: "blank object", targetTyp: "object", target: "  ", code: "errorInvalidTarget", message: "Invalid target value:   ."},
		{name: "unknown object", targetTyp: "object", target: "Nope__c", code: "errorObjectNotFound", message: "Object not found: Nope__c."},
		{name: "field name as the org writes it", targetTyp: "field", target: "account.customfield__C", want: ValidatedTarget{Type: "field", TargetName: "Account.CustomField__c", SobjectType: "Account", FieldAPIName: "CustomField__c"}},
		{name: "field without object", targetTyp: "field", target: "CustomField__c", code: "errorFieldTargetMustBeQualified", message: "Field target must be qualified as ObjectApiName.FieldApiName: CustomField__c."},
		{name: "field with three parts", targetTyp: "field", target: "a.b.c", code: "errorFieldTargetMustBeQualified", message: "Field target must be qualified as ObjectApiName.FieldApiName: a.b.c."},
		{name: "unknown field", targetTyp: "field", target: "Account.Nope__c", code: "errorFieldNotFound", message: "Object Account does not have field Nope__c."},
		{name: "apex class", targetTyp: "apex-class", target: "My'Controller", query: answers(on([]rec{{"Id": "01p1", "Name": "MyController"}}, "FROM ApexClass")),
			want: ValidatedTarget{Type: "apex-class", TargetName: "MyController", SetupEntityID: "01p1"},
			soql: `SELECT Id, Name FROM ApexClass WHERE Name = 'My\'Controller' LIMIT 1`},
		{name: "unknown apex class", targetTyp: "apex-class", target: "Nope", code: "errorApexClassNotFound", message: "Apex class not found: Nope."},
		{name: "unknown visualforce page", targetTyp: "vf-page", target: "Nope", code: "errorVisualforcePageNotFound", message: "Visualforce page not found: Nope.",
			soql: "SELECT Id, Name FROM ApexPage WHERE Name = 'Nope' LIMIT 1"},
		{name: "unknown custom permission", targetTyp: "custom-permission", target: "Nope", code: "errorCustomPermissionNotFound", message: "Custom permission not found: Nope.",
			soql: "SELECT Id, DeveloperName FROM CustomPermission WHERE DeveloperName = 'Nope' LIMIT 1"},
		{name: "tab", targetTyp: "tab", target: "Account", query: answers(on([]rec{{"DurableId": "Account", "Name": "standard-Account"}}, "FROM TabDefinition")),
			want: ValidatedTarget{Type: "tab", TargetName: "Account"},
			soql: "SELECT DurableId, Name FROM TabDefinition WHERE DurableId = 'Account' LIMIT 1"},
		{name: "unknown tab", targetTyp: "tab", target: "Nope", code: "errorTabNotFound", message: "Tab not found: Nope."},
		{name: "record type", targetTyp: "record-type", target: "Account.Business_Account", query: recordType(active),
			want: ValidatedTarget{Type: "record-type", TargetName: "Account.Business_Account", SobjectType: "Account", RecordTypeID: "0121"},
			soql: "SELECT Id, SobjectType, DeveloperName, Name, IsActive FROM RecordType WHERE SobjectType = 'Account' AND DeveloperName = 'Business_Account' LIMIT 2"},
		{name: "record type id", targetTyp: "record-type", target: "012000000000001", code: "errorRecordTypeTargetMustBeQualified", message: "Record type target must be qualified as SObject.DeveloperName for an active, non-master record type: 012000000000001."},
		{name: "record type without object", targetTyp: "record-type", target: "Business_Account", code: "errorRecordTypeTargetMustBeQualified"},
		{name: "master record type", targetTyp: "record-type", target: "Account.Master", code: "errorMasterRecordTypeUnsupported", message: "The Master record type is not supported. Specify an active non-master record type as SObject.DeveloperName: Account.Master."},
		{name: "unknown record type", targetTyp: "record-type", target: "Account.Nope", code: "errorRecordTypeNotFound", message: "Record type not found or ambiguous: Account.Nope."},
		{name: "two record types", targetTyp: "record-type", target: "Account.Business_Account", query: recordType(active, active), code: "errorRecordTypeAmbiguous", message: "Record type target matched multiple records and cannot be selected safely: Account.Business_Account."},
		{name: "inactive record type", targetTyp: "record-type", target: "Account.Business_Account", query: recordType(rec{"Id": "0121", "SobjectType": "Account", "DeveloperName": "Business_Account", "IsActive": false}), code: "errorRecordTypeInactive", message: "Record type is inactive: Account.Business_Account."},
		{name: "record type row without an id", targetTyp: "record-type", target: "Account.Business_Account", query: recordType(rec{"SobjectType": "Account", "DeveloperName": "Business_Account", "IsActive": true}), code: "errorRecordTypeNotFound"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			query := test.query
			if query == nil {
				query = answers()
			}
			conn := newConn(query)
			resolver, err := GetResolver(test.targetTyp)
			if err != nil {
				t.Fatal(err)
			}
			target, err := resolver.ValidateTarget(conn, test.target)
			if test.soql != "" {
				expectEqual(t, "query", strings.Join(conn.Queries, "\n"), test.soql)
			}
			if test.code != "" {
				accessError, ok := AsAccessError(err)
				if !ok {
					t.Fatalf("got %v, %v; want error %s", target, err, test.code)
				}
				expectEqual(t, "code", accessError.Code, test.code)
				if test.message != "" {
					expectEqual(t, "message", accessError.Message, test.message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if *target != test.want {
				t.Errorf("got %+v, want %+v", *target, test.want)
			}
		})
	}
}
