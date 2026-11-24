package provisioning

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

// PublicUserResult is the outcome for one user as the commands report it.
type PublicUserResult struct {
	Key string
	// ID is the Id of the user, or "" when it is not known.
	ID string
	// UserName and Username are nil when they are not known.
	UserName *string
	Username *string
	Personas []string
	// MatchedBy and MatchValue are nil when the user names no match field
	// or the field has no usable value.
	MatchedBy  *string
	MatchValue *string
	Matched    bool
	// Status is "created", "updated", "planned" or "failed".
	Status         string
	Actions        []string
	Errors         []string
	RelatedRecords []*RelatedRecordResult
}

// MarshalJSON writes the result with the keys of the upstream result.
func (r PublicUserResult) MarshalJSON() ([]byte, error) {
	object := core.NewObject()
	object.Set("key", r.Key)
	if r.ID != "" {
		object.Set("id", r.ID)
	}
	if r.UserName != nil {
		object.Set("userName", *r.UserName)
	}
	if r.Username != nil {
		object.Set("username", *r.Username)
	}
	object.Set("personas", nonNil(r.Personas))
	object.Set("matchedBy", r.MatchedBy)
	object.Set("matchValue", r.MatchValue)
	object.Set("matched", r.Matched)
	object.Set("status", r.Status)
	object.Set("actions", nonNil(r.Actions))
	object.Set("errors", nonNil(r.Errors))
	if r.RelatedRecords != nil {
		object.Set("relatedRecords", r.RelatedRecords)
	}
	return object.MarshalJSON()
}

// PermissionSetLicenseNote says that permission set licenses were not
// compared with what the org has available.
type PermissionSetLicenseNote struct {
	Evaluated bool   `json:"evaluated"`
	Note      string `json:"note"`
}

// Result is the outcome of a provisioning run or of its preview.
type Result struct {
	Summary Summary            `json:"summary"`
	Users   []PublicUserResult `json:"users"`
	// Licenses and PermissionSetLicenses are set for a preview only.
	Licenses              []LicenseUsage            `json:"licenses,omitzero"`
	PermissionSetLicenses *PermissionSetLicenseNote `json:"permissionSetLicenses,omitempty"`
}

// AddSourceContext puts the file and line of a CSV row before each error
// of the row.
func AddSourceContext(source *core.RowInfo, errors []string) []string {
	if source == nil {
		return errors
	}
	withContext := make([]string, len(errors))
	for i, text := range errors {
		withContext[i] = fmt.Sprintf("%s:%d — %s", source.Path, source.Line, text)
	}
	return withContext
}

// PublicProvisionResult returns the result of a user without the plan
// Id, the position and the CSV row.
func PublicProvisionResult(result *UserResult) PublicUserResult {
	public := PublicUserResult{
		Key:        result.Key,
		ID:         result.ID,
		UserName:   result.UserName,
		Username:   result.Username,
		Personas:   result.Personas,
		MatchedBy:  result.MatchedBy,
		MatchValue: result.MatchValue,
		Matched:    result.Matched,
		Status:     result.Status,
		Actions:    result.Actions,
		Errors:     AddSourceContext(result.Source, result.Errors),
	}
	if result.RelatedRecords != nil {
		public.RelatedRecords = PublicRelatedResults(result.RelatedRecords)
	}
	return public
}

func renderMatchProvenance(user PublicUserResult) string {
	if user.MatchedBy == nil || *user.MatchedBy == "" {
		return "unmatched"
	}
	label := "unmatched"
	if user.Matched {
		label = "matched"
	}
	if user.MatchValue == nil {
		return label + " " + *user.MatchedBy
	}
	return label + " " + *user.MatchedBy + " = " + *user.MatchValue
}

// RenderHuman writes a result as text: two lines for each user, a line
// for each action, related record and error, a summary line and, for a
// preview, the license lines.  personaSourceLabel says where the personas
// came from; "" means "file".  lookup gives the text of the summary and
// license lines; nil means RenderMessage.
func RenderHuman(output *Result, personaSourceLabel string, lookup MessageFunc) string {
	if personaSourceLabel == "" {
		personaSourceLabel = "file"
	}
	if lookup == nil {
		lookup = RenderMessage
	}
	lines := []string{"Persona source: " + personaSourceLabel, ""}
	for _, user := range output.Users {
		id := ""
		if user.ID != "" {
			id = " · " + user.ID
		}
		lines = append(lines, user.Key+id+" · "+user.Status)
		personas := strings.Join(user.Personas, ", ")
		if personas == "" {
			personas = "(none)"
		}
		lines = append(lines, "  "+renderMatchProvenance(user)+" · personas: "+personas)
		for _, action := range user.Actions {
			lines = append(lines, "  action: "+action)
		}
		for _, related := range user.RelatedRecords {
			recordID := ""
			if related.RecordID != "" {
				recordID = " · " + related.RecordID
			}
			lines = append(lines, "  related: "+related.Phase+" "+related.Sobject+" "+related.Action+recordID)
			if related.Error != "" {
				lines = append(lines, "  related error: "+related.Error)
			}
		}
		for _, text := range user.Errors {
			lines = append(lines, "  error: "+text)
		}
	}
	lines = append(lines, "")
	lines = append(lines, lookup("info.summary", fmt.Sprint(output.Summary.Total), fmt.Sprint(output.Summary.Created), fmt.Sprint(output.Summary.Updated), fmt.Sprint(output.Summary.Failed)))
	if output.Licenses != nil {
		lines = append(lines, "", lookup("info.licenses.header"))
		for _, license := range output.Licenses {
			available := "unlimited"
			if !license.Unlimited {
				available = "null"
				if license.Available != nil {
					available = fmt.Sprint(*license.Available)
				}
			}
			note := ""
			if license.Note != "" {
				note = " (" + license.Note + ")"
			}
			lines = append(lines, lookup("info.licenses.row", license.LicenseName, fmt.Sprint(license.Required), available, fmt.Sprint(license.Shortfall), note))
		}
		lines = append(lines, lookup("info.permissionSetLicenses.notEvaluated"))
	}
	return strings.Join(lines, "\n")
}

// ProvisionCsvColumns are the columns of RenderCsv.
var ProvisionCsvColumns = []string{"userKey", "userId", "userName", "username", "personas", "matchedBy", "status", "action", "detail", "error"}

// RenderCsv writes a result as CSV: for each user a row for each action,
// a row for each related record with the action "related", and a row for
// each error.  A user without actions, related records and errors has one
// row.
func RenderCsv(result *Result) string {
	rows := []map[string]any{}
	for _, user := range result.Users {
		row := func(action, detail, failure string) map[string]any {
			base := map[string]any{
				"userKey":  user.Key,
				"userId":   user.ID,
				"userName": "",
				"username": "",
				"personas": strings.Join(user.Personas, "; "),
				"status":   user.Status,
				"action":   action,
				"detail":   detail,
				"error":    failure,
			}
			if user.UserName != nil {
				base["userName"] = *user.UserName
			}
			if user.Username != nil {
				base["username"] = *user.Username
			}
			base["matchedBy"] = ""
			if user.MatchedBy != nil {
				base["matchedBy"] = *user.MatchedBy
			}
			return base
		}
		populated := 0
		for _, action := range user.Actions {
			rows = append(rows, row(action, "", ""))
			populated++
		}
		// A related record uses the same ten columns: the action
		// "related" and a detail made of its names.
		for _, related := range user.RelatedRecords {
			rows = append(rows, row("related", related.Relationship+" "+related.Phase+" "+related.Sobject+" "+related.Action, related.Error))
			populated++
		}
		for _, failure := range user.Errors {
			rows = append(rows, row("", "", failure))
		}
		if len(user.Errors) == 0 && populated == 0 {
			rows = append(rows, row("", "", ""))
		}
	}
	return core.SerializeCsv(rows, ProvisionCsvColumns)
}
