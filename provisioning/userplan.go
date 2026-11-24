package provisioning

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

// UserPlan is what provisioning will do for one user.  Applying a plan
// changes it: errors, actions and related results are added and a before
// relationship can set a field of Target.
type UserPlan struct {
	// PlanID is the position of the user, its key and its persona names.
	PlanID           string
	Order            int
	Key              string
	Personas         []string
	EffectivePersona Persona
	// MatchedBy is the field the user is matched on, or nil.
	MatchedBy *string
	// MatchValue is the value that was looked up, or nil when the field
	// has no usable value.
	MatchValue *string
	// Target holds the User fields to save.
	Target *core.Object
	// Existing is the user that was matched, or nil for a new user.
	Existing       *core.ExistingUser
	Actions        []string
	Errors         []string
	Source         *core.RowInfo
	RelatedPlans   []*RelatedRecordPlan
	RelatedResults []*RelatedRecordResult
}

func sourceJSON(source *core.RowInfo) any {
	object := core.NewObject()
	object.Set("path", source.Path)
	object.Set("line", float64(source.Line))
	return object
}

// MarshalJSON writes the plan with the keys of the upstream plan.
func (p *UserPlan) MarshalJSON() ([]byte, error) {
	object := core.NewObject()
	object.Set("planId", p.PlanID)
	object.Set("order", p.Order)
	object.Set("key", p.Key)
	object.Set("personas", nonNil(p.Personas))
	object.Set("effectivePersona", p.EffectivePersona)
	object.Set("matchedBy", p.MatchedBy)
	object.Set("matchValue", p.MatchValue)
	object.Set("target", p.Target)
	if p.Existing != nil {
		object.Set("existing", p.Existing.Row)
	}
	object.Set("actions", nonNil(p.Actions))
	object.Set("errors", nonNil(p.Errors))
	if p.Source != nil {
		object.Set("source", sourceJSON(p.Source))
	}
	if p.RelatedPlans != nil {
		object.Set("relatedPlans", p.RelatedPlans)
	}
	if p.RelatedResults != nil {
		object.Set("relatedResults", p.RelatedResults)
	}
	return object.MarshalJSON()
}

// UserResult is the outcome for one user before it is made public: it
// also carries the plan Id, the position and the CSV row of the user.
type UserResult struct {
	PlanID string
	Order  int
	Key    string
	// UserName and Username are nil when they are not known.
	UserName       *string
	Username       *string
	ID             string
	Personas       []string
	MatchedBy      *string
	MatchValue     *string
	Matched        bool
	Status         string
	Actions        []string
	Errors         []string
	RelatedRecords []*RelatedRecordResult
	Source         *core.RowInfo
}

// MarshalJSON writes the result with the keys of the upstream result.
func (r *UserResult) MarshalJSON() ([]byte, error) {
	object := core.NewObject()
	object.Set("planId", r.PlanID)
	object.Set("order", r.Order)
	object.Set("key", r.Key)
	if r.UserName != nil {
		object.Set("userName", *r.UserName)
	}
	if r.Username != nil {
		object.Set("username", *r.Username)
	}
	if r.ID != "" {
		object.Set("id", r.ID)
	}
	object.Set("personas", nonNil(r.Personas))
	object.Set("matchedBy", r.MatchedBy)
	object.Set("matchValue", r.MatchValue)
	object.Set("matched", r.Matched)
	object.Set("status", r.Status)
	object.Set("actions", nonNil(r.Actions))
	object.Set("errors", nonNil(r.Errors))
	if len(r.RelatedRecords) > 0 {
		object.Set("relatedRecords", r.RelatedRecords)
	}
	if r.Source != nil {
		object.Set("source", sourceJSON(r.Source))
	}
	return object.MarshalJSON()
}

// IdentityFromTarget returns the name and the username of a user: those
// of the existing user when it has them, otherwise the Name, or the first
// and last name, and the Username of the target fields.  A value that is
// not known is nil.
func IdentityFromTarget(target *core.Object, existing *core.ExistingUser) (userName, username *string) {
	if existing != nil {
		if name, ok := existing.Row["Name"].(string); ok {
			userName = &name
		}
		if login, ok := existing.Row["Username"].(string); ok {
			username = &login
		}
	}
	if userName == nil {
		if name, ok := target.String("Name"); ok {
			userName = &name
		} else {
			var parts []string
			for _, field := range []string{"FirstName", "LastName"} {
				if part, _ := target.String(field); part != "" {
					parts = append(parts, part)
				}
			}
			if joined := strings.Join(parts, " "); joined != "" {
				userName = &joined
			}
		}
	}
	if username == nil {
		if login, ok := target.String("Username"); ok {
			username = &login
		}
	}
	return userName, username
}

func ensureWritableFields(target *core.Object, existing *core.ExistingUser, fieldMap *core.FieldMap, errors *[]string) {
	for _, field := range target.Keys {
		if field == "Id" {
			continue
		}
		meta, ok := fieldMap.Get(field)
		if !ok {
			continue
		}
		if existing != nil && !meta.Updateable {
			*errors = append(*errors, Message("errorFieldNotWritable", field, "updateable"))
		} else if existing == nil && !meta.Createable {
			*errors = append(*errors, Message("errorFieldNotWritable", field, "createable"))
		}
	}
}

// BuildTarget returns the User fields to save for a user: its fields,
// IsActive true, and the profile and role Ids.  The profile comes from the
// profile key of the user, then a ProfileId field of the user, then the
// persona; the role likewise.  A named profile or role that did not
// resolve adds an error.
func BuildTarget(user *CanonicalUser, refs *ResolvedRefs, errors *[]string) *core.Object {
	persona := user.EffectivePersona
	target := user.Fields.Clone()
	target.Source = nil
	target.Set("IsActive", true)
	resolve := func(userRef, personaRef, field, sobject string, byRef *RefMap) {
		ref := userRef
		if ref == "" {
			if current, _ := target.Get(field); truthy(current) || personaRef == "" {
				return
			}
			ref = personaRef
		}
		if id := byRef.Get(ref); id == "" {
			*errors = append(*errors, Message("errorReferenceRequiredMissing", sobject, ref))
		} else {
			target.Set(field, id)
		}
	}
	resolve(user.ProfileRef, persona.Profile, "ProfileId", "Profile", refs.ProfilesByRef)
	resolve(user.RoleRef, persona.Role, "UserRoleId", "UserRole", refs.RolesByRef)
	return target
}

func applyInsertDefaults(target *core.Object, myDomain string) {
	if current, _ := target.Get("Username"); !truthy(current) && myDomain != "" {
		email, _ := target.Get("Email")
		if username := BuildDefaultUsername(email, myDomain); username != "" {
			target.Set("Username", username)
		}
	}
	if current, _ := target.Get("Alias"); !truthy(current) {
		firstName, _ := target.Get("FirstName")
		lastName, _ := target.Get("LastName")
		if alias := BuildDefaultAlias(firstName, lastName); alias != "" {
			target.Set("Alias", alias)
		}
	}
}

// AppendCrossReferenceCandidates adds, when an error names
// INVALID_CROSS_REFERENCE_KEY, an error that lists the Id fields of the
// target with their values.
func AppendCrossReferenceCandidates(errors []string, target *core.Object) []string {
	hasCrossRefError := false
	for _, text := range errors {
		hasCrossRefError = hasCrossRefError || strings.Contains(text, "INVALID_CROSS_REFERENCE_KEY")
	}
	if !hasCrossRefError {
		return errors
	}
	var candidateFields []string
	for _, field := range target.Keys {
		if strings.HasSuffix(field, "Id") && field != "Id" {
			candidateFields = append(candidateFields, field+"="+core.JSString(target.Values[field]))
		}
	}
	if len(candidateFields) == 0 {
		return errors
	}
	return append(append([]string{}, errors...), Message("errorCrossReferenceCandidates", strings.Join(candidateFields, ", ")))
}

// UserEntry is a user with its position in the users definition.
type UserEntry struct {
	User  *CanonicalUser
	Order int
}

// UserPlanOptions is what BuildUserPlans plans from.
type UserPlanOptions struct {
	ValidUsers []UserEntry
	Refs       *ResolvedRefs
	Existing   *core.Resolution
	FieldMap   *core.FieldMap
	// DefaultExternalIDField is the match field of users that name none,
	// or "".
	DefaultExternalIDField string
	MyDomain               string
	DryRun                 bool
	// Message is the text lookup; nil means Message.
	Message             MessageFunc
	RelatedPlansByOrder *RelatedPlansByOrder
}

// BuildUserPlans plans the save of each valid user: the target fields,
// the user it matched, the actions and the errors that stop the save.
func BuildUserPlans(options UserPlanOptions) []*UserPlan {
	message := options.Message
	if message == nil {
		message = Message
	}
	plans := make([]*UserPlan, 0, len(options.ValidUsers))
	for _, entry := range options.ValidUsers {
		user, order := entry.User, entry.Order
		effectivePersona := user.EffectivePersona
		errors := []string{}
		target := BuildTarget(user, options.Refs, &errors)
		var matchedBy *string
		if user.MatchField != "" {
			matchedBy = &user.MatchField
		} else if options.DefaultExternalIDField != "" {
			field := options.DefaultExternalIDField
			matchedBy = &field
		}
		var matchValue any
		var existingUser *core.ExistingUser
		if matchedBy != nil {
			matchValue, _ = target.Get(*matchedBy)
			if value, isText := matchValue.(string); isText {
				if found, ok := options.Existing.ExistingByField[*matchedBy][value]; ok {
					existingUser = &found
				}
				if options.Existing.Duplicates[core.MatchKey(*matchedBy, value)] {
					errors = append(errors, message("errorDuplicateExternalIdMatch", *matchedBy, value))
				}
			}
		}
		if existingUser == nil {
			applyInsertDefaults(target, options.MyDomain)
			profileIntended := user.ProfileRef != "" || effectivePersona.Profile != ""
			if missing := MissingRequiredFieldsForInsert(target, profileIntended); len(missing) > 0 {
				errors = append(errors, message("errorMissingRequiredFields", strings.Join(missing, ", ")))
			}
		}
		ensureWritableFields(target, existingUser, options.FieldMap, &errors)
		action := func(live, dryRun string) string {
			if options.DryRun {
				return dryRun
			}
			return live
		}
		actions := []string{action("created", "wouldCreate")}
		if existingUser != nil {
			actions = []string{action("updated", "wouldUpdate")}
		}
		if existingUser == nil || existingUser.IsActive == nil || !*existingUser.IsActive {
			actions = append(actions, action("activated", "wouldActivate"))
		}
		relatedPlans := options.RelatedPlansByOrder.Get(order)
		// A related plan that failed fails the user before any record is
		// changed, as every other entry of errors does.
		for _, relatedPlan := range relatedPlans {
			errors = append(errors, relatedPlan.Errors...)
		}
		plan := &UserPlan{
			PlanID:           fmt.Sprintf("%d:%s:%s", order, user.InputKey, strings.Join(user.Personas, "+")),
			Order:            order,
			Key:              user.InputKey,
			Personas:         user.Personas,
			EffectivePersona: effectivePersona,
			MatchedBy:        matchedBy,
			Target:           target,
			Existing:         existingUser,
			Actions:          actions,
			Errors:           errors,
			Source:           user.Source,
			RelatedPlans:     relatedPlans,
		}
		if value, usable := core.UsableMatchValue(matchValue); usable {
			plan.MatchValue = &value
		}
		plans = append(plans, plan)
	}
	return plans
}

// UserResultOverrides change what ToUserResult takes from the plan.
type UserResultOverrides struct {
	// ID is the Id to report; "" reports the Id of the existing user
	// unless OmitExistingID is set.
	ID             string
	OmitExistingID bool
	// Errors replaces the errors of the plan when it is not nil.
	Errors []string
}

// ToUserResult returns the result of a plan with a status.
func ToUserResult(plan *UserPlan, status string, overrides *UserResultOverrides) *UserResult {
	if overrides == nil {
		overrides = &UserResultOverrides{}
	}
	result := &UserResult{
		PlanID:     plan.PlanID,
		Order:      plan.Order,
		Key:        plan.Key,
		Personas:   plan.Personas,
		MatchedBy:  plan.MatchedBy,
		MatchValue: plan.MatchValue,
		Matched:    plan.Existing != nil,
		Status:     status,
		Actions:    plan.Actions,
		Errors:     plan.Errors,
		Source:     plan.Source,
	}
	result.UserName, result.Username = IdentityFromTarget(plan.Target, plan.Existing)
	if overrides.ID != "" {
		result.ID = overrides.ID
	} else if !overrides.OmitExistingID && plan.Existing != nil {
		result.ID = plan.Existing.ID
	}
	if overrides.Errors != nil {
		result.Errors = overrides.Errors
	}
	if len(plan.RelatedResults) > 0 {
		result.RelatedRecords = plan.RelatedResults
	}
	return result
}

// Summary counts the users of a result by status.
type Summary struct {
	Total    int `json:"total"`
	Created  int `json:"created"`
	Updated  int `json:"updated"`
	Failed   int `json:"failed"`
	Warnings int `json:"warnings"`
}

// Summarize counts the statuses "created", "updated" and "failed".
// globalWarningCount is the number of warnings that belong to no user.
func Summarize(statuses []string, globalWarningCount int) Summary {
	summary := Summary{Total: len(statuses), Warnings: globalWarningCount}
	for _, status := range statuses {
		switch status {
		case "created":
			summary.Created++
		case "updated":
			summary.Updated++
		case "failed":
			summary.Failed++
		}
	}
	return summary
}
