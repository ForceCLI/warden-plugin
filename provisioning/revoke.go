package provisioning

import (
	"sort"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// CategoryOrder is the order of the assignment categories in plans.
var CategoryOrder = []string{"PermissionSet", "PermissionSetGroup", "PublicGroup", "Queue"}

var categoryListKey = map[string]string{
	"PermissionSet":      "permissionSets",
	"PermissionSetGroup": "permissionSetGroups",
	"PublicGroup":        "publicGroups",
	"Queue":              "queues",
}

var categoryFlags = map[string]string{
	"PermissionSet":      "keep-permsets",
	"PermissionSetGroup": "keep-permset-groups",
	"PublicGroup":        "keep-public-groups",
	"Queue":              "keep-queues",
}

func categoryIndex(category string) int {
	for index, candidate := range CategoryOrder {
		if candidate == category {
			return index
		}
	}
	return -1
}

func referenceMap(refs *ResolvedRefs, category string) *RefMap {
	switch category {
	case "PermissionSet":
		return refs.PermissionSetIDsByRef
	case "PermissionSetGroup":
		return refs.PermissionSetGroupIDsByRef
	case "PublicGroup":
		return refs.PublicGroupIDsByRef
	}
	return refs.QueueIDsByRef
}

// PlanRow is one row of a reconciliation plan or a revocation plan.
type PlanRow struct {
	UserKey  string `json:"userKey"`
	UserID   string `json:"userId"`
	Category string `json:"category"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Error    string `json:"error"`
}

func sortPlanRows(rows []PlanRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if order := compareText(left.UserKey, right.UserKey); order != 0 {
			return order < 0
		}
		if order := categoryIndex(left.Category) - categoryIndex(right.Category); order != 0 {
			return order < 0
		}
		if order := compareText(left.Detail, right.Detail); order != 0 {
			return order < 0
		}
		return compareText(left.Action, right.Action) < 0
	})
}

// DeleteStep deletes the assignment or membership rows of one category.
type DeleteStep struct {
	Kind      string      `json:"kind"`
	Sobject   string      `json:"sobject"`
	IDs       []string    `json:"ids"`
	ActionKey string      `json:"actionKey"`
	Items     []ItemLabel `json:"items"`
}

// PersonaRevocationInput is what PlanPersonaRevocation plans from.
type PersonaRevocationInput struct {
	UserKey string
	UserID  string
	// Personas is the "personas" object of a personas document.
	Personas     *core.Object
	HeldPersonas []string
	// RemovePersonas names the personas to remove; RemoveAll removes
	// every held persona instead.
	RemovePersonas []string
	RemoveAll      bool
	Refs           *ResolvedRefs
	AssignmentRows []sfapi.Record
	MembershipRows []sfapi.Record
	// Flags are the command flags by name: "dry-run" and the keep-*
	// flags.
	Flags map[string]bool
	// LedgerTargets holds, for each removed persona, the target Ids the
	// ledger records as granted by it.  When it is not nil, a removed
	// target the ledger does not record adds a warning.
	LedgerTargets map[string]map[string]bool
}

// PersonaRevocationPlan is the outcome of PlanPersonaRevocation.
type PersonaRevocationPlan struct {
	Rows     []PlanRow
	Steps    []DeleteStep
	Warnings []string
	// RemovedTargets holds the target Ids that are removed, in plan order.
	RemovedTargets []string
}

func detailFor(refs *ResolvedRefs, id string) string {
	label, ok := refs.Labels.Lookup(id)
	if !ok {
		return id
	}
	if label.Label != "" {
		return label.Label
	}
	if label.APIName != "" {
		return label.APIName
	}
	return label.ID
}

func assignmentIDs(category, targetID string, assignments, memberships []sfapi.Record) ([]string, []ItemLabel) {
	ids := []string{}
	items := []ItemLabel{}
	addAssignment := func(row sfapi.Record) {
		ids = append(ids, text(row, "Id"))
		if item := PermissionSetAssignmentLabel(row); item != nil {
			items = append(items, *item)
		}
	}
	switch category {
	case "PermissionSet":
		for _, row := range assignments {
			if row["PermissionSetGroupId"] == nil && text(row, "PermissionSetId") == targetID && nested(row, "PermissionSet")["IsOwnedByProfile"] != true {
				addAssignment(row)
			}
		}
		return ids, items
	case "PermissionSetGroup":
		for _, row := range assignments {
			if text(row, "PermissionSetGroupId") == targetID {
				addAssignment(row)
			}
		}
		return ids, items
	}
	wantedType := "Regular"
	if category == "Queue" {
		wantedType = "Queue"
	}
	for _, row := range memberships {
		if text(row, "GroupId") == targetID && groupType(row) == wantedType {
			ids = append(ids, text(row, "Id"))
			items = append(items, GroupMemberLabel(row))
		}
	}
	return ids, items
}

type revocationTargets struct {
	// ids holds the resolved target Ids in the order they were first
	// named, and names the personas that name each.
	ids        []string
	names      map[string][]string
	unresolved []string
}

func collectTargets(names []string, personas *core.Object, category string, refs *ResolvedRefs) revocationTargets {
	targets := revocationTargets{names: map[string][]string{}}
	seenUnresolved := map[string]bool{}
	byRef := referenceMap(refs, category)
	for _, name := range names {
		definition := personas.Object(name)
		if definition == nil {
			continue
		}
		for _, reference := range definition.Strings(categoryListKey[category]) {
			targetID := byRef.Get(reference)
			if targetID == "" {
				targets.unresolved = appendUnique(targets.unresolved, seenUnresolved, reference)
				continue
			}
			if _, known := targets.names[targetID]; !known {
				targets.ids = append(targets.ids, targetID)
			}
			targets.names[targetID] = append(targets.names[targetID], name)
		}
	}
	return targets
}

func keptBy(id string, names []string, personas *core.Object, category string, refs *ResolvedRefs) []string {
	byRef := referenceMap(refs, category)
	var covering []string
	for _, name := range names {
		for _, reference := range personas.Object(name).Strings(categoryListKey[category]) {
			if byRef.Get(reference) == id {
				covering = append(covering, name)
				break
			}
		}
	}
	return covering
}

// PlanPersonaRevocation plans the removal of the access the removed
// personas grant a user.  A target that a persona the user keeps also
// names is kept.  It makes no request to the org.
func PlanPersonaRevocation(input PersonaRevocationInput) PersonaRevocationPlan {
	requested := input.RemovePersonas
	if input.RemoveAll {
		requested = append([]string{}, input.HeldPersonas...)
	}
	removedSet := map[string]bool{}
	var removedNames []string
	for _, name := range requested {
		if contains(input.HeldPersonas, name) {
			removedNames = appendUnique(removedNames, removedSet, name)
		}
	}
	var keptNames []string
	for _, name := range input.HeldPersonas {
		if !removedSet[name] {
			keptNames = append(keptNames, name)
		}
	}
	rows := []PlanRow{}
	stepsByCategory := map[string]*DeleteStep{}
	warnings := []string{}
	removedTargets := []string{}
	seenTarget := map[string]bool{}
	missing := func(names []string) []string {
		var result []string
		for _, name := range names {
			if input.Personas.Object(name) == nil {
				result = append(result, name)
			}
		}
		return result
	}
	missingRemovedNames := missing(removedNames)
	missingKeptNames := missing(keptNames)
	for _, name := range append(append([]string{}, missingRemovedNames...), missingKeptNames...) {
		retained := ""
		if contains(missingKeptNames, name) {
			retained = "retained "
		}
		rows = append(rows, PlanRow{UserKey: input.UserKey, UserID: input.UserID, Category: "PermissionSet", Action: "unresolved", Status: "unmanaged", Detail: name, Error: `Unresolved ` + retained + `persona definition "` + name + `".`})
	}
	for _, category := range CategoryOrder {
		removed := collectTargets(removedNames, input.Personas, category, input.Refs)
		for _, reference := range removed.unresolved {
			rows = append(rows, PlanRow{UserKey: input.UserKey, UserID: input.UserID, Category: category, Action: "unresolved", Status: "unmanaged", Detail: reference, Error: `Unresolved ` + category + ` reference "` + reference + `".`})
		}
		// A retained persona without a definition could grant any target,
		// so nothing is removed while one is missing.
		if len(missingKeptNames) > 0 {
			continue
		}
		for _, targetID := range removed.ids {
			grantingNames := removed.names[targetID]
			detail := detailFor(input.Refs, targetID)
			coveringNames := keptBy(targetID, keptNames, input.Personas, category, input.Refs)
			ids, items := assignmentIDs(category, targetID, input.AssignmentRows, input.MembershipRows)
			row := PlanRow{UserKey: input.UserKey, UserID: input.UserID, Category: category, Detail: detail}
			if len(coveringNames) > 0 {
				sort.SliceStable(coveringNames, func(i, j int) bool { return compareText(coveringNames[i], coveringNames[j]) < 0 })
				row.Action, row.Status = "keptByOtherPersona", "kept"
				row.Detail = detail + " (kept by " + strings.Join(coveringNames, ", ") + ")"
				rows = append(rows, row)
				continue
			}
			if len(ids) == 0 {
				row.Action, row.Status = "alreadyAbsent", "unchanged"
				rows = append(rows, row)
				continue
			}
			row.Action, row.Status = "wouldRemove", "planned"
			rows = append(rows, row)
			if input.Flags[categoryFlags[category]] {
				continue
			}
			removedTargets = appendUnique(removedTargets, seenTarget, targetID)
			if input.LedgerTargets != nil {
				for _, name := range grantingNames {
					if !input.LedgerTargets[name][targetID] {
						warnings = append(warnings, "removedUnmanagedGrant: "+detail+" for "+input.UserKey+".")
					}
				}
			}
			if input.Flags["dry-run"] {
				continue
			}
			if existing := stepsByCategory[category]; existing != nil {
				existing.IDs = append(existing.IDs, ids...)
				existing.Items = append(existing.Items, items...)
				continue
			}
			actionKey := "removedQueueMember"
			switch category {
			case "PermissionSet":
				actionKey = "removedPermissionSet"
			case "PermissionSetGroup":
				actionKey = "removedPermissionSetGroup"
			case "PublicGroup":
				actionKey = "removedPublicGroupMember"
			}
			sobject := "PermissionSetAssignment"
			if category == "PublicGroup" || category == "Queue" {
				sobject = "GroupMember"
			}
			stepsByCategory[category] = &DeleteStep{Kind: "delete", Sobject: sobject, IDs: ids, ActionKey: actionKey, Items: items}
		}
	}
	sortPlanRows(rows)
	steps := []DeleteStep{}
	for _, category := range CategoryOrder {
		if step := stepsByCategory[category]; step != nil {
			steps = append(steps, *step)
		}
	}
	return PersonaRevocationPlan{Rows: rows, Steps: steps, Warnings: warnings, RemovedTargets: removedTargets}
}
