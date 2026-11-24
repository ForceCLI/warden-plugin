package access

import (
	"errors"
	"strconv"
	"strings"

	"github.com/ForceCLI/warden-plugin/sfapi"
)

const (
	metadataBatchSize   = 10
	metadataConcurrency = 2
)

func metadataFailure(metadataType string, names []string, cause error) error {
	return newError("errorRecordTypeMetadataReadFailed", []string{metadataType, strings.Join(names, ", ")}, cause)
}

// validateBatch returns the components of a response that have a
// fullName.  A component that was not requested, or that appears twice,
// is an error.
func validateBatch(metadataType string, requestedNames []string, items []sfapi.Record) ([]sfapi.Record, error) {
	requested := map[string]bool{}
	for _, name := range requestedNames {
		requested[name] = true
	}
	seen := map[string]bool{}
	components := []sfapi.Record{}
	for _, item := range items {
		if item == nil {
			return nil, metadataFailure(metadataType, requestedNames, errors.New("Metadata API returned a non-object component."))
		}
		// Large orgs return a component without a usable fullName for a
		// requested component the running user cannot read.  It is skipped
		// here and reported as missing by readMetadataInBatches.
		fullName, isString := item["fullName"].(string)
		if !isString || fullName == "" {
			continue
		}
		if !requested[fullName] {
			return nil, metadataFailure(metadataType, requestedNames, errors.New("Unexpected component "+fullName+"."))
		}
		if seen[fullName] {
			return nil, metadataFailure(metadataType, requestedNames, errors.New("Duplicate component "+fullName+"."))
		}
		seen[fullName] = true
		components = append(components, item)
	}
	return components, nil
}

// partialMetadataWarning is the warning of a record-type audit that
// could not read every profile, permission set or muting permission set.
func partialMetadataWarning(missingNames []string) string {
	examples := missingNames[:min(5, len(missingNames))]
	suffix := ""
	if len(missingNames) > len(examples) {
		suffix = ", …"
	}
	return "Partial response: " + strconv.Itoa(len(missingNames)) + " metadata component(s) could not be read through the Metadata API " +
		"(" + strings.Join(examples, ", ") + suffix + "). Affected profiles and permission sets are omitted, and any muting they define " +
		`is not applied, so this audit may be incomplete. Grant the running user "API Enabled", ` +
		`"View Setup and Configuration", and either "Modify Metadata Through Metadata API Functions" or "Modify All Data" ` +
		"for a full accounting."
}

// metadataReadOptions sets the number of components of one read request.
// Zero means the default of 10.  The JavaScript library also takes a
// number of concurrent requests; this port reads the batches one after
// another, in order.
type metadataReadOptions struct {
	batchSize   int
	concurrency int
}

type metadataReadResult struct {
	metadata map[string]sfapi.Record
	missing  []string
}

// readMetadataInBatches reads components by fullName.  A failed read, or
// a response with an unexpected or duplicated component, is an error.  A
// requested component that the response leaves out is returned in
// missing: on large orgs the Metadata API omits components the running
// user cannot read instead of failing.
func readMetadataInBatches(conn sfapi.Conn, metadataType string, fullNames []string, options metadataReadOptions) (*metadataReadResult, error) {
	batchSize := options.batchSize
	if batchSize == 0 {
		batchSize = metadataBatchSize
	}
	concurrency := options.concurrency
	if concurrency == 0 {
		concurrency = metadataConcurrency
	}
	if batchSize < 0 {
		return nil, errors.New("Metadata batchSize must be greater than 0.")
	}
	if concurrency < 0 {
		return nil, errors.New("Metadata concurrency must be greater than 0.")
	}
	result := &metadataReadResult{metadata: map[string]sfapi.Record{}, missing: []string{}}
	uniqueNames := unique(fullNames)
	if len(uniqueNames) == 0 {
		return result, nil
	}
	for start := 0; start < len(uniqueNames); start += batchSize {
		batch := uniqueNames[start:min(start+batchSize, len(uniqueNames))]
		response, err := conn.MetadataRead(metadataType, batch)
		if err != nil {
			return nil, metadataFailure(metadataType, batch, err)
		}
		components, err := validateBatch(metadataType, batch, response)
		if err != nil {
			return nil, err
		}
		for _, component := range components {
			fullName := component["fullName"].(string)
			if _, exists := result.metadata[fullName]; exists {
				return nil, metadataFailure(metadataType, []string{fullName}, errors.New("Duplicate component "+fullName+"."))
			}
			result.metadata[fullName] = component
		}
	}
	for _, name := range uniqueNames {
		if _, ok := result.metadata[name]; !ok {
			result.missing = append(result.missing, name)
		}
	}
	return result, nil
}

// metadataNamesByID lists the components of a type and maps the first 15
// characters of each record id to the fullName the Metadata API reads.
//
// The listing is needed because a standard profile has a metadata name
// that differs from its SOQL Name ("Contract Manager" is
// "ContractManager", "Standard User" is "Standard"), system profiles such
// as Automated Process are not in the listing at all, and the ids of
// muting permission sets are not returned by a PermissionSet query.
// Callers skip the ids the map does not contain.
func metadataNamesByID(conn sfapi.Conn, metadataType string) (map[string]string, error) {
	items, err := conn.MetadataList(metadataType)
	if err != nil {
		return nil, err
	}
	byID := map[string]string{}
	for _, item := range items {
		id, _ := item["id"].(string)
		fullName, _ := item["fullName"].(string)
		if id != "" && fullName != "" {
			byID[prefix15(id)] = fullName
		}
	}
	return byID, nil
}

func profileMetadataNamesByID(conn sfapi.Conn) (map[string]string, error) {
	return metadataNamesByID(conn, "Profile")
}

func mutingPermissionSetMetadataNamesByID(conn sfapi.Conn) (map[string]string, error) {
	return metadataNamesByID(conn, "MutingPermissionSet")
}

type recordTypeVisibility struct {
	recordType string
	visible    bool
	// isDefault is nil when the entry has no default element.
	isDefault *bool
}

// recordTypeVisibilities returns the recordTypeVisibilities entries of a
// profile, permission set or muting permission set.
func recordTypeVisibilities(metadata sfapi.Record, metadataType string) ([]recordTypeVisibility, error) {
	fullName, _ := metadata["fullName"].(string)
	var entries []any
	switch raw := metadata["recordTypeVisibilities"].(type) {
	case nil:
	case []any:
		entries = raw
	default:
		entries = []any{raw}
	}
	normalized := []recordTypeVisibility{}
	for _, item := range entries {
		entry, isRecord := item.(sfapi.Record)
		recordType, hasRecordType := entry["recordType"].(string)
		visible, hasVisible := entry["visible"].(bool)
		if !isRecord || !hasRecordType || !hasVisible {
			return nil, metadataFailure(metadataType, []string{fullName}, errors.New("Malformed recordTypeVisibilities entry."))
		}
		visibility := recordTypeVisibility{recordType: recordType, visible: visible}
		if rawDefault, present := entry["default"]; present {
			isDefault, isBool := rawDefault.(bool)
			if !isBool {
				return nil, metadataFailure(metadataType, []string{fullName}, errors.New("Malformed record type default value."))
			}
			visibility.isDefault = &isDefault
		}
		normalized = append(normalized, visibility)
	}
	return normalized, nil
}

// findVisibility returns the entry of a component for one record type,
// or nil.  Two entries for the record type are an error.
func findVisibility(metadata sfapi.Record, metadataType, targetName string) (*recordTypeVisibility, error) {
	visibilities, err := recordTypeVisibilities(metadata, metadataType)
	if err != nil {
		return nil, err
	}
	var matches []recordTypeVisibility
	for _, entry := range visibilities {
		if entry.recordType == targetName {
			matches = append(matches, entry)
		}
	}
	if len(matches) > 1 {
		fullName, _ := metadata["fullName"].(string)
		return nil, newError("errorRecordTypeMetadataReadFailed", []string{metadataType, fullName}, errors.New("Duplicate visibility for "+targetName+"."))
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &matches[0], nil
}
