// Package access audits which active users can reach a field, object, Apex
// class, Visualforce page, custom permission, tab or record type, and
// through which profile, permission set or permission set group.  It is a
// port of the access modules of @syntax-syllogism/warden-core 0.7.0.
package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// The target types of an audit.
const (
	TypeField            = "field"
	TypeObject           = "object"
	TypeApexClass        = "apex-class"
	TypeVfPage           = "vf-page"
	TypeCustomPermission = "custom-permission"
	TypeTab              = "tab"
	TypeRecordType       = "record-type"
)

// The assignment types of a row.
const (
	AssignmentProfile            = "Profile"
	AssignmentPermissionSet      = "PermissionSet"
	AssignmentPermissionSetGroup = "PermissionSetGroup"
)

// Access is what a grant allows.  The implementations are FieldAccess,
// ObjectAccess, EnabledAccess, TabAccess and RecordTypeAccess.
type Access interface {
	// Kind is "field", "object", "enabled", "tab" or "record-type".
	Kind() string
	// column returns the value of a CSV column the access owns.
	column(name string) (any, bool)
	json.Marshaler
}

// FieldAccess is field-level security.
type FieldAccess struct {
	Read bool
	Edit bool
}

func (FieldAccess) Kind() string { return "field" }

func (a FieldAccess) column(name string) (any, bool) {
	switch name {
	case "kind":
		return a.Kind(), true
	case "read":
		return a.Read, true
	case "edit":
		return a.Edit, true
	}
	return nil, false
}

func (a FieldAccess) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"field","read":` + strconv.FormatBool(a.Read) + `,"edit":` + strconv.FormatBool(a.Edit) + `}`), nil
}

// ObjectAccess is the object permissions.
type ObjectAccess struct {
	Read      bool
	Create    bool
	Edit      bool
	Delete    bool
	ViewAll   bool
	ModifyAll bool
}

func (ObjectAccess) Kind() string { return "object" }

func (a ObjectAccess) column(name string) (any, bool) {
	switch name {
	case "kind":
		return a.Kind(), true
	case "read":
		return a.Read, true
	case "create":
		return a.Create, true
	case "edit":
		return a.Edit, true
	case "delete":
		return a.Delete, true
	case "viewAll":
		return a.ViewAll, true
	case "modifyAll":
		return a.ModifyAll, true
	}
	return nil, false
}

func (a ObjectAccess) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"object","read":` + strconv.FormatBool(a.Read) +
		`,"create":` + strconv.FormatBool(a.Create) +
		`,"edit":` + strconv.FormatBool(a.Edit) +
		`,"delete":` + strconv.FormatBool(a.Delete) +
		`,"viewAll":` + strconv.FormatBool(a.ViewAll) +
		`,"modifyAll":` + strconv.FormatBool(a.ModifyAll) + `}`), nil
}

// EnabledAccess is access to an Apex class, Visualforce page or custom
// permission.
type EnabledAccess struct {
	Enabled bool
}

func (EnabledAccess) Kind() string { return "enabled" }

func (a EnabledAccess) column(name string) (any, bool) {
	switch name {
	case "kind":
		return a.Kind(), true
	case "enabled":
		return a.Enabled, true
	}
	return nil, false
}

func (a EnabledAccess) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"enabled","enabled":` + strconv.FormatBool(a.Enabled) + `}`), nil
}

// TabAccess is the visibility of a tab.
type TabAccess struct {
	Visibility string
}

func (TabAccess) Kind() string { return "tab" }

func (a TabAccess) column(name string) (any, bool) {
	switch name {
	case "kind":
		return a.Kind(), true
	case "visibility":
		return a.Visibility, true
	}
	return nil, false
}

func (a TabAccess) MarshalJSON() ([]byte, error) {
	return []byte(`{"kind":"tab","visibility":` + jsonText(a.Visibility) + `}`), nil
}

// RecordTypeAccess is the visibility of a record type.  Default is nil
// for a permission set, which cannot set a default record type.
type RecordTypeAccess struct {
	Visible bool
	Default *bool
}

func (RecordTypeAccess) Kind() string { return "record-type" }

func (a RecordTypeAccess) column(name string) (any, bool) {
	switch name {
	case "kind":
		return a.Kind(), true
	case "visible":
		return a.Visible, true
	case "default":
		if a.Default == nil {
			return nil, true
		}
		return *a.Default, true
	}
	return nil, false
}

func (a RecordTypeAccess) MarshalJSON() ([]byte, error) {
	defaultText := "null"
	if a.Default != nil {
		defaultText = strconv.FormatBool(*a.Default)
	}
	return []byte(`{"kind":"record-type","visible":` + strconv.FormatBool(a.Visible) + `,"default":` + defaultText + `}`), nil
}

// Row is one path by which a user has access.
type Row struct {
	UserID         string
	UserName       string
	Username       string
	TargetType     string
	TargetName     string
	AssignmentType string
	SourceID       string
	SourceName     string
	SourceAPIName  string
	SourceLabel    string
	// ViaPermissionSetID and ViaPermissionSetName name the permission set
	// of a permission set group that holds the grant.  Both are left out
	// of the JSON when ViaPermissionSetID is empty.
	ViaPermissionSetID   string
	ViaPermissionSetName string
	Access               Access
	// reverse is set on the rows of a one-user audit, whose JSON writes
	// targetName after the source keys, as the JavaScript objects do.
	reverse bool
}

func marshalNoEscape(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// jsonText writes a string as JSON without the HTML escapes of
// encoding/json.
func jsonText(value string) string {
	encoded, err := marshalNoEscape(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func (r Row) accessJSON() string {
	if r.Access == nil {
		return "null"
	}
	encoded, err := r.Access.MarshalJSON()
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func (r Row) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	write := func(key, value string) {
		if out.Len() == 0 {
			out.WriteByte('{')
		} else {
			out.WriteByte(',')
		}
		out.WriteString(jsonText(key) + ":" + value)
	}
	text := func(key, value string) { write(key, jsonText(value)) }
	via := func() {
		if r.ViaPermissionSetID != "" {
			text("viaPermissionSetId", r.ViaPermissionSetID)
			text("viaPermissionSetName", r.ViaPermissionSetName)
		}
	}
	text("userId", r.UserID)
	text("userName", r.UserName)
	text("username", r.Username)
	text("targetType", r.TargetType)
	if !r.reverse {
		text("targetName", r.TargetName)
	}
	text("assignmentType", r.AssignmentType)
	text("sourceId", r.SourceID)
	text("sourceName", r.SourceName)
	text("sourceApiName", r.SourceAPIName)
	text("sourceLabel", r.SourceLabel)
	via()
	if r.reverse {
		text("targetName", r.TargetName)
	}
	write("access", r.accessJSON())
	out.WriteByte('}')
	return out.Bytes(), nil
}

// column returns a row value by its JSON name, and false for a name the
// row does not have or whose value is not set.
func (r Row) column(name string) (any, bool) {
	switch name {
	case "userId":
		return r.UserID, true
	case "userName":
		return r.UserName, true
	case "username":
		return r.Username, true
	case "targetType":
		return r.TargetType, true
	case "targetName":
		return r.TargetName, true
	case "assignmentType":
		return r.AssignmentType, true
	case "sourceId":
		return r.SourceID, true
	case "sourceName":
		return r.SourceName, true
	case "sourceApiName":
		return r.SourceAPIName, true
	case "sourceLabel":
		return r.SourceLabel, true
	case "viaPermissionSetId":
		return r.ViaPermissionSetID, r.ViaPermissionSetID != ""
	case "viaPermissionSetName":
		return r.ViaPermissionSetName, r.ViaPermissionSetID != ""
	}
	return nil, false
}

// Stats counts the distinct users and sources of the rows of a result.
type Stats struct {
	TotalActiveUsersWithAccess int `json:"totalActiveUsersWithAccess"`
	ProfileGrants              int `json:"profileGrants"`
	PermissionSetGrants        int `json:"permissionSetGrants"`
	PermissionSetGroupGrants   int `json:"permissionSetGroupGrants"`
}

// Result is the outcome of an audit.
type Result struct {
	TargetType   string   `json:"targetType"`
	TargetName   string   `json:"targetName"`
	SobjectType  string   `json:"sobjectType,omitempty"`
	FieldAPIName string   `json:"fieldApiName,omitempty"`
	Rows         []Row    `json:"rows"`
	Stats        Stats    `json:"stats"`
	Warnings     []string `json:"warnings"`
}

// ValidatedTarget is a target that exists in the org, with its names as
// the org writes them.
type ValidatedTarget struct {
	Type          string `json:"type"`
	TargetName    string `json:"targetName"`
	SobjectType   string `json:"sobjectType,omitempty"`
	FieldAPIName  string `json:"fieldApiName,omitempty"`
	SetupEntityID string `json:"setupEntityId,omitempty"`
	RecordTypeID  string `json:"recordTypeId,omitempty"`
}

// Resolver audits targets of one type.
type Resolver struct {
	Type       string
	validate   func(conn sfapi.Conn, target string) (*ValidatedTarget, error)
	resolve    func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error)
	csvColumns func() []string
}

// ValidateTarget finds the target in the org.
func (r *Resolver) ValidateTarget(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
	return r.validate(conn, target)
}

// Resolve returns the active users with access to the target.
func (r *Resolver) Resolve(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
	return r.resolve(conn, target)
}

// CsvColumns returns the CSV columns of the rows of this type.
func (r *Resolver) CsvColumns() []string {
	return r.csvColumns()
}

// errorName is the name of the JavaScript class of every error this
// package raises.
const errorName = "UserAccessError"

// newError returns an access error.  Its Data holds the message arguments
// under "args" and, when there is one, the underlying error under "cause".
func newError(code string, args []string, cause error) *core.WardenError {
	if args == nil {
		args = []string{}
	}
	data := map[string]any{"args": args}
	if cause != nil {
		data["cause"] = cause
	}
	accessError := core.NewAccessError(code, Message(code, args...), data)
	accessError.Name = errorName
	return accessError
}

// AsAccessError returns err as an error of this package when it is one.
func AsAccessError(err error) (*core.WardenError, bool) {
	var warden *core.WardenError
	if errors.As(err, &warden) && (warden.Name == errorName || warden.Name == "AccessError") {
		return warden, true
	}
	return nil, false
}

// ErrorArgs returns the message arguments of an access error.
func ErrorArgs(accessError *core.WardenError) []string {
	args, _ := accessError.Data["args"].([]string)
	return args
}

// ErrorCause returns the underlying error of an access error, or nil.
func ErrorCause(accessError *core.WardenError) error {
	cause, _ := accessError.Data["cause"].(error)
	return cause
}

// queryFailure reports an error of a resolver: an access error as it is,
// any other error as errorAccessQueryFailed with the error as its cause.
func queryFailure(target *ValidatedTarget, err error) error {
	if _, ok := AsAccessError(err); ok {
		return err
	}
	return newError("errorAccessQueryFailed", []string{target.Type, target.TargetName}, err)
}
