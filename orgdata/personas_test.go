package orgdata

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func component(id, personaID, componentType, reference string) sfapi.Record {
	return sfapi.Record{"Id": id, ComponentPersonaField: personaID, ComponentTypeField: componentType, ComponentReferenceField: reference}
}

func isComponentQuery(soql string) bool {
	return strings.Contains(soql, "FROM "+PersonaComponentObject)
}

func TestReadOrgPersonasMapsActivePersonasIntoAPersonaDocument(t *testing.T) {
	conn := sfapitest.New()
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if isComponentQuery(soql) {
			return []sfapi.Record{
				component("c01", "a01", "Permission Set", "Ops_Read"),
				component("c02", "a01", "Queue", "Support_Queue"),
				component("c03", "a02", "Public Group", "Support_Group"),
			}, true, nil
		}
		return []sfapi.Record{
			{"Id": "a01", PersonaAPINameField: "ops", PersonaActiveField: true},
			{"Id": "a02", PersonaAPINameField: "support", PersonaActiveField: true},
		}, true, nil
	}

	result, err := ReadOrgPersonas(conn, true)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, result.Count, 2)
	expectEqual(t, mustJSON(t, result.Document), `{"personas":{"ops":{"permissionSets":["Ops_Read"],"permissionSetGroups":[],"publicGroups":[],"queues":["Support_Queue"]},"support":{"permissionSets":[],"permissionSetGroups":[],"publicGroups":["Support_Group"],"queues":[]}}}`)
	expectEqual(t, conn.Queries, []string{
		"SELECT Id, wdn_Api_Name__c, wdn_Active__c FROM wdn_Persona__c WHERE wdn_Active__c = true",
		"SELECT Id, wdn_Persona__c, wdn_Type__c, wdn_Reference__c FROM wdn_Persona_Component__c WHERE wdn_Persona__c IN ('a01','a02')",
	})
}

func TestReadOrgPersonasReadsInactivePersonasWhenAsked(t *testing.T) {
	conn := sfapitest.New()
	if _, err := ReadOrgPersonas(conn, false); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, conn.Queries, []string{"SELECT Id, wdn_Api_Name__c, wdn_Active__c FROM wdn_Persona__c"})
}

func TestReadOrgPersonasFollowsLaterQueryPages(t *testing.T) {
	conn := sfapitest.New()
	conn.Results = []sfapitest.QueryAnswer{
		{Contains: "FROM " + PersonaObject, Records: []sfapi.Record{{"Id": "a01", PersonaAPINameField: "ops"}}, Next: "/next"},
	}
	conn.More["/next"] = &sfapi.QueryResult{Done: true, Records: []sfapi.Record{{"Id": "a02", PersonaAPINameField: "support"}}}

	result, err := ReadOrgPersonas(conn, true)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, result.Count, 2)
}

func TestReadHeldPersonasReadsActiveAssignmentsByUser(t *testing.T) {
	conn := sfapitest.New()
	conn.Answer("FROM "+PersonaAssignmentObject, sfapi.Record{
		"Id":             "a10",
		"wdn_User__c":    "005-user",
		"wdn_Persona__r": map[string]any{PersonaAPINameField: "support"},
	})

	result, err := ReadHeldPersonas(conn, []string{"005-user"})
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, result, []HeldPersonaAssignment{{ID: "a10", UserID: "005-user", Persona: "support"}})
	expectEqual(t, mustJSON(t, result), `[{"id":"a10","userId":"005-user","persona":"support"}]`)
	expectEqual(t, conn.Queries, []string{
		"SELECT Id, wdn_User__c, wdn_Persona__c, wdn_Persona__r.wdn_Api_Name__c FROM wdn_Persona_Assignment__c WHERE wdn_User__c IN ('005-user') AND wdn_Status__c = 'Active'",
	})
}

func TestReadHeldPersonasWithoutUsersQueriesNothing(t *testing.T) {
	conn := sfapitest.New()
	result, err := ReadHeldPersonas(conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, len(result), 0)
	expectEqual(t, len(conn.Queries), 0)
}

func TestImportOrgPersonasAddsMissingComponentsAndPrunesOnlyWhenAsked(t *testing.T) {
	newConn := func() *upsertConn {
		conn := &upsertConn{Conn: sfapitest.New()}
		conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
			if isComponentQuery(soql) {
				return []sfapi.Record{component("c01", "a01", "Permission Set", "Old")}, true, nil
			}
			return []sfapi.Record{{"Id": "a01", PersonaAPINameField: "ops"}}, true, nil
		}
		return conn
	}
	document := `{"personas":{"ops":{"permissionSets":["New"]}}}`

	conn := newConn()
	result, err := ImportOrgPersonas(conn, mustDocument(t, document), false)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, mustJSON(t, result), `{"personas":1,"components":1}`)
	expectEqual(t, conn.Changes, []sfapitest.Change{{
		Operation: "create",
		Sobject:   PersonaComponentObject,
		Records: []sfapi.Record{{
			ComponentPersonaField:   "a01",
			ComponentTypeField:      "Permission Set",
			ComponentReferenceField: "New",
		}},
	}})

	conn = newConn()
	result, err = ImportOrgPersonas(conn, mustDocument(t, document), true)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, mustJSON(t, result), `{"personas":1,"components":1,"prunedComponents":1}`)
	expectEqual(t, len(conn.Changes), 2)
	expectEqual(t, conn.Changes[1], sfapitest.Change{Operation: "delete", Sobject: PersonaComponentObject, IDs: []string{"c01"}})
}

func TestImportOrgPersonasSetsTheStandardNameOfANewPersona(t *testing.T) {
	conn := &upsertConn{Conn: sfapitest.New()}
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if isComponentQuery(soql) {
			return nil, true, nil
		}
		return []sfapi.Record{{"Id": "a-new", PersonaAPINameField: "new"}}, true, nil
	}

	if _, err := ImportOrgPersonas(conn, mustDocument(t, `{"personas":{"new":{}}}`), false); err != nil {
		t.Fatal(err)
	}

	expectEqual(t, conn.upserts, [][]sfapi.Record{{
		{PersonaNameField: "new", PersonaAPINameField: "new", PersonaActiveField: true},
	}})
}

func manyPersonas(count int, definition func(name string) string) (names []string, personaRecords []sfapi.Record, document string) {
	entries := make([]string, count)
	for index := range count {
		name := fmt.Sprintf("persona-%d", index)
		names = append(names, name)
		personaRecords = append(personaRecords, sfapi.Record{"Id": fmt.Sprintf("a%d", index), PersonaAPINameField: name})
		entries[index] = fmt.Sprintf("%q:%s", name, definition(name))
	}
	return names, personaRecords, `{"personas":{` + strings.Join(entries, ",") + `}}`
}

func TestImportOrgPersonasBatchesQueriesAndCollectionWrites(t *testing.T) {
	_, personaRecords, document := manyPersonas(201, func(name string) string {
		return fmt.Sprintf(`{"permissionSets":["%s-read"]}`, name)
	})
	conn := &upsertConn{Conn: sfapitest.New()}
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if isComponentQuery(soql) {
			return nil, true, nil
		}
		return personaRecords, true, nil
	}

	result, err := ImportOrgPersonas(conn, mustDocument(t, document), false)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, mustJSON(t, result), `{"personas":201,"components":201}`)
	expectEqual(t, len(conn.upserts), 2)
	expectEqual(t, len(conn.upserts[0]), 200)
	expectEqual(t, len(conn.upserts[1]), 1)
	expectEqual(t, len(conn.Changes), 2)
	expectEqual(t, len(conn.Changes[0].Records), 200)
	expectEqual(t, len(conn.Changes[1].Records), 1)
	quoted := regexp.MustCompile(`'[^']*'`)
	var counts []int
	for _, soql := range conn.Queries {
		counts = append(counts, len(quoted.FindAllString(soql, -1)))
	}
	expectEqual(t, counts, []int{200, 1, 200, 1})
}

func TestImportOrgPersonasBatchesPrunedComponentDeletes(t *testing.T) {
	names, personaRecords, document := manyPersonas(201, func(string) string { return "{}" })
	existingComponents := make([]sfapi.Record, len(names))
	for index, name := range names {
		existingComponents[index] = component(fmt.Sprintf("c%d", index), fmt.Sprintf("a%d", index), "Permission Set", name+"-old")
	}
	componentQueries := 0
	conn := &upsertConn{Conn: sfapitest.New()}
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if !isComponentQuery(soql) {
			return personaRecords, true, nil
		}
		componentQueries++
		if componentQueries == 1 {
			return existingComponents[:200], true, nil
		}
		return existingComponents[200:], true, nil
	}

	result, err := ImportOrgPersonas(conn, mustDocument(t, document), true)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, mustJSON(t, result), `{"personas":201,"components":0,"prunedComponents":201}`)
	expectEqual(t, len(conn.Changes), 2)
	expectEqual(t, conn.Changes[0].Operation, "delete")
	expectEqual(t, len(conn.Changes[0].IDs), 200)
	expectEqual(t, len(conn.Changes[1].IDs), 1)
}

func TestImportOrgPersonasFailsWhenALaterUpsertBatchReturnsAnError(t *testing.T) {
	_, _, document := manyPersonas(201, func(string) string { return "{}" })
	conn := &upsertConn{Conn: sfapitest.New()}
	conn.onUpsert = func(call int, records []sfapi.Record) ([]sfapi.SaveResult, error) {
		if call == 2 {
			return []sfapi.SaveResult{{Success: false, Errors: []sfapi.SaveError{{Message: "second batch failed"}}}}, nil
		}
		results := make([]sfapi.SaveResult, len(records))
		for i := range results {
			results[i] = sfapi.SaveResult{Success: true, ID: fmt.Sprintf("a%d", i)}
		}
		return results, nil
	}

	_, err := ImportOrgPersonas(conn, mustDocument(t, document), false)

	if err == nil {
		t.Fatal("expected an error")
	}
	expectEqual(t, err.Error(), `Persona upsert failed: [{"message":"second batch failed"}]`)
	expectEqual(t, len(conn.upserts), 2)
	expectEqual(t, len(conn.Queries), 0)
}

func TestImportOrgPersonasWithoutUpsertUpdatesExistingAndCreatesNewPersonas(t *testing.T) {
	conn := sfapitest.New()
	personaQueries := 0
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if isComponentQuery(soql) {
			return nil, true, nil
		}
		personaQueries++
		records := []sfapi.Record{{"Id": "a01", PersonaAPINameField: "ops"}}
		if personaQueries > 1 {
			records = append(records, sfapi.Record{"Id": "a02", PersonaAPINameField: "support"})
		}
		return records, true, nil
	}

	result, err := ImportOrgPersonas(conn, mustDocument(t, `{"personas":{"ops":{},"support":{"queues":["Cases"]}}}`), false)
	if err != nil {
		t.Fatal(err)
	}

	expectEqual(t, mustJSON(t, result), `{"personas":2,"components":1}`)
	expectEqual(t, conn.Changes, []sfapitest.Change{
		{Operation: "update", Sobject: PersonaObject, Records: []sfapi.Record{
			{"Id": "a01", PersonaNameField: "ops", PersonaAPINameField: "ops", PersonaActiveField: true},
		}},
		{Operation: "create", Sobject: PersonaObject, Records: []sfapi.Record{
			{PersonaNameField: "support", PersonaAPINameField: "support", PersonaActiveField: true},
		}},
		{Operation: "create", Sobject: PersonaComponentObject, Records: []sfapi.Record{
			{ComponentPersonaField: "a02", ComponentTypeField: "Queue", ComponentReferenceField: "Cases"},
		}},
	})
}

func TestDiffPersonaDocuments(t *testing.T) {
	cases := []struct {
		name string
		file string
		org  string
		want string
	}{
		{
			name: "classifies added, removed and changed personas",
			file: `{"personas":{"ops":{"permissionSets":["New"]},"added":{}}}`,
			org:  `{"personas":{"ops":{"permissionSets":["Old"]},"removed":{}}}`,
			want: `{"added":["added"],"removed":["removed"],"changed":[{"name":"ops","before":{"permissionSets":["Old"]},"after":{"permissionSets":["New"]}}]}`,
		},
		{
			name: "treats omitted lists as empty and ignores list order",
			file: `{"personas":{"ops":{"permissionSets":["B","A"]}}}`,
			org:  `{"personas":{"ops":{"permissionSets":["A","B"],"permissionSetGroups":[],"publicGroups":[],"queues":[]}}}`,
			want: `{"added":[],"removed":[],"changed":[]}`,
		},
		{
			name: "sorts names",
			file: `{"personas":{"b":{},"a":{},"z":{"queues":["Q"]},"y":{"queues":["Q"]}}}`,
			org:  `{"personas":{"z":{},"y":{},"d":{},"c":{}}}`,
			want: `{"added":["a","b"],"removed":["c","d"],"changed":[{"name":"y","before":{},"after":{"queues":["Q"]}},{"name":"z","before":{},"after":{"queues":["Q"]}}]}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			diff := DiffPersonaDocuments(mustDocument(t, test.file), mustDocument(t, test.org))
			expectEqual(t, mustJSON(t, diff), test.want)
		})
	}
}

func TestRenderPersonaDiff(t *testing.T) {
	diff := DiffPersonaDocuments(
		mustDocument(t, `{"personas":{"ops":{"permissionSets":["New"]},"added":{}}}`),
		mustDocument(t, `{"personas":{"ops":{"permissionSets":["Old"]},"removed":{}}}`),
	)
	expectEqual(t, RenderPersonaDiff(diff), "Added: 1\nRemoved: 1\nChanged: 1\n  + added\n  - removed\n  ~ ops")
}

func TestPersonaImportRequestPlansFromAFileAndAppliesThePlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personas.json")
	if err := os.WriteFile(path, []byte(`{"personas":{"ops":{"permissionSets":["New"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := &upsertConn{Conn: sfapitest.New()}
	conn.OnQuery = func(soql string) ([]sfapi.Record, bool, error) {
		if isComponentQuery(soql) {
			return nil, true, nil
		}
		return []sfapi.Record{{"Id": "a01", PersonaAPINameField: "ops"}}, true, nil
	}

	plan, err := PlanPersonaImportRequest(conn, PersonaImportRequestInput{Input: path, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, plan.Prune, true)
	expectEqual(t, mustJSON(t, plan.Diff), `{"added":[],"removed":[],"changed":[{"name":"ops","before":{"permissionSets":[],"permissionSetGroups":[],"publicGroups":[],"queues":[]},"after":{"permissionSets":["New"]}}]}`)
	expectEqual(t, len(conn.Changes), 0)
	expectEqual(t, len(conn.upserts), 0)

	result, err := ApplyPersonaImportRequest(conn, plan)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, mustJSON(t, result), `{"personas":1,"components":1,"prunedComponents":0}`)
}

func TestReadPersonaFileRejectsADocumentWithoutPersonas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personas.json")
	if err := os.WriteFile(path, []byte(`{"users":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPersonaFile(path); err == nil {
		t.Fatal("expected an error")
	}
}
