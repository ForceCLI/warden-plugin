package lifecycle

import (
	"strconv"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

var userDiffCsvColumns = []string{
	"userKey",
	"userId",
	"category",
	"kind",
	"value",
	"mode",
	"userName",
	"username",
	"valueApiName",
	"valueLabel",
	"valueType",
	"valueBefore",
	"valueAfter",
}

// RenderUserDiffCsv writes the rows of a diff as CSV.  The valueApiName,
// valueLabel and valueType columns name the record of the value, or of
// the value after the change for a profile or role row.
func RenderUserDiffCsv(result *UserDiffResult) string {
	rows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		label, found := result.Labels.Lookup(row.Value)
		if !found && row.ValueAfter != nil && *row.ValueAfter != "" {
			label, _ = result.Labels.Lookup(*row.ValueAfter)
		}
		rows = append(rows, map[string]any{
			"userKey":      row.UserKey,
			"userId":       row.UserID,
			"category":     row.Category,
			"kind":         row.Kind,
			"value":        row.Value,
			"mode":         row.Mode,
			"userName":     row.UserName,
			"username":     row.Username,
			"valueApiName": label.APIName,
			"valueLabel":   label.Label,
			"valueType":    label.Type,
			"valueBefore":  stringOr(row.ValueBefore, ""),
			"valueAfter":   stringOr(row.ValueAfter, ""),
		})
	}
	return core.SerializeCsv(rows, userDiffCsvColumns)
}

// displayValue names the record with an Id: "apiName (label)" when both
// are known and differ, otherwise the API name, the label or the value
// itself.
func displayValue(result *UserDiffResult, value string) string {
	label, found := result.Labels.Lookup(value)
	if !found {
		return value
	}
	if label.APIName != "" && label.Label != "" && label.APIName != label.Label {
		return label.APIName + " (" + label.Label + ")"
	}
	if label.APIName != "" {
		return label.APIName
	}
	if label.Label != "" {
		return label.Label
	}
	return value
}

func renderFieldDiff(result *UserDiffResult, label string, field DiffField) (string, bool) {
	if field.Matches {
		return "", false
	}
	return "  " + label + ": " + displayValue(result, stringOr(field.Current, "")) + " -> " + displayValue(result, stringOr(field.Intended, "")), true
}

func renderCategoryDiff(result *UserDiffResult, category namedDelta, verbose bool) []string {
	delta := category.delta
	if len(delta.Adds) == 0 && len(delta.Removes) == 0 && len(delta.InBoth) == 0 && len(delta.OnlyInOrg) == 0 {
		return nil
	}
	mode := ""
	if delta.Mode != "" {
		mode = " (" + delta.Mode + ")"
	}
	lines := []string{"  " + category.name + mode + ":"}
	for _, value := range delta.Adds {
		lines = append(lines, "    + "+displayValue(result, value))
	}
	for _, value := range delta.Removes {
		lines = append(lines, "    - "+displayValue(result, value))
	}
	if verbose {
		for _, value := range delta.InBoth {
			lines = append(lines, "    = "+displayValue(result, value))
		}
	}
	for _, value := range delta.OnlyInOrg {
		lines = append(lines, "    extra "+displayValue(result, value))
	}
	return lines
}

// RenderUserDiffHuman writes a diff as text.  The assignments both sides
// have are listed only when verbose is set.
func RenderUserDiffHuman(result *UserDiffResult, lookup MessageLookup, verbose bool) string {
	lines := []string{lookup("info.summary", strconv.Itoa(result.Summary.Total), strconv.Itoa(result.Summary.Changed), strconv.Itoa(result.Summary.Failed))}
	for _, warning := range result.Warnings {
		lines = append(lines, "warning: "+warning)
	}
	for _, user := range result.Users {
		id := ""
		if stringOr(user.ID, "") != "" {
			id = " (" + *user.ID + ")"
		}
		lines = append(lines, "", user.Key+id+": "+user.Status)
		for _, text := range user.Errors {
			lines = append(lines, "  error: "+text)
		}
		if line, ok := renderFieldDiff(result, "profile", user.Profile); ok {
			lines = append(lines, line)
		}
		if line, ok := renderFieldDiff(result, "role", user.Role); ok {
			lines = append(lines, line)
		}
		for _, category := range assignmentCategories(user.Assignments) {
			lines = append(lines, renderCategoryDiff(result, category, verbose)...)
		}
	}
	return strings.Join(lines, "\n")
}

// UserConformanceVerdict says whether a user has what its personas
// intend, and lists what differs.
type UserConformanceVerdict struct {
	Key        string   `json:"key"`
	Conformant bool     `json:"conformant"`
	Violations []string `json:"violations"`
}

func displayValues(result *UserDiffResult, values []string) string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = displayValue(result, value)
	}
	return strings.Join(names, ", ")
}

func conformanceViolationsForUser(result *UserDiffResult, user *UserDiff, lookup MessageLookup) []string {
	if user.Status == "would-create" {
		return []string{lookup("verify.violation.notFound")}
	}
	if user.Status == "failed" {
		if len(user.Errors) == 0 {
			return []string{lookup("verify.violation.error", "user diff failed")}
		}
		violations := make([]string, len(user.Errors))
		for i, text := range user.Errors {
			violations[i] = lookup("verify.violation.error", text)
		}
		return violations
	}
	violations := []string{}
	if !user.Profile.Matches {
		violations = append(violations, lookup("verify.violation.profile", displayValue(result, stringOr(user.Profile.Current, "")), displayValue(result, stringOr(user.Profile.Intended, ""))))
	}
	if !user.Role.Matches {
		violations = append(violations, lookup("verify.violation.role", displayValue(result, stringOr(user.Role.Current, "")), displayValue(result, stringOr(user.Role.Intended, ""))))
	}
	for _, category := range assignmentCategories(user.Assignments) {
		if len(category.delta.Adds) > 0 {
			violations = append(violations, lookup("verify.violation.missing", category.name, displayValues(result, category.delta.Adds)))
		}
		if category.delta.Mode == "sync" && len(category.delta.Removes) > 0 {
			violations = append(violations, lookup("verify.violation.extra", category.name, displayValues(result, category.delta.Removes)))
		}
	}
	return violations
}

// VerifyUserDiff returns a verdict for each user of a persona diff.  A
// user is conformant when it exists, was compared without error, has the
// intended profile and role, misses no intended assignment and, in a
// category in sync mode, has no other assignment.
func VerifyUserDiff(result *UserDiffResult, lookup MessageLookup) []UserConformanceVerdict {
	verdicts := make([]UserConformanceVerdict, 0, len(result.Users))
	for _, user := range result.Users {
		violations := conformanceViolationsForUser(result, user, lookup)
		verdicts = append(verdicts, UserConformanceVerdict{Key: user.Key, Conformant: len(violations) == 0, Violations: violations})
	}
	return verdicts
}

// RenderUserConformanceCsv writes the verdicts as CSV, the violations of
// a user joined by "; ".
func RenderUserConformanceCsv(verdicts []UserConformanceVerdict) string {
	rows := make([]map[string]any, 0, len(verdicts))
	for _, verdict := range verdicts {
		rows = append(rows, map[string]any{
			"key":        verdict.Key,
			"conformant": strconv.FormatBool(verdict.Conformant),
			"violations": strings.Join(verdict.Violations, "; "),
		})
	}
	return core.SerializeCsv(rows, []string{"key", "conformant", "violations"})
}

// RenderUserConformanceHuman writes a summary line and the violations of
// each user that is not conformant.
func RenderUserConformanceHuman(verdicts []UserConformanceVerdict, lookup MessageLookup) string {
	conformant := 0
	for _, verdict := range verdicts {
		if verdict.Conformant {
			conformant++
		}
	}
	lines := []string{lookup("verify.summary", strconv.Itoa(len(verdicts)), strconv.Itoa(conformant), strconv.Itoa(len(verdicts)-conformant))}
	for _, verdict := range verdicts {
		if verdict.Conformant {
			continue
		}
		lines = append(lines, "", lookup("verify.user", verdict.Key))
		for _, violation := range verdict.Violations {
			lines = append(lines, "  - "+violation)
		}
	}
	return strings.Join(lines, "\n")
}
