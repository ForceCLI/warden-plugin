package orgdata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

// upsertConn is a fake connection that also records upsert calls.
type upsertConn struct {
	*sfapitest.Conn
	upserts  [][]sfapi.Record
	onUpsert func(call int, records []sfapi.Record) ([]sfapi.SaveResult, error)
}

func (c *upsertConn) Upsert(sobject string, records []sfapi.Record, externalIDField string, allOrNone bool) ([]sfapi.SaveResult, error) {
	c.upserts = append(c.upserts, records)
	if sobject != PersonaObject || externalIDField != PersonaAPINameField || allOrNone {
		return nil, fmt.Errorf("unexpected upsert of %s by %s", sobject, externalIDField)
	}
	if c.onUpsert != nil {
		return c.onUpsert(len(c.upserts), records)
	}
	results := make([]sfapi.SaveResult, len(records))
	for i := range results {
		results[i] = sfapi.SaveResult{Success: true, ID: fmt.Sprintf("a%d", i)}
	}
	return results, nil
}

// describeCountingConn counts the describe calls of a fake connection.
type describeCountingConn struct {
	*sfapitest.Conn
	described []string
}

func (c *describeCountingConn) Describe(sobject string) (*sfapi.DescribeResult, error) {
	c.described = append(c.described, sobject)
	return c.Conn.Describe(sobject)
}

func mustDocument(t *testing.T, text string) *core.Object {
	t.Helper()
	value, err := core.ParseJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value.(*core.Object)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func expectEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func collectWarnings() (*[]string, func(string)) {
	warnings := []string{}
	return &warnings, func(message string) { warnings = append(warnings, message) }
}

func nested(grantType GrantType, pairs ...string) *OrderedMap[*OrderedMap[string]] {
	inner := NewOrderedMap[string]()
	for i := 0; i+1 < len(pairs); i += 2 {
		inner.Set(pairs[i], pairs[i+1])
	}
	maps := NewOrderedMap[*OrderedMap[string]]()
	maps.Set(string(grantType), inner)
	return maps
}

func stringMap(pairs ...string) *OrderedMap[string] {
	result := NewOrderedMap[string]()
	for i := 0; i+1 < len(pairs); i += 2 {
		result.Set(pairs[i], pairs[i+1])
	}
	return result
}
