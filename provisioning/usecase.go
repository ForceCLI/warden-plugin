package provisioning

import (
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Request is a provisioning run that plans and applies in one call.
type Request struct {
	Options
	// DryRun reports what a run would do and changes no record.
	DryRun bool
}

func definitionSource(options Options) core.DefinitionSource {
	return core.DefinitionSource{
		UsersDoc:         options.UsersDoc,
		PersonasDoc:      options.PersonasDoc,
		RelatedDoc:       options.RelatedDoc,
		UsersPath:        options.UsersPath,
		PersonasPath:     options.PersonasPath,
		RelatedPath:      options.RelatedPath,
		InputFormat:      options.InputFormat,
		CsvListDelimiter: options.CsvListDelimiter,
	}
}

func assembleResult(results []*UserResult, refs *ResolvedRefs, licenseUsage []LicenseUsage, dryRun bool) *Result {
	sortByOrder(results)
	users := publicResults(results)
	statuses := resultStatuses(users, func(user PublicUserResult) string { return user.Status })
	result := &Result{Summary: Summarize(statuses, len(refs.Warnings)+licenseShortfalls(licenseUsage)), Users: users}
	if dryRun {
		result.Licenses = licenseUsage
		result.PermissionSetLicenses = &PermissionSetLicenseNote{Evaluated: false, Note: "not evaluated"}
	}
	return result
}

func runLive(conn sfapi.Conn, plans []*UserPlan, refs *ResolvedRefs, validationResults []*UserResult, cleanupOnFailure bool, message MessageFunc) ([]*UserResult, error) {
	if err := ApplyBeforeUserSaves(conn, plans, nil); err != nil {
		return nil, err
	}
	var invalidResults []*UserResult
	for _, plan := range plans {
		if len(plan.Errors) > 0 {
			invalidResults = append(invalidResults, ToUserResult(MarkRelatedUnapplied(plan), "failed", &UserResultOverrides{OmitExistingID: true}))
		}
	}
	outcomes, err := ExecuteBulkUserSaves(conn, plans, nil)
	if err != nil {
		return nil, err
	}
	var saveFailures []*UserResult
	var savedOutcomes []*SaveOutcome
	for _, outcome := range outcomes {
		if outcome.Success {
			savedOutcomes = append(savedOutcomes, outcome)
			continue
		}
		saveFailures = append(saveFailures, ToUserResult(MarkRelatedUnapplied(outcome.Plan), "failed", &UserResultOverrides{
			Errors:         AppendCrossReferenceCandidates(outcome.Errors, outcome.Plan.Target),
			OmitExistingID: true,
		}))
	}
	// The after relationships are written between the User saves and the
	// work for each saved user.
	relatedByPlanID, err := ApplyRelatedPhase(conn, afterEntries(savedOutcomes), "after", nil)
	if err != nil {
		return nil, err
	}
	recordAfterResults(savedOutcomes, relatedByPlanID)
	var postSaveResults []*UserResult
	var savedPlanIDs []string
	for _, outcome := range savedOutcomes {
		result, err := ApplySavedPlan(SavedPlanOptions{Conn: conn, Outcome: outcome, Refs: refs, Message: message})
		if err != nil {
			return nil, err
		}
		postSaveResults = append(postSaveResults, result)
		savedPlanIDs = append(savedPlanIDs, outcome.Plan.PlanID)
	}
	results := append(append(invalidResults, saveFailures...), postSaveResults...)
	if cleanupOnFailure {
		CleanupProvisionFailures(conn, plans, results, savedPlanIDs)
	}
	return append(append([]*UserResult{}, validationResults...), results...), nil
}

// Execute plans and applies a provisioning run in one call, or reports
// what the run would do when the request is a dry run.  Unlike Plan it
// checks the form of the users and personas documents, and it reports a
// user that lists personas when no personas definition was supplied.
func Execute(conn sfapi.Conn, request Request) (*Result, error) {
	message := Message
	definitionMessages := core.DefinitionMessages{
		PersonasWithoutDefinition: func(userKey string) string { return message("errorPersonasWithoutDefinition", userKey) },
		InvalidJSON:               func(path, detail string) string { return message("errorInvalidJson", path, detail) },
	}
	// One describe cache for the run, shared by User and the sObjects of
	// the relationships.
	describeCache := core.DescribeCache{}
	var fieldMap *core.FieldMap
	var definitions *core.Definitions
	var err error
	if request.UsersDoc != nil {
		if definitions, err = core.LoadValidatedDefinitions(definitionSource(request.Options), core.BuildFieldMap(nil), definitionMessages); err != nil {
			return nil, err
		}
		if fieldMap, err = core.DescribeUserFields(conn, describeCache); err != nil {
			return nil, err
		}
	} else {
		if fieldMap, err = core.DescribeUserFields(conn, describeCache); err != nil {
			return nil, err
		}
		if definitions, err = core.LoadValidatedDefinitions(definitionSource(request.Options), fieldMap, definitionMessages); err != nil {
			return nil, err
		}
	}
	personasSupplied := definitions.PersonasSupplied
	if request.PersonasSupplied != nil {
		personasSupplied = *request.PersonasSupplied
	}
	personas, err := personasOf(definitions.PersonasDoc)
	if err != nil {
		return nil, err
	}
	if err := ValidatePersonaModes(personas); err != nil {
		return nil, err
	}
	externalID, err := ValidateExternalIDFieldForFlag(request.ExternalID, fieldMap)
	if err != nil {
		return nil, err
	}
	var catalog *Catalog
	if definitions.RelatedDoc != nil {
		if catalog, err = AssertValidRelatedCatalog(definitions.RelatedDoc, fieldMap, message); err != nil {
			return nil, err
		}
	}
	rawUsers, _ := definitions.UsersDoc.Get("users")
	users, err := ValidateAndCanonicalizeUsers(rawUsers, personas, fieldMap, personasSupplied, relatedSelectionFor(catalog))
	if err != nil {
		return nil, err
	}
	myDomain := DeriveMyDomain(conn.InstanceURL())
	defaultExternalIDField := ""
	if externalID != nil {
		defaultExternalIDField = externalID.Name
	}
	_, validUsers, validationFailureUsers := splitUsers(users)
	refs, err := ResolveReferences(conn, personas, usersOf(validUsers))
	if err != nil {
		return nil, err
	}
	existingResolution, err := GetExistingUsers(conn, usersOf(validUsers), ExistingUsersOptions{DefaultExternalIDField: defaultExternalIDField, DefaultFuzzyUsername: request.FuzzyUsername, FieldMap: fieldMap})
	if err != nil {
		return nil, err
	}
	preflight := EmptyPreflightResult()
	if catalog != nil {
		preflight, err = RunRelatedPreflight(PreflightOptions{Conn: conn, Catalog: catalog, Selected: selectedRelationships(validUsers), Cache: describeCache, UserFieldMap: fieldMap, Message: message})
		if err != nil {
			return nil, err
		}
	}
	refs.Warnings = append(refs.Warnings, preflight.Warnings...)
	var relatedPlansByOrder *RelatedPlansByOrder
	if catalog != nil {
		relatedPlansByOrder, err = BuildRelatedPlans(RelatedPlanOptions{Conn: conn, Users: validUsers, Catalog: catalog, Preflight: preflight, UserFieldMap: fieldMap, Cache: describeCache, Message: message})
		if err != nil {
			return nil, err
		}
	}
	plans := BuildUserPlans(UserPlanOptions{
		ValidUsers:             validUsers,
		Refs:                   refs,
		Existing:               existingResolution,
		FieldMap:               fieldMap,
		DefaultExternalIDField: defaultExternalIDField,
		MyDomain:               myDomain,
		DryRun:                 request.DryRun,
		Message:                message,
		RelatedPlansByOrder:    relatedPlansByOrder,
	})
	WarnRelatedProfileLicenses(conn, plans, &refs.Warnings, message)
	validationResults := ValidationResultsFor(validationFailureUsers)
	if !request.DryRun {
		results, err := runLive(conn, plans, refs, validationResults, request.CleanupOnFailure, message)
		if err != nil {
			return nil, err
		}
		return assembleResult(results, refs, nil, false), nil
	}
	licenseUsage, err := CalculateUserLicenseUsage(conn, plans)
	if err != nil {
		return nil, err
	}
	results := append([]*UserResult{}, validationResults...)
	for _, plan := range plans {
		planned, err := PlanDryRunResult(conn, plan, refs)
		if err != nil {
			return nil, err
		}
		results = append(results, planned)
	}
	return assembleResult(results, refs, licenseUsage, true), nil
}
