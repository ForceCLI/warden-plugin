package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

func TestRenderMessagesFor(t *testing.T) {
	tests := []struct {
		command string
		key     string
		args    []string
		want    string
	}{
		{"freeze", "info.summary", []string{"1", "", "1", "0", "0"}, "Processed 1 user: 1 changed, 0 unchanged, 0 failed."},
		{"strip", "info.summary", []string{"2", "s", "1", "0", "1"}, "Processed 2 users: 1 changed, 0 unchanged, 1 failed."},
		{"diff", "info.summary", []string{"3", "2", "1"}, "Compared 3 users: 2 with drift, 1 failed."},
		{"provision", "info.summary", []string{"4", "1", "2", "1"}, "Processed 4 users: 1 created, 2 updated, 1 failed."},
		{"diff", "frozen", nil, "Froze."},
		{"restore", "assignedPermissionSet", []string{"2"}, "Assigned 2 permission sets."},
		{"restore", "keptByOtherPersona", nil, "Kept ."},
		{"restore", "noSuchKey", []string{"ignored"}, "noSuchKey"},
	}
	for _, test := range tests {
		if got := RenderMessagesFor(test.command)(test.key, test.args...); got != test.want {
			t.Errorf("RenderMessagesFor(%q)(%q, %v) = %q, want %q", test.command, test.key, test.args, got, test.want)
		}
	}
}

func TestMakeNoticeSortsItemsAndLeavesOutAbsentParts(t *testing.T) {
	notice := MakeNotice("removedPermissionSet", Count(3), []LabelBundle{
		{ID: "0PS3", Label: "zeta"},
		{ID: "0PS1", APIName: "Beta_Perms"},
		{ID: "0PS2", APIName: "alpha_perms"},
	}, nil)
	var names []string
	for _, item := range notice.Items {
		names = append(names, itemName(item))
	}
	if want := []string{"alpha_perms", "Beta_Perms", "zeta"}; !reflect.DeepEqual(names, want) {
		t.Errorf("item order = %v, want %v", names, want)
	}
	tests := []struct {
		notice Notice
		want   string
	}{
		{MakeNotice("frozen", nil, nil, nil), `{"key":"frozen"}`},
		{MakeNotice("skippedQueues", Count(0), []LabelBundle{}, nil), `{"key":"skippedQueues","count":0}`},
		{MakeNotice("alreadyAbsent", nil, nil, stringPointer("Sales <A&B>")), `{"key":"alreadyAbsent","detail":"Sales <A&B>"}`},
	}
	for _, test := range tests {
		encoded, err := encodeJSON(test.notice)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Errorf("notice JSON = %s, want %s", encoded, test.want)
		}
	}
}

func TestMarkSuccessKeepsFailedStatus(t *testing.T) {
	result := FailedResult(core.TargetError{Key: "Username:a", Message: "failed earlier"})
	MarkSuccess(result, "frozen", nil, nil)
	if result.Status != "failed" || len(result.Actions) != 1 || result.Actions[0].Key != "frozen" {
		t.Errorf("result = %+v, want status failed with the action frozen", result)
	}
}

func TestFreezeDirections(t *testing.T) {
	tests := []struct {
		direction FreezeDirection
		row       sfapi.Record
		want      bool
	}{
		{FREEZE, sfapi.Record{"IsFrozen": true}, true},
		{FREEZE, sfapi.Record{"IsFrozen": false}, false},
		{FREEZE, sfapi.Record{}, false},
		{UNFREEZE, sfapi.Record{"IsFrozen": true}, false},
		{UNFREEZE, sfapi.Record{"IsFrozen": false}, true},
		{UNFREEZE, sfapi.Record{}, true},
	}
	for _, test := range tests {
		if got := test.direction.IsAlreadyInState(test.row); got != test.want {
			t.Errorf("%s IsAlreadyInState(%v) = %v, want %v", test.direction.ActionKey, test.row, got, test.want)
		}
	}
}

func TestValidateStripOptions(t *testing.T) {
	if err := ValidateStripOptions(StripOptions{AllPersonas: true, Personas: []string{"support"}}); err == nil || err.Error() != "personas and allPersonas are mutually exclusive." {
		t.Errorf("error = %v", err)
	}
	for _, options := range []StripOptions{{AllPersonas: true}, {Personas: []string{"support"}}, {}} {
		if err := ValidateStripOptions(options); err != nil {
			t.Errorf("ValidateStripOptions(%+v) = %v, want nil", options, err)
		}
	}
}

func TestReadSnapshotFileErrors(t *testing.T) {
	directory := t.TempDir()
	notJSON := filepath.Join(directory, "broken.json")
	if err := os.WriteFile(notJSON, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSnapshotFile(notJSON)
	wardenError, ok := core.AsWardenError(err)
	if !ok || wardenError.Name != "LifecycleError" || wardenError.Code != "errorInvalidSnapshot" || wardenError.Data["path"] != notJSON {
		t.Errorf("error for text that is not JSON = %#v, want a LifecycleError errorInvalidSnapshot with the path", err)
	}
	_, err = ReadSnapshotFile(filepath.Join(directory, "absent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error for an absent file = %v, want os.ErrNotExist", err)
	}
	_, err = AssertSnapshotFile([]any{})
	wardenError, ok = core.AsWardenError(err)
	if !ok || wardenError.Code != "errorInvalidSnapshot" {
		t.Errorf("error for a document that is not an object = %#v, want errorInvalidSnapshot", err)
	}
	if _, hasPath := wardenError.Data["path"]; hasPath {
		t.Errorf("AssertSnapshotFile error data has a path: %v", wardenError.Data)
	}
}

func TestSnapshotCsvKeepsLeadingApostrophesAndFormulaPrefixes(t *testing.T) {
	entry := core.NewObject()
	entry.Set("match", "Username")
	entry.Set("matchValue", "a@example.com")
	entry.Set("userId", "005a")
	entry.Set("name", "'Apostrophe")
	entry.Set("IsActive", true)
	entry.Set("IsFrozen", false)
	entry.Set("permissionSets", []any{"=Formula", "'=Quoted_Formula", "-Dash"})
	for _, key := range []string{"permissionSetGroups", "publicGroups", "queues", "permissionSetLicenses"} {
		entry.Set(key, []any{})
	}
	snapshot := core.NewObject()
	snapshot.Set("snapshotVersion", float64(1))
	snapshot.Set("users", []any{entry})
	read, err := DeserializeSnapshotCsv(SerializeSnapshotCsv(snapshot), "")
	if err != nil {
		t.Fatal(err)
	}
	user := snapshotEntries(read)[0]
	if got := stringOr(user.name, ""); got != "'Apostrophe" {
		t.Errorf("name = %q, want %q", got, "'Apostrophe")
	}
	if want := []string{"'=Quoted_Formula", "-Dash", "=Formula"}; !reflect.DeepEqual(user.lists["permissionSets"], want) {
		t.Errorf("permissionSets = %q, want %q", user.lists["permissionSets"], want)
	}
}

func TestQueryErrorStopsPlanning(t *testing.T) {
	failure := errors.New("request failed")
	tests := []struct {
		name string
		run  func(conn sfapi.Conn) error
	}{
		{"freeze", func(conn sfapi.Conn) error {
			_, err := PlanFreeze(conn, TargetOptions{User: "Username:a@example.com"})
			return err
		}},
		{"strip", func(conn sfapi.Conn) error {
			_, err := PlanStrip(conn, StripOptions{TargetOptions: TargetOptions{User: "Username:a@example.com"}})
			return err
		}},
		{"snapshot", func(conn sfapi.Conn) error {
			_, err := RunSnapshot(conn, SnapshotOptions{TargetOptions: TargetOptions{User: "Username:a@example.com"}})
			return err
		}},
		{"diff", func(conn sfapi.Conn) error {
			_, err := RunDiff(conn, DiffOptions{Mode: "user", User: "Username:a@example.com", Against: "Username:b@example.com"})
			return err
		}},
	}
	for _, test := range tests {
		conn := sfapitest.New()
		conn.DescribeFields("User", defaultUserFields...)
		conn.OnQuery = func(string) ([]sfapi.Record, bool, error) { return nil, false, failure }
		if err := test.run(conn); !errors.Is(err, failure) {
			t.Errorf("%s: error = %v, want the query error", test.name, err)
		}
	}
}

func TestSaveErrorStopsApply(t *testing.T) {
	failure := errors.New("request failed")
	conn := sfapitest.New()
	conn.DescribeFields("User", defaultUserFields...)
	conn.Answer("FROM User WHERE", sfapi.Record{"Id": "005a", "IsActive": true, "Username": "a@example.com"})
	conn.Answer("FROM UserLogin", sfapi.Record{"Id": "0LLa", "UserId": "005a", "IsFrozen": false})
	conn.OnSave = func(sfapitest.Change) ([]sfapi.SaveResult, error) { return nil, failure }
	freezePlan, err := PlanFreeze(conn, TargetOptions{User: "Username:a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFreeze(conn, freezePlan); !errors.Is(err, failure) {
		t.Errorf("ApplyFreeze error = %v, want the save error", err)
	}
	if freezePlan.Users[0].Status != "planned" {
		t.Errorf("ApplyFreeze changed the plan: status %q", freezePlan.Users[0].Status)
	}
	stripPlan, err := PlanStrip(conn, StripOptions{TargetOptions: TargetOptions{User: "Username:a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStrip(conn, stripPlan); !errors.Is(err, failure) {
		t.Errorf("ApplyStrip error = %v, want the save error", err)
	}
}
