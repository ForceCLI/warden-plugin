package access

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

var resolvers = map[string]*Resolver{
	TypeField:            fieldResolver,
	TypeObject:           objectResolver,
	TypeApexClass:        setupEntityResolver(TypeApexClass, "ApexClass"),
	TypeVfPage:           setupEntityResolver(TypeVfPage, "ApexPage"),
	TypeCustomPermission: setupEntityResolver(TypeCustomPermission, "CustomPermission"),
	TypeTab:              tabResolver,
	TypeRecordType:       recordTypeResolver,
}

// GetResolver returns the resolver of a target type.
func GetResolver(targetType string) (*Resolver, error) {
	resolver, ok := resolvers[targetType]
	if !ok {
		return nil, newError("errorUnsupportedAccessType", []string{targetType}, nil)
	}
	return resolver, nil
}

func toFieldAccess(row sfapi.Record) FieldAccess {
	return FieldAccess{Read: isTrue(row, "PermissionsRead"), Edit: isTrue(row, "PermissionsEdit")}
}

func anyFieldAccess(access FieldAccess) bool {
	return access.Read || access.Edit
}

func toObjectAccess(row sfapi.Record) ObjectAccess {
	return ObjectAccess{
		Read:      isTrue(row, "PermissionsRead"),
		Create:    isTrue(row, "PermissionsCreate"),
		Edit:      isTrue(row, "PermissionsEdit"),
		Delete:    isTrue(row, "PermissionsDelete"),
		ViewAll:   isTrue(row, "PermissionsViewAllRecords"),
		ModifyAll: isTrue(row, "PermissionsModifyAllRecords"),
	}
}

func anyObjectAccess(access ObjectAccess) bool {
	return access.Read || access.Create || access.Edit || access.Delete || access.ViewAll || access.ModifyAll
}

// combineObjectAccess returns the permissions either value has.
func combineObjectAccess(left, right ObjectAccess) ObjectAccess {
	return ObjectAccess{
		Read:      left.Read || right.Read,
		Create:    left.Create || right.Create,
		Edit:      left.Edit || right.Edit,
		Delete:    left.Delete || right.Delete,
		ViewAll:   left.ViewAll || right.ViewAll,
		ModifyAll: left.ModifyAll || right.ModifyAll,
	}
}

// withoutMuted returns the permissions of grant that muted does not have.
func withoutMuted(grant, muted ObjectAccess) ObjectAccess {
	return ObjectAccess{
		Read:      grant.Read && !muted.Read,
		Create:    grant.Create && !muted.Create,
		Edit:      grant.Edit && !muted.Edit,
		Delete:    grant.Delete && !muted.Delete,
		ViewAll:   grant.ViewAll && !muted.ViewAll,
		ModifyAll: grant.ModifyAll && !muted.ModifyAll,
	}
}

// collectGrants records the grant and the parent permission set of each
// entitlement row.  toAccess returns false for a row that grants nothing.
func collectGrants(rows []sfapi.Record, toAccess func(row sfapi.Record) (Access, bool)) (*orderedMap[Access], *orderedMap[sfapi.Record]) {
	grantByPermissionSetID := newOrderedMap[Access]()
	permissionSetByID := newOrderedMap[sfapi.Record]()
	for _, row := range rows {
		access, granted := toAccess(row)
		if !granted {
			continue
		}
		parentID := text(row, "ParentId")
		grantByPermissionSetID.set(parentID, access)
		if parent := related(row, "Parent"); parent != nil {
			permissionSetByID.set(parentID, withID(parent, parentID))
		}
	}
	return grantByPermissionSetID, permissionSetByID
}

func passGrant(grant Access, _ buildContext) (Access, bool) {
	return grant, true
}

type fieldMask struct {
	read bool
	edit bool
}

func combineFieldMasks(left, right fieldMask) fieldMask {
	return fieldMask{read: left.read || right.read, edit: left.edit || right.edit}
}

var fieldResolver = &Resolver{
	Type:       TypeField,
	validate:   validateFieldTarget,
	csvColumns: FieldCsvColumns,
	resolve: func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
		if target.SobjectType == "" || target.FieldAPIName == "" {
			return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
		}
		result, err := resolveFieldAccess(conn, target)
		if err != nil {
			return nil, queryFailure(target, err)
		}
		return result, nil
	},
}

func resolveFieldAccess(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
	qualifiedField := target.SobjectType + "." + target.FieldAPIName
	warnings := []string{}
	entitlementRows, err := queryAll(conn, strings.Join([]string{
		"SELECT ParentId, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type, PermissionsRead, PermissionsEdit",
		"FROM FieldPermissions",
		"WHERE SobjectType = '" + target.SobjectType + "'",
		"AND Field = '" + qualifiedField + "'",
		"AND Parent.Type != 'Muting'",
	}, " "))
	if err != nil {
		return nil, err
	}
	grantByPermissionSetID, permissionSetByID := collectGrants(entitlementRows, func(row sfapi.Record) (Access, bool) {
		access := toFieldAccess(row)
		return access, anyFieldAccess(access)
	})
	if grantByPermissionSetID.len() == 0 {
		warnings = append(warnings, "No explicit FieldPermissions records were found for "+qualifiedField+". Standard fields may still be visible through base object/profile behavior. This command reports explicit field-level security grants only.")
		return resultFor(TypeField, qualifiedField, nil, warnings, target.SobjectType, target.FieldAPIName), nil
	}
	muting := emptyMuting[fieldMask]()
	preparePsg := func(psgIDs []string) error {
		loaded, err := loadPsgMuting(conn, psgIDs, func(mutingSetIDs []string) (map[string]fieldMask, error) {
			mutingRows, err := queryAllInChunks(conn, mutingSetIDs, func(chunk []string) string {
				return strings.Join([]string{
					"SELECT ParentId, PermissionsRead, PermissionsEdit",
					"FROM FieldPermissions",
					"WHERE Parent.IsOwnedByProfile = false",
					"AND ParentId IN (" + quotedList(chunk) + ")",
					"AND SobjectType = '" + target.SobjectType + "'",
					"AND Field = '" + qualifiedField + "'",
				}, " ")
			})
			if err != nil {
				return nil, err
			}
			masks := map[string]fieldMask{}
			for _, row := range mutingRows {
				masks[text(row, "ParentId")] = fieldMask{read: isTrue(row, "PermissionsRead"), edit: isTrue(row, "PermissionsEdit")}
			}
			return masks, nil
		})
		if err != nil {
			return err
		}
		muting = loaded
		return nil
	}
	rows, err := resolveAssignees(conn, TypeField, qualifiedField, grantByPermissionSetID, permissionSetByID, func(grant Access, context buildContext) (Access, bool) {
		if context.assignmentType == AssignmentPermissionSet {
			return grant, true
		}
		fieldGrant := grant.(FieldAccess)
		muted := combinePsgMuting(context.psgID, muting, fieldMask{}, combineFieldMasks)
		effective := FieldAccess{Read: fieldGrant.Read && !muted.read, Edit: fieldGrant.Edit && !muted.edit}
		return effective, anyFieldAccess(effective)
	}, assigneeOptions{preparePsg: preparePsg})
	if err != nil {
		return nil, err
	}
	return resultFor(TypeField, qualifiedField, rows, warnings, target.SobjectType, target.FieldAPIName), nil
}

var objectResolver = &Resolver{
	Type:       TypeObject,
	validate:   validateObjectTarget,
	csvColumns: ObjectCsvColumns,
	resolve: func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
		if target.SobjectType == "" {
			return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
		}
		result, err := resolveObjectAccess(conn, target)
		if err != nil {
			return nil, queryFailure(target, err)
		}
		return result, nil
	},
}

func resolveObjectAccess(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
	warnings := []string{}
	entitlementRows, err := queryAll(conn, strings.Join([]string{
		"SELECT ParentId, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type, PermissionsRead, PermissionsCreate, PermissionsEdit, PermissionsDelete, PermissionsViewAllRecords, PermissionsModifyAllRecords",
		"FROM ObjectPermissions",
		"WHERE SobjectType = '" + target.SobjectType + "'",
		"AND Parent.Type != 'Muting'",
	}, " "))
	if err != nil {
		return nil, err
	}
	grantByPermissionSetID, permissionSetByID := collectGrants(entitlementRows, func(row sfapi.Record) (Access, bool) {
		access := toObjectAccess(row)
		return access, anyObjectAccess(access)
	})
	if grantByPermissionSetID.len() == 0 {
		return resultFor(TypeObject, target.SobjectType, nil, warnings, target.SobjectType, ""), nil
	}
	muting := emptyMuting[ObjectAccess]()
	preparePsg := func(psgIDs []string) error {
		loaded, err := loadPsgMuting(conn, psgIDs, func(mutingSetIDs []string) (map[string]ObjectAccess, error) {
			mutingRows, err := queryAllInChunks(conn, mutingSetIDs, func(chunk []string) string {
				return strings.Join([]string{
					"SELECT ParentId, PermissionsRead, PermissionsCreate, PermissionsEdit, PermissionsDelete, PermissionsViewAllRecords, PermissionsModifyAllRecords",
					"FROM ObjectPermissions",
					"WHERE Parent.IsOwnedByProfile = false",
					"AND ParentId IN (" + quotedList(chunk) + ")",
					"AND SobjectType = '" + target.SobjectType + "'",
				}, " ")
			})
			if err != nil {
				return nil, err
			}
			masks := map[string]ObjectAccess{}
			for _, row := range mutingRows {
				masks[text(row, "ParentId")] = toObjectAccess(row)
			}
			return masks, nil
		})
		if err != nil {
			return err
		}
		muting = loaded
		return nil
	}
	rows, err := resolveAssignees(conn, TypeObject, target.SobjectType, grantByPermissionSetID, permissionSetByID, func(grant Access, context buildContext) (Access, bool) {
		if context.assignmentType == AssignmentPermissionSet {
			return grant, true
		}
		muted := combinePsgMuting(context.psgID, muting, ObjectAccess{}, combineObjectAccess)
		effective := withoutMuted(grant.(ObjectAccess), muted)
		return effective, anyObjectAccess(effective)
	}, assigneeOptions{preparePsg: preparePsg})
	if err != nil {
		return nil, err
	}
	return resultFor(TypeObject, target.SobjectType, rows, warnings, target.SobjectType, ""), nil
}

func setupEntityResolver(targetType, setupEntityType string) *Resolver {
	return &Resolver{
		Type: targetType,
		validate: func(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
			return validateSetupEntityTarget(conn, targetType, target)
		},
		csvColumns: EnabledCsvColumns,
		resolve: func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
			if target.SetupEntityID == "" {
				return nil, newError("errorInvalidTarget", []string{target.TargetName}, nil)
			}
			rows, err := queryAll(conn, strings.Join([]string{
				"SELECT ParentId, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type",
				"FROM SetupEntityAccess",
				"WHERE SetupEntityType = '" + setupEntityType + "'",
				"AND SetupEntityId = '" + target.SetupEntityID + "'",
			}, " "))
			if err != nil {
				return nil, queryFailure(target, err)
			}
			grantByPermissionSetID, permissionSetByID := collectGrants(rows, func(sfapi.Record) (Access, bool) {
				return EnabledAccess{Enabled: true}, true
			})
			resolvedRows, err := resolveAssignees(conn, targetType, target.TargetName, grantByPermissionSetID, permissionSetByID, passGrant, assigneeOptions{})
			if err != nil {
				return nil, queryFailure(target, err)
			}
			return resultFor(targetType, target.TargetName, resolvedRows, nil, "", ""), nil
		},
	}
}

const tabProfileWarning = "Profile-level tab visibility is not included because it is not exposed as a clean PermissionSetTabSetting data-API grant."

var tabResolver = &Resolver{
	Type:       TypeTab,
	validate:   validateTabTarget,
	csvColumns: TabCsvColumns,
	resolve: func(conn sfapi.Conn, target *ValidatedTarget) (*Result, error) {
		settings, err := queryAll(conn, strings.Join([]string{
			"SELECT ParentId, Visibility, Parent.Id, Parent.Name, Parent.IsOwnedByProfile, Parent.ProfileId, Parent.Profile.Name, Parent.Type",
			"FROM PermissionSetTabSetting",
			"WHERE Name = '" + core.Esc(target.TargetName) + "'",
			"AND Visibility != 'Hidden'",
		}, " "))
		if err != nil {
			return nil, queryFailure(target, err)
		}
		grantByPermissionSetID, permissionSetByID := collectGrants(settings, func(row sfapi.Record) (Access, bool) {
			return TabAccess{Visibility: text(row, "Visibility")}, true
		})
		rows, err := resolveAssignees(conn, TypeTab, target.TargetName, grantByPermissionSetID, permissionSetByID, passGrant, assigneeOptions{})
		if err != nil {
			return nil, queryFailure(target, err)
		}
		return resultFor(TypeTab, target.TargetName, rows, []string{tabProfileWarning}, "", ""), nil
	},
}
