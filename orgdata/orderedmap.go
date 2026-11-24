// Package orgdata reads and writes the org objects of the optional
// warden-base package: personas, persona components, persona assignments,
// reconciliation runs, reconciliation items and the grant ledger.  It is a
// port of the userShared modules personas, orgWrites, orgContract,
// provisionLedger and personaImportRequest of the warden sf plugin.
package orgdata

// OrderedMap is a map with text keys that keeps the order in which the
// keys were first set.  The methods that read accept a nil map.
type OrderedMap[V any] struct {
	keys   []string
	values map[string]V
}

// NewOrderedMap returns an empty map.
func NewOrderedMap[V any]() *OrderedMap[V] {
	return &OrderedMap[V]{values: map[string]V{}}
}

// Get returns the value under key.
func (m *OrderedMap[V]) Get(key string) (V, bool) {
	if m == nil {
		var zero V
		return zero, false
	}
	value, ok := m.values[key]
	return value, ok
}

// Has reports whether key is set.
func (m *OrderedMap[V]) Has(key string) bool {
	_, ok := m.Get(key)
	return ok
}

// Set stores value under key; a new key goes after the existing ones.
func (m *OrderedMap[V]) Set(key string, value V) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Keys returns the keys in the order in which they were first set.
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
