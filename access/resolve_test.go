package access

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func groupAssignment(id, userID, name, psgID, label string) rec {
	return rec{
		"Id":                   id,
		"AssigneeId":           userID,
		"Assignee":             rec{"Name": name, "Username": strings.ToLower(name) + "@example.com", "IsActive": true},
		"PermissionSetGroupId": psgID,
		"PermissionSetGroup":   rec{"DeveloperName": "Sales_Ops", "MasterLabel": label},
	}
}

// groupRules is an org in which permission set 0PS1 is a component of
// group 0PG1, assigned to Gia, and of group 0PG2, assigned to Hal.  Group
// 0PG1 has the muting permission set 0PSMute.
func groupRules(grant rule, mutingMasks rule) []rule {
	component := func(psgID string) rec {
		return rec{
			"PermissionSetGroupId": psgID,
			"PermissionSetGroup":   rec{"DeveloperName": "Sales_Ops", "MasterLabel": "Sales Ops"},
			"PermissionSetId":      "0PS1",
			"PermissionSet":        rec{"Name": "Account Readers", "Type": "Regular"},
		}
	}
	return []rule{
		mutingMasks,
		grant,
		on([]rec{{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PSMute"}}, "FROM PermissionSetGroupComponent", "PermissionSet.Type = 'Muting'"),
		on([]rec{component("0PG1"), component("0PG2")}, "FROM PermissionSetGroupComponent"),
		on([]rec{}, "FROM PermissionSetAssignment", "AND PermissionSetId IN"),
		on([]rec{
			groupAssignment("0PA1", "0051", "Gia", "0PG1", "Sales Ops"),
			groupAssignment("0PA2", "0052", "Hal", "0PG2", "Sales Ops"),
		}, "FROM PermissionSetAssignment", "AND PermissionSetGroupId IN"),
	}
}

func TestForwardFieldAuditAppliesGroupMuting(t *testing.T) {
	conn := newConn(answers(groupRules(
		on([]rec{{"ParentId": "0PS1", "Parent": regular("0PS1", "Account Readers"), "PermissionsRead": true, "PermissionsEdit": true}}, "FROM FieldPermissions", "Parent.Type != 'Muting'"),
		on([]rec{{"ParentId": "0PSMute", "PermissionsRead": false, "PermissionsEdit": true}}, "FROM FieldPermissions", "Parent.IsOwnedByProfile = false"),
	)...))
	got := runAudit(t, conn, Options{Type: "field", Target: "Account.CustomField__c"}, "")
	expectEqual(t, "csv", got.csv, baseHeader+",read,edit\n"+
		"0051,Gia,gia@example.com,PermissionSetGroup,0PG1,Sales Ops,0PS1,Account Readers,field,Account.CustomField__c,Sales_Ops,Sales Ops,true,false\n"+
		"0052,Hal,hal@example.com,PermissionSetGroup,0PG2,Sales Ops,0PS1,Account Readers,field,Account.CustomField__c,Sales_Ops,Sales Ops,true,true")
	expectEqual(t, "first row json", resultJSON(t, got.result.Rows[0]), `{"userId":"0051","userName":"Gia","username":"gia@example.com","targetType":"field","targetName":"Account.CustomField__c","assignmentType":"PermissionSetGroup","sourceId":"0PG1","sourceName":"Sales Ops","sourceApiName":"Sales_Ops","sourceLabel":"Sales Ops","viaPermissionSetId":"0PS1","viaPermissionSetName":"Account Readers","access":{"kind":"field","read":true,"edit":false}}`)
	wantQueries := []string{
		"SELECT ParentId, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type, PermissionsRead, PermissionsEdit FROM FieldPermissions WHERE SobjectType = 'Account' AND Field = 'Account.CustomField__c' AND Parent.Type != 'Muting'",
		"SELECT PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel, PermissionSetId, PermissionSet.Name, PermissionSet.Type FROM PermissionSetGroupComponent WHERE PermissionSetId IN ('0PS1')",
		"SELECT PermissionSetGroupId, PermissionSetId FROM PermissionSetGroupComponent WHERE PermissionSetGroupId IN ('0PG1','0PG2') AND PermissionSet.Type = 'Muting'",
		"SELECT ParentId, PermissionsRead, PermissionsEdit FROM FieldPermissions WHERE Parent.IsOwnedByProfile = false AND ParentId IN ('0PSMute') AND SobjectType = 'Account' AND Field = 'Account.CustomField__c'",
	}
	expectEqual(t, "first queries", strings.Join(conn.Queries[:4], "\n"), strings.Join(wantQueries, "\n"))
	if !strings.HasSuffix(conn.Queries[5], "WHERE Assignee.IsActive = true AND PermissionSetGroupId IN ('0PG1','0PG2') ORDER BY Assignee.Name, Id") {
		t.Errorf("group assignment query: %s", conn.Queries[5])
	}
}

func TestForwardObjectAuditOmitsFullyMutedGroup(t *testing.T) {
	conn := newConn(answers(groupRules(
		on([]rec{accountReadGrant}, "FROM ObjectPermissions", "Parent.Type != 'Muting'"),
		on([]rec{{"ParentId": "0PSMute", "PermissionsRead": true}}, "FROM ObjectPermissions", "Parent.IsOwnedByProfile = false"),
	)...))
	got := runAudit(t, conn, Options{Type: "object", Target: "Account"}, "")
	expectEqual(t, "csv", got.csv, baseHeader+",read,create,edit,delete,viewAll,modifyAll\n"+
		"0052,Hal,hal@example.com,PermissionSetGroup,0PG2,Sales Ops,0PS1,Account Readers,object,Account,Sales_Ops,Sales Ops,true,false,false,false,false,false")
	if got.result.Stats != (Stats{TotalActiveUsersWithAccess: 1, PermissionSetGroupGrants: 1}) {
		t.Errorf("stats = %+v", got.result.Stats)
	}
}

func TestForwardAuditReportsProfileAndSkipsInactiveAssignee(t *testing.T) {
	profileParent := rec{"Id": "0PSProfile", "Name": "X00ex000001", "IsOwnedByProfile": true, "ProfileId": "00e1", "Profile": rec{"Name": "Sales"}, "Type": "Profile"}
	grant := rec{"ParentId": "0PSProfile", "Parent": profileParent, "PermissionsRead": true}
	conn := newConn(answers(
		on([]rec{grant}, "FROM ObjectPermissions"),
		on([]rec{
			{"Id": "0PA1", "AssigneeId": "0051", "Assignee": rec{"Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true}, "PermissionSetId": "0PSProfile"},
			{"Id": "0PA2", "AssigneeId": "0052", "Assignee": rec{"Name": "Gone User", "Username": "gone@example.com", "IsActive": false}, "PermissionSetId": "0PSProfile"},
			{"Id": "0PA3", "AssigneeId": "0053", "PermissionSetId": "0PSProfile"},
		}, "FROM PermissionSetAssignment"),
	))
	got := runAudit(t, conn, Options{Type: "object", Target: "Account"}, "")
	expectEqual(t, "csv", got.csv, baseHeader+",read,create,edit,delete,viewAll,modifyAll\n"+
		"0053,0053,,Profile,00e1,Sales,,,object,Account,X00ex000001,Sales,true,false,false,false,false,false\n"+
		"0051,Jane Smith,jane@example.com,Profile,00e1,Sales,,,object,Account,X00ex000001,Sales,true,false,false,false,false,false")
	if !strings.Contains(got.human, "Profile: Sales") {
		t.Errorf("human output:\n%s", got.human)
	}
	for _, query := range conn.Queries {
		if strings.Contains(query, "FROM PermissionSetGroupComponent") {
			t.Errorf("a profile-owned permission set was looked up as a group component: %s", query)
		}
	}
	if got.result.Stats != (Stats{TotalActiveUsersWithAccess: 2, ProfileGrants: 1}) {
		t.Errorf("stats = %+v", got.result.Stats)
	}
}

func TestQueryAllReadsEveryPage(t *testing.T) {
	conn := sfapitest.New()
	conn.Results = []sfapitest.QueryAnswer{{Contains: "FROM A", Records: []rec{{"Id": "1"}}, Next: "/next/1"}}
	conn.More["/next/1"] = &sfapi.QueryResult{Done: false, NextRecordsURL: "/next/2", Records: []rec{{"Id": "2"}}}
	conn.More["/next/2"] = &sfapi.QueryResult{Done: true, Records: []rec{{"Id": "3"}}}
	rows, err := queryAll(conn, "SELECT Id FROM A")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, text(row, "Id"))
	}
	expectEqual(t, "ids", strings.Join(ids, ","), "1,2,3")
}

func TestQueryAllInChunksUses250Values(t *testing.T) {
	conn := sfapitest.New()
	values := make([]string, 501)
	for i := range values {
		values[i] = fmt.Sprintf("id%d", i)
	}
	_, err := queryAllInChunks(conn, values, func(chunk []string) string {
		return fmt.Sprintf("%d %s %s", len(chunk), chunk[0], chunk[len(chunk)-1])
	})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "queries", strings.Join(conn.Queries, "\n"), "250 id0 id249\n250 id250 id499\n1 id500 id500")
	if _, err := chunkValues(values, 0); err == nil || err.Error() != "chunkSize must be greater than 0" {
		t.Errorf("chunkValues with size 0: %v", err)
	}
}

// recordTypeOrg is an org with one active user whose profile 00eProfile
// and direct permission set 0PSDirect are candidates, and, when withGroup
// is set, group 0PG1 with component 0PSGrouped and muting permission set
// 0PSMute.
type recordTypeOrg struct {
	withGroup bool
	read      func(metadataType string, names []string) ([]rec, error)
	list      func(metadataType string) ([]rec, error)
}

func visibility(recordType string, visible bool, extra ...any) rec {
	entry := rec{"recordType": recordType, "visible": visible}
	for i := 0; i+1 < len(extra); i += 2 {
		entry[extra[i].(string)] = extra[i+1]
	}
	return entry
}

func defaultRead(metadataType string, names []string) ([]rec, error) {
	components := []rec{}
	for _, name := range names {
		entry := visibility("Account.Business_Account", true)
		if metadataType == "Profile" {
			entry["default"] = true
		}
		// A component with one entry has a record, and one with several
		// has a list, as the Metadata API responses convert.
		var entries any = entry
		if metadataType == "PermissionSet" {
			entries = []any{visibility("Account.Other", false), entry}
		}
		components = append(components, rec{"fullName": name, "recordTypeVisibilities": entries})
	}
	return components, nil
}

func (org recordTypeOrg) conn() *sfapitest.Conn {
	jane := rec{"Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true}
	assignments := []rec{
		{"Id": "0PA1", "AssigneeId": "0051", "Assignee": jane, "PermissionSetId": "0PSProfile",
			"PermissionSet": rec{"Name": "Sales", "IsOwnedByProfile": true, "ProfileId": "00eProfile", "Profile": rec{"Name": "Sales"}, "Type": "Regular"}},
		{"Id": "0PA2", "AssigneeId": "0051", "Assignee": jane, "PermissionSetId": "0PSDirect",
			"PermissionSet": rec{"Name": "Business_Access", "IsOwnedByProfile": false, "Type": "Regular"}},
	}
	permissionSets := []rec{{"Id": "0PSDirect", "Name": "Business_Access", "IsOwnedByProfile": false, "Type": "Regular"}}
	components := []rec{}
	if org.withGroup {
		assignments = append(assignments, rec{"Id": "0PA3", "AssigneeId": "0051", "Assignee": jane, "PermissionSetGroupId": "0PG1",
			"PermissionSetGroup": rec{"DeveloperName": "Sales_Ops", "MasterLabel": "Sales Ops"}})
		permissionSets = append(permissionSets, rec{"Id": "0PSGrouped", "Name": "Grouped_Access", "IsOwnedByProfile": false, "Type": "Regular"})
		components = []rec{
			{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PSGrouped", "PermissionSet": rec{"Name": "Grouped_Access", "Type": "Regular"}},
			{"PermissionSetGroupId": "0PG1", "PermissionSetId": "0PSMute000000001AAA", "PermissionSet": nil},
		}
	}
	conn := withUser(newConn(answers(
		on([]rec{{"Id": "0121", "SobjectType": "Account", "DeveloperName": "Business_Account", "IsActive": true}}, "FROM RecordType"),
		on([]rec{{"Id": "0051", "Name": "Jane Smith", "Username": "jane@example.com", "IsActive": true}}, "FROM User WHERE Username"),
		on([]rec{{"ProfileId": "00eProfile"}, {"ProfileId": "00eProfile"}, {"ProfileId": "00eSystem"}}, "FROM User"),
		on([]rec{{"Id": "00eProfile", "Name": "Sales User"}}, "FROM Profile"),
		on([]rec{{"Id": "0PSProfile", "Name": "X00e", "IsOwnedByProfile": true, "ProfileId": "00eProfile", "Type": "Regular"}}, "FROM PermissionSet ", "IsOwnedByProfile = true"),
		on(permissionSets, "FROM PermissionSet ", "Id IN"),
		on([]rec{{"PermissionSetGroupId": "0PG1", "PermissionSetGroup": rec{"DeveloperName": "Sales_Ops", "MasterLabel": "Sales Ops"}, "PermissionSetId": "0PSGrouped"}}, "FROM PermissionSetGroupComponent", "WHERE PermissionSetId IN"),
		on(components, "FROM PermissionSetGroupComponent"),
		on(assignments, "FROM PermissionSetAssignment"),
	)))
	conn.OnMetadataList = org.list
	if conn.OnMetadataList == nil {
		conn.OnMetadataList = func(metadataType string) ([]rec, error) {
			if metadataType == "Profile" {
				return []rec{{"id": "00eProfile", "fullName": "Sales"}}, nil
			}
			return []rec{{"id": "0PSMute000000001", "fullName": "Sales_Ops_Muting"}}, nil
		}
	}
	conn.OnMetadataRead = org.read
	if conn.OnMetadataRead == nil {
		conn.OnMetadataRead = defaultRead
	}
	return conn
}

const recordTypeHeader = baseHeader + ",visible,default\n"

func TestRecordTypeForwardAudit(t *testing.T) {
	var reads []string
	org := recordTypeOrg{read: func(metadataType string, names []string) ([]rec, error) {
		reads = append(reads, metadataType+":"+strings.Join(names, ","))
		return defaultRead(metadataType, names)
	}}
	conn := org.conn()
	got := runAudit(t, conn, Options{Type: "record-type", Target: "Account.Business_Account"}, "")
	expectEqual(t, "metadata reads", strings.Join(reads, " "), "Profile:Sales PermissionSet:Business_Access")
	expectEqual(t, "csv", got.csv, recordTypeHeader+
		"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Business_Access,,,record-type,Account.Business_Account,Business_Access,Business_Access,true,\n"+
		"0051,Jane Smith,jane@example.com,Profile,00eProfile,Sales User,,,record-type,Account.Business_Account,X00e,Sales User,true,true")
	expectEqual(t, "human", got.human, strings.Join([]string{
		"Record Type: Account.Business_Account",
		"Active users with access: 1",
		"Profiles: 1 | Permission Sets: 1 | Permission Set Groups: 0",
		"",
		"User Name   Username          Visible  Default  Via",
		"Jane Smith  jane@example.com  yes      n/a      Permission Set: Business_Access",
		"Jane Smith  jane@example.com  yes      yes      Profile: Sales User",
	}, "\n"))
	expectEqual(t, "json", resultJSON(t, got.result.Rows), `[{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"record-type","targetName":"Account.Business_Account","assignmentType":"PermissionSet","sourceId":"0PSDirect","sourceName":"Business_Access","sourceApiName":"Business_Access","sourceLabel":"Business_Access","access":{"kind":"record-type","visible":true,"default":null}},`+
		`{"userId":"0051","userName":"Jane Smith","username":"jane@example.com","targetType":"record-type","targetName":"Account.Business_Account","assignmentType":"Profile","sourceId":"00eProfile","sourceName":"Sales User","sourceApiName":"X00e","sourceLabel":"Sales User","access":{"kind":"record-type","visible":true,"default":true}}]`)
	wantQueries := []string{
		"SELECT Id, SobjectType, DeveloperName, Name, IsActive FROM RecordType WHERE SobjectType = 'Account' AND DeveloperName = 'Business_Account' LIMIT 2",
		"SELECT ProfileId FROM User WHERE IsActive = true",
		"SELECT Id, Name FROM Profile WHERE Id IN ('00eProfile','00eSystem')",
		"SELECT Id, Name, Label, Type, IsOwnedByProfile, ProfileId, Profile.Name FROM PermissionSet WHERE IsOwnedByProfile = true AND ProfileId IN ('00eProfile','00eSystem')",
		"SELECT PermissionSetId, PermissionSet.Type, PermissionSet.IsOwnedByProfile, PermissionSetGroupId FROM PermissionSetAssignment WHERE Assignee.IsActive = true",
		"SELECT Id, Name, Label, Type, IsOwnedByProfile, ProfileId, Profile.Name FROM PermissionSet WHERE Id IN ('0PSDirect')",
		"SELECT PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel, PermissionSetId, PermissionSet.Name, PermissionSet.Type FROM PermissionSetGroupComponent WHERE PermissionSetId IN ('0PSDirect')",
	}
	expectEqual(t, "first queries", strings.Join(conn.Queries[:7], "\n"), strings.Join(wantQueries, "\n"))
}

func TestRecordTypeAuditsWithGroupAndMuting(t *testing.T) {
	mutingRead := func(mutes bool) func(string, []string) ([]rec, error) {
		return func(metadataType string, names []string) ([]rec, error) {
			if metadataType == "MutingPermissionSet" {
				return []rec{{"fullName": names[0], "recordTypeVisibilities": visibility("Account.Business_Account", mutes)}}, nil
			}
			return defaultRead(metadataType, names)
		}
	}
	groupRow := "0051,Jane Smith,jane@example.com,PermissionSetGroup,0PG1,Sales Ops,0PSGrouped,Grouped_Access,record-type,Account.Business_Account,Sales_Ops,Sales Ops,true,\n"
	cases := []struct {
		name    string
		mutes   bool
		reverse bool
		want    string
	}{
		{name: "forward, group not muted", want: recordTypeHeader +
			"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Business_Access,,,record-type,Account.Business_Account,Business_Access,Business_Access,true,\n" +
			groupRow +
			"0051,Jane Smith,jane@example.com,Profile,00eProfile,Sales User,,,record-type,Account.Business_Account,X00e,Sales User,true,true"},
		{name: "forward, group muted", mutes: true, want: recordTypeHeader +
			"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Business_Access,,,record-type,Account.Business_Account,Business_Access,Business_Access,true,\n" +
			"0051,Jane Smith,jane@example.com,Profile,00eProfile,Sales User,,,record-type,Account.Business_Account,X00e,Sales User,true,true"},
		{name: "reverse, group not muted", reverse: true, want: recordTypeHeader +
			"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Business_Access,,,record-type,Account.Business_Account,Business_Access,Business_Access,true,\n" +
			"0051,Jane Smith,jane@example.com,PermissionSetGroup,0PG1,Sales Ops,0PSGrouped,Grouped_Access,record-type,Account.Business_Account,Sales Ops,Sales Ops,true,\n" +
			"0051,Jane Smith,jane@example.com,Profile,00eProfile,Sales,,,record-type,Account.Business_Account,Sales,Sales,true,true"},
		{name: "reverse, group muted", reverse: true, mutes: true, want: recordTypeHeader +
			"0051,Jane Smith,jane@example.com,PermissionSet,0PSDirect,Business_Access,,,record-type,Account.Business_Account,Business_Access,Business_Access,true,\n" +
			"0051,Jane Smith,jane@example.com,Profile,00eProfile,Sales,,,record-type,Account.Business_Account,Sales,Sales,true,true"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			conn := recordTypeOrg{withGroup: true, read: mutingRead(test.mutes)}.conn()
			options := Options{Type: "record-type", Target: "Account.Business_Account"}
			label := ""
			if test.reverse {
				options.User = "Username:jane@example.com"
				label = "Jane Smith"
			}
			got := runAudit(t, conn, options, label)
			expectEqual(t, "csv", got.csv, test.want)
			if len(got.result.Warnings) != 0 {
				t.Errorf("warnings = %q", got.result.Warnings)
			}
		})
	}
}

func TestRecordTypeMetadataFailures(t *testing.T) {
	failure := "Unable to read complete %s metadata for record-type access audit (%s). No partial audit result was returned."
	cases := []struct {
		name string
		read func(metadataType string, names []string) ([]rec, error)
		list func(metadataType string) ([]rec, error)
		// args are the type and names of the error; empty means the
		// audit succeeds with the warning.
		args    []string
		cause   string
		code    string
		warning string
		rows    int
	}{
		{
			name:  "read request fails",
			read:  func(string, []string) ([]rec, error) { return nil, errors.New("INSUFFICIENT_ACCESS") },
			args:  []string{"Profile", "Sales"},
			cause: "INSUFFICIENT_ACCESS",
		},
		{
			name: "response has a component that was not requested",
			read: func(_ string, names []string) ([]rec, error) {
				return []rec{{"fullName": "Other"}}, nil
			},
			args:  []string{"Profile", "Sales"},
			cause: "Unexpected component Other.",
		},
		{
			name: "response has a component twice",
			read: func(_ string, names []string) ([]rec, error) {
				return []rec{{"fullName": names[0]}, {"fullName": names[0]}}, nil
			},
			args:  []string{"Profile", "Sales"},
			cause: "Duplicate component Sales.",
		},
		{
			name: "profile entry has no default",
			read: func(_ string, names []string) ([]rec, error) {
				return []rec{{"fullName": names[0], "recordTypeVisibilities": visibility("Account.Business_Account", true)}}, nil
			},
			args:  []string{"Profile", "Account.Business_Account"},
			cause: "Profile record type visibility has no default value.",
		},
		{
			name: "entry has no visible flag",
			read: func(_ string, names []string) ([]rec, error) {
				return []rec{{"fullName": names[0], "recordTypeVisibilities": rec{"recordType": "Account.Business_Account"}}}, nil
			},
			args:  []string{"Profile", "Sales"},
			cause: "Malformed recordTypeVisibilities entry.",
		},
		{
			name: "entry has a default that is not a boolean",
			read: func(_ string, names []string) ([]rec, error) {
				return []rec{{"fullName": names[0], "recordTypeVisibilities": visibility("Account.Business_Account", true, "default", "yes")}}, nil
			},
			args:  []string{"Profile", "Sales"},
			cause: "Malformed record type default value.",
		},
		{
			name: "component has two entries for the record type",
			read: func(_ string, names []string) ([]rec, error) {
				entry := visibility("Account.Business_Account", true, "default", true)
				return []rec{{"fullName": names[0], "recordTypeVisibilities": []any{entry, entry}}}, nil
			},
			args:  []string{"Profile", "Sales"},
			cause: "Duplicate visibility for Account.Business_Account.",
		},
		{
			name:  "metadata listing fails",
			list:  func(string) ([]rec, error) { return nil, errors.New("list failed") },
			code:  "errorAccessQueryFailed",
			args:  []string{"record-type", "Account.Business_Account"},
			cause: "list failed",
		},
		{
			name: "response omits the profile",
			read: func(metadataType string, names []string) ([]rec, error) {
				if metadataType == "Profile" {
					return []rec{{"fullName": ""}}, nil
				}
				return defaultRead(metadataType, names)
			},
			warning: `Partial response: 1 metadata component(s) could not be read through the Metadata API (Sales). Affected profiles and permission sets are omitted, and any muting they define is not applied, so this audit may be incomplete. Grant the running user "API Enabled", "View Setup and Configuration", and either "Modify Metadata Through Metadata API Functions" or "Modify All Data" for a full accounting.`,
			rows:    1,
		},
	}
	for _, test := range cases {
		for _, reverse := range []bool{false, true} {
			name := test.name + ", forward"
			options := Options{Type: "record-type", Target: "Account.Business_Account"}
			if reverse {
				name = test.name + ", reverse"
				options.User = "Username:jane@example.com"
			}
			t.Run(name, func(t *testing.T) {
				result, err := Run(recordTypeOrg{read: test.read, list: test.list}.conn(), options)
				if test.warning != "" {
					if err != nil {
						t.Fatal(err)
					}
					if len(result.Warnings) != 1 || result.Warnings[0] != test.warning {
						t.Errorf("warnings = %q", result.Warnings)
					}
					if len(result.Rows) != test.rows {
						t.Errorf("rows = %+v", result.Rows)
					}
					return
				}
				accessError, ok := AsAccessError(err)
				if !ok {
					t.Fatalf("got %v, want an access error", err)
				}
				code := test.code
				if code == "" {
					code = "errorRecordTypeMetadataReadFailed"
				}
				expectEqual(t, "code", accessError.Code, code)
				args := test.args
				// The reverse audit names the profile, not the record
				// type, when a profile entry has no default.
				if reverse && test.cause == "Profile record type visibility has no default value." {
					args = []string{"Profile", "Sales"}
				}
				expectEqual(t, "args", strings.Join(ErrorArgs(accessError), "|"), strings.Join(args, "|"))
				if code == "errorRecordTypeMetadataReadFailed" && !strings.HasPrefix(accessError.Message, fmt.Sprintf(failure, args[0], args[1])) {
					t.Errorf("message = %q", accessError.Message)
				}
				if cause := ErrorCause(accessError); cause == nil || cause.Error() != test.cause {
					t.Errorf("cause = %v, want %q", cause, test.cause)
				}
			})
		}
	}
}

func TestRecordTypeForwardAuditFailsWhenPermissionSetIsNotFound(t *testing.T) {
	org := recordTypeOrg{withGroup: true}
	conn := org.conn()
	inner := conn.OnQuery
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if strings.Contains(soql, "FROM PermissionSet ") && strings.Contains(soql, "Id IN") {
			return []rec{{"Id": "0PSDirect", "Name": "Business_Access", "Type": "Regular"}}, true, nil
		}
		return inner(soql)
	}
	_, err := Run(conn, Options{Type: "record-type", Target: "Account.Business_Account"})
	accessError, ok := AsAccessError(err)
	if !ok {
		t.Fatalf("got %v, want an access error", err)
	}
	expectEqual(t, "args", strings.Join(ErrorArgs(accessError), "|"), "PermissionSet|0PSGrouped")
	expectEqual(t, "cause", ErrorCause(accessError).Error(), "Permission Set source discovery was incomplete.")
}

func TestReadMetadataInBatches(t *testing.T) {
	conn := sfapitest.New()
	var batches []string
	conn.OnMetadataRead = func(_ string, names []string) ([]rec, error) {
		batches = append(batches, strings.Join(names, ","))
		components := []rec{}
		for _, name := range names {
			if name != "n3" {
				components = append(components, rec{"fullName": name})
			}
		}
		return components, nil
	}
	names := []string{"n0", "n1", "n0"}
	for i := 2; i < 12; i++ {
		names = append(names, fmt.Sprintf("n%d", i))
	}
	result, err := readMetadataInBatches(conn, "PermissionSet", names, metadataReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "batches", strings.Join(batches, " "), "n0,n1,n2,n3,n4,n5,n6,n7,n8,n9 n10,n11")
	expectEqual(t, "missing", strings.Join(result.missing, ","), "n3")
	if len(result.metadata) != 11 {
		t.Errorf("read %d components, want 11", len(result.metadata))
	}
	batches = nil
	if _, err := readMetadataInBatches(conn, "PermissionSet", []string{"a", "b", "c"}, metadataReadOptions{batchSize: 2}); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, "batches of 2", strings.Join(batches, " "), "a,b c")
	batches = nil
	empty, err := readMetadataInBatches(conn, "PermissionSet", nil, metadataReadOptions{})
	if err != nil || len(batches) != 0 || len(empty.metadata) != 0 || len(empty.missing) != 0 {
		t.Errorf("no names: %+v, %v, batches %v", empty, err, batches)
	}
	if _, err := readMetadataInBatches(conn, "PermissionSet", []string{"a"}, metadataReadOptions{batchSize: -1}); err == nil || err.Error() != "Metadata batchSize must be greater than 0." {
		t.Errorf("negative batch size: %v", err)
	}
}

func TestPartialMetadataWarningListsFiveNames(t *testing.T) {
	got := partialMetadataWarning([]string{"a", "b", "c", "d", "e", "f"})
	if !strings.HasPrefix(got, "Partial response: 6 metadata component(s) could not be read through the Metadata API (a, b, c, d, e, …). Affected") {
		t.Errorf("got %q", got)
	}
}

func TestMetadataNamesByIDKeysOn15Characters(t *testing.T) {
	conn := sfapitest.New()
	conn.OnMetadataList = func(string) ([]rec, error) {
		return []rec{
			{"id": "00e000000000001AAA", "fullName": "Admin"},
			{"id": "00e000000000002", "fullName": "Standard"},
			{"id": "", "fullName": "NoID"},
			{"id": "00e000000000003AAA"},
		}, nil
	}
	names, err := profileMetadataNamesByID(conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names["00e000000000001"] != "Admin" || names["00e000000000002"] != "Standard" {
		t.Errorf("names = %v", names)
	}
}

func TestCombinePsgMuting(t *testing.T) {
	state := psgMutingState[ObjectAccess]{
		permissionSetIDsByGroup: map[string][]string{"0PG1": {"m1", "m2", "unknown"}},
		masksByPermissionSetID:  map[string]ObjectAccess{"m1": {Read: true}, "m2": {Delete: true}},
	}
	if got := combinePsgMuting("0PG1", state, ObjectAccess{}, combineObjectAccess); got != (ObjectAccess{Read: true, Delete: true}) {
		t.Errorf("0PG1: %+v", got)
	}
	if got := combinePsgMuting("0PG2", state, ObjectAccess{}, combineObjectAccess); got != (ObjectAccess{}) {
		t.Errorf("0PG2: %+v", got)
	}
}

func TestReverseAuditsOfSetupEntitiesAndTabs(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		query   string
		csv     string
		warning string
	}{
		{
			name:    "apex class",
			options: Options{Type: "apex-class", Target: "MyController"},
			query:   "SELECT ParentId FROM SetupEntityAccess WHERE ParentId IN ('0PS1') AND SetupEntityType = 'ApexClass' AND SetupEntityId = '01p1'",
			csv:     baseHeader + ",enabled\n0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,apex-class,MyController,Account Readers,Account Readers,true",
		},
		{
			name:    "visualforce page",
			options: Options{Type: "vf-page", Target: "MyPage"},
			query:   "SELECT ParentId FROM SetupEntityAccess WHERE ParentId IN ('0PS1') AND SetupEntityType = 'ApexPage' AND SetupEntityId = '01pPage'",
			csv:     baseHeader + ",enabled\n0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,vf-page,MyPage,Account Readers,Account Readers,true",
		},
		{
			name:    "custom permission",
			options: Options{Type: "custom-permission", Target: "Can_Edit_Accounts"},
			query:   "SELECT ParentId FROM SetupEntityAccess WHERE ParentId IN ('0PS1') AND SetupEntityType = 'CustomPermission' AND SetupEntityId = '0CP1'",
			csv:     baseHeader + ",enabled\n0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,custom-permission,Can_Edit_Accounts,Account Readers,Account Readers,true",
		},
		{
			name:    "tab",
			options: Options{Type: "tab", Target: "Account"},
			query:   "SELECT ParentId, Name, Visibility FROM PermissionSetTabSetting WHERE ParentId IN ('0PS1') AND Name = 'Account' AND Visibility != 'Hidden'",
			csv:     baseHeader + ",visibility\n0051,Jane Smith,jane@example.com,PermissionSet,0PS1,Account Readers,,,tab,Account,Account Readers,Account Readers,DefaultOn",
			warning: tabProfileWarning,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rules := append([]rule{on(janeUser, "FROM User")}, baseRules()...)
			conn := withUser(newConn(answers(rules...)))
			test.options.User = "Username:jane@example.com"
			got := runAudit(t, conn, test.options, "Jane Smith")
			expectEqual(t, "csv", got.csv, test.csv)
			expectEqual(t, "grant query", conn.Queries[len(conn.Queries)-1], test.query)
			expectEqual(t, "warnings", strings.Join(got.result.Warnings, "|"), test.warning)
		})
	}
}

func TestRenderTablesRejectOtherAccessKinds(t *testing.T) {
	rows := []Row{{Access: TabAccess{Visibility: "DefaultOn"}}}
	cases := []struct {
		name   string
		render func() (string, error)
		want   string
	}{
		{"field", func() (string, error) { return RenderFieldTable(rows, false) }, "Field table received non-field access"},
		{"object", func() (string, error) { return RenderObjectTable(rows) }, "Object table received non-object access"},
		{"enabled", func() (string, error) { return RenderEnabledTable(rows) }, "Enabled table received non-enabled access"},
		{"record type", func() (string, error) { return RenderRecordTypeTable(rows) }, "Record type table received non-record-type access"},
		{"tab", func() (string, error) { return RenderTabTable([]Row{{Access: EnabledAccess{}}}) }, "Tab table received non-tab access"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.render(); err == nil || err.Error() != test.want {
				t.Errorf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestLocaleCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"Amy User", "Zed User", -1},
		{"amy", "Zed", -1},
		{"Zed", "amy", 1},
		{"a", "A", -1},
		{"A", "a", 1},
		{"a", "B", -1},
		{"Account", "Account.A__c", -1},
		{"Account.A__c", "Account.B__c", -1},
		{"0PG1", "0PG2", -1},
		{"a b", "a_b", -1},
		{"a_b", "a-b", -1},
		{"a1", "aa", -1},
		{"_x", "1x", -1},
		{"", "a", -1},
		{"same", "same", 0},
		{`{"kind":"field","read":true,"edit":false}`, `{"kind":"field","read":false,"edit":true}`, 1},
	}
	for _, test := range cases {
		if got := localeCompare(test.a, test.b); got != test.want {
			t.Errorf("localeCompare(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestFlattenAccessRow(t *testing.T) {
	isDefault := false
	row := Row{UserID: "0051", UserName: "Jane", SourceID: "0PS1", Access: RecordTypeAccess{Visible: true, Default: &isDefault}}
	flat := FlattenAccessRow(row, []string{"userId", "viaPermissionSetId", "visible", "default", "unknown"})
	if flat["userId"] != "0051" || flat["viaPermissionSetId"] != "" || flat["visible"] != true || flat["default"] != false || flat["unknown"] != "" {
		t.Errorf("flat = %v", flat)
	}
	row.Access = RecordTypeAccess{Visible: true}
	if value, present := FlattenAccessRow(row, []string{"default"})["default"]; !present || value != nil {
		t.Errorf("default of a permission set = %v", value)
	}
}
