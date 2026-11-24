package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func lifecycleOrg() *sfapitest.Conn {
	conn := sfapitest.New()
	userDescribe(conn)
	conn.Answer("FROM User WHERE Username IN", sfapi.Record{"Id": "005A", "IsActive": true, "Name": "Ada Lovelace", "Username": "ada@example.com", "Email": "ada@example.com", "ProfileId": "00eSTD", "Profile": sfapi.Record{"Name": "Standard User"}, "UserRoleId": nil})
	conn.Answer("FROM UserLogin", sfapi.Record{"Id": "0YwA", "UserId": "005A", "IsFrozen": false})
	return conn
}

type lifecycleEnvelope struct {
	Status int `json:"status"`
	Result struct {
		Summary struct{ Total, Changed, Unchanged, Failed int }
		Users   []struct {
			Key     string `json:"key"`
			ID      string `json:"id"`
			Status  string `json:"status"`
			Actions []struct {
				Key string `json:"key"`
			} `json:"actions"`
			Errors []string `json:"errors"`
		} `json:"users"`
	} `json:"result"`
	Warnings []string `json:"warnings"`
}

func decodeLifecycle(t *testing.T, text string) lifecycleEnvelope {
	t.Helper()
	var envelope lifecycleEnvelope
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("not JSON: %s", text)
	}
	return envelope
}

func changesOf(conn *sfapitest.Conn, operation, sobject string) []sfapitest.Change {
	var found []sfapitest.Change
	for _, change := range conn.Changes {
		if change.Operation == operation && change.Sobject == sobject {
			found = append(found, change)
		}
	}
	return found
}

func TestFreezeDryRunPlansWithoutWrites(t *testing.T) {
	conn := lifecycleOrg()
	useConn(t, conn)
	out, errOut, status := run("warden", "freeze", "--user", "Username:ada@example.com", "--dry-run", "--json")
	envelope := decodeLifecycle(t, out)
	if status != 0 || envelope.Result.Summary.Total != 1 || envelope.Result.Users[0].Status != "planned" || envelope.Result.Users[0].Actions[0].Key != "wouldFreeze" {
		t.Errorf("status %d, envelope %+v, stderr %s", status, envelope, errOut)
	}
	if len(conn.Changes) != 0 {
		t.Errorf("a dry run changed records: %+v", conn.Changes)
	}
}

func TestFreezeAndUnfreezeUpdateTheLogin(t *testing.T) {
	conn := lifecycleOrg()
	useConn(t, conn)
	out, errOut, status := run("warden", "freeze", "--user", "Username:ada@example.com", "--no-prompt")
	if status != 0 || !strings.Contains(out, "Processed 1 user: 1 changed, 0 unchanged, 0 failed.") {
		t.Fatalf("status %d, stdout %s, stderr %s", status, out, errOut)
	}
	updates := changesOf(conn, "update", "UserLogin")
	if len(updates) != 1 || updates[0].Records[0]["Id"] != "0YwA" || updates[0].Records[0]["IsFrozen"] != true {
		t.Errorf("updates = %+v", updates)
	}

	// The login is not frozen, so unfreeze has nothing to do.
	out, _, status = run("warden", "unfreeze", "--user", "Username:ada@example.com", "--no-prompt", "--json")
	envelope := decodeLifecycle(t, out)
	if status != 0 || envelope.Result.Summary.Unchanged != 1 || len(changesOf(conn, "update", "UserLogin")) != 1 {
		t.Errorf("unfreeze: status %d, envelope %+v", status, envelope)
	}
}

func TestFreezeAsksBeforeWritingAndStopsWhenDeclined(t *testing.T) {
	conn := lifecycleOrg()
	useConn(t, conn)
	previous := stdin
	stdin = strings.NewReader("n\n")
	t.Cleanup(func() { stdin = previous })
	_, errOut, status := run("warden", "freeze", "--user", "Username:ada@example.com")
	if status != 1 || !strings.Contains(errOut, "Continue with this operation? (y/n)") || !strings.Contains(errOut, "Operation cancelled.") || len(conn.Changes) != 0 {
		t.Errorf("status %d, stderr %s, changes %+v", status, errOut, conn.Changes)
	}
	stdin = strings.NewReader("y\n")
	if _, errOut, status := run("warden", "freeze", "--user", "Username:ada@example.com"); status != 0 || len(changesOf(conn, "update", "UserLogin")) != 1 {
		t.Errorf("confirmed: status %d, stderr %s", status, errOut)
	}
}

func TestLifecycleTargetChecks(t *testing.T) {
	useConn(t, lifecycleOrg())
	tests := []struct {
		args []string
		want string
		code int
	}{
		{[]string{"freeze"}, "Exactly one of the following must be provided: --user, --users-def", 1},
		{[]string{"freeze", "--user", "Nosuch:x"}, `Invalid user match field "Nosuch".`, 1},
		{[]string{"freeze", "--user", "novalue"}, `Invalid --user value "novalue". Expected field:value.`, 1},
		{[]string{"freeze", "--users-def", "/no/such.json"}, "No file found at /no/such.json", 1},
		{[]string{"unfreeze", "--user", "Username:x", "--input-format", "xml"}, "Expected --input-format=xml to be one of: json, csv", 2},
		{[]string{"restore"}, "Missing required flag snapshot", 1},
		{[]string{"diff", "--user", "Username:a"}, "All of the following must be provided when using --user: --against", 1},
		{[]string{"diff"}, "Exactly one of the following must be provided: --user, --users-def", 1},
	}
	for _, test := range tests {
		_, errOut, status := run(append([]string{"warden"}, test.args...)...)
		if status != test.code || !strings.Contains(errOut, test.want) {
			t.Errorf("%v: status %d, stderr %q; want %d and %q", test.args, status, errOut, test.code, test.want)
		}
	}
}

func TestUserThatMatchesNobodyFailsTheCommand(t *testing.T) {
	conn := sfapitest.New()
	userDescribe(conn)
	useConn(t, conn)
	out, _, status := run("warden", "freeze", "--user", "Username:nobody@example.com", "--no-prompt", "--json")
	envelope := decodeLifecycle(t, out)
	// The envelope reports status 0; the exit status reports the failure.
	if status != 1 || envelope.Status != 0 || envelope.Result.Summary.Failed != 1 || envelope.Result.Users[0].Errors[0] != `Username="nobody@example.com" matched no user` {
		t.Errorf("status %d, envelope %+v", status, envelope)
	}
}

func TestSnapshotThenRestoreDryRun(t *testing.T) {
	conn := lifecycleOrg()
	useConn(t, conn)
	snapshot := filepath.Join(t.TempDir(), "nested", "snapshot.json")
	out, errOut, status := run("warden", "snapshot", "--user", "Username:ada@example.com", "--out", snapshot)
	if status != 0 {
		t.Fatalf("snapshot: status %d, stdout %s, stderr %s", status, out, errOut)
	}
	content, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		SnapshotVersion int `json:"snapshotVersion"`
		Users           []struct {
			Match      string `json:"match"`
			MatchValue string `json:"matchValue"`
			UserID     string `json:"userId"`
			IsFrozen   bool   `json:"IsFrozen"`
			Profile    string `json:"profile"`
		} `json:"users"`
	}
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("snapshot is not JSON: %s", content)
	}
	if file.SnapshotVersion != 1 || len(file.Users) != 1 || file.Users[0].UserID != "005A" || file.Users[0].Match != "Username" || file.Users[0].MatchValue != "ada@example.com" || file.Users[0].Profile != "Standard User" {
		t.Errorf("snapshot = %s", content)
	}

	out, errOut, status = run("warden", "restore", "--snapshot", snapshot, "--dry-run", "--json")
	envelope := decodeLifecycle(t, out)
	if status != 0 || envelope.Result.Summary.Total != 1 || envelope.Result.Summary.Failed != 0 {
		t.Errorf("restore --dry-run: status %d, envelope %+v, stderr %s", status, envelope, errOut)
	}
	if len(conn.Changes) != 0 {
		t.Errorf("snapshot and restore --dry-run changed records: %+v", conn.Changes)
	}

	broken := writeFile(t, "broken.json", "{")
	if _, errOut, status := run("warden", "restore", "--snapshot", broken); status != 1 || !strings.Contains(errOut, "Failed to parse JSON file "+broken) {
		t.Errorf("broken snapshot: status %d, stderr %s", status, errOut)
	}
}

func TestStripDryRunListsWhatWouldChange(t *testing.T) {
	conn := lifecycleOrg()
	conn.Answer("FROM PermissionSetAssignment", sfapi.Record{"Id": "0PaA", "AssigneeId": "005A", "PermissionSetId": "0PSA", "PermissionSet": sfapi.Record{"Name": "Sales_Ops", "Label": "Sales Ops", "IsOwnedByProfile": false, "Type": "Regular"}})
	useConn(t, conn)
	out, errOut, status := run("warden", "strip", "--user", "Username:ada@example.com", "--dry-run", "--json")
	envelope := decodeLifecycle(t, out)
	if status != 0 || envelope.Result.Summary.Total != 1 || envelope.Result.Users[0].Status != "planned" {
		t.Fatalf("status %d, stdout %s, stderr %s", status, out, errOut)
	}
	var keys []string
	for _, action := range envelope.Result.Users[0].Actions {
		keys = append(keys, action.Key)
	}
	joined := strings.Join(keys, ",")
	for _, want := range []string{"wouldFreeze", "wouldDeactivate"} {
		if !strings.Contains(joined, want) {
			t.Errorf("actions %v lack %s", keys, want)
		}
	}
	if len(conn.Changes) != 0 {
		t.Errorf("a dry run changed records: %+v", conn.Changes)
	}
}

func TestDiffFlagCombinations(t *testing.T) {
	useConn(t, lifecycleOrg())
	users := writeFile(t, "users.json", `{"users":[]}`)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--user", "Username:a", "--against", "Username:b", "--external-id", "Email"}, "errorExternalIdUserMode"},
		{[]string{"--user", "Username:a", "--against", "Username:b", "--verify"}, "errorVerifyUserMode"},
		{[]string{"--users-def", users, "--verbose", "--output", "csv"}, "errorVerboseNonHuman"},
	}
	messages := loadMessages("warden.diff")
	for _, test := range tests {
		_, errOut, status := run(append([]string{"warden", "diff"}, test.args...)...)
		if status != 1 || !strings.Contains(errOut, messages.Get(test.want)) {
			t.Errorf("%v: status %d, stderr %q; want %q", test.args, status, errOut, messages.Get(test.want))
		}
	}
}
