package lifecycle

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

func itemName(item LabelBundle) string {
	if item.APIName != "" {
		return item.APIName
	}
	if item.Label != "" {
		return item.Label
	}
	return item.ID
}

func sortItems(items []LabelBundle) []LabelBundle {
	sorted := append([]LabelBundle{}, items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return localeCompare(itemName(sorted[i]), itemName(sorted[j])) < 0
	})
	return sorted
}

// MakeNotice returns a notice.  A nil count and a nil detail are left
// out; the items are sorted by API name, then label, then Id.
func MakeNotice(key string, count *int, items []LabelBundle, detail *string) Notice {
	notice := Notice{Key: key, Count: count, Detail: detail}
	if len(items) > 0 {
		notice.Items = sortItems(items)
	}
	return notice
}

// SummarizeLifecycle counts users by status.  A planned user counts as
// changed, because a dry run reports what would change.
func SummarizeLifecycle(users []*UserResult) Summary {
	summary := Summary{Total: len(users)}
	for _, user := range users {
		switch user.Status {
		case "changed", "planned":
			summary.Changed++
		case "unchanged":
			summary.Unchanged++
		case "failed":
			summary.Failed++
		}
	}
	return summary
}

// FailedResult returns the result of a user that could not be selected.
func FailedResult(targetError core.TargetError) *UserResult {
	return &UserResult{
		Key:      targetError.Key,
		Status:   "failed",
		Actions:  []Notice{},
		Skipped:  []Notice{},
		Warnings: []string{},
		Errors:   []string{targetError.Message},
	}
}

func failedResults(targetErrors []core.TargetError) []*UserResult {
	results := make([]*UserResult, 0, len(targetErrors))
	for _, targetError := range targetErrors {
		results = append(results, FailedResult(targetError))
	}
	return results
}

func loginIsFrozen(loginRow sfapi.Record) *bool {
	if frozen, ok := loginRow["IsFrozen"].(bool); ok {
		return &frozen
	}
	return nil
}

// ResolvedTargetResult returns the unchanged result of a selected user.
// loginRow is the UserLogin row of the user, or nil.
func ResolvedTargetResult(target core.Target, loginRow sfapi.Record) *UserResult {
	id, isActive := target.ID, target.IsActive
	return &UserResult{
		Key:      target.Key,
		ID:       &id,
		Name:     target.Name,
		Username: target.Username,
		IsActive: &isActive,
		IsFrozen: loginIsFrozen(loginRow),
		Status:   "unchanged",
		Actions:  []Notice{},
		Skipped:  []Notice{},
		Warnings: []string{},
		Errors:   []string{},
	}
}

func lifecycleCsvRow(user *UserResult) map[string]any {
	wasFrozen := ""
	if user.IsFrozen != nil {
		wasFrozen = strconv.FormatBool(*user.IsFrozen)
	}
	return map[string]any{
		"userKey":     user.Key,
		"userId":      stringOr(user.ID, ""),
		"userName":    stringOr(user.Name, ""),
		"username":    stringOr(user.Username, ""),
		"status":      user.Status,
		"wasFrozen":   wasFrozen,
		"action":      "",
		"category":    "",
		"name":        "",
		"error":       "",
		"itemId":      "",
		"itemApiName": "",
	}
}

// LifecycleToCsvRows returns one row for each item of each action, one
// for each action without items, one for each error, and one for a user
// with neither.
func LifecycleToCsvRows(result *Result) []map[string]any {
	rows := []map[string]any{}
	for _, user := range result.Users {
		for _, action := range user.Actions {
			items := sortItems(action.Items)
			if len(items) == 0 {
				row := lifecycleCsvRow(user)
				row["action"] = action.Key
				rows = append(rows, row)
				continue
			}
			for _, item := range items {
				row := lifecycleCsvRow(user)
				row["action"] = action.Key
				row["category"] = item.Type
				row["name"] = itemName(item)
				row["itemId"] = item.ID
				row["itemApiName"] = item.APIName
				rows = append(rows, row)
			}
		}
		for _, text := range user.Errors {
			row := lifecycleCsvRow(user)
			row["error"] = text
			rows = append(rows, row)
		}
		if len(user.Actions) == 0 && len(user.Errors) == 0 {
			rows = append(rows, lifecycleCsvRow(user))
		}
	}
	return rows
}

func renderLabelBundle(item LabelBundle) string {
	if item.APIName != "" && item.Label != "" && item.APIName != item.Label {
		return item.APIName + " (" + item.Label + ")"
	}
	return itemName(item)
}

func renderNoticeLines(notice Notice, lookup MessageLookup) (summary string, items []string) {
	var args []string
	if notice.Detail != nil {
		args = []string{*notice.Detail}
	} else if notice.Count != nil {
		args = []string{strconv.Itoa(*notice.Count)}
	}
	for _, item := range notice.Items {
		items = append(items, "    · "+renderLabelBundle(item))
	}
	return lookup(notice.Key, args...), items
}

func renderUserIdentity(user *UserResult) (string, bool) {
	if stringOr(user.Name, "") == "" || stringOr(user.Username, "") == "" || stringOr(user.ID, "") == "" {
		return "", false
	}
	return *user.Name + " <" + *user.Username + "> · " + *user.ID, true
}

func renderMatchProvenance(user *UserResult) string {
	match := strings.Replace(user.Key, ":", " = ", 1)
	state := "active"
	if user.IsFrozen != nil && *user.IsFrozen {
		state = "frozen"
	} else if user.IsActive != nil && !*user.IsActive {
		state = "inactive"
	}
	return "  matched " + match + " · was " + state
}

func renderReviewIdentity(identity ReviewIdentity, includeID bool) string {
	name := "(unknown)"
	for _, candidate := range []*string{identity.Name, identity.Username, identity.Email} {
		if candidate != nil {
			name = *candidate
			break
		}
	}
	address := identity.Username
	if address == nil {
		address = identity.Email
	}
	person := name
	if address != nil && *address != "" && identity.Name != nil && *identity.Name != "" {
		person = name + " <" + *address + ">"
	}
	if includeID && identity.UserID != nil {
		person += "  " + *identity.UserID
	}
	return person
}

// RenderLifecycleResult writes a result as text: a summary line, then for
// each user its identity, warnings, skipped steps, actions and errors.
func RenderLifecycleResult(result *Result, lookup MessageLookup) string {
	userSuffix := "s"
	if result.Summary.Total == 1 {
		userSuffix = ""
	}
	lines := []string{lookup("info.summary",
		strconv.Itoa(result.Summary.Total),
		userSuffix,
		strconv.Itoa(result.Summary.Changed),
		strconv.Itoa(result.Summary.Unchanged),
		strconv.Itoa(result.Summary.Failed),
	)}
	for _, user := range result.Users {
		lines = append(lines, "")
		if identity, ok := renderUserIdentity(user); ok {
			lines = append(lines, identity, renderMatchProvenance(user))
		} else {
			id := ""
			if stringOr(user.ID, "") != "" {
				id = " (" + *user.ID + ")"
			}
			lines = append(lines, user.Key+id+": "+user.Status)
		}
		if review := user.IdentityReview; review != nil {
			lines = append(lines, "  snapshot: "+renderReviewIdentity(review.Snapshot, false)+"  ("+review.Match.Field+" = "+review.Match.Value+")")
			lines = append(lines, "  org:      "+renderReviewIdentity(review.Org, true))
			for _, field := range []struct {
				name          string
				snapshot, org *string
			}{{"profile", review.Snapshot.Profile, review.Org.Profile}, {"role", review.Snapshot.Role, review.Org.Role}} {
				if field.snapshot != nil || field.org != nil {
					lines = append(lines, "  "+field.name+": "+stringOr(field.snapshot, "(missing)")+" → "+stringOr(field.org, "(missing)")+"  (reported, not applied)")
				}
			}
		}
		for _, warning := range user.Warnings {
			lines = append(lines, "  warning: "+warning)
		}
		for _, skipped := range user.Skipped {
			summary, items := renderNoticeLines(skipped, lookup)
			lines = append(lines, "  skipped: "+summary)
			lines = append(lines, items...)
		}
		for _, action := range user.Actions {
			summary, items := renderNoticeLines(action, lookup)
			lines = append(lines, "  action: "+summary)
			lines = append(lines, items...)
		}
		for _, text := range user.Errors {
			lines = append(lines, "  error: "+text)
		}
	}
	return strings.Join(lines, "\n")
}

func joinValues(values []string) string {
	return strings.Join(values, "; ")
}

func noticeKeys(notices []Notice) []string {
	keys := make([]string, len(notices))
	for i, notice := range notices {
		keys[i] = notice.Key
	}
	return keys
}

// RenderSnapshotCsv writes one row for each user of a snapshot command
// result.
func RenderSnapshotCsv(result *Result) string {
	rows := make([]map[string]any, 0, len(result.Users))
	for _, user := range result.Users {
		rows = append(rows, map[string]any{
			"key":      user.Key,
			"id":       stringOr(user.ID, ""),
			"status":   user.Status,
			"actions":  joinValues(noticeKeys(user.Actions)),
			"skipped":  joinValues(noticeKeys(user.Skipped)),
			"warnings": joinValues(user.Warnings),
			"errors":   joinValues(user.Errors),
		})
	}
	return core.SerializeCsv(rows, []string{"key", "id", "status", "actions", "skipped", "warnings", "errors"})
}

// RenderLifecycleCsv writes a freeze or unfreeze result as CSV.
func RenderLifecycleCsv(result *Result) string {
	return core.SerializeCsv(LifecycleToCsvRows(result), []string{"userKey", "userId", "userName", "username", "wasFrozen", "status", "action", "error"})
}

// RenderRestoreCsv writes a restore result as CSV.
func RenderRestoreCsv(result *Result) string {
	return core.SerializeCsv(LifecycleToCsvRows(result), []string{"userKey", "userId", "userName", "username", "status", "action", "category", "name", "error"})
}

// RenderStripCsv writes a strip result as CSV.
func RenderStripCsv(result *Result) string {
	return core.SerializeCsv(LifecycleToCsvRows(result), []string{"userKey", "userId", "userName", "username", "status", "action", "category", "itemId", "itemApiName", "error"})
}
