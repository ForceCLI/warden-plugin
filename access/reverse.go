package access

import (
	"errors"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// ReverseUser is the user of a one-user audit.
type ReverseUser struct {
	ID       string
	Name     string
	Username string
}

// grantSource is a path by which the user holds a permission set: its
// profile, a direct assignment, or a permission set group.
type grantSource struct {
	assignmentType       string
	sourceID             string
	sourceName           string
	sourceAPIName        string
	sourceLabel          string
	viaPermissionSetID   string
	viaPermissionSetName string
	psgID                string
}

func (s grantSource) key() string {
	return strings.Join([]string{s.assignmentType, s.sourceID, s.viaPermissionSetID}, "|")
}

func addSource(sources *orderedMap[[]grantSource], permissionSetID string, source grantSource) {
	existing, _ := sources.get(permissionSetID)
	found := false
	for _, entry := range existing {
		if entry.key() == source.key() {
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, source)
	}
	sources.set(permissionSetID, existing)
}

func permissionSetSource(id string, parent sfapi.Record) grantSource {
	source := grantSource{
		assignmentType: AssignmentPermissionSet,
		sourceID:       id,
		sourceName:     coalesce(opt(parent, "Name"), lit(id)),
		sourceAPIName:  coalesce(opt(parent, "DeveloperName"), opt(parent, "Name"), lit(id)),
		sourceLabel:    coalesce(opt(parent, "Profile", "Name"), opt(parent, "Label"), opt(parent, "Name"), lit(id)),
	}
	if isTrue(parent, "IsOwnedByProfile") {
		source.assignmentType = AssignmentProfile
		source.sourceID = coalesce(opt(parent, "ProfileId"), lit(id))
		source.sourceName = coalesce(opt(parent, "Profile", "Name"), opt(parent, "Name"), lit(id))
	}
	return source
}

type grantContext struct {
	permissionSetIDs              []string
	psgIDs                        []string
	sourcesByPermissionSetID      *orderedMap[[]grantSource]
	metadataNameByPermissionSetID *orderedMap[metadataSource]
	mutingMetadataNameByPsgID     *orderedMap[string]
}

// collectGrantContext reads the assignments of a user and returns the
// permission sets the user holds with the sources of each.
func collectGrantContext(conn sfapi.Conn, userID string, profileMetadataNames, mutingMetadataNames map[string]string) (*grantContext, error) {
	assignments, err := queryAll(conn, strings.Join([]string{
		"SELECT PermissionSetId, PermissionSet.Name, PermissionSet.Label, PermissionSet.IsOwnedByProfile, PermissionSet.ProfileId, PermissionSet.Profile.Name, PermissionSet.Type, PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel",
		"FROM PermissionSetAssignment",
		"WHERE AssigneeId = '" + userID + "'",
	}, " "))
	if err != nil {
		return nil, err
	}
	sourcesByPermissionSetID := newOrderedMap[[]grantSource]()
	metadataNameByPermissionSetID := newOrderedMap[metadataSource]()
	mutingMetadataNameByPsgID := newOrderedMap[string]()
	psgIDs := []string{}
	psgNames := map[string]string{}
	for _, assignment := range assignments {
		psgID := text(assignment, "PermissionSetGroupId")
		if psgID == "" {
			continue
		}
		psgIDs = append(psgIDs, psgID)
		psgNames[psgID] = coalesce(
			opt(assignment, "PermissionSetGroup", "MasterLabel"),
			opt(assignment, "PermissionSetGroup", "DeveloperName"),
			lit(psgID),
		)
	}
	psgIDs = unique(psgIDs)
	directPermissionSetIDs := []string{}
	for _, assignment := range assignments {
		permissionSetID := text(assignment, "PermissionSetId")
		permissionSet := related(assignment, "PermissionSet")
		permissionSetType := text(permissionSet, "Type")
		if permissionSetID == "" || permissionSetType == "Muting" || permissionSetType == "Group" {
			continue
		}
		directPermissionSetIDs = append(directPermissionSetIDs, permissionSetID)
		addSource(sourcesByPermissionSetID, permissionSetID, permissionSetSource(permissionSetID, permissionSet))
		fullName := coalesce(opt(permissionSet, "Name"), opt(permissionSet, "DeveloperName"))
		isProfile := isTrue(permissionSet, "IsOwnedByProfile")
		if fullName != "" && (permissionSetType == "Regular" || isProfile) {
			source := metadataSource{metadataType: "PermissionSet", fullName: fullName}
			if isProfile {
				// A standard profile is read through a Metadata API name
				// that differs from its SOQL Name, so the name comes from
				// the metadata listing by the profile's record id.
				profileMetadataName := maybe{}
				if profileID := text(permissionSet, "ProfileId"); profileID != "" {
					profileMetadataName = fromMap(profileMetadataNames, prefix15(profileID))
				}
				source = metadataSource{
					metadataType: "Profile",
					fullName:     coalesce(profileMetadataName, opt(permissionSet, "Profile", "Name"), lit(fullName)),
				}
			}
			metadataNameByPermissionSetID.set(permissionSetID, source)
		}
	}
	components, err := queryAllInChunks(conn, psgIDs, func(chunk []string) string {
		return strings.Join([]string{
			"SELECT PermissionSetGroupId, PermissionSetId, PermissionSet.Name, PermissionSet.Label, PermissionSet.Type",
			"FROM PermissionSetGroupComponent",
			"WHERE PermissionSetGroupId IN (" + core.SoqlIn(chunk) + ")",
		}, " ")
	})
	if err != nil {
		return nil, err
	}
	componentPermissionSetIDs := []string{}
	componentPermissionSetsByID := map[string]sfapi.Record{}
	unresolvedComponentIDs := []string{}
	for _, component := range components {
		if text(component, "PermissionSet", "Type") != "" {
			continue
		}
		idKey, err := idPrefix(component, "PermissionSetId")
		if err != nil {
			return nil, err
		}
		if _, isMuting := mutingMetadataNames[idKey]; !isMuting {
			unresolvedComponentIDs = append(unresolvedComponentIDs, text(component, "PermissionSetId"))
		}
	}
	unresolvedComponentIDs = unique(unresolvedComponentIDs)
	if len(unresolvedComponentIDs) > 0 {
		resolvedComponents, err := queryAllInChunks(conn, unresolvedComponentIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT Id, Name, Label, Type, IsOwnedByProfile, ProfileId, Profile.Name",
				"FROM PermissionSet",
				"WHERE Id IN (" + core.SoqlIn(chunk) + ")",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		for _, permissionSet := range resolvedComponents {
			componentPermissionSetsByID[text(permissionSet, "Id")] = permissionSet
		}
	}
	for _, component := range components {
		mutingName, err := mutingNameOf(component, mutingMetadataNames)
		if err != nil {
			return nil, err
		}
		psgID := text(component, "PermissionSetGroupId")
		componentID := text(component, "PermissionSetId")
		if mutingName != "" {
			mutingMetadataNameByPsgID.set(psgID, mutingName)
			continue
		}
		permissionSet := related(component, "PermissionSet")
		if permissionSet == nil {
			permissionSet = componentPermissionSetsByID[componentID]
		}
		if text(permissionSet, "Type") != "Regular" && !isTrue(permissionSet, "IsOwnedByProfile") {
			continue
		}
		componentPermissionSetIDs = append(componentPermissionSetIDs, componentID)
		psgName := coalesce(fromMap(psgNames, psgID), lit(psgID))
		addSource(sourcesByPermissionSetID, componentID, grantSource{
			assignmentType:       AssignmentPermissionSetGroup,
			sourceID:             psgID,
			sourceName:           psgName,
			sourceAPIName:        psgName,
			sourceLabel:          psgName,
			viaPermissionSetID:   componentID,
			viaPermissionSetName: coalesce(opt(permissionSet, "Name"), lit(componentID)),
			psgID:                psgID,
		})
		if fullName := coalesce(opt(permissionSet, "Name"), opt(permissionSet, "DeveloperName")); fullName != "" {
			metadataNameByPermissionSetID.set(componentID, metadataSource{metadataType: "PermissionSet", fullName: fullName})
		}
	}
	return &grantContext{
		permissionSetIDs:              unique(append(append([]string{}, directPermissionSetIDs...), componentPermissionSetIDs...)),
		psgIDs:                        psgIDs,
		sourcesByPermissionSetID:      sourcesByPermissionSetID,
		metadataNameByPermissionSetID: metadataNameByPermissionSetID,
		mutingMetadataNameByPsgID:     mutingMetadataNameByPsgID,
	}, nil
}

// fieldScope returns the conditions that limit a FieldPermissions query
// to the object of the target and, for a single-field target, its field.
func fieldScope(target *ValidatedTarget) []string {
	conditions := []string{"AND SobjectType = '" + target.SobjectType + "'"}
	if target.FieldAPIName != "" {
		conditions = append(conditions, "AND Field = '"+target.SobjectType+"."+target.FieldAPIName+"'")
	}
	return conditions
}

// fieldMuting reads, for each muting permission set of the groups, the
// field permissions it removes, by qualified field name.
func fieldMuting(conn sfapi.Conn, target *ValidatedTarget, psgIDs []string) (psgMutingState[map[string]fieldMask], error) {
	return loadPsgMuting(conn, psgIDs, func(mutingSetIDs []string) (map[string]map[string]fieldMask, error) {
		rows, err := queryAllInChunks(conn, mutingSetIDs, func(chunk []string) string {
			return strings.Join(append([]string{
				"SELECT ParentId, Field, PermissionsRead, PermissionsEdit",
				"FROM FieldPermissions",
				"WHERE Parent.IsOwnedByProfile = false",
				"AND ParentId IN (" + core.SoqlIn(chunk) + ")",
			}, fieldScope(target)...), " ")
		})
		if err != nil {
			return nil, err
		}
		masksByPermissionSetID := map[string]map[string]fieldMask{}
		for _, row := range rows {
			fieldName := text(row, "Field")
			if fieldName == "" {
				continue
			}
			parentID := text(row, "ParentId")
			if masksByPermissionSetID[parentID] == nil {
				masksByPermissionSetID[parentID] = map[string]fieldMask{}
			}
			masksByPermissionSetID[parentID][fieldName] = fieldMask{read: isTrue(row, "PermissionsRead"), edit: isTrue(row, "PermissionsEdit")}
		}
		return masksByPermissionSetID, nil
	})
}

func objectMuting(conn sfapi.Conn, target *ValidatedTarget, psgIDs []string) (psgMutingState[ObjectAccess], error) {
	return loadPsgMuting(conn, psgIDs, func(mutingSetIDs []string) (map[string]ObjectAccess, error) {
		rows, err := queryAllInChunks(conn, mutingSetIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT ParentId, PermissionsRead, PermissionsCreate, PermissionsEdit, PermissionsDelete, PermissionsViewAllRecords, PermissionsModifyAllRecords",
				"FROM ObjectPermissions",
				"WHERE Parent.IsOwnedByProfile = false",
				"AND ParentId IN (" + core.SoqlIn(chunk) + ")",
				"AND SobjectType = '" + target.SobjectType + "'",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		masks := map[string]ObjectAccess{}
		for _, row := range rows {
			masks[text(row, "ParentId")] = toObjectAccess(row)
		}
		return masks, nil
	})
}

func baseRow(user ReverseUser, targetType string, source grantSource) Row {
	return Row{
		UserID:               user.ID,
		UserName:             user.Name,
		Username:             user.Username,
		TargetType:           targetType,
		AssignmentType:       source.assignmentType,
		SourceID:             source.sourceID,
		SourceName:           source.sourceName,
		SourceAPIName:        source.sourceAPIName,
		SourceLabel:          source.sourceLabel,
		ViaPermissionSetID:   source.viaPermissionSetID,
		ViaPermissionSetName: source.viaPermissionSetName,
		reverse:              true,
	}
}

func combineFieldMaskMaps(left, right map[string]fieldMask) map[string]fieldMask {
	combined := make(map[string]fieldMask, len(left)+len(right))
	for fieldName, mask := range left {
		combined[fieldName] = mask
	}
	for fieldName, mask := range right {
		combined[fieldName] = combineFieldMasks(combined[fieldName], mask)
	}
	return combined
}

func resolveReverse(conn sfapi.Conn, user ReverseUser, target *ValidatedTarget) (*Result, error) {
	profileMetadataNames := map[string]string{}
	mutingMetadataNames := map[string]string{}
	if target.Type == TypeRecordType {
		var err error
		if profileMetadataNames, err = profileMetadataNamesByID(conn); err != nil {
			return nil, err
		}
		if mutingMetadataNames, err = mutingPermissionSetMetadataNamesByID(conn); err != nil {
			return nil, err
		}
	}
	context, err := collectGrantContext(conn, user.ID, profileMetadataNames, mutingMetadataNames)
	if err != nil {
		return nil, err
	}
	if len(context.permissionSetIDs) == 0 {
		return resultFor(target.Type, target.TargetName, nil, nil, target.SobjectType, target.FieldAPIName), nil
	}
	rows := []Row{}
	warnings := []string{}
	sourcesOf := func(grant sfapi.Record) []grantSource {
		sources, _ := context.sourcesByPermissionSetID.get(text(grant, "ParentId"))
		return sources
	}
	switch target.Type {
	case TypeRecordType:
		missingNames := []string{}
		for _, id := range context.permissionSetIDs {
			if !context.metadataNameByPermissionSetID.has(id) {
				missingNames = append(missingNames, id)
			}
		}
		if len(missingNames) > 0 {
			return nil, newError("errorRecordTypeMetadataReadFailed", []string{"PermissionSet", strings.Join(missingNames, ", ")}, errors.New("Permission Set metadata names were not discovered."))
		}
		profileNames := []string{}
		permissionSetNames := []string{}
		for _, source := range values(context.metadataNameByPermissionSetID) {
			if source.metadataType == "Profile" {
				profileNames = append(profileNames, source.fullName)
			} else if source.metadataType == "PermissionSet" {
				permissionSetNames = append(permissionSetNames, source.fullName)
			}
		}
		profiles, err := readMetadataInBatches(conn, "Profile", profileNames, metadataReadOptions{})
		if err != nil {
			return nil, err
		}
		permissionSets, err := readMetadataInBatches(conn, "PermissionSet", permissionSetNames, metadataReadOptions{})
		if err != nil {
			return nil, err
		}
		mutingPermissionSets, err := readMetadataInBatches(conn, "MutingPermissionSet", values(context.mutingMetadataNameByPsgID), metadataReadOptions{})
		if err != nil {
			return nil, err
		}
		missingMetadataNames := append(append(append([]string{}, profiles.missing...), permissionSets.missing...), mutingPermissionSets.missing...)
		if len(missingMetadataNames) > 0 {
			warnings = append(warnings, partialMetadataWarning(missingMetadataNames))
		}
		mutedPsgIDs := map[string]bool{}
		for _, psgID := range context.mutingMetadataNameByPsgID.keys {
			fullName, _ := context.mutingMetadataNameByPsgID.get(psgID)
			// Metadata the running user cannot read is reported by the
			// warning above; the audit continues without it.
			metadata, ok := mutingPermissionSets.metadata[fullName]
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
		for _, permissionSetID := range context.metadataNameByPermissionSetID.keys {
			source, _ := context.metadataNameByPermissionSetID.get(permissionSetID)
			read := permissionSets
			if source.metadataType == "Profile" {
				read = profiles
			}
			metadata, ok := read.metadata[source.fullName]
			if !ok {
				continue
			}
			entry, err := findVisibility(metadata, source.metadataType, target.TargetName)
			if err != nil {
				return nil, err
			}
			if entry == nil || !entry.visible {
				continue
			}
			access := RecordTypeAccess{Visible: true}
			if source.metadataType == "Profile" {
				if entry.isDefault == nil {
					return nil, newError("errorRecordTypeMetadataReadFailed", []string{source.metadataType, source.fullName}, errors.New("Profile record type visibility has no default value."))
				}
				isDefault := *entry.isDefault
				access.Default = &isDefault
			}
			grantSources, _ := context.sourcesByPermissionSetID.get(permissionSetID)
			for _, grantSource := range grantSources {
				if grantSource.psgID != "" && mutedPsgIDs[grantSource.psgID] {
					continue
				}
				row := baseRow(user, TypeRecordType, grantSource)
				row.TargetName = target.TargetName
				row.Access = access
				rows = append(rows, row)
			}
		}
	case TypeField:
		if target.SobjectType == "" {
			return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
		}
		muting, err := fieldMuting(conn, target, context.psgIDs)
		if err != nil {
			return nil, err
		}
		grants, err := queryAllInChunks(conn, context.permissionSetIDs, func(chunk []string) string {
			parts := []string{
				"SELECT ParentId, SobjectType, Field, PermissionsRead, PermissionsEdit",
				"FROM FieldPermissions",
				"WHERE ParentId IN (" + core.SoqlIn(chunk) + ")",
			}
			parts = append(parts, fieldScope(target)...)
			parts = append(parts, "AND Parent.Type != 'Muting'")
			return strings.Join(parts, " ")
		})
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			rawAccess := toFieldAccess(grant)
			if !anyFieldAccess(rawAccess) {
				continue
			}
			for _, source := range sourcesOf(grant) {
				mutedMask := fieldMask{}
				if source.psgID != "" {
					mutedMask = combinePsgMuting(source.psgID, muting, map[string]fieldMask{}, combineFieldMaskMaps)[text(grant, "Field")]
				}
				access := FieldAccess{Read: rawAccess.Read && !mutedMask.read, Edit: rawAccess.Edit && !mutedMask.edit}
				if !anyFieldAccess(access) {
					continue
				}
				row := baseRow(user, TypeField, source)
				row.TargetName = coalesce(opt(grant, "Field"), lit(text(grant, "SobjectType")+"."+target.FieldAPIName))
				row.Access = access
				rows = append(rows, row)
			}
		}
	case TypeObject:
		if target.SobjectType == "" {
			return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
		}
		muting, err := objectMuting(conn, target, context.psgIDs)
		if err != nil {
			return nil, err
		}
		grants, err := queryAllInChunks(conn, context.permissionSetIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT ParentId, SobjectType, PermissionsRead, PermissionsCreate, PermissionsEdit, PermissionsDelete, PermissionsViewAllRecords, PermissionsModifyAllRecords",
				"FROM ObjectPermissions",
				"WHERE ParentId IN (" + core.SoqlIn(chunk) + ")",
				"AND SobjectType = '" + target.SobjectType + "'",
				"AND Parent.Type != 'Muting'",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			rawAccess := toObjectAccess(grant)
			if !anyObjectAccess(rawAccess) {
				continue
			}
			for _, source := range sourcesOf(grant) {
				muted := ObjectAccess{}
				if source.psgID != "" {
					muted = combinePsgMuting(source.psgID, muting, ObjectAccess{}, combineObjectAccess)
				}
				access := withoutMuted(rawAccess, muted)
				if !anyObjectAccess(access) {
					continue
				}
				row := baseRow(user, TypeObject, source)
				row.TargetName = coalesce(opt(grant, "SobjectType"), lit(target.SobjectType))
				row.Access = access
				rows = append(rows, row)
			}
		}
	case TypeApexClass, TypeVfPage, TypeCustomPermission:
		setupEntityType := "CustomPermission"
		if target.Type == TypeApexClass {
			setupEntityType = "ApexClass"
		} else if target.Type == TypeVfPage {
			setupEntityType = "ApexPage"
		}
		grants, err := queryAllInChunks(conn, context.permissionSetIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT ParentId",
				"FROM SetupEntityAccess",
				"WHERE ParentId IN (" + core.SoqlIn(chunk) + ")",
				"AND SetupEntityType = '" + setupEntityType + "'",
				"AND SetupEntityId = '" + target.SetupEntityID + "'",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			for _, source := range sourcesOf(grant) {
				row := baseRow(user, target.Type, source)
				row.TargetName = target.TargetName
				row.Access = EnabledAccess{Enabled: true}
				rows = append(rows, row)
			}
		}
	default:
		grants, err := queryAllInChunks(conn, context.permissionSetIDs, func(chunk []string) string {
			return strings.Join([]string{
				"SELECT ParentId, Name, Visibility",
				"FROM PermissionSetTabSetting",
				"WHERE ParentId IN (" + core.SoqlIn(chunk) + ")",
				"AND Name = '" + target.TargetName + "'",
				"AND Visibility != 'Hidden'",
			}, " ")
		})
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			for _, source := range sourcesOf(grant) {
				row := baseRow(user, TypeTab, source)
				row.TargetName = target.TargetName
				row.Access = TabAccess{Visibility: text(grant, "Visibility")}
				rows = append(rows, row)
			}
		}
	}
	if target.Type == TypeTab {
		warnings = append(warnings, tabProfileWarning)
	}
	return resultFor(target.Type, target.TargetName, rows, warnings, target.SobjectType, target.FieldAPIName), nil
}

// ResolveReverseAccess returns the paths by which one user has access to
// the target.  A field or object target without a field or with only an
// sObject reports every matching grant on that sObject.
func ResolveReverseAccess(conn sfapi.Conn, user ReverseUser, target *ValidatedTarget) (*Result, error) {
	result, err := resolveReverse(conn, user, target)
	if err != nil {
		return nil, queryFailure(target, err)
	}
	return result, nil
}

// ReverseCsvColumns returns the CSV columns of a one-user audit of a
// target type.
func ReverseCsvColumns(targetType string) []string {
	switch targetType {
	case TypeField:
		return FieldCsvColumns()
	case TypeObject:
		return ObjectCsvColumns()
	case TypeTab:
		return TabCsvColumns()
	case TypeRecordType:
		return RecordTypeCsvColumns()
	}
	return EnabledCsvColumns()
}
