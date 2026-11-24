package provisioning

import (
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// PermissionSetAssignmentLabel names what a PermissionSetAssignment row
// assigns: its permission set group when it has one, otherwise its
// permission set.  The result is nil for a row with neither.
func PermissionSetAssignmentLabel(row sfapi.Record) *ItemLabel {
	if id := text(row, "PermissionSetGroupId"); id != "" {
		group := nested(row, "PermissionSetGroup")
		return &ItemLabel{ID: id, APIName: text(group, "DeveloperName"), Label: text(group, "MasterLabel"), Type: "PermissionSetGroup"}
	}
	if id := text(row, "PermissionSetId"); id != "" {
		permissionSet := nested(row, "PermissionSet")
		return &ItemLabel{ID: id, APIName: text(permissionSet, "Name"), Label: text(permissionSet, "Label"), Type: "PermissionSet"}
	}
	return nil
}

// GroupMemberLabel names the group of a GroupMember row.
func GroupMemberLabel(row sfapi.Record) ItemLabel {
	group := nested(row, "Group")
	label := ItemLabel{ID: text(row, "GroupId"), APIName: text(group, "DeveloperName"), Label: text(group, "Name"), Type: "PublicGroup"}
	if text(group, "Type") == "Queue" {
		label.Type = "Queue"
	}
	return label
}

// PermissionSetLicenseLabel names the license of a
// PermissionSetLicenseAssign row.
func PermissionSetLicenseLabel(row sfapi.Record) ItemLabel {
	license := nested(row, "PermissionSetLicense")
	return ItemLabel{ID: text(row, "PermissionSetLicenseId"), APIName: text(license, "DeveloperName"), Label: text(license, "MasterLabel"), Type: "PermissionSetLicense"}
}

const assignmentQueryBatchSize = 100

// AssignmentState holds the rows of the users, by user Id.
type AssignmentState struct {
	UserLoginByUserID map[string][]sfapi.Record
	PsaByUserID       map[string][]sfapi.Record
	GroupByUserID     map[string][]sfapi.Record
	PslByUserID       map[string][]sfapi.Record
}

// AssignmentStateOptions names the rows to load.
type AssignmentStateOptions struct {
	UserLogin                bool
	PermissionSetAssignments bool
	GroupMemberships         bool
	PermissionSetLicenses    bool
}

func queryRowsByUserBatched(conn sfapi.Conn, targetIDs []string, buildSoql func(inClause string) string, keyField string) (map[string][]sfapi.Record, error) {
	merged := map[string][]sfapi.Record{}
	for _, idBatch := range core.Batch(targetIDs, assignmentQueryBatchSize) {
		page, err := conn.Query(buildSoql(core.SoqlIn(idBatch)))
		if err != nil {
			return nil, err
		}
		for _, row := range page.Records {
			key := ""
			if value := row[keyField]; value != nil {
				key = core.JSString(value)
			}
			merged[key] = append(merged[key], row)
		}
	}
	return merged, nil
}

// LoadAssignmentState queries the UserLogin, PermissionSetAssignment,
// GroupMember and PermissionSetLicenseAssign rows of the users, 100 users
// to a query.  With nil options every kind of row is loaded.
func LoadAssignmentState(conn sfapi.Conn, targetIDs []string, options *AssignmentStateOptions) (*AssignmentState, error) {
	state := &AssignmentState{
		UserLoginByUserID: map[string][]sfapi.Record{},
		PsaByUserID:       map[string][]sfapi.Record{},
		GroupByUserID:     map[string][]sfapi.Record{},
		PslByUserID:       map[string][]sfapi.Record{},
	}
	if len(targetIDs) == 0 {
		return state, nil
	}
	var err error
	if options == nil || options.UserLogin {
		state.UserLoginByUserID, err = queryRowsByUserBatched(conn, targetIDs, func(inClause string) string {
			return "SELECT Id, UserId, IsFrozen FROM UserLogin WHERE UserId IN (" + inClause + ")"
		}, "UserId")
		if err != nil {
			return nil, err
		}
	}
	if options == nil || options.PermissionSetAssignments {
		state.PsaByUserID, err = queryRowsByUserBatched(conn, targetIDs, func(inClause string) string {
			return "SELECT Id, AssigneeId, PermissionSetId, PermissionSetGroupId, PermissionSet.IsOwnedByProfile, PermissionSet.Name, PermissionSet.Label, PermissionSetGroup.DeveloperName, PermissionSetGroup.MasterLabel FROM PermissionSetAssignment WHERE AssigneeId IN (" + inClause + ")"
		}, "AssigneeId")
		if err != nil {
			return nil, err
		}
	}
	if options == nil || options.GroupMemberships {
		state.GroupByUserID, err = queryRowsByUserBatched(conn, targetIDs, func(inClause string) string {
			return "SELECT Id, GroupId, UserOrGroupId, Group.Type, Group.DeveloperName, Group.Name FROM GroupMember WHERE UserOrGroupId IN (" + inClause + ") AND Group.Type IN ('Regular', 'Queue')"
		}, "UserOrGroupId")
		if err != nil {
			return nil, err
		}
	}
	if options == nil || options.PermissionSetLicenses {
		state.PslByUserID, err = queryRowsByUserBatched(conn, targetIDs, func(inClause string) string {
			return "SELECT Id, AssigneeId, PermissionSetLicenseId, PermissionSetLicense.DeveloperName, PermissionSetLicense.MasterLabel FROM PermissionSetLicenseAssign WHERE AssigneeId IN (" + inClause + ")"
		}, "AssigneeId")
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}
