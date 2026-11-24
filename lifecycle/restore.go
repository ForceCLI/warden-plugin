package lifecycle

import (
	"errors"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/provisioning"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// RestoreOptions name the snapshot to restore: a document that was
// already read, or the path of a snapshot file.
type RestoreOptions struct {
	SnapshotPath string
	SnapshotDoc  *core.Object
}

// UserActivation is the update that activates a user.
type UserActivation struct {
	ID       string `json:"Id"`
	IsActive bool   `json:"IsActive"`
}

// RestoreUserPlan is what restore would do for one user.  Result is the
// live result, which ApplyRestore changes; Preview is the dry-run result.
// The Adds are the Ids to assign and the Items name them.
type RestoreUserPlan struct {
	UserID                  string          `json:"userId"`
	Result                  *UserResult     `json:"result"`
	Preview                 *UserResult     `json:"preview"`
	UserUpdate              *UserActivation `json:"userUpdate,omitempty"`
	LoginUpdates            []LoginUpdate   `json:"loginUpdates"`
	PermissionSetAdds       []string        `json:"permissionSetAdds"`
	PermissionSetGroupAdds  []string        `json:"permissionSetGroupAdds"`
	PublicGroupAdds         []string        `json:"publicGroupAdds"`
	QueueAdds               []string        `json:"queueAdds"`
	LicenseAdds             []string        `json:"licenseAdds"`
	PermissionSetItems      []LabelBundle   `json:"permissionSetItems"`
	PermissionSetGroupItems []LabelBundle   `json:"permissionSetGroupItems"`
	PublicGroupItems        []LabelBundle   `json:"publicGroupItems"`
	QueueItems              []LabelBundle   `json:"queueItems"`
	LicenseItems            []LabelBundle   `json:"licenseItems"`
}

// RestorePlan is what a restore would do.  Errors holds the snapshot
// entries whose user could not be selected.
type RestorePlan struct {
	Users    []*RestoreUserPlan `json:"users"`
	Errors   []*UserResult      `json:"errors"`
	Warnings []string           `json:"warnings"`
	Preview  *Result            `json:"preview"`
}

func identityReview(snapshotUser snapshotEntry, target core.Target) *IdentityReview {
	snapshotIdentity := ReviewIdentity{
		Name:     snapshotUser.name,
		Username: snapshotUser.username,
		Email:    snapshotUser.email,
		Profile:  snapshotUser.profile,
		Role:     snapshotUser.role,
	}
	if snapshotIdentity == (ReviewIdentity{}) {
		return nil
	}
	userID := target.ID
	return &IdentityReview{
		Snapshot: snapshotIdentity,
		Org: ReviewIdentity{
			Name:     target.Name,
			Username: target.Username,
			Email:    target.Email,
			Profile:  target.Profile,
			Role:     target.Role,
			UserID:   &userID,
		},
		Match: ReviewMatch{Field: target.Field, Value: target.Value},
	}
}

func identityMismatches(review *IdentityReview) []string {
	var mismatches []string
	for _, field := range []struct {
		name             string
		expected, actual *string
	}{
		{"name", review.Snapshot.Name, review.Org.Name},
		{"username", review.Snapshot.Username, review.Org.Username},
		{"email", review.Snapshot.Email, review.Org.Email},
		{"profile", review.Snapshot.Profile, review.Org.Profile},
		{"role", review.Snapshot.Role, review.Org.Role},
	} {
		if field.expected != nil && (field.actual == nil || *field.expected != *field.actual) {
			mismatches = append(mismatches, `Snapshot `+field.name+` "`+*field.expected+`" differs from org "`+stringOr(field.actual, "(missing)")+`".`)
		}
	}
	return mismatches
}

// referenceResolution holds the Ids the names of one category resolved
// to and the names that were not found.
type referenceResolution struct {
	resolved map[string]string
	missing  []string
}

type restoreReferences struct {
	permissionSets, permissionSetGroups, publicGroups, queues, licenses referenceResolution
}

func resolveRestoreRefs(conn sfapi.Conn, users []snapshotEntry) (*restoreReferences, error) {
	query := func(table, listKey, name, where string) (referenceResolution, error) {
		resolution := referenceResolution{resolved: map[string]string{}}
		seen := map[string]bool{}
		var unique []string
		for _, user := range users {
			for _, value := range user.lists[listKey] {
				if !seen[value] {
					seen[value] = true
					unique = append(unique, value)
				}
			}
		}
		if len(unique) == 0 {
			return resolution, nil
		}
		soql := "SELECT Id, " + name + " FROM " + table + " WHERE " + name + " IN (" + core.SoqlIn(unique) + ")"
		if where != "" {
			soql += " AND " + where
		}
		page, err := conn.Query(soql)
		if err != nil {
			return resolution, err
		}
		for _, row := range page.Records {
			resolution.resolved[text(row, name)] = text(row, "Id")
		}
		for _, value := range unique {
			if resolution.resolved[value] == "" {
				resolution.missing = append(resolution.missing, value)
			}
		}
		return resolution, nil
	}
	refs := &restoreReferences{}
	var err error
	if refs.permissionSets, err = query("PermissionSet", "permissionSets", "Name", ""); err != nil {
		return nil, err
	}
	if refs.permissionSetGroups, err = query("PermissionSetGroup", "permissionSetGroups", "DeveloperName", ""); err != nil {
		return nil, err
	}
	if refs.publicGroups, err = query("Group", "publicGroups", "DeveloperName", "Type = 'Regular'"); err != nil {
		return nil, err
	}
	if refs.queues, err = query("Group", "queues", "DeveloperName", "Type = 'Queue'"); err != nil {
		return nil, err
	}
	if refs.licenses, err = query("PermissionSetLicense", "permissionSetLicenses", "DeveloperName", ""); err != nil {
		return nil, err
	}
	return refs, nil
}

func resolvedIDs(names []string, refs referenceResolution) []string {
	ids := []string{}
	for _, name := range names {
		if id := refs.resolved[name]; id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func missingIDs(wanted, current []string) []string {
	missing := []string{}
	for _, id := range wanted {
		if !contains(current, id) {
			missing = append(missing, id)
		}
	}
	return missing
}

func fieldValues(rows []sfapi.Record, field string) []string {
	var values []string
	for _, row := range rows {
		if value := text(row, field); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func restoreItems(names []string, resolution referenceResolution, itemType string, wanted []string) []LabelBundle {
	items := []LabelBundle{}
	for _, name := range names {
		if id := resolution.resolved[name]; id != "" && contains(wanted, id) {
			items = append(items, LabelBundle{ID: id, APIName: name, Type: itemType})
		}
	}
	return items
}

func makeRestoreUserPlan(state *provisioning.AssignmentState, refs *restoreReferences, snapshotUser snapshotEntry, target core.Target) *RestoreUserPlan {
	loginRows := state.UserLoginByUserID[target.ID]
	id, isActive := target.ID, target.IsActive
	result := &UserResult{
		Key:      target.Key,
		ID:       &id,
		Name:     target.Name,
		Username: target.Username,
		IsActive: &isActive,
		IsFrozen: loginIsFrozen(firstRow(loginRows)),
		Status:   "unchanged",
		Actions:  []Notice{},
		Skipped:  []Notice{},
		Warnings: []string{},
		Errors:   []string{},
	}
	if review := identityReview(snapshotUser, target); review != nil {
		result.IdentityReview = review
		result.Warnings = append(result.Warnings, identityMismatches(review)...)
	}
	psa := state.PsaByUserID[target.ID]
	groups := state.GroupByUserID[target.ID]
	psl := state.PslByUserID[target.ID]
	permissionSets := snapshotUser.lists["permissionSets"]
	permissionSetGroups := snapshotUser.lists["permissionSetGroups"]
	publicGroups := snapshotUser.lists["publicGroups"]
	queues := snapshotUser.lists["queues"]
	licenses := snapshotUser.lists["permissionSetLicenses"]
	permissionSetAdds := missingIDs(resolvedIDs(permissionSets, refs.permissionSets), fieldValues(psa, "PermissionSetId"))
	permissionSetGroupAdds := missingIDs(resolvedIDs(permissionSetGroups, refs.permissionSetGroups), fieldValues(psa, "PermissionSetGroupId"))
	publicGroupAdds := missingIDs(resolvedIDs(publicGroups, refs.publicGroups), fieldValues(filterRows(groups, hasGroupType("Regular")), "GroupId"))
	queueAdds := missingIDs(resolvedIDs(queues, refs.queues), fieldValues(filterRows(groups, hasGroupType("Queue")), "GroupId"))
	licenseAdds := missingIDs(resolvedIDs(licenses, refs.licenses), fieldValues(psl, "PermissionSetLicenseId"))
	loginUpdates := []LoginUpdate{}
	for _, row := range loginRows {
		if row["IsFrozen"] == true {
			loginUpdates = append(loginUpdates, LoginUpdate{ID: text(row, "Id"), IsFrozen: false})
		}
	}
	var userUpdate *UserActivation
	if !target.IsActive {
		userUpdate = &UserActivation{ID: target.ID, IsActive: true}
	}
	warnMissing := func(table string, names []string, resolution referenceResolution) {
		for _, name := range names {
			if contains(resolution.missing, name) {
				result.Warnings = append(result.Warnings, provisioning.Message("warningReferenceMissing", table, name))
			}
		}
	}
	warnMissing("PermissionSet", permissionSets, refs.permissionSets)
	warnMissing("PermissionSetGroup", permissionSetGroups, refs.permissionSetGroups)
	warnMissing("Group", publicGroups, refs.publicGroups)
	warnMissing("Group", queues, refs.queues)
	warnMissing("PermissionSetLicense", licenses, refs.licenses)
	pendingDml := userUpdate != nil || len(loginUpdates) > 0
	preview := *result
	preview.Status = "unchanged"
	preview.Actions = []Notice{}
	if userUpdate != nil {
		preview.Actions = append(preview.Actions, MakeNotice("wouldActivate", nil, nil, nil))
	}
	if len(loginUpdates) > 0 {
		preview.Actions = append(preview.Actions, MakeNotice("wouldUnfreeze", nil, nil, nil))
	}
	action := func(key string, values []string, entries []LabelBundle) {
		if len(values) > 0 {
			preview.Status = "planned"
			preview.Actions = append(preview.Actions, MakeNotice(key, Count(len(values)), entries, nil))
		}
	}
	if pendingDml {
		preview.Status = "planned"
	}
	plan := &RestoreUserPlan{
		UserID:                  target.ID,
		Result:                  result,
		Preview:                 &preview,
		UserUpdate:              userUpdate,
		LoginUpdates:            loginUpdates,
		PermissionSetAdds:       permissionSetAdds,
		PermissionSetGroupAdds:  permissionSetGroupAdds,
		PublicGroupAdds:         publicGroupAdds,
		QueueAdds:               queueAdds,
		LicenseAdds:             licenseAdds,
		PermissionSetItems:      restoreItems(permissionSets, refs.permissionSets, "PermissionSet", permissionSetAdds),
		PermissionSetGroupItems: restoreItems(permissionSetGroups, refs.permissionSetGroups, "PermissionSetGroup", permissionSetGroupAdds),
		PublicGroupItems:        restoreItems(publicGroups, refs.publicGroups, "PublicGroup", publicGroupAdds),
		QueueItems:              restoreItems(queues, refs.queues, "Queue", queueAdds),
		LicenseItems:            restoreItems(licenses, refs.licenses, "PermissionSetLicense", licenseAdds),
	}
	action("wouldAssignPermissionSet", permissionSetAdds, plan.PermissionSetItems)
	action("wouldAssignPermissionSetGroup", permissionSetGroupAdds, plan.PermissionSetGroupItems)
	action("wouldAddPublicGroupMember", publicGroupAdds, plan.PublicGroupItems)
	action("wouldAddQueueMember", queueAdds, plan.QueueItems)
	action("wouldAssignPermissionSetLicense", licenseAdds, plan.LicenseItems)
	return plan
}

func assignmentRows(userField, userID, targetField string, targetIDs []string) []sfapi.Record {
	rows := make([]sfapi.Record, len(targetIDs))
	for i, targetID := range targetIDs {
		rows[i] = sfapi.Record{userField: userID, targetField: targetID}
	}
	return rows
}

func applyRestoreUserPlan(conn sfapi.Conn, plan *RestoreUserPlan) error {
	result := plan.Result
	if plan.UserUpdate != nil {
		if err := RunRecordUpdate(conn, result, "User", []sfapi.Record{{"Id": plan.UserUpdate.ID, "IsActive": plan.UserUpdate.IsActive}}, "activated"); err != nil {
			return err
		}
	}
	loginRows := make([]sfapi.Record, len(plan.LoginUpdates))
	for i, update := range plan.LoginUpdates {
		loginRows[i] = update.record()
	}
	if err := RunRecordUpdate(conn, result, "UserLogin", loginRows, "unfrozen"); err != nil {
		return err
	}
	for _, create := range []struct {
		sobject   string
		rows      []sfapi.Record
		actionKey string
		items     []LabelBundle
	}{
		{"PermissionSetAssignment", assignmentRows("AssigneeId", plan.UserID, "PermissionSetId", plan.PermissionSetAdds), "assignedPermissionSet", plan.PermissionSetItems},
		{"PermissionSetAssignment", assignmentRows("AssigneeId", plan.UserID, "PermissionSetGroupId", plan.PermissionSetGroupAdds), "assignedPermissionSetGroup", plan.PermissionSetGroupItems},
		{"GroupMember", assignmentRows("UserOrGroupId", plan.UserID, "GroupId", plan.PublicGroupAdds), "addedPublicGroupMember", plan.PublicGroupItems},
		{"GroupMember", assignmentRows("UserOrGroupId", plan.UserID, "GroupId", plan.QueueAdds), "addedQueueMember", plan.QueueItems},
		{"PermissionSetLicenseAssign", assignmentRows("AssigneeId", plan.UserID, "PermissionSetLicenseId", plan.LicenseAdds), "assignedPermissionSetLicense", plan.LicenseItems},
	} {
		if err := RunAssignmentCreates(conn, result, create.sobject, create.rows, create.actionKey, create.items); err != nil {
			return err
		}
	}
	return nil
}

// PlanRestore reads the snapshot, selects its users and plans what is
// missing for each: activation, unfreeze and the assignments the user no
// longer has.  It never plans a removal and changes no record.
func PlanRestore(conn sfapi.Conn, options RestoreOptions) (*RestorePlan, error) {
	var snapshotFile *core.Object
	var err error
	switch {
	case options.SnapshotDoc != nil:
		snapshotFile, err = ParseSnapshotDocument(options.SnapshotDoc)
	case options.SnapshotPath != "":
		snapshotFile, err = ReadSnapshotFile(options.SnapshotPath)
	default:
		return nil, errors.New("A snapshot document or path is required.")
	}
	if err != nil {
		return nil, err
	}
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	snapshotUsers := snapshotEntries(snapshotFile)
	var requests []core.MatchRequest
	failed := []*UserResult{}
	for order, user := range snapshotUsers {
		field, ok := core.ResolveTargetField(user.match, fieldMap)
		if !ok {
			failed = append(failed, &UserResult{
				Key:      user.match + ":" + user.matchValue,
				Status:   "failed",
				Actions:  []Notice{},
				Skipped:  []Notice{},
				Warnings: []string{},
				Errors:   []string{core.LifecycleMessage("errorInvalidUserMatchField", user.match)},
			})
			continue
		}
		requests = append(requests, core.MatchRequest{Key: field + ":" + user.matchValue, Field: field, Value: user.matchValue, Order: order})
	}
	targets, resolutionErrors, err := core.ResolveTargets(conn, requests, fieldMap)
	if err != nil {
		return nil, err
	}
	failed = append(failed, failedResults(resolutionErrors)...)
	targetIDs := make([]string, len(targets))
	for i, target := range targets {
		targetIDs[i] = target.ID
	}
	state, err := provisioning.LoadAssignmentState(conn, targetIDs, nil)
	if err != nil {
		return nil, err
	}
	refs, err := resolveRestoreRefs(conn, snapshotUsers)
	if err != nil {
		return nil, err
	}
	plans := make([]*RestoreUserPlan, 0, len(targets))
	warnings := []string{}
	previewUsers := append([]*UserResult{}, failed...)
	for _, target := range targets {
		plan := makeRestoreUserPlan(state, refs, snapshotUsers[target.Order], target)
		plans = append(plans, plan)
		warnings = append(warnings, plan.Preview.Warnings...)
		previewUsers = append(previewUsers, plan.Preview)
	}
	return &RestorePlan{
		Users:    plans,
		Errors:   failed,
		Warnings: warnings,
		Preview:  &Result{Summary: SummarizeLifecycle(previewUsers), Users: previewUsers},
	}, nil
}

// ApplyRestore carries out a restore plan, one user after the other, and
// returns the live results.  It changes the Result of each user plan.
func ApplyRestore(conn sfapi.Conn, plan *RestorePlan) (*Result, error) {
	for _, user := range plan.Users {
		if err := applyRestoreUserPlan(conn, user); err != nil {
			return nil, err
		}
	}
	users := append([]*UserResult{}, plan.Errors...)
	for _, user := range plan.Users {
		users = append(users, user.Result)
	}
	return &Result{Summary: SummarizeLifecycle(users), Users: users}, nil
}
