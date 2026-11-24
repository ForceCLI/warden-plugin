package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// PublicRelatedResults returns copies of the results without the record
// of which run created the record.
func PublicRelatedResults(results []*RelatedRecordResult) []*RelatedRecordResult {
	public := make([]*RelatedRecordResult, len(results))
	for i, result := range results {
		copied := *result
		copied.CreatedInThisRun = nil
		public[i] = &copied
	}
	return public
}

// CleanupEntry is a failed user whose related records are candidates for
// deletion.
type CleanupEntry struct {
	PlanID string
	// UserSaved is true when the User save of this user succeeded.
	UserSaved bool
	// LinkedFields are the User fields the before relationships set.
	LinkedFields []string
	RelatedPlans []*RelatedRecordPlan
	Results      []*RelatedRecordResult
}

type cleanupCandidate struct {
	planID string
	result *RelatedRecordResult
}

func recordIdentity(result *RelatedRecordResult) *RelatedRecordResult {
	return &RelatedRecordResult{Relationship: result.Relationship, Phase: result.Phase, Sobject: result.Sobject, RecordID: result.RecordID}
}

// CleanupFailedPlans deletes the related records this run created for
// failed users: the after records first, then the before records, 200 to
// a request.  A before record that a saved User references is kept.  A
// delete that fails is reported and does not stop the others.  The result
// maps a plan Id to the results to add.  A nil message means Message.
func CleanupFailedPlans(conn sfapi.Conn, entries []CleanupEntry, message MessageFunc) map[string][]*RelatedRecordResult {
	if message == nil {
		message = Message
	}
	results := map[string][]*RelatedRecordResult{}
	var candidates []cleanupCandidate
	for _, entry := range entries {
		for _, result := range entry.Results {
			if result.CreatedInThisRun == nil || !*result.CreatedInThisRun || result.RecordID == "" || strings.ToLower(result.Sobject) == "user" {
				continue
			}
			field := ""
			for _, plan := range entry.RelatedPlans {
				if plan.Relationship == result.Relationship {
					if plan.LinkUser != nil {
						field = plan.LinkUser.UserField
					}
					break
				}
			}
			if entry.UserSaved && result.Phase == "before" && field != "" && contains(entry.LinkedFields, field) {
				kept := recordIdentity(result)
				kept.Action, kept.Status, kept.Detail = "skipped", "skipped", message("detailRelatedRetainedLinkedUser", field)
				results[entry.PlanID] = append(results[entry.PlanID], kept)
			} else {
				candidates = append(candidates, cleanupCandidate{planID: entry.PlanID, result: result})
			}
		}
	}
	for _, phase := range []string{"after", "before"} {
		var phaseCandidates []cleanupCandidate
		var sobjects []string
		seen := map[string]bool{}
		for _, candidate := range candidates {
			if candidate.result.Phase == phase {
				phaseCandidates = append(phaseCandidates, candidate)
				sobjects = appendUnique(sobjects, seen, candidate.result.Sobject)
			}
		}
		for _, sobject := range sobjects {
			var forSobject []cleanupCandidate
			for _, candidate := range phaseCandidates {
				if candidate.result.Sobject == sobject {
					forSobject = append(forSobject, candidate)
				}
			}
			for _, partition := range core.Batch(forSobject, RelatedDmlChunkSize) {
				ids := make([]string, len(partition))
				for i, candidate := range partition {
					ids[i] = candidate.result.RecordID
				}
				outcomes, err := conn.Delete(sobject, ids, false)
				if err != nil {
					outcomes = make([]sfapi.SaveResult, len(partition))
					for i := range outcomes {
						outcomes[i] = sfapi.SaveResult{Errors: []sfapi.SaveError{{Message: err.Error()}}}
					}
				}
				for i, candidate := range partition {
					deleted := recordIdentity(candidate.result)
					switch {
					case i < len(outcomes) && outcomes[i].Success:
						deleted.Action, deleted.Status = "deleted", "applied"
					case i < len(outcomes) && len(outcomes[i].Errors) > 0:
						deleted.Action, deleted.Status, deleted.Error = "deleteFailed", "failed", strings.Join(saveErrorTexts(outcomes[i].Errors), "; ")
					default:
						deleted.Action, deleted.Status, deleted.Error = "deleteFailed", "failed", "Related record delete returned no result."
					}
					results[candidate.planID] = append(results[candidate.planID], deleted)
				}
			}
		}
	}
	return results
}

// CleanupProvisionFailures runs CleanupFailedPlans for the failed results
// that have related records and adds its results to them.  savedPlanIDs
// names the plans whose User save succeeded.
func CleanupProvisionFailures(conn sfapi.Conn, plans []*UserPlan, results []*UserResult, savedPlanIDs []string) {
	plansByID := map[string]*UserPlan{}
	for _, plan := range plans {
		plansByID[plan.PlanID] = plan
	}
	var entries []CleanupEntry
	for _, result := range results {
		if result.Status != "failed" || len(result.RelatedRecords) == 0 {
			continue
		}
		plan := plansByID[result.PlanID]
		entry := CleanupEntry{PlanID: result.PlanID, UserSaved: contains(savedPlanIDs, result.PlanID), RelatedPlans: plan.RelatedPlans, Results: result.RelatedRecords}
		for _, related := range plan.RelatedPlans {
			if related.LinkUser == nil {
				continue
			}
			if _, set := plan.Target.Get(related.LinkUser.UserField); set {
				entry.LinkedFields = append(entry.LinkedFields, related.LinkUser.UserField)
			}
		}
		entries = append(entries, entry)
	}
	cleanup := CleanupFailedPlans(conn, entries, nil)
	for _, result := range results {
		if additions := cleanup[result.PlanID]; additions != nil {
			result.RelatedRecords = append(append([]*RelatedRecordResult{}, result.RelatedRecords...), additions...)
		}
	}
}
