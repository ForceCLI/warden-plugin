package provisioning

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// truthy reports whether a JSON value is truthy in JavaScript.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0 && typed == typed
	case int:
		return typed != 0
	}
	return true
}

// strictEqual is the === of JavaScript for JSON values: lists and objects
// are equal only when they are the same value in memory.
func strictEqual(left, right any) bool {
	switch a := left.(type) {
	case nil:
		return right == nil
	case bool:
		b, ok := right.(bool)
		return ok && a == b
	case string:
		b, ok := right.(string)
		return ok && a == b
	case float64:
		b, ok := right.(float64)
		return ok && a == b
	case *core.Object:
		b, ok := right.(*core.Object)
		return ok && a == b
	case []any:
		b, ok := right.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		return len(a) == 0 || &a[0] == &b[0]
	}
	return false
}

// jsonStringify writes a value as JSON text without HTML escaping.
func jsonStringify(value any) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return core.JSString(value)
	}
	return string(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
}

// jsLength is the length of a text in UTF-16 code units.
func jsLength(text string) int {
	return len(utf16.Encode([]rune(text)))
}

// compareText orders texts by UTF-16 code units, as the < of JavaScript.
func compareText(left, right string) int {
	if left == right {
		return 0
	}
	a, b := utf16.Encode([]rune(left)), utf16.Encode([]rune(right))
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

// text returns the string under key of a record, or "".
func text(row sfapi.Record, key string) string {
	value, _ := row[key].(string)
	return value
}

// nested returns the record under key of a record, or nil.
func nested(row sfapi.Record, key string) sfapi.Record {
	value, _ := row[key].(map[string]any)
	return value
}

// toRecord copies an ordered object into a record for a save request.
func toRecord(object *core.Object) sfapi.Record {
	record := sfapi.Record{}
	if object == nil {
		return record
	}
	for _, key := range object.Keys {
		record[key] = object.Values[key]
	}
	return record
}

func appendUnique(list []string, seen map[string]bool, value string) []string {
	if seen[value] {
		return list
	}
	seen[value] = true
	return append(list, value)
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		result = appendUnique(result, seen, value)
	}
	return result
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func saveErrorTexts(saveErrors []sfapi.SaveError) []string {
	texts := make([]string, 0, len(saveErrors))
	for _, saveError := range saveErrors {
		texts = append(texts, core.FormatSaveError(saveError))
	}
	return texts
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func joinPlus(values []string) string {
	return strings.Join(values, "+")
}
