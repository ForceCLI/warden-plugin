package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

// useConn makes the commands of a test run against a fake org.
func useConn(t *testing.T, conn sfapi.Conn) {
	t.Helper()
	previous := connect
	connect = func() (sfapi.Conn, error) { return conn, nil }
	t.Cleanup(func() { connect = previous })
}

func run(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	status := Run(args, &out, &errOut)
	return out.String(), errOut.String(), status
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func userDescribe(conn *sfapitest.Conn) {
	var fields []sfapi.Field
	for _, name := range []string{"Id", "Username", "LastName", "FirstName", "Email", "Alias", "TimeZoneSidKey", "LocaleSidKey", "EmailEncodingKey", "LanguageLocaleKey", "ProfileId", "UserRoleId", "FederationIdentifier"} {
		fields = append(fields, sfapitest.Field(name, "string"))
	}
	conn.DescribeFields("User", fields...)
}

func TestManifestListsEveryCommandWithAnOrg(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, command := range manifest.Commands {
		ids = append(ids, command.ID)
		if command.Org != "required" || command.Summary == "" {
			t.Errorf("command %+v", command)
		}
	}
	for _, want := range []string{"warden access", "warden persona diff", "warden persona export", "warden persona import", "warden provision"} {
		if !strings.Contains(strings.Join(ids, ","), want) {
			t.Errorf("the manifest lacks %q: %v", want, ids)
		}
	}
}

func TestHelpShowsSummaryFlagsAndExamples(t *testing.T) {
	out, _, status := run("warden", "access", "--help")
	for _, want := range []string{"Audit active-user access for a permission target.", "--type string", "sf warden access --type field --target Account.CustomField__c --target-org myOrg"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if status != 0 {
		t.Errorf("status = %d", status)
	}
}

func TestUnknownCommandAndFlag(t *testing.T) {
	if _, errOut, status := run("warden", "nosuch"); status != 2 || !strings.Contains(errOut, "is not a command of the warden plugin") {
		t.Errorf("unknown command: status %d, stderr %s", status, errOut)
	}
	if _, errOut, status := run("warden", "access", "--nosuch"); status != 2 || !strings.Contains(errOut, "Nonexistent flag: --nosuch") {
		t.Errorf("unknown flag: status %d, stderr %s", status, errOut)
	}
}

func TestCommandWithoutAnOrgNamesTheFlag(t *testing.T) {
	for _, name := range []string{"SF_PLUGIN_INSTANCE_URL", "SF_PLUGIN_ACCESS_TOKEN"} {
		t.Setenv(name, "")
	}
	out, _, status := run("warden", "access", "--type", "object", "--target", "Account", "--json")
	var envelope struct {
		Name     string `json:"name"`
		Message  string `json:"message"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	if status != 1 || envelope.Name != "SfError" || envelope.Message != "No default environment found. Use -o or --target-org to specify an environment." {
		t.Errorf("status %d, envelope %+v", status, envelope)
	}
}

func TestAccessFlagChecks(t *testing.T) {
	useConn(t, sfapitest.New())
	tests := []struct {
		args []string
		want string
		code int
	}{
		{[]string{"--target", "Account"}, "Missing required flag type", 1},
		{[]string{"--type", "object"}, "Target-audit mode requires --target.", 1},
		{[]string{"--type", "object", "--sobject", "Account"}, "--sobject is only supported with --user reverse-audit mode.", 1},
		{[]string{"--type", "object", "--target", "Account", "--sobject", "Account", "--user", "Username:a"}, "Use only one reverse-audit scope: --target or --sobject.", 1},
		{[]string{"--type", "object", "--user", "Username:a"}, "Reverse user mode requires exactly one scope: --target or --sobject.", 1},
		{[]string{"--type", "widget", "--target", "x"}, "Expected --type=widget to be one of: field, object, apex-class, vf-page, custom-permission, tab, record-type", 2},
		{[]string{"--type", "object", "--target", "Account", "--output", "csv", "--json"}, "`--output` and `--json` both write to stdout.", 1},
		{[]string{"--type", "object", "--target", "Account", "--interactive"}, "Interactive mode is not available in this port of warden.", 1},
	}
	for _, test := range tests {
		_, errOut, status := run(append([]string{"warden", "access"}, test.args...)...)
		combined := errOut
		if strings.Contains(strings.Join(test.args, " "), "--json") {
			out, _, _ := run(append([]string{"warden", "access"}, test.args...)...)
			combined = out
		}
		if status != test.code || !strings.Contains(combined, test.want) {
			t.Errorf("%v: status %d, output %q; want %d and %q", test.args, status, combined, test.code, test.want)
		}
	}
}

func TestAccessReportsATargetThatDoesNotExist(t *testing.T) {
	conn := sfapitest.New()
	useConn(t, conn)
	_, errOut, status := run("warden", "access", "--type", "apex-class", "--target", "Missing")
	if status != 1 || !strings.Contains(errOut, "Error (UserAccessError): Apex class not found: Missing.") {
		t.Errorf("status %d, stderr %s", status, errOut)
	}
}

func personaOrg() *sfapitest.Conn {
	conn := sfapitest.New()
	conn.DescribeFields("wdn_Persona__c", sfapitest.Field("wdn_Api_Name__c", "string"))
	conn.Answer("FROM wdn_Persona__c", sfapi.Record{"Id": "a01A", "wdn_Api_Name__c": "Sales", "wdn_Active__c": true})
	conn.Answer("FROM wdn_Persona_Component__c",
		sfapi.Record{"wdn_Persona__c": "a01A", "wdn_Type__c": "Permission Set", "wdn_Reference__c": "Sales_Ops"},
	)
	return conn
}

func TestPersonaExportWritesTheOrgPersonas(t *testing.T) {
	useConn(t, personaOrg())
	output := filepath.Join(t.TempDir(), "personas.json")
	out, errOut, status := run("warden", "persona", "export", "--output", output)
	if status != 0 || out != "Exported 1 personas to "+output+"\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, out, errOut)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Personas map[string]struct {
			PermissionSets []string `json:"permissionSets"`
		} `json:"personas"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("exported file is not JSON: %s", content)
	}
	sales := document.Personas["Sales"]
	if len(sales.PermissionSets) != 1 || sales.PermissionSets[0] != "Sales_Ops" || !strings.HasSuffix(string(content), "}\n") {
		t.Errorf("exported file = %s", content)
	}

	if _, errOut, status := run("warden", "persona", "export"); status != 2 || !strings.Contains(errOut, "Missing required flag output") {
		t.Errorf("without --output: status %d, stderr %s", status, errOut)
	}
}

func TestPersonaDiffAndImportDryRun(t *testing.T) {
	useConn(t, personaOrg())
	input := writeFile(t, "personas.json", `{"personas":{"Sales":{"permissionSets":["Sales_Ops"]},"Support":{"permissionSets":["Support_Ops"]}}}`)
	out, _, status := run("warden", "persona", "diff", "--input", input)
	if status != 0 || !strings.HasPrefix(out, "Added: 1\nRemoved: 0\nChanged: 0\n") || !strings.Contains(out, "Support") {
		t.Errorf("diff: status %d, stdout %q", status, out)
	}
	out, _, status = run("warden", "persona", "import", "--input", input, "--dry-run", "--json")
	var envelope struct {
		Status int `json:"status"`
		Result struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || status != 0 || len(envelope.Result.Added) != 1 || envelope.Result.Added[0] != "Support" || envelope.Result.Removed == nil {
		t.Errorf("import --dry-run --json: status %d, stdout %s, err %v", status, out, err)
	}
	if _, errOut, status := run("warden", "persona", "diff", "--input", "/no/such/file.json"); status != 1 || !strings.Contains(errOut, "No file found at /no/such/file.json") {
		t.Errorf("missing input: status %d, stderr %s", status, errOut)
	}
}

func provisionOrg() *sfapitest.Conn {
	conn := sfapitest.New()
	userDescribe(conn)
	conn.Answer("FROM Profile", sfapi.Record{"Id": "00eSTD", "Name": "Standard User", "UserLicenseId": "100A"})
	return conn
}

const newUser = `{"users":[{"match":"Username","Username":"ada@example.com","LastName":"Lovelace","Email":"ada@example.com","Alias":"ada","TimeZoneSidKey":"Europe/London","LocaleSidKey":"en_GB","EmailEncodingKey":"UTF-8","LanguageLocaleKey":"en_US","personas":["Sales"]}]}`

func TestProvisionDryRunPlansWithoutWrites(t *testing.T) {
	conn := provisionOrg()
	useConn(t, conn)
	users := writeFile(t, "users.json", newUser)
	personas := writeFile(t, "personas.json", `{"personas":{"Sales":{"profile":"Standard User"}}}`)
	out, errOut, status := run("warden", "provision", "--users-def", users, "--personas-def", personas, "--dry-run", "--json")
	if status != 0 {
		t.Fatalf("status %d, stdout %s, stderr %s", status, out, errOut)
	}
	var envelope struct {
		Result struct {
			Summary struct{ Total, Created, Updated, Failed int }
			Users   []struct {
				Key     string   `json:"key"`
				Status  string   `json:"status"`
				Matched bool     `json:"matched"`
				Errors  []string `json:"errors"`
			} `json:"users"`
			PersonaSource string `json:"personaSource"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	result := envelope.Result
	if result.Summary.Total != 1 || len(result.Users) != 1 || result.Users[0].Status != "planned" || result.Users[0].Matched || len(result.Users[0].Errors) != 0 {
		t.Errorf("result = %+v", result)
	}
	if result.PersonaSource != "file ("+personas+") [default]" {
		t.Errorf("personaSource = %q", result.PersonaSource)
	}
	if len(conn.Changes) != 0 {
		t.Errorf("a dry run changed records: %+v", conn.Changes)
	}
}

func TestProvisionCreatesTheUser(t *testing.T) {
	conn := provisionOrg()
	useConn(t, conn)
	users := writeFile(t, "users.json", newUser)
	personas := writeFile(t, "personas.json", `{"personas":{"Sales":{"profile":"Standard User"}}}`)
	out, errOut, status := run("warden", "provision", "--users-def", users, "--personas-def", personas, "--no-prompt")
	if status != 0 {
		t.Fatalf("status %d, stdout %s, stderr %s", status, out, errOut)
	}
	created := false
	for _, change := range conn.Changes {
		if change.Operation == "create" && change.Sobject == "User" {
			created = true
			record := change.Records[0]
			if record["Username"] != "ada@example.com" || record["ProfileId"] != "00eSTD" {
				t.Errorf("created user = %+v", record)
			}
		}
	}
	if !created {
		t.Errorf("no User was created: %+v", conn.Changes)
	}
	if !strings.Contains(out, "1 created") && !strings.Contains(out, "created") {
		t.Errorf("stdout = %s", out)
	}
}

func TestProvisionInputChecks(t *testing.T) {
	useConn(t, provisionOrg())
	users := writeFile(t, "users.json", newUser)
	csvUsers := writeFile(t, "users.csv", "Username\nada@example.com\n")
	related := writeFile(t, "related.json", `{"relationships":{}}`)
	badPersonas := writeFile(t, "personas.json", `{"people":{}}`)
	tests := []struct {
		args []string
		want string
	}{
		{nil, "Missing required flag users-def"},
		{[]string{"--users-def", users}, `User "ada@example.com" lists personas but no --personas-def was supplied.`},
		{[]string{"--users-def", csvUsers, "--related-def", related}, "--related-def requires a JSON --users-def."},
		{[]string{"--users-def", users, "--personas-def", badPersonas}, "persona-def.json must contain a personas object."},
		{[]string{"--users-def", "/no/such/users.json"}, "No file found at /no/such/users.json"},
	}
	for _, test := range tests {
		_, errOut, status := run(append([]string{"warden", "provision", "--dry-run", "--persona-source", "file"}, test.args...)...)
		if status != 1 || !strings.Contains(errOut, test.want) {
			t.Errorf("%v: status %d, stderr %q; want %q", test.args, status, errOut, test.want)
		}
	}
}
