package access

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// ComputeStats counts the distinct users and the distinct sources of
// each assignment type in rows.
func ComputeStats(rows []Row) Stats {
	users := map[string]bool{}
	sources := map[string]map[string]bool{
		AssignmentProfile:            {},
		AssignmentPermissionSet:      {},
		AssignmentPermissionSetGroup: {},
	}
	for _, row := range rows {
		users[row.UserID] = true
		if bySource, ok := sources[row.AssignmentType]; ok {
			bySource[row.SourceID] = true
		}
	}
	return Stats{
		TotalActiveUsersWithAccess: len(users),
		ProfileGrants:              len(sources[AssignmentProfile]),
		PermissionSetGrants:        len(sources[AssignmentPermissionSet]),
		PermissionSetGroupGrants:   len(sources[AssignmentPermissionSetGroup]),
	}
}

func keyFor(row Row) string {
	return strings.Join([]string{row.UserID, row.AssignmentType, row.SourceID, row.ViaPermissionSetID}, "|")
}

// buildContext says how an assignment reaches a permission set that
// holds a grant: directly, or as a component of a permission set group.
type buildContext struct {
	assignmentType  string
	permissionSetID string
	psgID           string
}

// buildAccess returns the access a grant gives in a context, or false
// when the grant gives none there.
type buildAccess func(grant Access, context buildContext) (Access, bool)

type assigneeOptions struct {
	// preparePsg is called with the ids of the permission set groups
	// that contain a granting permission set, before assignments are read.
	preparePsg func(psgIDs []string) error
}

type psgEntry struct {
	psgID   string
	psgName string
}

// resolveAssignees returns a row for each active user and each path by
// which the user is assigned a permission set of grantByPermissionSetID:
// through a profile, directly, or through a permission set group.
func resolveAssignees(conn sfapi.Conn, targetType, targetName string, grantByPermissionSetID *orderedMap[Access], permissionSetByID *orderedMap[sfapi.Record], build buildAccess, options assigneeOptions) ([]Row, error) {
	directPermissionSetIDs := []string{}
	for _, id := range grantByPermissionSetID.keys {
		parent, _ := permissionSetByID.get(id)
		if text(parent, "Type") != "Group" {
			directPermissionSetIDs = append(directPermissionSetIDs, id)
		}
	}
	componentPermissionSetIDs := []string{}
	for _, id := range directPermissionSetIDs {
		parent, _ := permissionSetByID.get(id)
		if !isTrue(parent, "IsOwnedByProfile") {
			componentPermissionSetIDs = append(componentPermissionSetIDs, id)
		}
	}
	psgComponentRows, err := queryAllInChunks(conn, componentPermissionSetIDs, func(chunk []string) string {
		return strings.Join([]string{
			"SELECT PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel, PermissionSetId, PermissionSet.Name, PermissionSet.Type",
			"FROM PermissionSetGroupComponent",
			"WHERE PermissionSetId IN (" + core.SoqlIn(chunk) + ")",
		}, " ")
	})
	if err != nil {
		return nil, err
	}
	psgByPermissionSetID := newOrderedMap[[]psgEntry]()
	psgNameByID := map[string]string{}
	psgAPINameByID := map[string]string{}
	psgLabelByID := map[string]string{}
	psgIDs := []string{}
	for _, row := range psgComponentRows {
		psgID := text(row, "PermissionSetGroupId")
		psgIDs = append(psgIDs, psgID)
		if developerName := text(row, "PermissionSetGroup", "DeveloperName"); developerName != "" {
			psgAPINameByID[psgID] = developerName
		}
		if masterLabel := text(row, "PermissionSetGroup", "MasterLabel"); masterLabel != "" {
			psgLabelByID[psgID] = masterLabel
		}
		psgName := coalesce(
			opt(row, "PermissionSetGroup", "MasterLabel"),
			opt(row, "PermissionSetGroup", "DeveloperName"),
			fromMap(psgNameByID, psgID),
			lit("(Unnamed Permission Set Group)"),
		)
		psgNameByID[psgID] = psgName
		permissionSetID := text(row, "PermissionSetId")
		entries, _ := psgByPermissionSetID.get(permissionSetID)
		psgByPermissionSetID.set(permissionSetID, append(entries, psgEntry{psgID: psgID, psgName: psgName}))
	}
	psgIDs = unique(psgIDs)
	if options.preparePsg != nil {
		if err := options.preparePsg(psgIDs); err != nil {
			return nil, err
		}
	}
	if len(directPermissionSetIDs) == 0 && len(psgIDs) == 0 {
		return []Row{}, nil
	}
	assignmentRowsByID := newOrderedMap[sfapi.Record]()
	assignmentSelect := []string{
		"SELECT Id, AssigneeId, Assignee.Name, Assignee.Username, Assignee.IsActive, PermissionSetId, PermissionSet.Name, PermissionSet.IsOwnedByProfile, PermissionSet.ProfileId, PermissionSet.Profile.Name, PermissionSet.Type, PermissionSetGroupId, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel",
		"FROM PermissionSetAssignment",
		"WHERE Assignee.IsActive = true",
	}
	assignmentQuery := func(idField string) func(chunk []string) string {
		return func(chunk []string) string {
			parts := append(append([]string{}, assignmentSelect...), "AND "+idField+" IN ("+core.SoqlIn(chunk)+")", "ORDER BY Assignee.Name, Id")
			return strings.Join(parts, " ")
		}
	}
	assignmentRowsByPermissionSet, err := queryAllInChunks(conn, directPermissionSetIDs, assignmentQuery("PermissionSetId"))
	if err != nil {
		return nil, err
	}
	for _, row := range assignmentRowsByPermissionSet {
		assignmentRowsByID.set(text(row, "Id"), row)
	}
	assignmentRowsByPsg, err := queryAllInChunks(conn, psgIDs, assignmentQuery("PermissionSetGroupId"))
	if err != nil {
		return nil, err
	}
	for _, row := range assignmentRowsByPsg {
		assignmentRowsByID.set(text(row, "Id"), row)
	}
	rowsByKey := newOrderedMap[Row]()
	for _, assignmentID := range assignmentRowsByID.keys {
		row, _ := assignmentRowsByID.get(assignmentID)
		if isFalse(row, "Assignee", "IsActive") {
			continue
		}
		assigneeID := text(row, "AssigneeId")
		userName := coalesce(opt(row, "Assignee", "Name"), lit(assigneeID))
		username := coalesce(opt(row, "Assignee", "Username"), lit(""))
		permissionSetID := text(row, "PermissionSetId")
		if permissionSetID != "" && grantByPermissionSetID.has(permissionSetID) {
			parent, known := permissionSetByID.get(permissionSetID)
			if !known || parent == nil {
				parent = related(row, "PermissionSet")
			}
			if text(parent, "Type") != "Group" {
				context := buildContext{assignmentType: AssignmentPermissionSet, permissionSetID: permissionSetID}
				grant, _ := grantByPermissionSetID.get(permissionSetID)
				var access Access
				granted := false
				if grant != nil {
					access, granted = build(grant, context)
				}
				if granted {
					isProfile := isTrue(parent, "IsOwnedByProfile")
					resolvedRow := Row{
						UserID:         assigneeID,
						UserName:       userName,
						Username:       username,
						TargetType:     targetType,
						TargetName:     targetName,
						AssignmentType: AssignmentPermissionSet,
						SourceID:       permissionSetID,
						SourceName:     coalesce(opt(parent, "Name"), lit(permissionSetID)),
						SourceAPIName:  coalesce(opt(parent, "DeveloperName"), opt(parent, "Name"), lit(permissionSetID)),
						SourceLabel:    coalesce(opt(parent, "MasterLabel"), opt(parent, "Label"), opt(parent, "Profile", "Name"), opt(parent, "Name"), lit(permissionSetID)),
						Access:         access,
					}
					if isProfile {
						resolvedRow.AssignmentType = AssignmentProfile
						resolvedRow.SourceID = coalesce(opt(parent, "ProfileId"), lit(permissionSetID))
						resolvedRow.SourceName = coalesce(opt(parent, "Profile", "Name"), opt(parent, "Name"), lit("Profile"))
					}
					rowsByKey.set(keyFor(resolvedRow), resolvedRow)
				}
			}
		}
		psgID := text(row, "PermissionSetGroupId")
		if psgID != "" {
			// One entry for each time the group lists a permission set.
			entries := []string{}
			for _, componentID := range psgByPermissionSetID.keys {
				psgEntries, _ := psgByPermissionSetID.get(componentID)
				for _, entry := range psgEntries {
					if entry.psgID == psgID {
						entries = append(entries, componentID)
					}
				}
			}
			for _, componentID := range entries {
				grantingAccess, _ := grantByPermissionSetID.get(componentID)
				if grantingAccess == nil {
					continue
				}
				context := buildContext{assignmentType: AssignmentPermissionSetGroup, permissionSetID: componentID, psgID: psgID}
				access, granted := build(grantingAccess, context)
				if !granted {
					continue
				}
				sourceName := coalesce(
					opt(row, "PermissionSetGroup", "MasterLabel"),
					opt(row, "PermissionSetGroup", "DeveloperName"),
					fromMap(psgNameByID, psgID),
					lit(psgID),
				)
				component, _ := permissionSetByID.get(componentID)
				resolvedRow := Row{
					UserID:               assigneeID,
					UserName:             userName,
					Username:             username,
					TargetType:           targetType,
					TargetName:           targetName,
					AssignmentType:       AssignmentPermissionSetGroup,
					SourceID:             psgID,
					SourceName:           sourceName,
					SourceAPIName:        coalesce(opt(row, "PermissionSetGroup", "DeveloperName"), fromMap(psgAPINameByID, psgID), lit(psgID)),
					SourceLabel:          coalesce(opt(row, "PermissionSetGroup", "MasterLabel"), fromMap(psgLabelByID, psgID), lit(sourceName)),
					ViaPermissionSetID:   componentID,
					ViaPermissionSetName: coalesce(opt(component, "Name"), lit(componentID)),
					Access:               access,
				}
				rowsByKey.set(keyFor(resolvedRow), resolvedRow)
			}
		}
	}
	rows := make([]Row, 0, rowsByKey.len())
	for _, key := range rowsByKey.keys {
		row, _ := rowsByKey.get(key)
		rows = append(rows, row)
	}
	return rows, nil
}

// resultFor builds a result with the stats of its rows.
func resultFor(targetType, targetName string, rows []Row, warnings []string, sobjectType, fieldAPIName string) *Result {
	if rows == nil {
		rows = []Row{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return &Result{
		TargetType:   targetType,
		TargetName:   targetName,
		SobjectType:  sobjectType,
		FieldAPIName: fieldAPIName,
		Rows:         rows,
		Stats:        ComputeStats(rows),
		Warnings:     warnings,
	}
}
