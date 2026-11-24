package provisioning

import (
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

const dryRunCreateID = "dry-run-create"

func appendAssignmentActions(actions *[]string, dmlPlan AssignmentDmlPlan) {
	addActionIfAny := func(values []string, action string) {
		if len(values) > 0 {
			*actions = append(*actions, action)
		}
	}
	addActionIfAny(dmlPlan.PermissionSets.Adds, "wouldAssignPermissionSet")
	addActionIfAny(dmlPlan.PermissionSets.Removes, "wouldRemovePermissionSet")
	addActionIfAny(dmlPlan.PermissionSetGroups.Adds, "wouldAssignPermissionSetGroup")
	addActionIfAny(dmlPlan.PermissionSetGroups.Removes, "wouldRemovePermissionSetGroup")
	addActionIfAny(dmlPlan.PublicGroups.Adds, "wouldAddPublicGroupMember")
	addActionIfAny(dmlPlan.PublicGroups.Removes, "wouldRemovePublicGroupMember")
	addActionIfAny(dmlPlan.Queues.Adds, "wouldAddQueueMember")
	addActionIfAny(dmlPlan.Queues.Removes, "wouldRemoveQueueMember")
}

func performAssignmentDml(conn sfapi.Conn, userID string, dmlPlan AssignmentDmlPlan, actions, errors *[]string, beforeWrite func() error) error {
	runDml := func(values []string, action string, operation func() ([]sfapi.SaveResult, error)) error {
		if len(values) == 0 {
			return nil
		}
		if err := beforeWrite(); err != nil {
			return err
		}
		saveResults, err := operation()
		if err != nil {
			return err
		}
		for _, saveResult := range saveResults {
			if saveResult.Success {
				*actions = append(*actions, action)
				break
			}
		}
		*errors = append(*errors, core.SaveErrors(saveResults)...)
		return nil
	}
	create := func(sobject string, ids []string, record func(id string) sfapi.Record) func() ([]sfapi.SaveResult, error) {
		return func() ([]sfapi.SaveResult, error) {
			records := make([]sfapi.Record, len(ids))
			for i, id := range ids {
				records[i] = record(id)
			}
			return conn.Create(sobject, records, false)
		}
	}
	remove := func(sobject string, ids []string) func() ([]sfapi.SaveResult, error) {
		return func() ([]sfapi.SaveResult, error) { return conn.Delete(sobject, ids, false) }
	}
	groupMember := func(groupID string) sfapi.Record {
		return sfapi.Record{"GroupId": groupID, "UserOrGroupId": userID}
	}
	stages := []struct {
		values    []string
		action    string
		operation func() ([]sfapi.SaveResult, error)
	}{
		{dmlPlan.PermissionSets.Adds, "assignedPermissionSet", create("PermissionSetAssignment", dmlPlan.PermissionSets.Adds, func(id string) sfapi.Record {
			return sfapi.Record{"AssigneeId": userID, "PermissionSetId": id}
		})},
		{dmlPlan.PermissionSets.Removes, "removedPermissionSet", remove("PermissionSetAssignment", dmlPlan.PermissionSets.Removes)},
		{dmlPlan.PermissionSetGroups.Adds, "assignedPermissionSetGroup", create("PermissionSetAssignment", dmlPlan.PermissionSetGroups.Adds, func(id string) sfapi.Record {
			return sfapi.Record{"AssigneeId": userID, "PermissionSetGroupId": id}
		})},
		{dmlPlan.PermissionSetGroups.Removes, "removedPermissionSetGroup", remove("PermissionSetAssignment", dmlPlan.PermissionSetGroups.Removes)},
		{dmlPlan.PublicGroups.Adds, "addedPublicGroupMember", create("GroupMember", dmlPlan.PublicGroups.Adds, groupMember)},
		{dmlPlan.PublicGroups.Removes, "removedPublicGroupMember", remove("GroupMember", dmlPlan.PublicGroups.Removes)},
		{dmlPlan.Queues.Adds, "addedQueueMember", create("GroupMember", dmlPlan.Queues.Adds, groupMember)},
		{dmlPlan.Queues.Removes, "removedQueueMember", remove("GroupMember", dmlPlan.Queues.Removes)},
	}
	for _, stage := range stages {
		if err := runDml(stage.values, stage.action, stage.operation); err != nil {
			return err
		}
	}
	return nil
}

func applyAssignments(conn sfapi.Conn, userID string, persona *Persona, refs *ResolvedRefs, dryRun bool, actions, errors *[]string, beforeWrite func() error) error {
	intended, err := HasAssignmentIntent(persona, refs)
	if err != nil || !intended {
		return err
	}
	var assignmentRows, membershipRows []sfapi.Record
	if userID != dryRunCreateID {
		state, err := LoadAssignmentState(conn, []string{userID}, &AssignmentStateOptions{PermissionSetAssignments: true, GroupMemberships: true})
		if err != nil {
			return err
		}
		assignmentRows = state.PsaByUserID[userID]
		membershipRows = state.GroupByUserID[userID]
	}
	delta, err := ComputeAssignmentDeltaFromState(persona, refs, assignmentRows, membershipRows)
	if err != nil {
		return err
	}
	dmlPlan := ToDmlPlan(delta, assignmentRows, membershipRows)
	if dryRun {
		appendAssignmentActions(actions, dmlPlan)
		return nil
	}
	return performAssignmentDml(conn, userID, dmlPlan, actions, errors, beforeWrite)
}

// SaveOutcome is the outcome of the save of one User.
type SaveOutcome struct {
	Plan    *UserPlan
	Success bool
	// ID is the Id of the saved user, or "" when the save returned none.
	ID     string
	Errors []string
}

// ExecuteBulkUserSaves saves the users of the plans without errors: the
// new users in one create call and the existing users in one update call,
// each with allOrNone false.  beforeStage runs before each call and stops
// the saves when it returns an error; nil means no check.
func ExecuteBulkUserSaves(conn sfapi.Conn, plans []*UserPlan, beforeStage func() error) ([]*SaveOutcome, error) {
	if beforeStage == nil {
		beforeStage = noCheck
	}
	var createPlans, updatePlans []*UserPlan
	for _, plan := range plans {
		if len(plan.Errors) > 0 {
			continue
		}
		if plan.Existing == nil {
			createPlans = append(createPlans, plan)
		} else {
			updatePlans = append(updatePlans, plan)
		}
	}
	outcomes := []*SaveOutcome{}
	collect := func(stagePlans []*UserPlan, saveResults []sfapi.SaveResult) {
		for i, plan := range stagePlans {
			outcome := &SaveOutcome{Plan: plan, Errors: []string{}}
			if i < len(saveResults) {
				outcome.Success = saveResults[i].Success && saveResults[i].ID != ""
				outcome.ID = saveResults[i].ID
				outcome.Errors = saveErrorTexts(saveResults[i].Errors)
			}
			if outcome.ID == "" && plan.Existing != nil {
				outcome.ID = plan.Existing.ID
			}
			outcomes = append(outcomes, outcome)
		}
	}
	if len(createPlans) > 0 {
		if err := beforeStage(); err != nil {
			return nil, err
		}
		records := make([]sfapi.Record, len(createPlans))
		for i, plan := range createPlans {
			records[i] = toRecord(plan.Target)
		}
		saveResults, err := conn.Create("User", records, false)
		if err != nil {
			return nil, err
		}
		collect(createPlans, saveResults)
	}
	if len(updatePlans) > 0 {
		if err := beforeStage(); err != nil {
			return nil, err
		}
		records := make([]sfapi.Record, len(updatePlans))
		for i, plan := range updatePlans {
			records[i] = toRecord(plan.Target)
			records[i]["Id"] = plan.Existing.ID
		}
		saveResults, err := conn.Update("User", records, false)
		if err != nil {
			return nil, err
		}
		collect(updatePlans, saveResults)
	}
	return outcomes, nil
}

// MarkRelatedUnapplied records the related plans of a user that the run
// did not attempt as skipped results.
func MarkRelatedUnapplied(plan *UserPlan) *UserPlan {
	if plan.RelatedPlans != nil {
		var unattempted []*RelatedRecordPlan
		for _, related := range plan.RelatedPlans {
			if findResult(plan.RelatedResults, related.Relationship) == nil {
				unattempted = append(unattempted, related)
			}
		}
		plan.RelatedResults = append(append([]*RelatedRecordResult{}, plan.RelatedResults...), ToUnappliedResults(unattempted)...)
	}
	return plan
}

// QueryFrozenLoginIDs returns the Ids of the frozen UserLogin rows of a
// user.
func QueryFrozenLoginIDs(conn sfapi.Conn, userID string) ([]string, error) {
	page, err := conn.Query("SELECT Id FROM UserLogin WHERE UserId = '" + core.Esc(userID) + "' AND IsFrozen = true")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(page.Records))
	for i, row := range page.Records {
		ids[i] = text(row, "Id")
	}
	return ids, nil
}

// PlanDryRunResult returns what a live run would do for a plan.  It adds
// the actions for the unfreeze and the assignment changes to the plan and
// changes no record.
func PlanDryRunResult(conn sfapi.Conn, plan *UserPlan, refs *ResolvedRefs) (*UserResult, error) {
	if plan.RelatedPlans != nil {
		plan.RelatedResults = ToDryRunResults(plan.RelatedPlans)
	}
	if len(plan.Errors) > 0 {
		return ToUserResult(plan, "failed", &UserResultOverrides{OmitExistingID: true}), nil
	}
	dryRunID := dryRunCreateID
	if plan.Existing != nil {
		if plan.Existing.ID != "" {
			dryRunID = plan.Existing.ID
		}
		frozen, err := QueryFrozenLoginIDs(conn, plan.Existing.ID)
		if err != nil {
			return nil, err
		}
		if len(frozen) > 0 {
			plan.Actions = append(plan.Actions, "wouldUnfreeze")
		}
	}
	if err := applyAssignments(conn, dryRunID, &plan.EffectivePersona, refs, true, &plan.Actions, &plan.Errors, noCheck); err != nil {
		return nil, err
	}
	return ToUserResult(plan, "planned", nil), nil
}

// SavedPlanOptions is what ApplySavedPlan applies.
type SavedPlanOptions struct {
	Conn    sfapi.Conn
	Outcome *SaveOutcome
	Refs    *ResolvedRefs
	// Message is the text lookup; nil means Message.
	Message MessageFunc
	// BeforeWrite runs before each record change and stops the work when
	// it returns an error; nil means no check.
	BeforeWrite func() error
}

// ApplySavedPlan does the work that follows the save of a User: it
// unfreezes the frozen logins of the user and makes the assignment
// changes.  The status of the result is "failed" when the plan has
// errors, otherwise "updated" or "created".
func ApplySavedPlan(options SavedPlanOptions) (*UserResult, error) {
	message := options.Message
	if message == nil {
		message = Message
	}
	beforeWrite := options.BeforeWrite
	if beforeWrite == nil {
		beforeWrite = noCheck
	}
	plan, id := options.Outcome.Plan, options.Outcome.ID
	if id == "" {
		return ToUserResult(plan, "failed", &UserResultOverrides{OmitExistingID: true, Errors: []string{message("errorMissingSaveId")}}), nil
	}
	frozenIDs, err := QueryFrozenLoginIDs(options.Conn, id)
	if err != nil {
		return nil, err
	}
	if len(frozenIDs) > 0 {
		if err := beforeWrite(); err != nil {
			return nil, err
		}
		records := make([]sfapi.Record, len(frozenIDs))
		for i, loginID := range frozenIDs {
			records[i] = sfapi.Record{"Id": loginID, "IsFrozen": false}
		}
		unfreezeResults, err := options.Conn.Update("UserLogin", records, false)
		if err != nil {
			return nil, err
		}
		if unfreezeErrors := core.SaveErrors(unfreezeResults); len(unfreezeErrors) > 0 {
			plan.Errors = append(plan.Errors, unfreezeErrors...)
		} else {
			plan.Actions = append(plan.Actions, "unfrozen")
		}
	}
	if err := applyAssignments(options.Conn, id, &plan.EffectivePersona, options.Refs, false, &plan.Actions, &plan.Errors, beforeWrite); err != nil {
		return nil, err
	}
	status := "created"
	if len(plan.Errors) > 0 {
		status = "failed"
	} else if plan.Existing != nil {
		status = "updated"
	}
	return ToUserResult(plan, status, &UserResultOverrides{ID: id}), nil
}
