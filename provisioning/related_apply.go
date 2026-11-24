package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// RelatedRecordResult is the outcome of one relationship of one user.
type RelatedRecordResult struct {
	Relationship string `json:"relationship"`
	Phase        string `json:"phase"`
	Sobject      string `json:"sobject"`
	RecordID     string `json:"recordId,omitempty"`
	// Action is "created", "updated", "matched", "skipped", "deleted",
	// "deleteFailed" or, in a dry run, "wouldCreate", "wouldUpdate",
	// "matched" or "wouldSkip".
	Action string `json:"action"`
	// CreatedInThisRun records, for cleanup, whether this run created the
	// record.  Public results do not have it.
	CreatedInThisRun *bool `json:"createdInThisRun,omitempty"`
	// Status is "planned", "applied", "skipped" or "failed".
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func baseResult(plan *RelatedRecordPlan) *RelatedRecordResult {
	return &RelatedRecordResult{Relationship: plan.Relationship, Phase: plan.Phase, Sobject: plan.Sobject}
}

func hasChanges(plan *RelatedRecordPlan) bool {
	return len(plan.Fields.Keys) > 0 || len(plan.PendingUserIDFields) > 0
}

// ToDryRunResults returns what a live run would do for the plans.  A
// record that does not exist yet has no record Id.
func ToDryRunResults(plans []*RelatedRecordPlan) []*RelatedRecordResult {
	results := make([]*RelatedRecordResult, 0, len(plans))
	for _, plan := range plans {
		result := baseResult(plan)
		switch {
		case plan.Status == "failed":
			result.Action, result.Status, result.Error = "wouldSkip", "failed", strings.Join(plan.Errors, "; ")
		case plan.Status == "skipped":
			result.Action, result.Status = "wouldSkip", "skipped"
		case plan.ExistingID != "":
			result.RecordID, result.Action, result.Status = plan.ExistingID, "wouldUpdate", "planned"
			if !hasChanges(plan) {
				result.Action = "matched"
			}
		default:
			result.Action, result.Status = "wouldCreate", "planned"
		}
		results = append(results, result)
	}
	return results
}

// ToUnappliedResults returns the results of plans a live run did not
// reach, because the user had errors or its save failed.
func ToUnappliedResults(plans []*RelatedRecordPlan) []*RelatedRecordResult {
	results := make([]*RelatedRecordResult, 0, len(plans))
	for _, plan := range plans {
		result := baseResult(plan)
		result.Action, result.Status = "skipped", "skipped"
		if plan.Status == "failed" {
			result.Status, result.Error = "failed", strings.Join(plan.Errors, "; ")
		}
		results = append(results, result)
	}
	return results
}

func buildPayload(plan *RelatedRecordPlan, savedUserID string) sfapi.Record {
	fields := toRecord(plan.Fields)
	if savedUserID != "" {
		for _, name := range plan.PendingUserIDFields {
			fields[name] = savedUserID
		}
	}
	if plan.ExistingID != "" {
		fields["Id"] = plan.ExistingID
	} else if plan.RecordTypeID != "" {
		fields["RecordTypeId"] = plan.RecordTypeID
	}
	return fields
}

type relatedWrite struct {
	planID  string
	plan    *RelatedRecordPlan
	payload sfapi.Record
}

func recordOutcome(write relatedWrite, saveResult *sfapi.SaveResult, resultsByPlanID map[string][]*RelatedRecordResult) {
	plan := write.plan
	isUpdate := plan.ExistingID != ""
	result := baseResult(plan)
	recordID := plan.ExistingID
	if saveResult != nil && saveResult.ID != "" {
		recordID = saveResult.ID
	}
	if saveResult != nil && saveResult.Success && recordID != "" {
		created := !isUpdate
		result.RecordID = recordID
		result.Action, result.Status, result.CreatedInThisRun = "created", "applied", &created
		if isUpdate {
			result.Action = "updated"
		}
	} else {
		failure := "Related record save returned no result."
		if saveResult != nil && len(saveResult.Errors) > 0 {
			failure = strings.Join(saveErrorTexts(saveResult.Errors), "; ")
		}
		plan.Errors = append(plan.Errors, failure)
		plan.Status = "failed"
		created := false
		result.RecordID = plan.ExistingID
		result.Action, result.Status, result.CreatedInThisRun, result.Error = "skipped", "failed", &created, failure
	}
	resultsByPlanID[write.planID] = append(resultsByPlanID[write.planID], result)
}

func noCheck() error { return nil }

func runPartitionedDml(writes []relatedWrite, operation func(payloads []sfapi.Record) ([]sfapi.SaveResult, error), resultsByPlanID map[string][]*RelatedRecordResult, beforeBatch func() error) error {
	for _, partition := range core.Batch(writes, RelatedDmlChunkSize) {
		if err := beforeBatch(); err != nil {
			return err
		}
		payloads := make([]sfapi.Record, len(partition))
		for i, write := range partition {
			payloads[i] = write.payload
		}
		saveResults, err := operation(payloads)
		if err != nil {
			return err
		}
		for i, write := range partition {
			var saveResult *sfapi.SaveResult
			if i < len(saveResults) {
				saveResult = &saveResults[i]
			}
			recordOutcome(write, saveResult, resultsByPlanID)
		}
	}
	return nil
}

// RelatedPhaseEntry is the related plans of one user for a phase.
type RelatedPhaseEntry struct {
	PlanID       string
	RelatedPlans []*RelatedRecordPlan
	// SavedUserID is the Id of the saved User, for the after phase.
	SavedUserID string
	// Target holds the User fields to save, for the before phase.
	Target *core.Object
}

// ApplyRelatedPhase writes the related records of one phase: for each
// sObject one stage of creates and one of updates, 200 records to a
// request.  The result maps a plan Id to the results of its
// relationships.  beforeBatch runs before each request and stops the
// phase when it returns an error; nil means no check.
func ApplyRelatedPhase(conn sfapi.Conn, entries []RelatedPhaseEntry, phase string, beforeBatch func() error) (map[string][]*RelatedRecordResult, error) {
	if beforeBatch == nil {
		beforeBatch = noCheck
	}
	resultsByPlanID := map[string][]*RelatedRecordResult{}
	var writes []relatedWrite
	for _, entry := range entries {
		for _, plan := range entry.RelatedPlans {
			if plan.Phase != phase {
				continue
			}
			if plan.Status != "planned" {
				resultsByPlanID[entry.PlanID] = append(resultsByPlanID[entry.PlanID], ToUnappliedResults([]*RelatedRecordPlan{plan})...)
				continue
			}
			if plan.ExistingID != "" && !hasChanges(plan) {
				result := baseResult(plan)
				result.RecordID, result.Action, result.Status = plan.ExistingID, "matched", "applied"
				resultsByPlanID[entry.PlanID] = append(resultsByPlanID[entry.PlanID], result)
				continue
			}
			writes = append(writes, relatedWrite{planID: entry.PlanID, plan: plan, payload: buildPayload(plan, entry.SavedUserID)})
		}
	}
	var sobjects []string
	seen := map[string]bool{}
	for _, write := range writes {
		sobjects = appendUnique(sobjects, seen, write.plan.Sobject)
	}
	for _, sobject := range sobjects {
		var creates, updates []relatedWrite
		for _, write := range writes {
			if write.plan.Sobject != sobject {
				continue
			}
			if write.plan.ExistingID == "" {
				creates = append(creates, write)
			} else {
				updates = append(updates, write)
			}
		}
		if len(creates) > 0 {
			err := runPartitionedDml(creates, func(payloads []sfapi.Record) ([]sfapi.SaveResult, error) {
				return conn.Create(sobject, payloads, false)
			}, resultsByPlanID, beforeBatch)
			if err != nil {
				return nil, err
			}
		}
		if len(updates) > 0 {
			err := runPartitionedDml(updates, func(payloads []sfapi.Record) ([]sfapi.SaveResult, error) {
				return conn.Update(sobject, payloads, false)
			}, resultsByPlanID, beforeBatch)
			if err != nil {
				return nil, err
			}
		}
	}
	return resultsByPlanID, nil
}

func findResult(results []*RelatedRecordResult, relationship string) *RelatedRecordResult {
	for _, result := range results {
		if result.Relationship == relationship {
			return result
		}
	}
	return nil
}

type linkRead struct {
	key     string
	sobject string
	field   string
	ids     []string
	seen    map[string]bool
}

// collectLinkReads names the records this run created whose link field
// is not Id; the field has to be read back before the User save.
func collectLinkReads(entries []RelatedPhaseEntry, applied map[string][]*RelatedRecordResult) []*linkRead {
	var reads []*linkRead
	byKey := map[string]*linkRead{}
	for _, entry := range entries {
		for _, plan := range entry.RelatedPlans {
			if plan.Phase != "before" || plan.LinkUser == nil || plan.ExistingID != "" || strings.ToLower(plan.LinkUser.FromRelatedField) == "id" {
				continue
			}
			result := findResult(applied[entry.PlanID], plan.Relationship)
			if result == nil || result.Status != "applied" || result.RecordID == "" {
				continue
			}
			key := plan.Sobject + "|" + plan.LinkUser.FromRelatedField
			group := byKey[key]
			if group == nil {
				group = &linkRead{key: key, sobject: plan.Sobject, field: plan.LinkUser.FromRelatedField, seen: map[string]bool{}}
				byKey[key] = group
				reads = append(reads, group)
			}
			group.ids = appendUnique(group.ids, group.seen, result.RecordID)
		}
	}
	return reads
}

// BeforeOutcome is the results of the before relationships of one user.
type BeforeOutcome struct {
	Results []*RelatedRecordResult
	Failed  bool
}

// ApplyBeforeRelatedPhase writes the before relationships and sets the
// linked User field of each entry's Target from the related record.  A
// link value that cannot be read fails the result of that relationship.
// The result maps a plan Id to its outcome.
func ApplyBeforeRelatedPhase(conn sfapi.Conn, entries []RelatedPhaseEntry, beforeBatch func() error) (map[string]*BeforeOutcome, error) {
	if beforeBatch == nil {
		beforeBatch = noCheck
	}
	applied, err := ApplyRelatedPhase(conn, entries, "before", beforeBatch)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	readErrors := map[string]string{}
	for _, group := range collectLinkReads(entries, applied) {
		prefix := "SELECT Id, " + group.field + " FROM " + group.sobject + " WHERE Id IN ("
		chunks, err := MatchQueryBatches(group.ids, prefix, nil)
		if err != nil {
			return nil, err
		}
		for _, chunk := range chunks {
			if err := beforeBatch(); err != nil {
				return nil, err
			}
			page, err := conn.Query(prefix + core.SoqlIn(chunk) + ")")
			if err != nil {
				for _, id := range chunk {
					readErrors[group.key+"|"+id] = err.Error()
				}
				continue
			}
			for _, row := range page.Records {
				values[group.key+"|"+core.JSString(row["Id"])] = row[group.field]
			}
		}
	}
	outcomes := map[string]*BeforeOutcome{}
	for _, entry := range entries {
		results, ok := applied[entry.PlanID]
		if !ok {
			continue
		}
		for _, plan := range entry.RelatedPlans {
			if plan.Phase != "before" || plan.LinkUser == nil {
				continue
			}
			result := findResult(results, plan.Relationship)
			if result == nil || result.Status != "applied" {
				continue
			}
			key := plan.Sobject + "|" + plan.LinkUser.FromRelatedField + "|" + result.RecordID
			var value any
			if plan.ExistingID != "" {
				value = plan.LinkValue
				if written, has := plan.Fields.Get(plan.LinkUser.FromRelatedField); has {
					value = written
				}
			} else if strings.ToLower(plan.LinkUser.FromRelatedField) == "id" {
				value = result.RecordID
			} else {
				value = values[key]
			}
			if isAbsent(value) {
				result.Status = "failed"
				result.Error = "Cannot resolve " + plan.Sobject + "." + plan.LinkUser.FromRelatedField + " for User." + plan.LinkUser.UserField + "."
				if readError, failed := readErrors[key]; failed {
					result.Error += " " + readError
				}
			} else {
				entry.Target.Set(plan.LinkUser.UserField, value)
			}
		}
		outcome := &BeforeOutcome{Results: results}
		for _, result := range results {
			outcome.Failed = outcome.Failed || result.Status == "failed"
		}
		outcomes[entry.PlanID] = outcome
	}
	return outcomes, nil
}

// ApplyBeforeUserSaves writes the before relationships of the plans
// without errors and records the results on the plans.  A before
// relationship that failed adds its error to the plan, which keeps that
// user out of the User save.
func ApplyBeforeUserSaves(conn sfapi.Conn, plans []*UserPlan, beforeBatch func() error) error {
	var entries []RelatedPhaseEntry
	for _, plan := range plans {
		if len(plan.Errors) == 0 && plan.RelatedPlans != nil {
			entries = append(entries, RelatedPhaseEntry{PlanID: plan.PlanID, RelatedPlans: plan.RelatedPlans, Target: plan.Target})
		}
	}
	outcomes, err := ApplyBeforeRelatedPhase(conn, entries, beforeBatch)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		outcome := outcomes[plan.PlanID]
		if outcome == nil {
			continue
		}
		plan.RelatedResults = outcome.Results
		if !outcome.Failed {
			continue
		}
		for _, result := range outcome.Results {
			if result.Status != "failed" {
				continue
			}
			if result.Error != "" {
				plan.Errors = append(plan.Errors, result.Error)
			} else {
				plan.Errors = append(plan.Errors, "Before relationship failed.")
			}
		}
	}
	return nil
}
