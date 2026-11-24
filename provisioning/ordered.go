package provisioning

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OrderedMap is a map from text keys that keeps the order in which keys
// were first set.  It is written to JSON as an object in that order.
type OrderedMap[V any] struct {
	keys   []string
	values map[string]V
}

// RefMap maps a reference (a name or an Id) to the Id it resolved to.
type RefMap = OrderedMap[string]

// LabelMap maps the Id of a referenced record to its names.
type LabelMap = OrderedMap[ItemLabel]

// NewRefMap returns an empty reference map.
func NewRefMap() *RefMap {
	return &RefMap{}
}

// NewLabelMap returns an empty label map.
func NewLabelMap() *LabelMap {
	return &LabelMap{}
}

// Set stores value under key; a new key goes after the existing ones.
func (m *OrderedMap[V]) Set(key string, value V) {
	if m.values == nil {
		m.values = map[string]V{}
	}
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Lookup returns the value under key and whether the key is set.
func (m *OrderedMap[V]) Lookup(key string) (V, bool) {
	if m == nil {
		var zero V
		return zero, false
	}
	value, ok := m.values[key]
	return value, ok
}

// Get returns the value under key, or the zero value.
func (m *OrderedMap[V]) Get(key string) V {
	value, _ := m.Lookup(key)
	return value
}

// Has reports whether key is set.
func (m *OrderedMap[V]) Has(key string) bool {
	_, ok := m.Lookup(key)
	return ok
}

// Keys returns the keys in the order they were first set.
func (m *OrderedMap[V]) Keys() []string {
	if m == nil {
		return nil
	}
	return m.keys
}

// Len returns the number of keys.
func (m *OrderedMap[V]) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

func (m *OrderedMap[V]) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range m.Keys() {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(jsonStringify(key))
		out.WriteByte(':')
		out.WriteString(jsonStringify(m.values[key]))
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func (m *OrderedMap[V]) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("expected a JSON object")
	}
	m.keys, m.values = nil, nil
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, _ := keyToken.(string)
		var value V
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		m.Set(key, value)
	}
	_, err = decoder.Token()
	return err
}

// ItemLabel names a permission set, permission set group, public group,
// queue, profile or role.
type ItemLabel struct {
	ID      string `json:"id"`
	APIName string `json:"apiName,omitempty"`
	Label   string `json:"label,omitempty"`
	Type    string `json:"type,omitempty"`
}
