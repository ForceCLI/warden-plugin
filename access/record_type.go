package access

import (
	"errors"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// metadataSource is the Metadata API component that holds the record
// type visibility of a permission set.  metadataType is "Profile" for a
// permission set that a profile owns, and "PermissionSet" otherwise.
type metadataSource struct {
	metadataType string
	fullName     string
}

type candidateSources struct {
	permissionSetByID             *orderedMap[sfapi.Record]
	metadataNameByPermissionSetID *orderedMap[metadataSource]
	mutingMetadataNameByPsgID     *orderedMap[string]
}

// mutingNameOf returns the metadata name of a group component that is a
// muting permission set, or "".
//
// Muting permission sets are a separate object: their component rows
// have a null PermissionSet relationship and their ids are not returned
// by a PermissionSet query, so the name comes from the metadata listing.
func mutingNameOf(component sfapi.Record, mutingMetadataNames map[string]string) (string, error) {
	idKey, err := idPrefix(component, "PermissionSetId")
	if err != nil {
		return "", err
	}
	if name, ok := mutingMetadataNames[idKey]; ok {
		return name, nil
	}
	if text(component, "PermissionSet", "Type") == "Muting" {
		return text(component, "PermissionSet", "Name"), nil
	}
	return "", nil
}

// discoverCandidateSources finds the profiles of the active users and
// the permission sets assigned to them, directly or through a group.
func discoverCandidateSources(conn sfapi.Conn) (*candidateSources, error) {
	activeUsers, err := queryAll(conn, "SELECT ProfileId FROM User WHERE IsActive = true")
	if err != nil {
		return nil, err
	}
	profileIDs := []string{}
	for _, user := range activeUsers {
		if profileID := text(user, "ProfileId"); profileID != "" {
			profileIDs = append(profileIDs, profileID)
		}
	}
	profileIDs = unique(profileIDs)
	profileMetadataNames, err := profileMetadataNamesByID(conn)
	if err != nil {
		return nil, err
	}
	mutingMetadataNames, err := mutingPermissionSetMetadataNamesByID(conn)
	if err != nil {
		return nil, err
	}
	profiles, err := queryAllInChunks(conn, profileIDs, func(chunk []string) string {
		return "SELECT Id, Name FROM Profile WHERE Id IN (" + core.SoqlIn(chunk) + ")"
	})
	if err != nil {
		return nil, err
	}
	profilesByID := map[string]sfapi.Record{}
	for _, profile := range profiles {
		profilesByID[text(profile, "Id")] = profile
	}
	profilePermissionSets, err := queryAllInChunks(conn, profileIDs, func(chunk []string) string {
		return strings.Join([]string{
			"SELECT Id, Name, Label, Type, IsOwnedByProfile, ProfileId, Profile.Name",
			"FROM PermissionSet",
			"WHERE IsOwnedByProfile = true",
			"AND ProfileId IN (" + core.SoqlIn(chunk) + ")",
		}, " ")
	})
	if err != nil {
		return nil, err
	}
	assignments, err := queryAll(conn, strings.Join([]string{
		"SELECT PermissionSetId, PermissionSet.Type, PermissionSet.IsOwnedByProfile, PermissionSetGroupId",
		"FROM PermissionSetAssignment",
		"WHERE Assignee.IsActive = true",
	}, " "))
	if err != nil {
		return nil, err
	}
	permissionSetIDs := []string{}
	mutingMetadataNameByPsgID := newOrderedMap[string]()
	psgIDs := []string{}
	for _, assignment := range assignments {
		if psgID := text(assignment, "PermissionSetGroupId"); psgID != "" {
			psgIDs = append(psgIDs, psgID)
		}
	}
	psgIDs = unique(psgIDs)
	for _, assignment := range assignments {
		permissionSetID := text(assignment, "PermissionSetId")
		if permissionSetID != "" &&
			text(assignment, "PermissionSet", "Type") == "Regular" &&
			!isTrue(assignment, "PermissionSet", "IsOwnedByProfile") {
			permissionSetIDs = append(permissionSetIDs, permissionSetID)
		}
	}
	components, err := queryAllInChunks(conn, psgIDs, func(chunk []string) string {
		return strings.Join([]string{
			"SELECT PermissionSetGroupId, PermissionSetId, PermissionSet.Name, PermissionSet.Type",
			"FROM PermissionSetGroupComponent",
			"WHERE PermissionSetGroupId IN (" + core.SoqlIn(chunk) + ")",
		}, " ")
	})
	if err != nil {
		return nil, err
	}
	for _, component := range components {
		mutingName, err := mutingNameOf(component, mutingMetadataNames)
		if err != nil {
			return nil, err
		}
		if mutingName != "" {
			mutingMetadataNameByPsgID.set(text(component, "PermissionSetGroupId"), mutingName)
			continue
		}
		if text(component, "PermissionSet", "Type") != "Group" {
			permissionSetIDs = append(permissionSetIDs, text(component, "PermissionSetId"))
		}
	}
	permissionSetIDs = unique(permissionSetIDs)
	permissionSetByID := newOrderedMap[sfapi.Record]()
	metadataNameByPermissionSetID := newOrderedMap[metadataSource]()
	profilePermissionSetByProfileID := map[string]sfapi.Record{}
	for _, permissionSet := range profilePermissionSets {
		if profileID := text(permissionSet, "ProfileId"); profileID != "" {
			profilePermissionSetByProfileID[profileID] = permissionSet
		}
	}
	for _, profileID := range profileIDs {
		profile := profilesByID[profileID]
		profilePermissionSet := profilePermissionSetByProfileID[profileID]
		metadataName := profileMetadataNames[prefix15(profileID)]
		// Profiles the Metadata API does not list (Automated Process and
		// other system profiles) cannot be audited for record type
		// visibility, so they are skipped instead of failing the audit.
		if profile == nil || profilePermissionSet == nil || metadataName == "" {
			continue
		}
		permissionSetID := text(profilePermissionSet, "Id")
		owned := withID(profilePermissionSet, permissionSetID)
		owned["IsOwnedByProfile"] = true
		owned["ProfileId"] = profileID
		owned["Profile"] = sfapi.Record{"Name": profile["Name"]}
		permissionSetByID.set(permissionSetID, owned)
		metadataNameByPermissionSetID.set(permissionSetID, metadataSource{metadataType: "Profile", fullName: metadataName})
	}
	if len(permissionSetIDs) > 0 {
		regularPermissionSets, err := queryAllInChunks(conn, permissionSetIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT Id, Name, Label, Type, IsOwnedByProfile, ProfileId, Profile.Name",
				"FROM PermissionSet",
				"WHERE Id IN (" + core.SoqlIn(chunk) + ")",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		regularByID := map[string]bool{}
		for _, permissionSet := range regularPermissionSets {
			regularByID[text(permissionSet, "Id")] = true
		}
		missingIDs := []string{}
		for _, id := range permissionSetIDs {
			if !regularByID[id] {
				missingIDs = append(missingIDs, id)
			}
		}
		if len(missingIDs) > 0 {
			return nil, newError("errorRecordTypeMetadataReadFailed", []string{"PermissionSet", strings.Join(missingIDs, ", ")}, errors.New("Permission Set source discovery was incomplete."))
		}
		for _, permissionSet := range regularPermissionSets {
			if text(permissionSet, "Type") == "Group" || isTrue(permissionSet, "IsOwnedByProfile") {
				continue
			}
			id := text(permissionSet, "Id")
			fullName := coalesce(opt(permissionSet, "Name"), opt(permissionSet, "DeveloperName"))
			if fullName == "" {
				return nil, newError("errorRecordTypeMetadataReadFailed", []string{"PermissionSet", id}, errors.New("Permission Set has no metadata name."))
			}
			permissionSetByID.set(id, permissionSet)
			metadataNameByPermissionSetID.set(id, metadataSource{metadataType: "PermissionSet", fullName: fullName})
		}
	}
	return &candidateSources{
		permissionSetByID:             permissionSetByID,
		metadataNameByPermissionSetID: metadataNameByPermissionSetID,
		mutingMetadataNameByPsgID:     mutingMetadataNameByPsgID,
	}, nil
}

// grantFor returns the access of a visible entry.  A profile entry must
// say whether the record type is the default.
func grantFor(entry *recordTypeVisibility, metadataType string) (RecordTypeAccess, error) {
	if metadataType != "Profile" {
		return RecordTypeAccess{Visible: true}, nil
	}
	if entry.isDefault == nil {
		return RecordTypeAccess{}, newError("errorRecordTypeMetadataReadFailed", []string{metadataType, entry.recordType}, errors.New("Profile record type visibility has no default value."))
	}
	isDefault := *entry.isDefault
	return RecordTypeAccess{Visible: true, Default: &isDefault}, nil
}

// values returns the values of a map in key insertion order.
func values[V any](m *orderedMap[V]) []V {
	list := make([]V, 0, m.len())
	for _, key := range m.keys {
		list = append(list, m.values[key])
	}
	return list
}

var recordTypeResolver = &Resolver{
	Type:       TypeRecordType,
	validate:   validateRecordTypeTarget,
	csvColumns: RecordTypeCsvColumns,
	resolve: func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
		if target.SobjectType == "" {
			return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
		}
		result, err := resolveRecordTypeAccess(conn, target)
		if err != nil {
			return nil, queryFailure(target, err)
		}
		return result, nil
	},
}

func resolveRecordTypeAccess(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
	sources, err := discoverCandidateSources(conn)
	if err != nil {
		return nil, err
	}
	metadataNamesByType := map[string][]string{}
	for _, source := range values(sources.metadataNameByPermissionSetID) {
		metadataNamesByType[source.metadataType] = append(metadataNamesByType[source.metadataType], source.fullName)
	}
	profileMetadata, err := readMetadataInBatches(conn, "Profile", metadataNamesByType["Profile"], metadataReadOptions{})
	if err != nil {
		return nil, err
	}
	permissionSetMetadata, err := readMetadataInBatches(conn, "PermissionSet", metadataNamesByType["PermissionSet"], metadataReadOptions{})
	if err != nil {
		return nil, err
	}
	mutingMetadata, err := readMetadataInBatches(conn, "MutingPermissionSet", values(sources.mutingMetadataNameByPsgID), metadataReadOptions{})
	if err != nil {
		return nil, err
	}
	mutedPsgIDs := map[string]bool{}
	for _, psgID := range sources.mutingMetadataNameByPsgID.keys {
		fullName, _ := sources.mutingMetadataNameByPsgID.get(psgID)
		metadata, ok := mutingMetadata.metadata[fullName]
		// A muting permission set the running user cannot read leaves the
		// group unmuted, which may report more access than the user has.
		// The partial-response warning below says so.
		if !ok {
			continue
		}
		entry, err := findVisibility(metadata, "MutingPermissionSet", target.TargetName)
		if err != nil {
			return nil, err
		}
		if entry != nil && entry.visible {
			mutedPsgIDs[psgID] = true
		}
	}
	grantByPermissionSetID := newOrderedMap[Access]()
	for _, permissionSetID := range sources.metadataNameByPermissionSetID.keys {
		source, _ := sources.metadataNameByPermissionSetID.get(permissionSetID)
		read := permissionSetMetadata
		if source.metadataType == "Profile" {
			read = profileMetadata
		}
		// A source the running user cannot read is left out of the audit
		// and reported in the partial-response warning below.
		metadata, ok := read.metadata[source.fullName]
		if !ok {
			continue
		}
		entry, err := findVisibility(metadata, source.metadataType, target.TargetName)
		if err != nil {
			return nil, err
		}
		if entry != nil && entry.visible {
			grant, err := grantFor(entry, source.metadataType)
			if err != nil {
				return nil, err
			}
			grantByPermissionSetID.set(permissionSetID, grant)
		}
	}
	rows, err := resolveAssignees(conn, TypeRecordType, target.TargetName, grantByPermissionSetID, sources.permissionSetByID, func(grant Access, context buildContext) (Access, bool) {
		if context.psgID != "" && mutedPsgIDs[context.psgID] {
			return nil, false
		}
		return grant, true
	}, assigneeOptions{})
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	missingNames := append(append(append([]string{}, profileMetadata.missing...), permissionSetMetadata.missing...), mutingMetadata.missing...)
	if len(missingNames) > 0 {
		warnings = append(warnings, partialMetadataWarning(missingNames))
	}
	return resultFor(TypeRecordType, target.TargetName, rows, warnings, target.SobjectType, ""), nil
}
