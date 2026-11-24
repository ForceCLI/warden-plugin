package provisioning

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
)

func readFixture(t *testing.T, path string) any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input, err := core.ParseJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func fixturePaths(t *testing.T, pattern string, want int) []string {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != want {
		t.Fatalf("found %d fixtures for %s, want %d", len(paths), pattern, want)
	}
	return paths
}

func TestPlanFromStateMatchesEveryConformanceFixture(t *testing.T) {
	for _, path := range fixturePaths(t, "testdata/conformance/*.json", 8) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fixture, err := ParseConformanceFixture(readFixture(t, path))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := PlanFromState(fixture.Definitions, fixture.OrgState)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rows, fixture.ExpectedPlan) {
				t.Errorf("planned rows differ\n got: %+v\nwant: %+v", rows, fixture.ExpectedPlan)
			}
		})
	}
}

func TestPlanRevocationFromStateMatchesEveryRevocationFixture(t *testing.T) {
	for _, path := range fixturePaths(t, "testdata/conformance/revocation/*.json", 7) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fixture, err := ParseRevocationFixture(readFixture(t, path))
			if err != nil {
				t.Fatal(err)
			}
			rows := PlanRevocationFromState(fixture)
			if !reflect.DeepEqual(rows, fixture.ExpectedPlan) {
				t.Errorf("planned rows differ\n got: %+v\nwant: %+v", rows, fixture.ExpectedPlan)
			}
		})
	}
}

func TestParseConformanceFixtureReportsSchemaProblems(t *testing.T) {
	cases := []struct {
		name string
		json string
		code string
		text string
	}{
		{"unsupported version", `{"schemaVersion":2}`, "schema-version-unsupported", "schemaVersion: Unsupported schemaVersion; this reader supports version 1."},
		{"missing version", `{"definitions":{"personas":{"personas":{}},"users":{"users":[]}},"orgState":{"permissionSets":[],"permissionSetGroups":[],"groups":[],"permissionSetAssignments":[],"groupMembers":[]},"expectedPlan":[]}`, "schema-invalid", "schemaVersion: Invalid input: expected 1"},
		{"user without key", `{"schemaVersion":1,"definitions":{"personas":{"personas":{}},"users":{"users":[{"userId":"005"}]}},"orgState":{"permissionSets":[],"permissionSetGroups":[],"groups":[],"permissionSetAssignments":[],"groupMembers":[]},"expectedPlan":[]}`, "schema-invalid", "definitions.users.users.0.userKey: Invalid input: expected string, received undefined"},
		{"group without type", `{"schemaVersion":1,"definitions":{"personas":{"personas":{}},"users":{"users":[]}},"orgState":{"permissionSets":[],"permissionSetGroups":[],"groups":[{"id":"00G1"}],"permissionSetAssignments":[],"groupMembers":[]},"expectedPlan":[]}`, "schema-invalid", `orgState.groups.0.type: Invalid option: expected one of "Regular"|"Queue"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input, err := core.ParseJSON([]byte(test.json))
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseConformanceFixture(input)
			failure, ok := core.AsWardenError(err)
			if !ok {
				t.Fatalf("got error %v, want a WardenError", err)
			}
			if failure.Code != test.code || failure.Message != test.text {
				t.Errorf("got %s %q, want %s %q", failure.Code, failure.Message, test.code, test.text)
			}
		})
	}
}
