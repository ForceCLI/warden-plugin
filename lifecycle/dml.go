package lifecycle

import (
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// MarkSuccess records a step that succeeded.  A result that already
// failed stays failed.
func MarkSuccess(result *UserResult, actionKey string, count *int, items []LabelBundle) {
	if result.Status != "failed" {
		result.Status = "changed"
	}
	result.Actions = append(result.Actions, MakeNotice(actionKey, count, items, nil))
}

func recordAssignmentResults(result *UserResult, saveResults []sfapi.SaveResult, items []LabelBundle, actionKey string) {
	successfulItems := []LabelBundle{}
	for index, item := range items {
		if index < len(saveResults) && saveResults[index].Success {
			successfulItems = append(successfulItems, item)
		}
	}
	if len(successfulItems) > 0 {
		MarkSuccess(result, actionKey, Count(len(successfulItems)), successfulItems)
	}
	// The result is marked failed at once so that the steps that follow
	// for the same user see the failure.
	if len(successfulItems) < len(items) {
		result.Status = "failed"
		result.Errors = append(result.Errors, core.SaveErrors(saveResults)...)
	}
}

// RunAssignmentCreates creates assignment or membership rows and records
// the items whose row was created.  items[i] names rows[i].
func RunAssignmentCreates(conn sfapi.Conn, result *UserResult, sobject string, rows []sfapi.Record, actionKey string, items []LabelBundle) error {
	if len(rows) == 0 {
		return nil
	}
	saveResults, err := conn.Create(sobject, rows, false)
	if err != nil {
		return err
	}
	recordAssignmentResults(result, saveResults, items, actionKey)
	return nil
}

// RunAssignmentDeletes deletes assignment or membership rows and records
// the items whose row was deleted.  items[i] names ids[i].
func RunAssignmentDeletes(conn sfapi.Conn, result *UserResult, sobject string, ids []string, actionKey string, items []LabelBundle) error {
	if len(ids) == 0 {
		return nil
	}
	saveResults, err := conn.Delete(sobject, ids, false)
	if err != nil {
		return err
	}
	recordAssignmentResults(result, saveResults, items, actionKey)
	return nil
}

// RunRecordUpdate updates User or UserLogin rows.  The action is recorded
// when a row was updated, and the result fails when a row was not.
func RunRecordUpdate(conn sfapi.Conn, result *UserResult, sobject string, rows []sfapi.Record, actionKey string) error {
	if len(rows) == 0 {
		return nil
	}
	saveResults, err := conn.Update(sobject, rows, false)
	if err != nil {
		return err
	}
	anySucceeded, anyFailed := false, false
	for _, saveResult := range saveResults {
		anySucceeded = anySucceeded || saveResult.Success
		anyFailed = anyFailed || !saveResult.Success
	}
	if anySucceeded {
		MarkSuccess(result, actionKey, nil, nil)
	}
	if anyFailed {
		result.Status = "failed"
		result.Errors = append(result.Errors, core.SaveErrors(saveResults)...)
	}
	return nil
}
