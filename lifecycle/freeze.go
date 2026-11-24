package lifecycle

import (
	"errors"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// FreezeDirection is the state freeze or unfreeze moves a login to and
// the notice keys it reports.
type FreezeDirection struct {
	TargetState bool
	AlreadyKey  string
	WouldKey    string
	ActionKey   string
}

// IsAlreadyInState reports whether a UserLogin row is in the target
// state.
func (d FreezeDirection) IsAlreadyInState(loginRow sfapi.Record) bool {
	return (loginRow["IsFrozen"] == true) == d.TargetState
}

// FREEZE and UNFREEZE are the two directions.
var (
	FREEZE   = FreezeDirection{TargetState: true, AlreadyKey: "alreadyFrozen", WouldKey: "wouldFreeze", ActionKey: "frozen"}
	UNFREEZE = FreezeDirection{TargetState: false, AlreadyKey: "alreadyUnfrozen", WouldKey: "wouldUnfreeze", ActionKey: "unfrozen"}
)

// TargetOptions select the users a command acts on: one user as
// "field:value", a users definition document, or the path of a users
// definition file.
type TargetOptions struct {
	User             string
	UsersPath        string
	UsersDoc         *core.Object
	ExternalID       string
	InputFormat      string
	CsvListDelimiter string
}

func targetRequests(options TargetOptions, fieldMap *core.FieldMap) ([]core.MatchRequest, []core.TargetError, error) {
	if options.User != "" {
		parsedField, value, err := core.ParseUserFlag(options.User)
		if err != nil {
			return nil, nil, err
		}
		field, ok := core.ResolveTargetField(parsedField, fieldMap)
		if !ok {
			return nil, nil, errors.New(core.LifecycleMessage("errorInvalidUserMatchField", parsedField))
		}
		return []core.MatchRequest{{Key: field + ":" + value, Field: field, Value: value, Order: 0}}, nil, nil
	}
	if options.UsersDoc != nil {
		requests, targetErrors := core.ExtractDefTargets(options.UsersDoc, options.ExternalID, fieldMap)
		return requests, targetErrors, nil
	}
	if options.UsersPath == "" {
		return nil, nil, errors.New("A user or users definition is required.")
	}
	return core.BuildTargetRequests(core.TargetFlags{
		UsersDef:         options.UsersPath,
		ExternalID:       options.ExternalID,
		InputFormat:      options.InputFormat,
		CsvListDelimiter: options.CsvListDelimiter,
	}, fieldMap, core.DefaultTargetMessages())
}

func resolveTargetSelection(conn sfapi.Conn, options TargetOptions, fieldMap *core.FieldMap) ([]core.Target, []core.TargetError, error) {
	requests, requestErrors, err := targetRequests(options, fieldMap)
	if err != nil {
		return nil, nil, err
	}
	targets, resolutionErrors, err := core.ResolveTargets(conn, requests, fieldMap)
	if err != nil {
		return nil, nil, err
	}
	return targets, append(append([]core.TargetError{}, requestErrors...), resolutionErrors...), nil
}

// LoginUpdate is the change of one UserLogin row.
type LoginUpdate struct {
	ID       string `json:"Id"`
	IsFrozen bool   `json:"IsFrozen"`
}

func (u LoginUpdate) record() sfapi.Record {
	return sfapi.Record{"Id": u.ID, "IsFrozen": u.IsFrozen}
}

// FreezeUpdate is a planned UserLogin update and the position in the plan
// of the user it belongs to.
type FreezeUpdate struct {
	ResultIndex int         `json:"resultIndex"`
	Row         LoginUpdate `json:"row"`
	ActionKey   string      `json:"actionKey"`
}

// FreezePlan is what freeze or unfreeze would do.  Users holds the
// dry-run result of each user.
type FreezePlan struct {
	// Direction is "freeze" or "unfreeze".
	Direction string         `json:"direction"`
	Users     []*UserResult  `json:"users"`
	Updates   []FreezeUpdate `json:"updates"`
	Warnings  []string       `json:"warnings"`
}

func freezePlan(conn sfapi.Conn, options TargetOptions, direction FreezeDirection) (*FreezePlan, error) {
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	targets, selectionErrors, err := resolveTargetSelection(conn, options, fieldMap)
	if err != nil {
		return nil, err
	}
	byUserID := map[string]sfapi.Record{}
	if len(targets) > 0 {
		ids := make([]string, len(targets))
		for i, target := range targets {
			ids[i] = target.ID
		}
		page, err := conn.Query("SELECT Id, UserId, IsFrozen FROM UserLogin WHERE UserId IN (" + core.SoqlIn(ids) + ")")
		if err != nil {
			return nil, err
		}
		for _, row := range page.Records {
			byUserID[text(row, "UserId")] = row
		}
	}
	users := failedResults(selectionErrors)
	updates := []FreezeUpdate{}
	for _, target := range targets {
		login, found := byUserID[target.ID]
		var result *UserResult
		switch {
		case !found:
			result = ResolvedTargetResult(target, nil)
			result.Warnings = append(result.Warnings, core.LifecycleMessage("warningMissingUserLogin"))
		case direction.IsAlreadyInState(login):
			result = ResolvedTargetResult(target, login)
			result.Actions = append(result.Actions, MakeNotice(direction.AlreadyKey, nil, nil, nil))
		default:
			result = ResolvedTargetResult(target, login)
			result.Status = "planned"
			result.Actions = append(result.Actions, MakeNotice(direction.WouldKey, nil, nil, nil))
			updates = append(updates, FreezeUpdate{
				ResultIndex: len(users),
				Row:         LoginUpdate{ID: text(login, "Id"), IsFrozen: direction.TargetState},
				ActionKey:   direction.ActionKey,
			})
		}
		users = append(users, result)
	}
	plan := &FreezePlan{Direction: "unfreeze", Users: users, Updates: updates, Warnings: []string{}}
	if direction.TargetState {
		plan.Direction = "freeze"
	}
	return plan, nil
}

// PlanFreeze selects the users and plans the freeze of each login that is
// not frozen.  It changes no record.
func PlanFreeze(conn sfapi.Conn, options TargetOptions) (*FreezePlan, error) {
	return freezePlan(conn, options, FREEZE)
}

// PlanUnfreeze selects the users and plans the unfreeze of each login
// that is frozen.  It changes no record.
func PlanUnfreeze(conn sfapi.Conn, options TargetOptions) (*FreezePlan, error) {
	return freezePlan(conn, options, UNFREEZE)
}

// ApplyFreeze carries out a freeze or unfreeze plan with one UserLogin
// update request.  The plan is not changed.
func ApplyFreeze(conn sfapi.Conn, plan *FreezePlan) (*Result, error) {
	users := make([]*UserResult, len(plan.Users))
	for i, planned := range plan.Users {
		user := *planned
		if user.Status == "planned" {
			user.Status = "unchanged"
		}
		user.Actions = []Notice{}
		for _, action := range planned.Actions {
			if action.Key != "wouldFreeze" && action.Key != "wouldUnfreeze" {
				user.Actions = append(user.Actions, action)
			}
		}
		user.Errors = append([]string{}, planned.Errors...)
		user.Warnings = append([]string{}, planned.Warnings...)
		users[i] = &user
	}
	if len(plan.Updates) > 0 {
		rows := make([]sfapi.Record, len(plan.Updates))
		for i, update := range plan.Updates {
			rows[i] = update.Row.record()
		}
		saveResults, err := conn.Update("UserLogin", rows, false)
		if err != nil {
			return nil, err
		}
		for index, saveResult := range saveResults {
			if index >= len(plan.Updates) {
				break
			}
			user := users[plan.Updates[index].ResultIndex]
			if saveResult.Success {
				user.Status = "changed"
				user.Actions = append(user.Actions, MakeNotice(plan.Updates[index].ActionKey, nil, nil, nil))
			} else {
				user.Status = "failed"
				for _, saveError := range saveResult.Errors {
					user.Errors = append(user.Errors, saveError.Message)
				}
			}
		}
	}
	return &Result{Summary: SummarizeLifecycle(users), Users: users}, nil
}

// ApplyUnfreeze carries out an unfreeze plan.
func ApplyUnfreeze(conn sfapi.Conn, plan *FreezePlan) (*Result, error) {
	return ApplyFreeze(conn, plan)
}
