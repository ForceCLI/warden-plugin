// Package lifecycle freezes, unfreezes, strips, snapshots, restores and
// compares users.  It is a port of the lifecycle modules and use cases of
// @syntax-syllogism/warden-core 0.7.0.
package lifecycle

import (
	"bytes"
	"encoding/json"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/provisioning"
)

// MessageLookup returns the text for a message key with its arguments.
type MessageLookup = func(key string, args ...string) string

// LabelBundle names a permission set, permission set group, public group,
// queue, profile, role or permission set license.
type LabelBundle = provisioning.ItemLabel

// Notice is one action or skipped step of a user result.
type Notice struct {
	Key string `json:"key"`
	// Count is nil for a notice without a count.
	Count *int          `json:"count,omitempty"`
	Items []LabelBundle `json:"items,omitempty"`
	// Detail, when set, is the argument of the message instead of Count.
	Detail *string `json:"detail,omitempty"`
}

// ReviewIdentity is the identity of a user in a snapshot or in the org.
// UserID is set only for the org side.
type ReviewIdentity struct {
	Name     *string `json:"name,omitempty"`
	Username *string `json:"username,omitempty"`
	Email    *string `json:"email,omitempty"`
	Profile  *string `json:"profile,omitempty"`
	Role     *string `json:"role,omitempty"`
	UserID   *string `json:"userId,omitempty"`
}

// ReviewMatch is the field and value a snapshot entry was matched on.
type ReviewMatch struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// IdentityReview puts the identity a snapshot recorded next to the
// identity of the user restore found.
type IdentityReview struct {
	Snapshot ReviewIdentity `json:"snapshot"`
	Org      ReviewIdentity `json:"org"`
	Match    ReviewMatch    `json:"match"`
}

// UserResult is the outcome of a lifecycle command for one user.  Status
// is "changed", "failed", "planned" or "unchanged".
type UserResult struct {
	Key            string          `json:"key"`
	ID             *string         `json:"id,omitempty"`
	Name           *string         `json:"name,omitempty"`
	Username       *string         `json:"username,omitempty"`
	IsActive       *bool           `json:"isActive,omitempty"`
	IsFrozen       *bool           `json:"isFrozen,omitempty"`
	Status         string          `json:"status"`
	Actions        []Notice        `json:"actions"`
	Skipped        []Notice        `json:"skipped"`
	Warnings       []string        `json:"warnings"`
	Errors         []string        `json:"errors"`
	IdentityReview *IdentityReview `json:"identityReview,omitempty"`
}

// Summary counts the users of a result by status.  A planned user counts
// as changed.
type Summary struct {
	Total     int `json:"total"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Failed    int `json:"failed"`
}

// Result is the outcome of a lifecycle command.
type Result struct {
	Summary Summary       `json:"summary"`
	Users   []*UserResult `json:"users"`
}

// encodeJSON writes a value as JSON without HTML escaping.
func encodeJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// jsonObject writes a JSON object whose keys keep the order of the calls
// to field.
type jsonObject struct {
	out   bytes.Buffer
	count int
	err   error
}

func (o *jsonObject) field(key string, value any) {
	if o.err != nil {
		return
	}
	encodedKey, err := encodeJSON(key)
	if err != nil {
		o.err = err
		return
	}
	encodedValue, err := encodeJSON(value)
	if err != nil {
		o.err = err
		return
	}
	if o.count == 0 {
		o.out.WriteByte('{')
	} else {
		o.out.WriteByte(',')
	}
	o.count++
	o.out.Write(encodedKey)
	o.out.WriteByte(':')
	o.out.Write(encodedValue)
}

func (o *jsonObject) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.count == 0 {
		return []byte("{}"), nil
	}
	return append(o.out.Bytes(), '}'), nil
}

// targetJSON writes a target with the keys of the upstream
// ResolvedTargetUser.
type targetJSON core.Target

func (t targetJSON) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("key", t.Key)
	object.field("Id", t.ID)
	object.field("IsActive", t.IsActive)
	for _, optional := range []struct {
		key   string
		value *string
	}{{"name", t.Name}, {"username", t.Username}, {"email", t.Email}, {"profile", t.Profile}, {"role", t.Role}} {
		if optional.value != nil {
			object.field(optional.key, *optional.value)
		}
	}
	object.field("field", t.Field)
	object.field("value", t.Value)
	object.field("order", t.Order)
	return object.bytes()
}

// targetErrorJSON writes a target error with the keys of the upstream
// TargetError.
type targetErrorJSON core.TargetError

func (e targetErrorJSON) MarshalJSON() ([]byte, error) {
	var object jsonObject
	object.field("key", e.Key)
	object.field("field", e.Field)
	if e.Value != "" {
		object.field("value", e.Value)
	}
	object.field("message", e.Message)
	object.field("order", e.Order)
	if e.Source != nil {
		var source jsonObject
		source.field("path", e.Source.Path)
		source.field("line", e.Source.Line)
		encoded, err := source.bytes()
		if err != nil {
			return nil, err
		}
		object.field("source", json.RawMessage(encoded))
	}
	return object.bytes()
}

func targetErrorsJSON(targetErrors []core.TargetError) []targetErrorJSON {
	list := make([]targetErrorJSON, len(targetErrors))
	for i, targetError := range targetErrors {
		list[i] = targetErrorJSON(targetError)
	}
	return list
}

func text(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

func nested(row map[string]any, key string) map[string]any {
	value, _ := row[key].(map[string]any)
	return value
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

func stringPointer(value string) *string {
	return &value
}

func stringOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

// Count returns a pointer to n, for the count of a notice.
func Count(n int) *int {
	return &n
}
