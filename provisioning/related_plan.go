package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// RelatedDmlChunkSize is the largest number of records in one create,
// update or delete request.
const RelatedDmlChunkSize = 200

// RelatedRecordPlan is what provisioning will do for one relationship of
// one user.
type RelatedRecordPlan struct {
	Relationship string
	Phase        string
	LinkUser     *LinkUser
	Sobject      string
	MatchField   string
	// Fields holds the values to write.
	Fields *core.Object
	// PendingUserIDFields are the fields that get the Id of the saved
	// User.
	PendingUserIDFields []string
	Mode                string
	// Status is "planned", "skipped" or "failed".
	Status string
	Errors []string
	// MatchValue is nil when the plan has none.
	MatchValue *string
	// LinkValue is the value of LinkUser.FromRelatedField on the existing
	// record.
	LinkValue any
	// ExistingID is the Id of the matched record, or "".
	ExistingID     string
	ExistingValues sfapi.Record
	// RecordTypeID is set for a record that will be created with a
	// record type.
	RecordTypeID string
}

// MarshalJSON writes the plan with the keys of the upstream plan.
func (p *RelatedRecordPlan) MarshalJSON() ([]byte, error) {
	object := core.NewObject()
	object.Set("relationship", p.Relationship)
	object.Set("phase", p.Phase)
	if p.LinkUser != nil {
		object.Set("linkUser", p.LinkUser)
	}
	object.Set("sobject", p.Sobject)
	object.Set("matchField", p.MatchField)
	object.Set("fields", p.Fields)
	object.Set("pendingUserIdFields", nonNil(p.PendingUserIDFields))
	object.Set("mode", p.Mode)
	object.Set("status", p.Status)
	object.Set("errors", nonNil(p.Errors))
	if p.MatchValue != nil {
		object.Set("matchValue", *p.MatchValue)
	}
	if p.LinkValue != nil {
		object.Set("linkValue", p.LinkValue)
	}
	if p.ExistingID != "" {
		object.Set("existingId", p.ExistingID)
	}
	if p.ExistingValues != nil {
		object.Set("existingValues", p.ExistingValues)
	}
	if p.RecordTypeID != "" {
		object.Set("recordTypeId", p.RecordTypeID)
	}
	return object.MarshalJSON()
}

// RelatedPlansByOrder holds the related plans of each user by the
// position of the user.
type RelatedPlansByOrder struct {
	orders  []int
	byOrder map[int][]*RelatedRecordPlan
}

// Get returns the plans of the user at a position, or nil.
func (m *RelatedPlansByOrder) Get(order int) []*RelatedRecordPlan {
	if m == nil {
		return nil
	}
	return m.byOrder[order]
}

// Orders returns the positions that have plans, in the order of the
// users.
func (m *RelatedPlansByOrder) Orders() []int {
	if m == nil {
		return nil
	}
	return m.orders
}

func (m *RelatedPlansByOrder) set(order int, plans []*RelatedRecordPlan) {
	if _, exists := m.byOrder[order]; !exists {
		m.orders = append(m.orders, order)
	}
	m.byOrder[order] = plans
}

func matchGroupKey(sobject, matchField string) string {
	return strings.ToLower(sobject) + "|" + strings.ToLower(matchField)
}

func matchValueKey(value string, def *RelationshipDef, preflight *PreflightResult) string {
	if matchField, ok := preflight.FieldsBySobject[strings.ToLower(def.Sobject)].Get(def.Match.Field); ok && isFalse(matchField.CaseSensitive) {
		return strings.ToLower(value)
	}
	return value
}

// isEmptyExistingValue reports whether a field of an existing record is
// null or the empty text.  A value of only white space is a value, and
// setIfEmpty keeps it.
func isEmptyExistingValue(existing sfapi.Record, name string) bool {
	value, present := existing[name]
	return present && (value == nil || value == "")
}

func canonicalFieldName(field string, fieldMap *core.FieldMap) string {
	if meta, ok := fieldMap.Get(field); ok {
		return meta.Name
	}
	return field
}

func selectFieldsFor(defs []*RelationshipDef, fieldMap *core.FieldMap) []string {
	names := []string{"Id"}
	seen := map[string]bool{"Id": true}
	if _, ok := fieldMap.Get("recordtypeid"); ok {
		names = appendUnique(names, seen, "RecordTypeId")
	}
	for _, def := range defs {
		names = appendUnique(names, seen, canonicalFieldName(def.Match.Field, fieldMap))
		if def.LinkUser != nil {
			names = appendUnique(names, seen, canonicalFieldName(def.LinkUser.FromRelatedField, fieldMap))
		}
		for _, field := range def.FieldNames {
			names = appendUnique(names, seen, canonicalFieldName(field, fieldMap))
		}
	}
	return names
}

func skippedPlan(relationship string, def *RelationshipDef) *RelatedRecordPlan {
	plan := &RelatedRecordPlan{
		Relationship:        relationship,
		Phase:               def.Phase,
		Sobject:             def.Sobject,
		MatchField:          def.Match.Field,
		Fields:              core.NewObject(),
		PendingUserIDFields: []string{},
		Mode:                def.Mode,
		Status:              "skipped",
		Errors:              []string{},
	}
	if plan.Mode == "" {
		plan.Mode = "setIfEmpty"
	}
	if def.LinkUser != nil {
		link := *def.LinkUser
		plan.LinkUser = &link
	}
	return plan
}

type pendingMatch struct {
	def        *RelationshipDef
	matchValue string
}

// RelatedPlanOptions is what BuildRelatedPlans plans from.
type RelatedPlanOptions struct {
	Conn         sfapi.Conn
	Users        []UserEntry
	Catalog      *Catalog
	Preflight    *PreflightResult
	UserFieldMap *core.FieldMap
	Cache        core.DescribeCache
	// Message is the text lookup; nil means Message.
	Message MessageFunc
}

// resolveMatchValues starts the plans of each user and resolves their
// match values.  A value that cannot be resolved fails that plan only.
func resolveMatchValues(users []UserEntry, options RelatedPlanOptions, message MessageFunc) ([]pendingMatch, *RelatedPlansByOrder) {
	var pending []pendingMatch
	plansByOrder := &RelatedPlansByOrder{byOrder: map[int][]*RelatedRecordPlan{}}
	for _, entry := range users {
		var plans []*RelatedRecordPlan
		for _, relationship := range entry.User.Related {
			def := options.Catalog.Relationships[relationship]
			if def == nil {
				continue
			}
			plan := skippedPlan(relationship, def)
			plans = append(plans, plan)
			if !options.Preflight.Eligible[relationship] {
				continue
			}
			resolved := ResolveSource(SourceExpr{HasFrom: true, From: def.Match.From}, SourceContext{Relationship: relationship, FieldName: "match.from", UserFields: entry.User.Fields, UserFieldMap: options.UserFieldMap})
			if resolved.Error != nil {
				plan.Status = "failed"
				plan.Errors = []string{message(resolved.Error.Code, resolved.Error.MessageArgs...)}
				continue
			}
			matchValue := core.JSString(resolved.Value)
			plan.MatchField = canonicalFieldName(def.Match.Field, options.Preflight.FieldsBySobject[strings.ToLower(def.Sobject)])
			plan.Status = "planned"
			plan.MatchValue = &matchValue
			pending = append(pending, pendingMatch{def: def, matchValue: matchValue})
		}
		if len(plans) > 0 {
			plansByOrder.set(entry.Order, plans)
		}
	}
	return pending, plansByOrder
}

type matchGroup struct {
	key    string
	defs   []*RelationshipDef
	values []string
	seen   map[string]bool
}

// queryExistingRecords finds the records the match values name, with the
// values of all users for one sObject and match field in the same
// queries.  The result maps a group key and a value key to the rows.
func queryExistingRecords(conn sfapi.Conn, pending []pendingMatch, preflight *PreflightResult) (map[string]map[string][]sfapi.Record, error) {
	var groups []*matchGroup
	groupByKey := map[string]*matchGroup{}
	for _, entry := range pending {
		key := matchGroupKey(entry.def.Sobject, entry.def.Match.Field)
		group := groupByKey[key]
		if group == nil {
			group = &matchGroup{key: key, seen: map[string]bool{}}
			groupByKey[key] = group
			groups = append(groups, group)
		}
		known := false
		for _, def := range group.defs {
			known = known || def == entry.def
		}
		if !known {
			group.defs = append(group.defs, entry.def)
		}
		group.values = appendUnique(group.values, group.seen, entry.matchValue)
	}
	index := map[string]map[string][]sfapi.Record{}
	for _, group := range groups {
		primaryDef := group.defs[0]
		fieldMap := preflight.FieldsBySobject[strings.ToLower(primaryDef.Sobject)]
		matchField := canonicalFieldName(primaryDef.Match.Field, fieldMap)
		prefix := "SELECT " + strings.Join(selectFieldsFor(group.defs, fieldMap), ", ") + " FROM " + primaryDef.Sobject + " WHERE " + matchField + " IN ("
		chunks, err := MatchQueryBatches(group.values, prefix, nil)
		if err != nil {
			return nil, err
		}
		byValue := map[string][]sfapi.Record{}
		for _, chunk := range chunks {
			page, err := conn.Query(prefix + core.SoqlIn(chunk) + ")")
			if err != nil {
				return nil, err
			}
			for _, row := range page.Records {
				rowValue := row[matchField]
				if rowValue == nil {
					continue
				}
				valueKey := matchValueKey(core.JSString(rowValue), primaryDef, preflight)
				byValue[valueKey] = append(byValue[valueKey], row)
			}
		}
		index[group.key] = byValue
	}
	return index, nil
}

// assertPlannedFieldsWritable checks the fields this create or update
// sends.
func assertPlannedFieldsWritable(plan *RelatedRecordPlan, fieldMap *core.FieldMap, message MessageFunc) {
	requiredPermission := "createable"
	if plan.ExistingID != "" {
		requiredPermission = "updateable"
	}
	var unavailable []string
	for _, field := range append(append([]string{}, plan.Fields.Keys...), plan.PendingUserIDFields...) {
		meta, known := fieldMap.Get(field)
		if !known {
			continue
		}
		if (plan.ExistingID != "" && !meta.Updateable) || (plan.ExistingID == "" && !meta.Createable) {
			unavailable = append(unavailable, field)
		}
	}
	if len(unavailable) > 0 {
		plan.Status = "failed"
		plan.Errors = append(plan.Errors, message("errorRelatedFieldsNotWritableForOperation", plan.Sobject, strings.Join(unavailable, ", "), requiredPermission))
	}
}

func finalizePlan(plan *RelatedRecordPlan, def *RelationshipDef, user *CanonicalUser, userFieldMap, targetFieldMap *core.FieldMap, matches []sfapi.Record, recordTypeID string, message MessageFunc) {
	matchValue := *plan.MatchValue
	if len(matches) > 1 {
		plan.Status = "failed"
		plan.Errors = append(plan.Errors, message("errorAmbiguousRelatedMatch", plan.Relationship, def.Sobject, def.Match.Field, matchValue))
		return
	}
	var existing sfapi.Record
	if len(matches) == 1 {
		existing = matches[0]
	}
	if def.LinkUser != nil {
		plan.LinkUser = &LinkUser{
			UserField:        canonicalFieldName(def.LinkUser.UserField, userFieldMap),
			FromRelatedField: canonicalFieldName(def.LinkUser.FromRelatedField, targetFieldMap),
		}
		if existing != nil {
			plan.LinkValue = existing[plan.LinkUser.FromRelatedField]
		}
	}
	fields := core.NewObject()
	var pendingUserIDFields, resolveErrors []string
	for _, fieldName := range def.FieldNames {
		resolved := ResolveSource(def.Fields[fieldName], SourceContext{Relationship: plan.Relationship, FieldName: fieldName, UserFields: user.Fields, UserFieldMap: userFieldMap, RelatedContext: user.RelatedContext})
		if resolved.Error != nil {
			resolveErrors = append(resolveErrors, message(resolved.Error.Code, resolved.Error.MessageArgs...))
		} else if resolved.Pending {
			pendingUserIDFields = append(pendingUserIDFields, canonicalFieldName(fieldName, targetFieldMap))
		} else {
			fields.Set(canonicalFieldName(fieldName, targetFieldMap), resolved.Value)
		}
	}
	if len(resolveErrors) > 0 {
		plan.Status = "failed"
		plan.Errors = append(plan.Errors, resolveErrors...)
		return
	}
	if existing != nil {
		plan.ExistingID = text(existing, "Id")
		plan.ExistingValues = sfapi.Record{}
		for key, value := range existing {
			plan.ExistingValues[key] = value
		}
		if recordTypeID != "" && existing["RecordTypeId"] != recordTypeID {
			developerName := ""
			if def.RecordType != nil {
				developerName = def.RecordType.DeveloperName
			}
			plan.Status = "failed"
			plan.Errors = append(plan.Errors, message("errorRelatedRecordTypeMismatch", plan.Relationship, def.Sobject, developerName))
			return
		}
	} else if recordTypeID != "" {
		// RecordTypeId is written on create only; the record type of an
		// existing record is never changed.
		plan.RecordTypeID = recordTypeID
	}
	setIfEmpty := existing != nil && plan.Mode != "sync"
	plan.Fields = core.NewObject()
	for _, name := range fields.Keys {
		if !setIfEmpty || isEmptyExistingValue(existing, name) {
			plan.Fields.Set(name, fields.Values[name])
		}
	}
	if existing == nil {
		// A create writes the match field, so that a later run matches
		// the record this run created.
		plan.Fields.Set(canonicalFieldName(def.Match.Field, targetFieldMap), matchValue)
	}
	plan.PendingUserIDFields = []string{}
	for _, name := range pendingUserIDFields {
		if existing == nil || plan.Mode != "setIfEmpty" || isEmptyExistingValue(existing, name) {
			plan.PendingUserIDFields = append(plan.PendingUserIDFields, name)
		}
	}
	assertPlannedFieldsWritable(plan, targetFieldMap, message)
}

// applyCollisionCheck fails every plan whose relationship and match value
// another plan has too.
func applyCollisionCheck(plansByOrder *RelatedPlansByOrder, catalog *Catalog, preflight *PreflightResult, message MessageFunc) {
	var keys []string
	byKey := map[string][]*RelatedRecordPlan{}
	for _, order := range plansByOrder.orders {
		for _, plan := range plansByOrder.byOrder[order] {
			if plan.Status != "planned" || plan.MatchValue == nil {
				continue
			}
			key := plan.Relationship + " " + matchValueKey(*plan.MatchValue, catalog.Relationships[plan.Relationship], preflight)
			if _, known := byKey[key]; !known {
				keys = append(keys, key)
			}
			byKey[key] = append(byKey[key], plan)
		}
	}
	for _, key := range keys {
		colliding := byKey[key]
		if len(colliding) < 2 {
			continue
		}
		for _, plan := range colliding {
			plan.Status = "failed"
			plan.Errors = append(plan.Errors, message("errorRelatedMatchCollision", plan.Relationship, plan.MatchField, *plan.MatchValue))
		}
	}
}

// applyLinkConflictCheck fails the before plans of a user that link to
// the same User field.
func applyLinkConflictCheck(plansByOrder *RelatedPlansByOrder, message MessageFunc) {
	for _, order := range plansByOrder.orders {
		var fields []string
		writers := map[string][]*RelatedRecordPlan{}
		for _, plan := range plansByOrder.byOrder[order] {
			if plan.Phase != "before" || plan.LinkUser == nil {
				continue
			}
			field := strings.ToLower(plan.LinkUser.UserField)
			if _, known := writers[field]; !known {
				fields = append(fields, field)
			}
			writers[field] = append(writers[field], plan)
		}
		for _, field := range fields {
			claims := writers[field]
			if len(claims) < 2 {
				continue
			}
			for _, plan := range claims {
				plan.Status = "failed"
				plan.Errors = append(plan.Errors, message("errorConflictingLinkUser", field))
			}
		}
	}
}

// BuildRelatedPlans plans the related records of every user for both
// phases.  It only reads from the org, so a dry run can call it.
func BuildRelatedPlans(options RelatedPlanOptions) (*RelatedPlansByOrder, error) {
	message := options.Message
	if message == nil {
		message = Message
	}
	cache := options.Cache
	if cache == nil {
		cache = core.DescribeCache{}
	}
	contexts := ResolveRelatedContext(ContextOptions{Conn: options.Conn, Users: options.Users, Catalog: options.Catalog, UserFieldMap: options.UserFieldMap, Cache: cache, Message: message})
	resolvedUsers := make([]UserEntry, len(options.Users))
	usersByOrder := map[int]*CanonicalUser{}
	for i, entry := range options.Users {
		user := *entry.User
		user.RelatedContext = nil
		if context := contexts[entry.Order]; context != nil {
			user.RelatedContext = context.Values
		}
		resolvedUsers[i] = UserEntry{User: &user, Order: entry.Order}
		usersByOrder[entry.Order] = &user
	}
	pending, plansByOrder := resolveMatchValues(resolvedUsers, options, message)
	for _, order := range plansByOrder.orders {
		for _, plan := range plansByOrder.byOrder[order] {
			def := options.Catalog.Relationships[plan.Relationship]
			var contextErrors []string
			seen := map[string]bool{}
			for _, fieldName := range def.FieldNames {
				expr := def.Fields[fieldName]
				if !expr.HasFrom || contexts[order] == nil {
					continue
				}
				if parsed := ParseSource(expr.From); parsed.Kind == "context" {
					for _, contextError := range contexts[order].ErrorsByName[parsed.Name] {
						contextErrors = appendUnique(contextErrors, seen, contextError)
					}
				}
			}
			if len(contextErrors) == 0 {
				continue
			}
			plan.Status = "failed"
			plan.Errors = append(plan.Errors, contextErrors...)
		}
	}
	if len(plansByOrder.orders) == 0 {
		return plansByOrder, nil
	}
	index, err := queryExistingRecords(options.Conn, pending, options.Preflight)
	if err != nil {
		return nil, err
	}
	for _, order := range plansByOrder.orders {
		user := usersByOrder[order]
		if user == nil {
			continue
		}
		for _, plan := range plansByOrder.byOrder[order] {
			if plan.Status != "planned" || plan.MatchValue == nil {
				continue
			}
			def := options.Catalog.Relationships[plan.Relationship]
			matches := index[matchGroupKey(def.Sobject, def.Match.Field)][matchValueKey(*plan.MatchValue, def, options.Preflight)]
			finalizePlan(plan, def, user, options.UserFieldMap, options.Preflight.FieldsBySobject[strings.ToLower(def.Sobject)], matches, options.Preflight.RecordTypeIDByRelationship[plan.Relationship], message)
		}
	}
	applyCollisionCheck(plansByOrder, options.Catalog, options.Preflight, message)
	applyLinkConflictCheck(plansByOrder, message)
	return plansByOrder, nil
}

// WarnRelatedProfileLicenses adds a warning for each profile of the users
// that link ContactId from a before relationship: the name of its user
// license and the number of those users.  Salesforce decides at save time
// whether the license allows the link.
func WarnRelatedProfileLicenses(conn sfapi.Conn, plans []*UserPlan, warnings *[]string, message MessageFunc) {
	if message == nil {
		message = Message
	}
	var ids []string
	counts := map[string]int{}
	for _, plan := range plans {
		linksContact := false
		for _, related := range plan.RelatedPlans {
			linksContact = linksContact || (related.Phase == "before" && related.LinkUser != nil && strings.ToLower(related.LinkUser.UserField) == "contactid")
		}
		if !linksContact {
			continue
		}
		id, present := plan.Target.Get("ProfileId")
		if (!present || id == nil) && plan.Existing != nil {
			id = plan.Existing.Row["ProfileId"]
		}
		if profileID, isText := id.(string); isText {
			if _, known := counts[profileID]; !known {
				ids = append(ids, profileID)
			}
			counts[profileID]++
		}
	}
	if len(ids) == 0 {
		return
	}
	prefix := "SELECT Id, Name, UserLicense.Name FROM Profile WHERE Id IN ("
	fail := func(err error) {
		*warnings = append(*warnings, "Could not inspect Profile licenses for ContactId links: "+err.Error())
	}
	chunks, err := MatchQueryBatches(ids, prefix, nil)
	if err != nil {
		fail(err)
		return
	}
	var profiles []sfapi.Record
	for _, chunk := range chunks {
		page, err := conn.Query(prefix + core.SoqlIn(chunk) + ")")
		if err != nil {
			fail(err)
			return
		}
		profiles = append(profiles, page.Records...)
	}
	for _, profile := range profiles {
		licenseName := "unknown"
		if name, ok := nested(profile, "UserLicense")["Name"]; ok && name != nil {
			licenseName = core.JSString(name)
		}
		*warnings = append(*warnings, message("warningRelatedProfileLicense", text(profile, "Name"), licenseName, core.JSString(float64(counts[text(profile, "Id")]))))
	}
}
