package core

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func userFields() *FieldMap {
	return BuildFieldMap([]FieldMeta{
		{Name: "Id", Filterable: true},
		{Name: "Username", Filterable: true, Createable: true, Updateable: true},
		{Name: "Email", Filterable: true, Createable: true, Updateable: true},
		{Name: "FederationIdentifier", Filterable: true, Createable: true, Updateable: true},
		{Name: "IsActive", Filterable: true, IsBoolean: true},
		{Name: "AboutMe"},
	})
}

func TestFormatReplacesPlaceholdersInOrder(t *testing.T) {
	if got := Format("%s reference \"%s\" was not found.", "Profile", "Admin"); got != `Profile reference "Admin" was not found.` {
		t.Errorf("Format = %q", got)
	}
	if got := Format("%s and %s", "one"); got != "one and " {
		t.Errorf("Format with a missing argument = %q", got)
	}
	if got := LifecycleMessage("noSuchCode", "x"); got != "noSuchCode" {
		t.Errorf("an unknown code = %q, want the code", got)
	}
}

func TestParseJSONKeepsKeyOrder(t *testing.T) {
	value, err := ParseJSON([]byte(`{"zeta":1,"alpha":{"b":true,"a":null},"list":[{"y":1,"x":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	object := value.(*Object)
	if !reflect.DeepEqual(object.Keys, []string{"zeta", "alpha", "list"}) {
		t.Errorf("keys = %v", object.Keys)
	}
	encoded, err := object.MarshalJSON()
	if err != nil || string(encoded) != `{"zeta":1,"alpha":{"b":true,"a":null},"list":[{"y":1,"x":2}]}` {
		t.Errorf("round trip = %s, %v", encoded, err)
	}
	if _, err := ParseJSON([]byte(`{"a":1} extra`)); err == nil {
		t.Error("content after the value was accepted")
	}
}

func TestReadCsvRows(t *testing.T) {
	rows, err := ReadCsvRows("a,b\r\n\"x,1\",\"line\nbreak\"\n\"say \"\"hi\"\"\",\n", "users.csv", ',')
	if err != nil {
		t.Fatal(err)
	}
	want := []CsvRow{
		{Cells: []string{"a", "b"}, Line: 1},
		{Cells: []string{"x,1", "line\nbreak"}, Line: 2},
		{Cells: []string{`say "hi"`, ""}, Line: 4},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v, want %+v", rows, want)
	}
	for text, detail := range map[string]string{
		"a\n\"open":     "Unterminated quoted cell.",
		"a\nb\"c":       "Unexpected quote in an unquoted cell.",
		"a\n\"b\"c,d\n": "Unexpected character after a closing quote.",
	} {
		_, err := ReadCsvRows(text, "users.csv", ',')
		if err == nil || !strings.Contains(err.Error(), detail) {
			t.Errorf("ReadCsvRows(%q) = %v, want %q", text, err, detail)
		}
	}
}

func TestReadCsvUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.csv")
	content := "username,Personas,IsActive,fuzzyUsername,Email\nada@example.com,Sales; Support ,yes,TRUE,\nbob@example.com,,0,,bob@example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadCsvUsers(path, userFields(), ";")
	if err != nil {
		t.Fatal(err)
	}
	users, _ := doc.List("users")
	if len(users) != 2 {
		t.Fatalf("users = %d", len(users))
	}
	first := users[0].(*Object)
	if !reflect.DeepEqual(first.Keys, []string{"Username", "personas", "IsActive", "fuzzyUsername"}) {
		t.Errorf("keys of the first user = %v", first.Keys)
	}
	if !reflect.DeepEqual(first.Strings("personas"), []string{"Sales", "Support"}) || first.Values["IsActive"] != true || first.Values["fuzzyUsername"] != true {
		t.Errorf("first user = %+v", first.Values)
	}
	if first.Source == nil || first.Source.Line != 2 || first.Source.Path != path {
		t.Errorf("source of the first user = %+v", first.Source)
	}
	second := users[1].(*Object)
	if second.Values["IsActive"] != false || second.Values["Email"] != "bob@example.com" {
		t.Errorf("second user = %+v", second.Values)
	}

	for content, want := range map[string]string{
		"Usernme\nx\n":           `Unknown column "Usernme"; did you mean "Username"?`,
		"Username,username\nx,y": `Duplicate column "username".`,
		"Username,Email\nx\n":    "Expected 2 cells but found 1.",
		"IsActive\nmaybe\n":      `IsActive must be a boolean; got "maybe".`,
		"":                       "CSV must contain a header row.",
	} {
		os.WriteFile(path, []byte(content), 0o644)
		_, err := ReadCsvUsers(path, userFields(), ";")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("content %q: %v, want %q", content, err, want)
		}
	}
}

func TestCsvFormulaNeutralizing(t *testing.T) {
	rows := []map[string]any{{"a": "=1+1", "b": `say "x", y`, "c": nil}, {"a": true, "b": float64(3), "c": "'=kept"}}
	got := SerializeCsv(rows, []string{"a", "b", "c"})
	want := "a,b,c\n'=1+1,\"say \"\"x\"\", y\",\ntrue,3,'=kept"
	if got != want {
		t.Errorf("SerializeCsv =\n%s\nwant\n%s", got, want)
	}
	if RestoreCsvFormula("'=1+1") != "=1+1" || RestoreCsvFormula("'plain") != "'plain" {
		t.Error("RestoreCsvFormula does not undo NeutralizeCsvFormula only")
	}
}

func TestDefinitionValidation(t *testing.T) {
	parse := func(text string) any {
		value, err := ParseJSON([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, err := ParsePersonaDefinitions(parse(`{"personas":{"Sales":{"profile":"Standard User","permissionSets":["A"],"custom":1}}}`)); err != nil {
		t.Errorf("a valid personas document: %v", err)
	}
	tests := []struct {
		name  string
		run   func() error
		code  string
		wants string
	}{
		{"personas missing", func() error { _, err := ParsePersonaDefinitions(parse(`{}`)); return err }, "schema-invalid", "personas: Invalid input: expected object, received undefined"},
		{"bad mode", func() error {
			_, err := ParsePersonaDefinitions(parse(`{"personas":{"S":{"queueMode":"replace"}}}`))
			return err
		}, "schema-invalid", `personas.S.queueMode: Invalid option: expected one of "additive"|"sync"`},
		{"newer schema", func() error { _, err := ParseUsersDefinition(parse(`{"schemaVersion":2,"users":[]}`)); return err }, "schema-version-unsupported", "schemaVersion: Unsupported schemaVersion; this reader supports version 1."},
		{"users not a list", func() error { _, err := ParseUsersDefinition(parse(`{"users":{}}`)); return err }, "schema-invalid", "users: Invalid input: expected array, received object"},
		{"personas of a user", func() error { _, err := ParseUsersDefinition(parse(`{"users":[{"personas":"Sales"}]}`)); return err }, "schema-invalid", "users.0.personas: Invalid input: expected array, received string"},
		{"relationship phase", func() error {
			_, err := ParseRelatedCatalog(parse(`{"relationships":{"contact":{"sobject":"Contact","phase":"during","match":{"field":"Email","from":"user.Email"},"fields":{}}}}`))
			return err
		}, "schema-invalid", `relationships.contact.phase: Invalid option: expected one of "before"|"after"`},
		{"snapshot version", func() error { _, err := ParseSnapshot(parse(`{"snapshotVersion":2,"users":[]}`)); return err }, "schema-invalid", "snapshotVersion: Invalid input: expected 1"},
		{"not JSON", func() error { _, err := ParseText("{", "Snapshot", ParseSnapshot); return err }, "schema-invalid", "Snapshot: $: Invalid JSON:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			warden, ok := AsWardenError(err)
			if !ok || warden.Code != test.code || !strings.Contains(warden.Message, test.wants) {
				t.Errorf("error = %v, want code %s and text %q", err, test.code, test.wants)
			}
		})
	}
}

func TestLoadValidatedDefinitionsNeedsPersonasWhenUsersListThem(t *testing.T) {
	users, _ := ParseJSON([]byte(`{"users":[{"Email":"a@example.com"},{"Username":"ada@example.com","Personas":["Sales"]}]}`))
	_, err := LoadValidatedDefinitions(DefinitionSource{UsersDoc: users.(*Object)}, userFields(), DefaultDefinitionMessages())
	warden, ok := AsWardenError(err)
	if !ok || warden.Code != "errorPersonasWithoutDefinition" || warden.Message != `User "ada@example.com" lists personas but no --personas-def was supplied.` {
		t.Errorf("error = %v", err)
	}
	_, err = ResolveDefinitions(DefinitionSource{}, userFields(), DefaultDefinitionMessages())
	if err == nil || err.Error() != "Failed to parse JSON file users-def: missing path" {
		t.Errorf("no users source: %v", err)
	}
}

func userRow(id, username string) sfapi.Record {
	return sfapi.Record{"Id": id, "IsActive": true, "Name": "Name " + id, "Username": username, "Email": username, "ProfileId": "00e1", "Profile": sfapi.Record{"Name": "Standard User"}, "UserRoleId": nil}
}

func TestResolveTargets(t *testing.T) {
	conn := sfapitest.New()
	conn.Answer("WHERE Username IN (", userRow("005A", "ADA@example.com"), userRow("005B", "dup@example.com"), userRow("005C", "dup@example.com"))
	requests := []MatchRequest{
		{Key: "Username:ada@example.com", Field: "Username", Value: "ada@example.com", Order: 0},
		{Key: "Username:dup@example.com", Field: "Username", Value: "dup@example.com", Order: 1},
		{Key: "Username:none@example.com", Field: "Username", Value: "none@example.com", Order: 2},
	}
	targets, targetErrors, err := ResolveTargets(conn, requests, userFields())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != "005A" || *targets[0].Profile != "Standard User" || targets[0].Role != nil || !targets[0].IsActive {
		t.Errorf("targets = %+v", targets)
	}
	if len(targetErrors) != 2 || targetErrors[0].Message != `Username="dup@example.com" matched multiple users` || targetErrors[1].Message != `Username="none@example.com" matched no user` {
		t.Errorf("errors = %+v", targetErrors)
	}
	want := "SELECT Id, IsActive, Name, Username, Email, ProfileId, Profile.Name, UserRoleId, UserRole.Name FROM User WHERE Username IN ('ada@example.com','dup@example.com','none@example.com')"
	if len(conn.Queries) != 1 || conn.Queries[0] != want {
		t.Errorf("queries = %q", conn.Queries)
	}

	if _, _, err := ResolveTargets(conn, []MatchRequest{{Field: "AboutMe", Value: "x"}}, userFields()); err == nil || err.Error() != `Invalid match field "AboutMe".` {
		t.Errorf("a field that cannot be filtered: %v", err)
	}
}

func TestFuzzyUsernameMatchesSandboxSuffix(t *testing.T) {
	conn := sfapitest.New()
	conn.Answer("Username LIKE", userRow("005A", "ada@example.com.uat"))
	resolution, err := ResolveExistingUsers(conn, []MatchRequest{{Field: "Username", Value: "ada@example.com", Fuzzy: true}}, userFields())
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := resolution.ExistingByField["Username"]["ada@example.com"]; !ok || user.ID != "005A" {
		t.Errorf("resolution = %+v", resolution)
	}
	want := `SELECT Id, IsActive, Name, Username, Email, ProfileId, Profile.Name, UserRoleId, UserRole.Name FROM User WHERE (Username = 'ada@example.com' OR Username LIKE 'ada@example.com.%')`
	if conn.Queries[0] != want {
		t.Errorf("query = %s", conn.Queries[0])
	}
}

func TestExtractDefTargets(t *testing.T) {
	doc, _ := ParseJSON([]byte(`{"users":[
		{"match":"username","Username":"ada@example.com","fuzzyUsername":true},
		{"Email":"bob@example.com"},
		{"match":"AboutMe","AboutMe":"x"},
		{"match":"Email"},
		"text"
	]}`))
	requests, targetErrors := ExtractDefTargets(doc.(*Object), "", userFields())
	if len(requests) != 1 || requests[0].Key != "Username:ada@example.com" || !requests[0].Fuzzy {
		t.Errorf("requests = %+v", requests)
	}
	wantMessages := []string{
		"Each users-def entry must specify a match field or the --external-id flag must be set.",
		`Invalid match field "AboutMe".`,
		"Email must be populated on the user-def entry.",
		"Each users-def entry must be an object.",
	}
	if len(targetErrors) != len(wantMessages) {
		t.Fatalf("errors = %+v", targetErrors)
	}
	for i, want := range wantMessages {
		if targetErrors[i].Message != want {
			t.Errorf("error %d = %q, want %q", i, targetErrors[i].Message, want)
		}
	}
	requests, _ = ExtractDefTargets(doc.(*Object), "Email", userFields())
	if len(requests) != 2 || requests[1].Key != "Email:bob@example.com" {
		t.Errorf("with a default external id: %+v", requests)
	}
}

func TestParseUserFlag(t *testing.T) {
	field, value, err := ParseUserFlag("Username: ada@example.com:x")
	if err != nil || field != "Username" || value != " ada@example.com:x" {
		t.Errorf("ParseUserFlag = %q, %q, %v", field, value, err)
	}
	for _, raw := range []string{"novalue", ":x", "Username:"} {
		if _, _, err := ParseUserFlag(raw); err == nil || err.Error() != `Invalid --user value "`+raw+`". Expected field:value.` {
			t.Errorf("ParseUserFlag(%q) = %v", raw, err)
		}
	}
}
