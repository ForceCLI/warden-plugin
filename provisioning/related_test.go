package provisioning

import (
	"errors"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func relatedUser(key, extra string) string {
	entry := `"FederationIdentifier":"` + key + `","ProfileId":"00exx0000000001AAA","Username":"` + key + `@example.test","LastName":"External","Alias":"extern","related":["contact"]`
	if extra != "" {
		entry += "," + extra
	}
	return userJSON(entry)
}

func contactCatalog(sobject, extraFields, extraRelationships string) string {
	recordType, linkField := "", "Id"
	if sobject == "Account" {
		recordType, linkField = `"recordType":{"developerName":"Person"},`, "PersonContactId"
	}
	return `{"relationships":{"contact":{"sobject":"` + sobject + `","phase":"before",` + recordType +
		`"match":{"field":"External_Id__c","from":"user.FederationIdentifier"},` +
		`"fields":{"LastName":{"from":"user.LastName"}` + extraFields + `},` +
		`"linkUser":{"userField":"ContactId","fromRelatedField":"` + linkField + `"}}` + extraRelationships + `}}`
}

const employeeRelationship = `"employee":{"sobject":"Employee__c","phase":"after","match":{"field":"External_Id__c","from":"user.FederationIdentifier"},"fields":{"User__c":{"from":"user.Id"}}}`

func writable(name string) sfapi.Field {
	return sfapi.Field{Name: name, Createable: true, Updateable: true}
}

func externalIDField(name string) sfapi.Field {
	field := sfapitest.Field(name, "string")
	field.ExternalID = true
	return field
}

func readable(name string) sfapi.Field {
	yes := true
	return sfapi.Field{Name: name, Readable: &yes}
}

// relatedSetup describes User with ContactId, the related sObject and
// Employee__c.
func relatedSetup(sobject string) func(conn *sfapitest.Conn) {
	return func(conn *sfapitest.Conn) {
		conn.DescribeFields("User", append(userDescribeFields(), writable("ContactId"))...)
		yes := true
		conn.Describes[strings.ToLower(sobject)] = &sfapi.DescribeResult{
			Name:            sobject,
			Fields:          []sfapi.Field{readable("Id"), externalIDField("External_Id__c"), writable("LastName"), writable("AccountId"), readable("PersonContactId")},
			RecordTypeInfos: []sfapi.RecordTypeInfo{{RecordTypeID: "012person", Available: &yes, IsPersonType: true}},
		}
		conn.DescribeFields("Employee__c", externalIDField("External_Id__c"), writable("Department__c"), writable("User__c"))
	}
}

var relatedAnswers = []answer{
	{"FROM Profile", `[{"Id":"00exx0000000001AAA","Name":"Admin","UserLicense":{"Name":"Salesforce"}}]`},
	{"FROM RecordType", `[{"Id":"012person","Name":"Person","DeveloperName":"Person","SobjectType":"Account","IsPersonType":true}]`},
}

func changeIndex(conn *sfapitest.Conn, operation, sobject string) int {
	for index, change := range conn.Changes {
		if change.Operation == operation && change.Sobject == sobject {
			return index
		}
	}
	return -1
}

func relatedJSON(t *testing.T, user PublicUserResult) string {
	return toJSON(t, user.RelatedRecords)
}

func TestProvisionRelatedRecords(t *testing.T) {
	accountLookup := `"relatedContext":{"account":{"lookup":{"sobject":"Account","field":"External_Id__c","value":{"value":"PARENT"}}}}`
	parentAccount := answer{"FROM Account", `[{"Id":"001parent","External_Id__c":"PARENT"}]`}
	describeAccount := func(conn *sfapitest.Conn) {
		relatedSetup("Contact")(conn)
		lookupField := externalIDField("External_Id__c")
		lookupField.Readable = readable("x").Readable
		conn.DescribeFields("Account", lookupField)
	}
	cases := []provisionCase{
		{
			name:       "an after relationship is written after the User save with the Id of the saved user",
			users:      usersJSON(userJSON(`"related":["employee"],"FederationIdentifier":"EMP-1","Department":"Operations","ProfileId":"00exx0000000001AAA","Username":"employee@example.test"`)),
			noPersonas: true,
			related:    `{"relationships":{"employee":{"sobject":"Employee__c","phase":"after","match":{"field":"External_Id__c","from":"user.FederationIdentifier"},"fields":{"User__c":{"from":"user.Id"},"Department__c":{"from":"user.Department"}}}}}`,
			setup:      relatedSetup("Contact"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				if changeIndex(conn, "create", "Employee__c") < changeIndex(conn, "create", "User") {
					t.Errorf("the related record was created before the user: %+v", conn.Changes)
				}
				expectEqual(t, "payload", changes(conn, "create", "Employee__c")[0].Records, []sfapi.Record{{"External_Id__c": "EMP-1", "Department__c": "Operations", "User__c": result.Users[0].ID}})
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[{"relationship":"employee","phase":"after","sobject":"Employee__c","recordId":"Employee__c-2","action":"created","status":"applied"}]`)
				expectEqual(t, "match query", queriesContaining(conn, "FROM Employee__c"), []string{"SELECT Id, External_Id__c, User__c, Department__c FROM Employee__c WHERE External_Id__c IN ('EMP-1')"})
				expectEqual(t, "preview", relatedJSON(t, plan.Preview.Users[0]), `[{"relationship":"employee","phase":"after","sobject":"Employee__c","action":"wouldCreate","status":"planned"}]`)
				expectEqual(t, "related plan", toJSON(t, plan.Plans[0].RelatedPlans), `[{"relationship":"employee","phase":"after","sobject":"Employee__c","matchField":"External_Id__c","fields":{"Department__c":"Operations","External_Id__c":"EMP-1"},"pendingUserIdFields":["User__c"],"mode":"setIfEmpty","status":"planned","errors":[],"matchValue":"EMP-1"}]`)
			},
		},
		{
			name:       "an after relationship that fails marks the saved user failed",
			users:      usersJSON(userJSON(`"related":["employee"],"FederationIdentifier":"EMP-2","ProfileId":"00exx0000000001AAA","Username":"employee-two@example.test"`)),
			noPersonas: true,
			related:    `{"relationships":{` + employeeRelationship + `}}`,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				failSaves(conn, "create", "Employee__c", failure("required related field missing"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "user creates", len(changes(conn, "create", "User")), 1)
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "id", result.Users[0].ID, "User-1")
				expectEqual(t, "errors", result.Users[0].Errors, []string{"required related field missing"})
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[{"relationship":"employee","phase":"after","sobject":"Employee__c","action":"skipped","status":"failed","error":"required related field missing"}]`)
			},
		},
		{
			name:       "a before relationship creates a Contact and links it to the User before the User save",
			users:      usersJSON(relatedUser("CONTACT-1", "")),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ""),
			answers:    relatedAnswers,
			setup:      relatedSetup("Contact"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "created")
				if changeIndex(conn, "create", "Contact") > changeIndex(conn, "create", "User") {
					t.Errorf("the user was created before the contact: %+v", conn.Changes)
				}
				expectEqual(t, "allOrNone", changes(conn, "create", "Contact")[0].AllOrNone, false)
				expectEqual(t, "contact payload", changes(conn, "create", "Contact")[0].Records, []sfapi.Record{{"LastName": "External", "External_Id__c": "CONTACT-1"}})
				expectEqual(t, "ContactId", changes(conn, "create", "User")[0].Records[0]["ContactId"], "Contact-1")
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"created","status":"applied"}]`)
				expectEqual(t, "warnings", plan.Warnings, []string{`Profile "Admin" uses license "Salesforce" for 1 users linking ContactId. Salesforce validates eligibility at save time.`})
				if _, sent := changes(conn, "create", "User")[0].Records[0]["related"]; sent {
					t.Errorf("the related key was sent to the org")
				}
			},
		},
		{
			name:       "the contact Id of a created person account is read between the Account save and the User save",
			users:      usersJSON(relatedUser("PERSON-1", "")),
			noPersonas: true,
			related:    contactCatalog("Account", "", ""),
			answers:    append([]answer{{"FROM Account WHERE Id IN", `[{"Id":"Account-1","PersonContactId":"003person"}]`}}, relatedAnswers...),
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Account")(conn)
				conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
					if strings.Contains(soql, "WHERE Id IN") && strings.Contains(soql, "PersonContactId") {
						if changeIndex(conn, "create", "Account") != 0 || len(conn.Changes) != 1 {
							return nil, false, errors.New("the link field was read at the wrong time")
						}
					}
					return nil, false, nil
				}
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "created")
				expectEqual(t, "read", queriesContaining(conn, "FROM Account WHERE Id IN"), []string{"SELECT Id, PersonContactId FROM Account WHERE Id IN ('Account-1')"})
				expectEqual(t, "ContactId", changes(conn, "create", "User")[0].Records[0]["ContactId"], "003person")
				expectEqual(t, "account payload", changes(conn, "create", "Account")[0].Records, []sfapi.Record{{"LastName": "External", "External_Id__c": "PERSON-1", "RecordTypeId": "012person"}})
				expectEqual(t, "record type query", queriesContaining(conn, "FROM RecordType"), []string{"SELECT Id, DeveloperName, SobjectType, IsActive FROM RecordType WHERE SobjectType IN ('Account','PersonAccount') AND DeveloperName IN ('Person')"})
			},
		},
		{
			name:       "context lookups of two users share one query and the lookup keys are not sent with the User",
			users:      usersJSON(relatedUser("LOOKUP-1", accountLookup), relatedUser("LOOKUP-2", accountLookup)),
			noPersonas: true,
			related:    contactCatalog("Contact", `,"AccountId":{"from":"context.account"}`, ""),
			answers:    append([]answer{parentAccount}, relatedAnswers...),
			setup:      describeAccount,
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "created", result.Summary.Created, 2)
				expectEqual(t, "account queries", queriesContaining(conn, "FROM Account"), []string{"SELECT Id, External_Id__c FROM Account WHERE External_Id__c IN ('PARENT')"})
				contacts := changes(conn, "create", "Contact")[0].Records
				expectEqual(t, "account ids", []any{contacts[0]["AccountId"], contacts[1]["AccountId"]}, []any{"001parent", "001parent"})
				for _, record := range changes(conn, "create", "User")[0].Records {
					if _, sent := record["relatedContext"]; sent {
						t.Errorf("relatedContext was sent to the org: %+v", record)
					}
				}
			},
		},
		{
			name:       "a before record that fails keeps only that user out of the User save",
			users:      usersJSON(relatedUser("OK-1", ""), relatedUser("FAIL", ""), relatedUser("OK-3", "")),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ""),
			answers:    relatedAnswers,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				failSaves(conn, "create", "Contact", sfapi.SaveResult{Success: true, ID: "003a"}, failure("Contact rejected"), sfapi.SaveResult{Success: true, ID: "003c"})
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "summary", result.Summary, Summary{Total: 3, Created: 2, Failed: 1, Warnings: 1})
				users := changes(conn, "create", "User")[0].Records
				expectEqual(t, "saved users", []any{users[0]["FederationIdentifier"], users[1]["FederationIdentifier"]}, []any{"OK-1", "OK-3"})
				expectEqual(t, "failed related", relatedJSON(t, result.Users[1]), `[{"relationship":"contact","phase":"before","sobject":"Contact","action":"skipped","status":"failed","error":"Contact rejected"}]`)
				expectEqual(t, "failed errors", result.Users[1].Errors, []string{"Contact rejected"})
			},
		},
		{
			name:       "a dry run previews before records and resolves context lookups without changes",
			users:      usersJSON(relatedUser("PREVIEW", accountLookup)),
			noPersonas: true,
			related:    contactCatalog("Contact", `,"AccountId":{"from":"context.account"}`, ""),
			dryRun:     true,
			cleanup:    true,
			answers:    append([]answer{parentAccount}, relatedAnswers...),
			setup:      describeAccount,
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "changes", len(conn.Changes), 0)
				expectEqual(t, "account queries", len(queriesContaining(conn, "FROM Account")), 1)
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[{"relationship":"contact","phase":"before","sobject":"Contact","action":"wouldCreate","status":"planned"}]`)
				expectEqual(t, "plan fields", toJSON(t, plan.Plans[0].RelatedPlans[0].Fields), `{"LastName":"External","AccountId":"001parent","External_Id__c":"PREVIEW"}`)
			},
		},
		{
			name:       "the created before record is kept when the User save fails",
			users:      usersJSON(relatedUser("ORPHAN", "")),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ""),
			answers:    relatedAnswers,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				failSaves(conn, "create", "User", failure("User rejected"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "errors", result.Users[0].Errors, []string{"User rejected"})
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"created","status":"applied"}]`)
				expectEqual(t, "deletes", len(changes(conn, "delete", "Contact")), 0)
			},
		},
		{
			name:       "cleanup deletes the created before record after the User save fails",
			users:      usersJSON(relatedUser("CLEANUP", "")),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ""),
			cleanup:    true,
			answers:    relatedAnswers,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				failSaves(conn, "create", "User", failure("User rejected"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[`+
					`{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"created","status":"applied"},`+
					`{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"deleted","status":"applied"}]`)
				deletes := changes(conn, "delete", "Contact")
				if len(deletes) != 1 || deletes[0].AllOrNone || len(deletes[0].IDs) != 1 || deletes[0].IDs[0] != "Contact-1" {
					t.Errorf("contact deletes: %+v", deletes)
				}
				expectEqual(t, "summary", result.Summary, Summary{Total: 1, Failed: 1, Warnings: 1})
				expectEqual(t, "user deletes", len(changes(conn, "delete", "User")), 0)
				expectEqual(t, "csv", RenderCsv(result), "userKey,userId,userName,username,personas,matchedBy,status,action,detail,error\n"+
					"CLEANUP,,External,CLEANUP@example.test,,,failed,created,,\n"+
					"CLEANUP,,External,CLEANUP@example.test,,,failed,activated,,\n"+
					"CLEANUP,,External,CLEANUP@example.test,,,failed,related,contact before Contact created,\n"+
					"CLEANUP,,External,CLEANUP@example.test,,,failed,related,contact before Contact deleted,\n"+
					"CLEANUP,,External,CLEANUP@example.test,,,failed,,,User rejected")
				expectContains(t, "human", RenderHuman(result, "", nil), "  related: before Contact created · Contact-1\n  related: before Contact deleted · Contact-1\n  error: User rejected\n")
			},
		},
		{
			name:       "a cleanup delete that fails is reported",
			users:      usersJSON(relatedUser("CLEANUP", "")),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ""),
			cleanup:    true,
			answers:    relatedAnswers,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				conn.OnSave = func(change sfapitest.Change) ([]sfapi.SaveResult, error) {
					switch change.Operation + " " + change.Sobject {
					case "create User":
						return []sfapi.SaveResult{failure("User rejected")}, nil
					case "delete Contact":
						return []sfapi.SaveResult{failure("Delete blocked")}, nil
					}
					return []sfapi.SaveResult{{Success: true, ID: "003created"}}, nil
				}
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "cleanup result", toJSON(t, result.Users[0].RelatedRecords[1]), `{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"003created","action":"deleteFailed","status":"failed","error":"Delete blocked"}`)
				expectContains(t, "csv", RenderCsv(result), "related,contact before Contact deleteFailed,Delete blocked")
				expectContains(t, "human", RenderHuman(result, "", nil), "  related: before Contact deleteFailed · 003created\n  related error: Delete blocked\n")
				if strings.Contains(toJSON(t, result), "createdInThisRun") {
					t.Errorf("the public result has createdInThisRun")
				}
			},
		},
		{
			name:       "cleanup keeps a Contact that a saved User references when an after relationship fails",
			users:      usersJSON(relatedUser("LINKED", `"related":["contact","employee"]`)),
			noPersonas: true,
			related:    contactCatalog("Contact", "", ","+employeeRelationship),
			cleanup:    true,
			answers:    relatedAnswers,
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				failSaves(conn, "create", "Employee__c", failure("Employee rejected"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "status", result.Users[0].Status, "failed")
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[`+
					`{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"created","status":"applied"},`+
					`{"relationship":"employee","phase":"after","sobject":"Employee__c","action":"skipped","status":"failed","error":"Employee rejected"},`+
					`{"relationship":"contact","phase":"before","sobject":"Contact","recordId":"Contact-1","action":"skipped","status":"skipped","detail":"Kept related record because the saved User references it through ContactId."}]`)
				expectEqual(t, "ContactId", changes(conn, "create", "User")[0].Records[0]["ContactId"], "Contact-1")
				for _, change := range conn.Changes {
					if change.Operation == "delete" {
						t.Errorf("a record was deleted: %+v", change)
					}
				}
			},
		},
		{
			name:       "a context name the user does not define fails only that user",
			users:      usersJSON(relatedUser("MISSING", ""), relatedUser("PRESENT", `"relatedContext":{"account":"001parent"}`)),
			noPersonas: true,
			related:    contactCatalog("Contact", `,"AccountId":{"from":"context.account"}`, ""),
			answers:    relatedAnswers,
			setup:      relatedSetup("Contact"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"failed", "created"})
				expectEqual(t, "errors", result.Users[0].Errors, []string{`Relationship "MISSING" references unknown context name "account".`})
				users := changes(conn, "create", "User")[0].Records
				expectEqual(t, "saved users", len(users), 1)
				expectEqual(t, "saved user", users[0]["FederationIdentifier"], "PRESENT")
				expectEqual(t, "contact", changes(conn, "create", "Contact")[0].Records, []sfapi.Record{{"LastName": "External", "AccountId": "001parent", "External_Id__c": "PRESENT"}})
			},
		},
		{
			name:       "relatedContext without a catalog is not used and not sent",
			users:      usersJSON(userJSON(`"FederationIdentifier":"UNUSED","ProfileId":"00exx0000000001AAA","Username":"unused@example.test","relatedContext":{"account":"001parent"}`)),
			noPersonas: true,
			setup:      relatedSetup("Contact"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "created", result.Summary.Created, 1)
				if _, sent := changes(conn, "create", "User")[0].Records[0]["relatedContext"]; sent {
					t.Errorf("relatedContext was sent to the org")
				}
			},
		},
		{
			name:       "an existing record gets only its empty fields in setIfEmpty mode and every field in sync mode",
			users:      usersJSON(userJSON(`"related":["fill","sync"],"FederationIdentifier":"EMP-9","Department":"Operations","ProfileId":"00exx0000000001AAA","Username":"employee@example.test"`)),
			noPersonas: true,
			related: `{"relationships":{` +
				`"fill":{"sobject":"Employee__c","phase":"after","match":{"field":"external_id__c","from":"user.FederationIdentifier"},"fields":{"user__c":{"from":"user.Id"},"Department__c":{"from":"user.Department"}}},` +
				`"sync":{"sobject":"Badge__c","phase":"after","mode":"sync","match":{"field":"External_Id__c","from":"user.FederationIdentifier"},"fields":{"Department__c":{"from":"user.Department"},"Level__c":{"value":3}}}}}`,
			answers: []answer{
				{"FROM Employee__c", `[{"Id":"a01emp","External_Id__c":"EMP-9","User__c":"005old","Department__c":null}]`},
				{"FROM Badge__c", `[{"Id":"a02badge","External_Id__c":"EMP-9","Department__c":"Old","Level__c":1}]`},
			},
			setup: func(conn *sfapitest.Conn) {
				relatedSetup("Contact")(conn)
				conn.DescribeFields("Badge__c", externalIDField("External_Id__c"), writable("Department__c"), writable("Level__c"))
			},
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "employee update", changes(conn, "update", "Employee__c")[0].Records, []sfapi.Record{{"Department__c": "Operations", "Id": "a01emp"}})
				expectEqual(t, "badge update", changes(conn, "update", "Badge__c")[0].Records, []sfapi.Record{{"Department__c": "Operations", "Level__c": float64(3), "Id": "a02badge"}})
				expectEqual(t, "related", relatedJSON(t, result.Users[0]), `[`+
					`{"relationship":"fill","phase":"after","sobject":"Employee__c","recordId":"a01emp","action":"updated","status":"applied"},`+
					`{"relationship":"sync","phase":"after","sobject":"Badge__c","recordId":"a02badge","action":"updated","status":"applied"}]`)
				expectEqual(t, "preview", relatedJSON(t, plan.Preview.Users[0]), `[`+
					`{"relationship":"fill","phase":"after","sobject":"Employee__c","recordId":"a01emp","action":"wouldUpdate","status":"planned"},`+
					`{"relationship":"sync","phase":"after","sobject":"Badge__c","recordId":"a02badge","action":"wouldUpdate","status":"planned"}]`)
				expectEqual(t, "creates", len(changes(conn, "create", "Employee__c")), 0)
			},
		},
		{
			name: "two users with the same match value, an ambiguous match and an ineligible relationship",
			users: usersJSON(
				userJSON(`"related":["employee"],"FederationIdentifier":"SAME","ProfileId":"00exx0000000001AAA","Username":"one@example.test"`),
				userJSON(`"related":["employee"],"FederationIdentifier":"SAME","ProfileId":"00exx0000000001AAA","Username":"two@example.test"`),
				userJSON(`"related":["employee"],"FederationIdentifier":"TWICE","ProfileId":"00exx0000000001AAA","Username":"three@example.test"`),
				userJSON(`"related":["employee"],"ProfileId":"00exx0000000001AAA","Username":"four@example.test"`),
				userJSON(`"related":["gone"],"FederationIdentifier":"FIVE","ProfileId":"00exx0000000001AAA","Username":"five@example.test"`)),
			noPersonas: true,
			related:    `{"relationships":{` + employeeRelationship + `,"gone":{"sobject":"Missing__c","phase":"after","match":{"field":"External_Id__c","from":"user.FederationIdentifier"},"fields":{"User__c":{"from":"user.Id"}}}}}`,
			dryRun:     true,
			answers:    []answer{{"FROM Employee__c", `[{"Id":"a01one","External_Id__c":"TWICE"},{"Id":"a01two","External_Id__c":"TWICE"}]`}},
			setup:      relatedSetup("Contact"),
			check: func(t *testing.T, conn *sfapitest.Conn, plan *PlanResult, result *Result) {
				expectEqual(t, "statuses", statuses(result), []string{"failed", "failed", "failed", "failed", "planned"})
				collision := `Relationship "employee" resolves to External_Id__c="SAME" for more than one user.`
				expectEqual(t, "first", result.Users[0].Errors, []string{collision})
				expectEqual(t, "second", result.Users[1].Errors, []string{collision})
				expectEqual(t, "ambiguous", result.Users[2].Errors, []string{`Relationship "employee" matched multiple Employee__c records on External_Id__c="TWICE".`})
				expectEqual(t, "empty source", result.Users[3].Errors, []string{`Relationship "employee" field "match.from" resolved to an empty value from source FederationIdentifier.`})
				expectEqual(t, "skipped", relatedJSON(t, result.Users[4]), `[{"relationship":"gone","phase":"after","sobject":"Missing__c","action":"wouldSkip","status":"skipped"}]`)
				expectEqual(t, "warnings", plan.Warnings, []string{`Relationship "gone" will be skipped: sObject Missing__c could not be described.`})
				expectEqual(t, "match query", queriesContaining(conn, "FROM Employee__c"), []string{"SELECT Id, External_Id__c, User__c FROM Employee__c WHERE External_Id__c IN ('SAME','TWICE')"})
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { runProvisionCase(t, test) })
	}
}

func TestAssertValidRelatedCatalogReportsTheFirstProblem(t *testing.T) {
	fieldMap := core.BuildFieldMap([]core.FieldMeta{{Name: "FederationIdentifier", Filterable: true}})
	relationship := func(body string) string { return `{"relationships":{"r":` + body + `}}` }
	match := `"match":{"field":"Ext__c","from":"user.FederationIdentifier"}`
	cases := []struct {
		name string
		doc  string
		kind string
		code string
		text string
	}{
		{"no relationships", `{}`, "DefinitionError", "schema-invalid", "relationships: Invalid input: expected object, received undefined"},
		{"blank sobject", relationship(`{"sobject":" ","phase":"after",` + match + `,"fields":{"A__c":{"value":1}}}`), "RelatedRecordsError", "errorRelationshipInvalidSobject", `Relationship "r" must declare a non-empty "sobject".`},
		{"linkUser on after", relationship(`{"sobject":"X__c","phase":"after",` + match + `,"fields":{"A__c":{"value":1}},"linkUser":{"userField":"ContactId","fromRelatedField":"Id"}}`), "RelatedRecordsError", "errorLinkUserUnsupported", `Relationship "r" uses "linkUser". linkUser is only supported on before relationships.`},
		{"match on user id", relationship(`{"sobject":"X__c","phase":"after","match":{"field":"Ext__c","from":"user.Id"},"fields":{"A__c":{"value":1}}}`), "RelatedRecordsError", "errorRelationshipMatchFromUserId", `Relationship "r" cannot match on "user.Id" because matching runs before the User is saved.`},
		{"match on context", relationship(`{"sobject":"X__c","phase":"after","match":{"field":"Ext__c","from":"context.account"},"fields":{"A__c":{"value":1}}}`), "RelatedRecordsError", "errorRelatedContextUnsupported", `Relationship "r" field "match.from" uses a "context." source. Context sources cannot be used in match.from.`},
		{"user id in a before field", relationship(`{"sobject":"X__c","phase":"before",` + match + `,"fields":{"A__c":{"from":"user.Id"}}}`), "RelatedRecordsError", "errorRelationshipMatchFromUserId", `Relationship "r" cannot match on "user.Id" because matching runs before the User is saved.`},
		{"unknown user field", relationship(`{"sobject":"X__c","phase":"after",` + match + `,"fields":{"A__c":{"from":"user.Nope"}}}`), "RelatedRecordsError", "errorRelationshipUnknownUserField", `Relationship "r" field "A__c" references unknown User field "Nope".`},
		{"invalid source", relationship(`{"sobject":"X__c","phase":"after",` + match + `,"fields":{"A__c":{"from":"account.Name"}}}`), "RelatedRecordsError", "errorRelationshipInvalidFrom", `Relationship "r" field "A__c" has an invalid source "account.Name". Expected "user.<Field>" or "user.Id".`},
		{"no fields", relationship(`{"sobject":"X__c","phase":"after",` + match + `,"fields":{}}`), "RelatedRecordsError", "errorRelationshipInvalidFields", `Relationship "r" must declare a non-empty "fields" object.`},
		{"person contact id", relationship(`{"sobject":"Account","phase":"after",` + match + `,"fields":{"PersonContactId":{"value":"003"}}}`), "RelatedRecordsError", "errorRelationshipUnwritableField", `Relationship "r" cannot write "PersonContactId"; the field is not writable.`},
		{"blank record type", relationship(`{"sobject":"X__c","phase":"after","recordType":{"developerName":""},` + match + `,"fields":{"A__c":{"value":1}}}`), "RelatedRecordsError", "errorRelationshipInvalidRecordType", `Relationship "r" recordType must declare a non-empty "developerName".`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := AssertValidRelatedCatalog(parseDoc(t, test.doc), fieldMap, nil)
			failure, ok := core.AsWardenError(err)
			if !ok {
				t.Fatalf("got error %v, want a WardenError", err)
			}
			expectEqual(t, "error", []string{failure.Name, failure.Code, failure.Message}, []string{test.kind, test.code, test.text})
		})
	}
}
