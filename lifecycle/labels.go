package lifecycle

import (
	"bytes"

	"github.com/ForceCLI/warden-plugin/provisioning"
)

// labelEntry is a label and the order in which its apiName, label and
// type keys are written.  The upstream code merges labels with object
// spreads, so a key keeps the position at which it was first set.
type labelEntry struct {
	bundle LabelBundle
	order  []string
}

func (e labelEntry) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("id", e.bundle.ID)
	for _, key := range e.order {
		value := e.bundle.Type
		switch key {
		case "apiName":
			value = e.bundle.APIName
		case "label":
			value = e.bundle.Label
		}
		if value != "" {
			object.field(key, value)
		}
	}
	return object.bytes()
}

// LabelMap maps the Id of each record a diff names to its names, in the
// order the Ids were first added.
type LabelMap struct {
	keys   []string
	values map[string]labelEntry
}

func newLabelMap() *LabelMap {
	return &LabelMap{values: map[string]labelEntry{}}
}

// Lookup returns the label of an Id and whether there is one.
func (m *LabelMap) Lookup(id string) (LabelBundle, bool) {
	if m == nil {
		return LabelBundle{}, false
	}
	entry, ok := m.values[id]
	return entry.bundle, ok
}

// Keys returns the Ids in the order they were first added.
func (m *LabelMap) Keys() []string {
	if m == nil {
		return nil
	}
	return m.keys
}

func (m *LabelMap) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range m.Keys() {
		if i > 0 {
			out.WriteByte(',')
		}
		encodedKey, err := encodeJSON(key)
		if err != nil {
			return nil, err
		}
		encodedValue, err := encodeJSON(m.values[key])
		if err != nil {
			return nil, err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(encodedValue)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func (m *LabelMap) set(id string, entry labelEntry) {
	if _, exists := m.values[id]; !exists {
		m.keys = append(m.keys, id)
	}
	m.values[id] = entry
}

// addReference copies a label of the resolved references.  Such a label
// has an apiName or label key only when it has the value.
func (m *LabelMap) addReference(label provisioning.ItemLabel) {
	var order []string
	if label.APIName != "" {
		order = append(order, "apiName")
	}
	if label.Label != "" {
		order = append(order, "label")
	}
	m.set(label.ID, labelEntry{bundle: label, order: append(order, "type")})
}

// assignmentLabelKeys and profileRoleLabelKeys are the keys of the labels
// made from assignment rows and from the profile and role of a user.
var (
	assignmentLabelKeys  = []string{"apiName", "label", "type"}
	profileRoleLabelKeys = []string{"label", "type"}
)

// add merges a label into the map: a value the new label has replaces
// the one already there, and a value it lacks is kept.
func (m *LabelMap) add(bundle LabelBundle, bundleKeys []string) {
	current := m.values[bundle.ID]
	merged := labelEntry{bundle: current.bundle, order: append([]string{}, current.order...)}
	merged.bundle.ID = bundle.ID
	if bundle.APIName != "" {
		merged.bundle.APIName = bundle.APIName
	}
	if bundle.Label != "" {
		merged.bundle.Label = bundle.Label
	}
	if bundle.Type != "" {
		merged.bundle.Type = bundle.Type
	}
	for _, key := range bundleKeys {
		if !contains(merged.order, key) {
			merged.order = append(merged.order, key)
		}
	}
	m.set(bundle.ID, merged)
}
