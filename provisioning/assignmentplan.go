package provisioning

import "github.com/ForceCLI/warden-plugin/sfapi"

// ResolvedRefs holds the Ids the references of the personas and users
// resolved to.  Each map has an entry for a reference by the text used in
// the definition.
type ResolvedRefs struct {
	ProfilesByRef              *RefMap `json:"profilesByRef"`
	RolesByRef                 *RefMap `json:"rolesByRef"`
	PermissionSetIDsByRef      *RefMap `json:"permissionSetIdsByRef"`
	PermissionSetGroupIDsByRef *RefMap `json:"permissionSetGroupIdsByRef"`
	PublicGroupIDsByRef        *RefMap `json:"publicGroupIdsByRef"`
	QueueIDsByRef              *RefMap `json:"queueIdsByRef"`
	// Labels maps the Id of each resolved record to its names.
	Labels *LabelMap `json:"labels,omitempty"`
	// Warnings names the references that were not found.
	Warnings []string `json:"warnings"`
}

// NewResolvedRefs returns references with empty maps.
func NewResolvedRefs() *ResolvedRefs {
	return &ResolvedRefs{
		ProfilesByRef:              NewRefMap(),
		RolesByRef:                 NewRefMap(),
		PermissionSetIDsByRef:      NewRefMap(),
		PermissionSetGroupIDsByRef: NewRefMap(),
		PublicGroupIDsByRef:        NewRefMap(),
		QueueIDsByRef:              NewRefMap(),
		Warnings:                   []string{},
	}
}

// AssignmentCategoryDelta compares the Ids a user holds in one category
// with the Ids the persona names.
type AssignmentCategoryDelta struct {
	Adds    []string `json:"adds"`
	Removes []string `json:"removes"`
	InBoth  []string `json:"inBoth"`
	// OnlyInOrg holds the Ids the user has, the persona does not name and
	// the mode does not remove.
	OnlyInOrg []string `json:"onlyInOrg"`
	Mode      string   `json:"mode,omitempty"`
}

// AssignmentDelta is the delta of each category.
type AssignmentDelta struct {
	PermissionSets      AssignmentCategoryDelta `json:"permissionSets"`
	PermissionSetGroups AssignmentCategoryDelta `json:"permissionSetGroups"`
	PublicGroups        AssignmentCategoryDelta `json:"publicGroups"`
	Queues              AssignmentCategoryDelta `json:"queues"`
}

// AssignmentChanges are the Ids to assign and the record Ids to delete in
// one category.
type AssignmentChanges struct {
	Adds    []string `json:"adds"`
	Removes []string `json:"removes"`
}

// AssignmentDmlPlan is the changes of each category.
type AssignmentDmlPlan struct {
	PermissionSets      AssignmentChanges `json:"permissionSets"`
	PermissionSetGroups AssignmentChanges `json:"permissionSetGroups"`
	PublicGroups        AssignmentChanges `json:"publicGroups"`
	Queues              AssignmentChanges `json:"queues"`
}

func resolveTargets(refs []string, resolved *RefMap) []string {
	targets := []string{}
	for _, ref := range refs {
		if id := resolved.Get(ref); id != "" {
			targets = append(targets, id)
		}
	}
	return targets
}

// HasAssignmentIntent reports whether the persona names a resolved
// assignment or has a category in sync mode.
func HasAssignmentIntent(persona *Persona, refs *ResolvedRefs) (bool, error) {
	targetCount := len(resolveTargets(persona.PermissionSets, refs.PermissionSetIDsByRef)) +
		len(resolveTargets(persona.PermissionSetGroups, refs.PermissionSetGroupIDsByRef)) +
		len(resolveTargets(persona.PublicGroups, refs.PublicGroupIDsByRef)) +
		len(resolveTargets(persona.Queues, refs.QueueIDsByRef))
	if targetCount > 0 {
		return true, nil
	}
	for _, raw := range []string{persona.PermissionSetMode, persona.PermissionSetGroupMode, persona.PublicGroupMode, persona.QueueMode} {
		mode, err := NormalizeMode(raw)
		if err != nil {
			return false, err
		}
		if mode == "sync" {
			return true, nil
		}
	}
	return false, nil
}

func computeCategoryDelta(currentIDs, targetIDs []string, modeRaw string) (AssignmentCategoryDelta, error) {
	current := unique(currentIDs)
	target := unique(targetIDs)
	inCurrent := map[string]bool{}
	for _, id := range current {
		inCurrent[id] = true
	}
	inTarget := map[string]bool{}
	for _, id := range target {
		inTarget[id] = true
	}
	mode, err := NormalizeMode(modeRaw)
	if err != nil {
		return AssignmentCategoryDelta{}, err
	}
	delta := AssignmentCategoryDelta{Adds: []string{}, Removes: []string{}, InBoth: []string{}, OnlyInOrg: []string{}, Mode: mode}
	for _, id := range current {
		if inTarget[id] {
			continue
		}
		if mode == "sync" {
			delta.Removes = append(delta.Removes, id)
		} else {
			delta.OnlyInOrg = append(delta.OnlyInOrg, id)
		}
	}
	for _, id := range target {
		if inCurrent[id] {
			delta.InBoth = append(delta.InBoth, id)
		} else {
			delta.Adds = append(delta.Adds, id)
		}
	}
	return delta, nil
}

// CurrentIDs are the Ids a user holds in each category.
type CurrentIDs struct {
	PermissionSets      []string `json:"permissionSets"`
	PermissionSetGroups []string `json:"permissionSetGroups"`
	PublicGroups        []string `json:"publicGroups"`
	Queues              []string `json:"queues"`
}

func groupType(row sfapi.Record) string {
	return text(nested(row, "Group"), "Type")
}

// ExtractCurrentIDs reads the Ids a user holds from its
// PermissionSetAssignment and GroupMember rows.  An assignment that came
// from a permission set group or from a profile is not a permission set
// of the user.
func ExtractCurrentIDs(assignmentRows, membershipRows []sfapi.Record) CurrentIDs {
	current := CurrentIDs{PermissionSets: []string{}, PermissionSetGroups: []string{}, PublicGroups: []string{}, Queues: []string{}}
	for _, row := range assignmentRows {
		if row["PermissionSetGroupId"] == nil && nested(row, "PermissionSet")["IsOwnedByProfile"] != true {
			if id := text(row, "PermissionSetId"); id != "" {
				current.PermissionSets = append(current.PermissionSets, id)
			}
		}
	}
	for _, row := range assignmentRows {
		if id := text(row, "PermissionSetGroupId"); id != "" {
			current.PermissionSetGroups = append(current.PermissionSetGroups, id)
		}
	}
	for _, row := range membershipRows {
		switch groupType(row) {
		case "Regular":
			current.PublicGroups = append(current.PublicGroups, text(row, "GroupId"))
		case "Queue":
			current.Queues = append(current.Queues, text(row, "GroupId"))
		}
	}
	return current
}

// ComputeAssignmentDeltaFromState compares what the persona names, as
// resolved by refs, with the rows of a user.
func ComputeAssignmentDeltaFromState(persona *Persona, refs *ResolvedRefs, assignmentRows, membershipRows []sfapi.Record) (AssignmentDelta, error) {
	current := ExtractCurrentIDs(assignmentRows, membershipRows)
	var delta AssignmentDelta
	var err error
	if delta.PermissionSets, err = computeCategoryDelta(current.PermissionSets, resolveTargets(persona.PermissionSets, refs.PermissionSetIDsByRef), persona.PermissionSetMode); err != nil {
		return delta, err
	}
	if delta.PermissionSetGroups, err = computeCategoryDelta(current.PermissionSetGroups, resolveTargets(persona.PermissionSetGroups, refs.PermissionSetGroupIDsByRef), persona.PermissionSetGroupMode); err != nil {
		return delta, err
	}
	if delta.PublicGroups, err = computeCategoryDelta(current.PublicGroups, resolveTargets(persona.PublicGroups, refs.PublicGroupIDsByRef), persona.PublicGroupMode); err != nil {
		return delta, err
	}
	if delta.Queues, err = computeCategoryDelta(current.Queues, resolveTargets(persona.Queues, refs.QueueIDsByRef), persona.QueueMode); err != nil {
		return delta, err
	}
	return delta, nil
}

// ToDmlPlan turns a delta into record changes: the adds stay target Ids
// and each remove becomes the Id of the assignment or membership row.
func ToDmlPlan(delta AssignmentDelta, assignmentRows, membershipRows []sfapi.Record) AssignmentDmlPlan {
	findAssignmentIDs := func(ids []string, field string) []string {
		found := []string{}
		for _, row := range assignmentRows {
			if value := text(row, field); value != "" && contains(ids, value) {
				found = append(found, text(row, "Id"))
			}
		}
		return found
	}
	findMembershipIDs := func(ids []string, wantedType string) []string {
		found := []string{}
		for _, row := range membershipRows {
			if groupType(row) == wantedType && contains(ids, text(row, "GroupId")) {
				found = append(found, text(row, "Id"))
			}
		}
		return found
	}
	return AssignmentDmlPlan{
		PermissionSets:      AssignmentChanges{Adds: delta.PermissionSets.Adds, Removes: findAssignmentIDs(delta.PermissionSets.Removes, "PermissionSetId")},
		PermissionSetGroups: AssignmentChanges{Adds: delta.PermissionSetGroups.Adds, Removes: findAssignmentIDs(delta.PermissionSetGroups.Removes, "PermissionSetGroupId")},
		PublicGroups:        AssignmentChanges{Adds: delta.PublicGroups.Adds, Removes: findMembershipIDs(delta.PublicGroups.Removes, "Regular")},
		Queues:              AssignmentChanges{Adds: delta.Queues.Adds, Removes: findMembershipIDs(delta.Queues.Removes, "Queue")},
	}
}
