package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Object is a JSON object whose keys keep the order of the document.  The
// definition files are loose: keys this program does not know are kept and
// written back, and the order of the User fields of an entry is the order
// in which they are applied and reported.
//
// A value is nil, bool, float64, string, []any or *Object.
type Object struct {
	Keys   []string
	Values map[string]any
	// Source is the file and line of the CSV row the object was read
	// from, or nil.
	Source *RowInfo
}

// RowInfo is the place of a row of a CSV file.
type RowInfo struct {
	Path string
	Line int
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{Values: map[string]any{}}
}

// Get returns the value under key.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	value, ok := o.Values[key]
	return value, ok
}

// Set stores value under key; a new key goes after the existing ones.
func (o *Object) Set(key string, value any) {
	if _, exists := o.Values[key]; !exists {
		o.Keys = append(o.Keys, key)
	}
	o.Values[key] = value
}

// Delete removes key.
func (o *Object) Delete(key string) {
	if _, exists := o.Values[key]; !exists {
		return
	}
	delete(o.Values, key)
	kept := o.Keys[:0:0]
	for _, existing := range o.Keys {
		if existing != key {
			kept = append(kept, existing)
		}
	}
	o.Keys = kept
}

// String returns the text under key, or false when it is absent or not
// text.
func (o *Object) String(key string) (string, bool) {
	value, _ := o.Get(key)
	text, ok := value.(string)
	return text, ok
}

// Object returns the object under key, or nil.
func (o *Object) Object(key string) *Object {
	value, _ := o.Get(key)
	object, _ := value.(*Object)
	return object
}

// List returns the list under key, or false when it is absent or not a
// list.
func (o *Object) List(key string) ([]any, bool) {
	value, _ := o.Get(key)
	list, ok := value.([]any)
	return list, ok
}

// Strings returns the text items of the list under key.
func (o *Object) Strings(key string) []string {
	list, _ := o.List(key)
	var result []string
	for _, item := range list {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// GetFold returns the value under the first key that equals key without
// regard to letter case.
func (o *Object) GetFold(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	for _, existing := range o.Keys {
		if equalFold(existing, key) {
			return o.Values[existing], true
		}
	}
	return nil, false
}

// Clone returns a copy that shares nested values.
func (o *Object) Clone() *Object {
	clone := NewObject()
	if o == nil {
		return clone
	}
	for _, key := range o.Keys {
		clone.Set(key, o.Values[key])
	}
	clone.Source = o.Source
	return clone
}

// MarshalJSON writes the keys in their order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range o.Keys {
		if i > 0 {
			out.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		out.Write(encodedKey)
		out.WriteByte(':')
		encoded, err := marshalNoEscape(o.Values[key])
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func marshalNoEscape(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := NewObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("expected an object key")
				}
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Set(key, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			list := []any{}
			for decoder.More() {
				value, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected %v", typed)
	default:
		return token, nil
	}
}

// ParseJSON decodes a JSON text; objects become *Object.
func ParseJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("unexpected content after the JSON value")
	}
	return value, nil
}

// JSString is String(value) of JavaScript for a JSON value.
func JSString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []any:
		var out bytes.Buffer
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if item != nil {
				out.WriteString(JSString(item))
			}
		}
		return out.String()
	case *Object:
		return "[object Object]"
	}
	return fmt.Sprint(value)
}

// JSONTypeOf names the JSON type of a value as zod does in its messages.
func JSONTypeOf(value any, present bool) string {
	if !present {
		return "undefined"
	}
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	}
	return "object"
}
