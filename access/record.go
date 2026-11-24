package access

import (
	"errors"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// orderedMap is a map that keeps its keys in the order they were first
// set, as a JavaScript Map does.
type orderedMap[V any] struct {
	keys   []string
	values map[string]V
}

func newOrderedMap[V any]() *orderedMap[V] {
	return &orderedMap[V]{values: map[string]V{}}
}

func (m *orderedMap[V]) set(key string, value V) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *orderedMap[V]) get(key string) (V, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *orderedMap[V]) has(key string) bool {
	_, ok := m.values[key]
	return ok
}

func (m *orderedMap[V]) len() int {
	return len(m.keys)
}

// unique returns the distinct values in the order they first appear.
func unique(values []string) []string {
	seen := map[string]bool{}
	distinct := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			distinct = append(distinct, value)
		}
	}
	return distinct
}

// field follows relationship names from a record, as a?.b?.c does.  ok is
// false when any step is missing or null.
func field(record sfapi.Record, path ...string) (any, bool) {
	var current any = record
	for _, key := range path {
		object, isRecord := current.(sfapi.Record)
		if !isRecord {
			return nil, false
		}
		value, present := object[key]
		if !present || value == nil {
			return nil, false
		}
		current = value
	}
	return current, true
}

// related returns the record of a relationship, or nil.
func related(record sfapi.Record, path ...string) sfapi.Record {
	value, _ := field(record, path...)
	object, _ := value.(sfapi.Record)
	return object
}

// maybe is a text that may be missing, for chains of the ?? operator.
type maybe struct {
	value string
	ok    bool
}

func opt(record sfapi.Record, path ...string) maybe {
	value, ok := field(record, path...)
	if !ok {
		return maybe{}
	}
	if text, isString := value.(string); isString {
		return maybe{text, true}
	}
	return maybe{core.JSString(value), true}
}

func lit(value string) maybe {
	return maybe{value, true}
}

func fromMap(values map[string]string, key string) maybe {
	value, ok := values[key]
	return maybe{value, ok}
}

// coalesce returns the first text that is present, or "".
func coalesce(values ...maybe) string {
	for _, value := range values {
		if value.ok {
			return value.value
		}
	}
	return ""
}

// text returns a value as text, or "" when it is missing or null.
func text(record sfapi.Record, path ...string) string {
	return opt(record, path...).value
}

// isTrue reports whether a value is the boolean true.
func isTrue(record sfapi.Record, path ...string) bool {
	value, _ := field(record, path...)
	flag, _ := value.(bool)
	return flag
}

// isFalse reports whether a value is the boolean false.
func isFalse(record sfapi.Record, path ...string) bool {
	value, ok := field(record, path...)
	flag, isBool := value.(bool)
	return ok && isBool && !flag
}

// idPrefix returns the first 15 characters of an id field.  A record
// without the field is an error, as reading substring of undefined is in
// JavaScript.
func idPrefix(record sfapi.Record, key string) (string, error) {
	value, ok := field(record, key)
	id, isString := value.(string)
	if !ok || !isString {
		return "", errors.New("Cannot read properties of undefined (reading 'substring')")
	}
	return prefix15(id), nil
}

func prefix15(id string) string {
	if len(id) > 15 {
		return id[:15]
	}
	return id
}

// withID copies a record and sets its Id.
func withID(record sfapi.Record, id string) sfapi.Record {
	copied := make(sfapi.Record, len(record)+1)
	for key, value := range record {
		copied[key] = value
	}
	copied["Id"] = id
	return copied
}
