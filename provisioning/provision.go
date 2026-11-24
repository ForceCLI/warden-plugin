package provisioning

import (
	"context"
	"errors"
	"sort"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Options are the options of a provisioning run.
type Options struct {
	// UsersPath and UsersDoc name the users definition: a document that
	// was already read, or the path of a JSON, CSV or TSV file.
	UsersPath string
	UsersDoc  *core.Object
	// PersonasPath and PersonasDoc name the personas definition.
	PersonasPath string
	PersonasDoc  *core.Object
	// PersonasSupplied overrides whether a personas definition counts as
	// supplied; nil means it is when a document or a path is given.
	PersonasSupplied *bool
	// CleanupOnFailure deletes the related records this run created for
	// users that failed.
	CleanupOnFailure bool
	// RelatedPath and RelatedDoc name the related-record catalog.
	RelatedPath string
	RelatedDoc  *core.Object
	// ExternalID is the match field of users that name none.
	ExternalID string
	// InputFormat is "json", "csv" or "" to decide by file extension.
	InputFormat      string
	CsvListDelimiter string
	// FuzzyUsername makes a Username match also accept usernames that
	// continue with a dot and more text.
	FuzzyUsername bool
}

// RunContext lets a caller cancel a run and follow its progress.  A nil
// *RunContext does neither.
type RunContext struct {
	// Context, when set, cancels the run: the next check after it is done
	// returns a WardenError with the code "cancelled".
	Context context.Context
	// OnProgress is called with "start" and "complete" around Plan and
	// with "apply" and "complete" around Apply.
	OnProgress func(phase string)
}

func (c *RunContext) checkCancelled() error {
	if c != nil && c.Context != nil && c.Context.Err() != nil {
		return &core.WardenError{Name: "WardenError", Code: "cancelled", Message: "Operation cancelled."}
	}
	return nil
}

func (c *RunContext) progress(phase string) error {
	if err := c.checkCancelled(); err != nil {
		return err
	}
	if c != nil && c.OnProgress != nil {
		c.OnProgress(phase)
	}
	return nil
}

// PlanResult is a provisioning plan.  Plans and the related plans inside
// them are changed by Apply.
type PlanResult struct {
	CleanupOnFailure bool        `json:"cleanupOnFailure"`
	Plans            []*UserPlan `json:"plans"`
	// Refs holds the resolved references.  The upstream plan stores its
	// maps as plain objects; here the maps are written to JSON as objects
	// in the same key order.
	Refs *ResolvedRefs `json:"refs"`
	// ValidationResults are the failed results of the users that were not
	// planned because of validation errors.
	ValidationResults []*UserResult  `json:"validationResults"`
	Licenses          []LicenseUsage `json:"licenses"`
	// Preview is what a dry run reports.
	Preview  *Result  `json:"preview"`
	Warnings []string `json:"warnings"`
}

func resultStatuses[T any](results []T, status func(T) string) []string {
	statuses := make([]string, len(results))
	for i, result := range results {
		statuses[i] = status(result)
	}
	return statuses
}

func licenseShortfalls(licenses []LicenseUsage) int {
	count := 0
	for _, license := range licenses {
		if license.Shortfall > 0 {
			count++
		}
	}
	return count
}

func personasOf(personasDoc *core.Object) (*core.Object, error) {
	personas := personasDoc.Object("personas")
	if personas == nil {
		return nil, core.NewProvisioningError("errorInvalidPersonaDefinition", Message("errorInvalidPersonaDefinition"), nil)
	}
	return personas, nil
}

func relatedSelectionFor(catalog *Catalog) *RelatedSelection {
	selection := &RelatedSelection{CatalogSupplied: catalog != nil, Names: map[string]bool{}}
	if catalog != nil {
		for _, name := range catalog.Names {
			selection.Names[name] = true
		}
	}
	return selection
}

func splitUsers(users []*CanonicalUser) (entries, validUsers, failedUsers []UserEntry) {
	for order, user := range users {
		entry := UserEntry{User: user, Order: order}
		entries = append(entries, entry)
		if len(user.ValidationErrors) > 0 {
			failedUsers = append(failedUsers, entry)
		} else {
			validUsers = append(validUsers, entry)
		}
	}
	return entries, validUsers, failedUsers
}

func usersOf(entries []UserEntry) []*CanonicalUser {
	users := make([]*CanonicalUser, len(entries))
	for i, entry := range entries {
		users[i] = entry.User
	}
	return users
}

func selectedRelationships(validUsers []UserEntry) []string {
	var selected []string
	for _, entry := range validUsers {
		selected = append(selected, entry.User.Related...)
	}
	return unique(selected)
}

// ExistingUsersOptions say how GetExistingUsers matches users.
type ExistingUsersOptions struct {
	// DefaultExternalIDField is the match field of users that name none,
	// or "".
	DefaultExternalIDField string
	DefaultFuzzyUsername   bool
	FieldMap               *core.FieldMap
}

// GetExistingUsers finds the users the definition entries match.  An
// entry is looked up when its match field, or the default one, has a
// non-empty text value.
func GetExistingUsers(conn sfapi.Conn, users []*CanonicalUser, options ExistingUsersOptions) (*core.Resolution, error) {
	var requests []core.MatchRequest
	for _, user := range users {
		field := user.MatchField
		if field == "" {
			field = options.DefaultExternalIDField
		}
		if field == "" {
			continue
		}
		raw, _ := user.Fields.Get(field)
		value, usable := core.UsableMatchValue(raw)
		if !usable {
			continue
		}
		fuzzy := options.DefaultFuzzyUsername
		if user.FuzzyUsername != nil {
			fuzzy = *user.FuzzyUsername
		}
		requests = append(requests, core.MatchRequest{Field: field, Value: value, Fuzzy: field == "Username" && fuzzy})
	}
	return core.ResolveExistingUsers(conn, requests, options.FieldMap)
}

// ValidationResultsFor returns a failed result for each user that has
// validation errors.
func ValidationResultsFor(validationFailureUsers []UserEntry) []*UserResult {
	results := make([]*UserResult, 0, len(validationFailureUsers))
	for _, entry := range validationFailureUsers {
		user := entry.User
		result := &UserResult{
			PlanID:   itoa(entry.Order) + ":" + user.InputKey + ":" + joinPlus(user.Personas) + ":validation",
			Order:    entry.Order,
			Key:      user.InputKey,
			Personas: user.Personas,
			// These users were not matched against the org.
			Matched: false,
			Status:  "failed",
			Actions: []string{},
			Errors:  []string{},
			Source:  user.Source,
		}
		result.UserName, result.Username = IdentityFromTarget(user.Fields, nil)
		if user.MatchField != "" {
			matchedBy := user.MatchField
			result.MatchedBy = &matchedBy
			raw, _ := user.Fields.Get(matchedBy)
			if value, usable := core.UsableMatchValue(raw); usable {
				result.MatchValue = &value
			}
		}
		for _, validationError := range user.ValidationErrors {
			result.Errors = append(result.Errors, Message(validationError.Code, validationError.MessageArgs...))
		}
		results = append(results, result)
	}
	return results
}

func sortByOrder(results []*UserResult) {
	sort.SliceStable(results, func(i, j int) bool { return results[i].Order < results[j].Order })
}

func publicResults(results []*UserResult) []PublicUserResult {
	public := make([]PublicUserResult, len(results))
	for i, result := range results {
		public[i] = PublicProvisionResult(result)
	}
	return public
}

// Plan reads the definitions, checks them against the org and plans the
// save of every user.  It changes no record.  The Preview of the plan is
// what a dry run reports; Apply carries the plan out.
func Plan(conn sfapi.Conn, options Options) (*PlanResult, error) {
	return PlanWithContext(nil, conn, options)
}

// PlanWithContext is Plan with cancellation and progress reports.
func PlanWithContext(ctx *RunContext, conn sfapi.Conn, options Options) (*PlanResult, error) {
	if err := ctx.progress("start"); err != nil {
		return nil, err
	}
	if err := ctx.checkCancelled(); err != nil {
		return nil, err
	}
	describeCache := core.DescribeCache{}
	fieldMap, err := core.DescribeUserFields(conn, describeCache)
	if err != nil {
		return nil, err
	}
	definitions, err := core.ResolveDefinitions(core.DefinitionSource{
		UsersDoc:         options.UsersDoc,
		PersonasDoc:      options.PersonasDoc,
		RelatedDoc:       options.RelatedDoc,
		UsersPath:        options.UsersPath,
		PersonasPath:     options.PersonasPath,
		RelatedPath:      options.RelatedPath,
		InputFormat:      options.InputFormat,
		CsvListDelimiter: options.CsvListDelimiter,
	}, fieldMap, core.DefaultDefinitionMessages())
	if err != nil {
		return nil, err
	}
	personas, err := personasOf(definitions.PersonasDoc)
	if err != nil {
		return nil, err
	}
	if err := ValidatePersonaModes(personas); err != nil {
		return nil, err
	}
	externalID, err := ValidateExternalIDFieldForFlag(options.ExternalID, fieldMap)
	if err != nil {
		return nil, err
	}
	var catalog *Catalog
	if definitions.RelatedDoc != nil {
		if catalog, err = AssertValidRelatedCatalog(definitions.RelatedDoc, fieldMap, nil); err != nil {
			return nil, err
		}
	}
	personasSupplied := definitions.PersonasSupplied
	if options.PersonasSupplied != nil {
		personasSupplied = *options.PersonasSupplied
	}
	rawUsers, _ := definitions.UsersDoc.Get("users")
	users, err := ValidateAndCanonicalizeUsers(rawUsers, personas, fieldMap, personasSupplied, relatedSelectionFor(catalog))
	if err != nil {
		return nil, err
	}
	_, validUsers, failedUsers := splitUsers(users)
	refs, err := ResolveReferences(conn, personas, usersOf(validUsers))
	if err != nil {
		return nil, err
	}
	defaultExternalIDField := ""
	if externalID != nil {
		defaultExternalIDField = externalID.Name
	}
	existing, err := GetExistingUsers(conn, usersOf(validUsers), ExistingUsersOptions{DefaultExternalIDField: defaultExternalIDField, DefaultFuzzyUsername: options.FuzzyUsername, FieldMap: fieldMap})
	if err != nil {
		return nil, err
	}
	preflight := EmptyPreflightResult()
	if catalog != nil {
		preflight, err = RunRelatedPreflight(PreflightOptions{Conn: conn, Catalog: catalog, Selected: selectedRelationships(validUsers), Cache: describeCache, UserFieldMap: fieldMap})
		if err != nil {
			return nil, err
		}
	}
	refs.Warnings = append(refs.Warnings, preflight.Warnings...)
	var relatedPlansByOrder *RelatedPlansByOrder
	if catalog != nil {
		relatedPlansByOrder, err = BuildRelatedPlans(RelatedPlanOptions{Conn: conn, Users: validUsers, Catalog: catalog, Preflight: preflight, UserFieldMap: fieldMap, Cache: describeCache})
		if err != nil {
			return nil, err
		}
	}
	planOptions := UserPlanOptions{
		ValidUsers:             validUsers,
		Refs:                   refs,
		Existing:               existing,
		FieldMap:               fieldMap,
		DefaultExternalIDField: defaultExternalIDField,
		MyDomain:               DeriveMyDomain(conn.InstanceURL()),
		RelatedPlansByOrder:    relatedPlansByOrder,
	}
	plans := BuildUserPlans(planOptions)
	WarnRelatedProfileLicenses(conn, plans, &refs.Warnings, nil)
	planOptions.DryRun = true
	previewPlans := BuildUserPlans(planOptions)
	licenses, err := CalculateUserLicenseUsage(conn, plans)
	if err != nil {
		return nil, err
	}
	validationResults := ValidationResultsFor(failedUsers)
	allPreviewResults := append([]*UserResult{}, validationResults...)
	for _, plan := range previewPlans {
		planned, err := PlanDryRunResult(conn, plan, refs)
		if err != nil {
			return nil, err
		}
		allPreviewResults = append(allPreviewResults, planned)
	}
	sortByOrder(allPreviewResults)
	statuses := resultStatuses(allPreviewResults, func(result *UserResult) string { return result.Status })
	result := &PlanResult{
		CleanupOnFailure:  options.CleanupOnFailure,
		Plans:             plans,
		Refs:              refs,
		ValidationResults: validationResults,
		Licenses:          licenses,
		Preview: &Result{
			Summary:               Summarize(statuses, len(refs.Warnings)+licenseShortfalls(licenses)),
			Users:                 publicResults(allPreviewResults),
			Licenses:              licenses,
			PermissionSetLicenses: &PermissionSetLicenseNote{Evaluated: false, Note: "not evaluated"},
		},
		Warnings: refs.Warnings,
	}
	if err := ctx.progress("complete"); err != nil {
		return nil, err
	}
	return result, nil
}

// Apply carries out a plan: the before relationships, the User saves,
// the after relationships, the unfreeze and assignment changes of each
// saved user and, when the plan asks for it, the cleanup of related
// records of failed users.  A user that fails does not stop the others.
func Apply(conn sfapi.Conn, plan *PlanResult) (*Result, error) {
	return ApplyWithContext(nil, conn, plan)
}

// ApplyWithContext is Apply with cancellation and progress reports.
// Cancellation is checked before each stage and each record change; the
// cleanup, once started, is not interrupted.
func ApplyWithContext(ctx *RunContext, conn sfapi.Conn, plan *PlanResult) (*Result, error) {
	if plan == nil {
		return nil, errors.New("a provisioning plan is required")
	}
	if err := ctx.progress("apply"); err != nil {
		return nil, err
	}
	refs := plan.Refs
	if err := ctx.checkCancelled(); err != nil {
		return nil, err
	}
	if err := ApplyBeforeUserSaves(conn, plan.Plans, ctx.checkCancelled); err != nil {
		return nil, err
	}
	outcomes, err := ExecuteBulkUserSaves(conn, plan.Plans, ctx.checkCancelled)
	if err != nil {
		return nil, err
	}
	var invalid, failures []*UserResult
	for _, item := range plan.Plans {
		if len(item.Errors) > 0 {
			invalid = append(invalid, ToUserResult(MarkRelatedUnapplied(item), "failed", &UserResultOverrides{OmitExistingID: true}))
		}
	}
	var saved []*SaveOutcome
	for _, outcome := range outcomes {
		if outcome.Success {
			saved = append(saved, outcome)
		} else {
			failures = append(failures, ToUserResult(MarkRelatedUnapplied(outcome.Plan), "failed", &UserResultOverrides{Errors: outcome.Errors, OmitExistingID: true}))
		}
	}
	if err := ctx.checkCancelled(); err != nil {
		return nil, err
	}
	related, err := ApplyRelatedPhase(conn, afterEntries(saved), "after", ctx.checkCancelled)
	if err != nil {
		return nil, err
	}
	recordAfterResults(saved, related)
	var applied []*UserResult
	var savedPlanIDs []string
	for _, outcome := range saved {
		if err := ctx.checkCancelled(); err != nil {
			return nil, err
		}
		outcomeResult, err := ApplySavedPlan(SavedPlanOptions{Conn: conn, Outcome: outcome, Refs: refs, BeforeWrite: ctx.checkCancelled})
		if err != nil {
			return nil, err
		}
		applied = append(applied, outcomeResult)
		savedPlanIDs = append(savedPlanIDs, outcome.Plan.PlanID)
	}
	if err := ctx.checkCancelled(); err != nil {
		return nil, err
	}
	results := append(append(invalid, failures...), applied...)
	if plan.CleanupOnFailure {
		CleanupProvisionFailures(conn, plan.Plans, results, savedPlanIDs)
	}
	all := append(append([]*UserResult{}, plan.ValidationResults...), results...)
	sortByOrder(all)
	users := publicResults(all)
	if err := ctx.progress("complete"); err != nil {
		return nil, err
	}
	statuses := resultStatuses(users, func(user PublicUserResult) string { return user.Status })
	return &Result{Summary: Summarize(statuses, len(plan.Warnings)+licenseShortfalls(plan.Licenses)), Users: users}, nil
}

// afterEntries returns the after-phase entries of the saved users that
// have related plans.
func afterEntries(saved []*SaveOutcome) []RelatedPhaseEntry {
	var entries []RelatedPhaseEntry
	for _, outcome := range saved {
		if outcome.Plan.RelatedPlans != nil && outcome.ID != "" {
			entries = append(entries, RelatedPhaseEntry{PlanID: outcome.Plan.PlanID, RelatedPlans: outcome.Plan.RelatedPlans, SavedUserID: outcome.ID})
		}
	}
	return entries
}

// recordAfterResults adds the after-phase results to the plans of the
// saved users, and the error of each failed result to the plan errors.
func recordAfterResults(saved []*SaveOutcome, related map[string][]*RelatedRecordResult) {
	for _, outcome := range saved {
		results, ok := related[outcome.Plan.PlanID]
		if !ok {
			continue
		}
		outcome.Plan.RelatedResults = append(append([]*RelatedRecordResult{}, outcome.Plan.RelatedResults...), results...)
		for _, result := range results {
			if result.Status == "failed" && result.Error != "" {
				outcome.Plan.Errors = append(outcome.Plan.Errors, result.Error)
			}
		}
	}
}
