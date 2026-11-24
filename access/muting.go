package access

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// psgMutingState holds, for permission set groups, the ids of their
// muting permission sets and what each muting permission set removes.
type psgMutingState[M any] struct {
	permissionSetIDsByGroup map[string][]string
	masksByPermissionSetID  map[string]M
}

func emptyMuting[M any]() psgMutingState[M] {
	return psgMutingState[M]{permissionSetIDsByGroup: map[string][]string{}, masksByPermissionSetID: map[string]M{}}
}

// loadPsgMuting reads the muting permission sets of the groups and calls
// loadMasks with their ids.
func loadPsgMuting[M any](conn sfapi.Conn, psgIDs []string, loadMasks func(permissionSetIDs []string) (map[string]M, error)) (psgMutingState[M], error) {
	if len(psgIDs) == 0 {
		return emptyMuting[M](), nil
	}
	components, err := queryAllInChunks(conn, psgIDs, func(chunk []string) string {
		return strings.Join([]string{
			"SELECT PermissionSetGroupId, PermissionSetId",
			"FROM PermissionSetGroupComponent",
			"WHERE PermissionSetGroupId IN (" + core.SoqlIn(chunk) + ")",
			"AND PermissionSet.Type = 'Muting'",
		}, " ")
	})
	if err != nil {
		return psgMutingState[M]{}, err
	}
	state := emptyMuting[M]()
	permissionSetIDs := []string{}
	for _, component := range components {
		psgID := text(component, "PermissionSetGroupId")
		permissionSetID := text(component, "PermissionSetId")
		state.permissionSetIDsByGroup[psgID] = append(state.permissionSetIDsByGroup[psgID], permissionSetID)
		permissionSetIDs = append(permissionSetIDs, permissionSetID)
	}
	masks, err := loadMasks(unique(permissionSetIDs))
	if err != nil {
		return psgMutingState[M]{}, err
	}
	state.masksByPermissionSetID = masks
	return state, nil
}

// combinePsgMuting combines the masks of the muting permission sets of
// one group, starting from emptyMask.
func combinePsgMuting[M any](psgID string, state psgMutingState[M], emptyMask M, combine func(left, right M) M) M {
	muted := emptyMask
	for _, permissionSetID := range state.permissionSetIDsByGroup[psgID] {
		mask, ok := state.masksByPermissionSetID[permissionSetID]
		if !ok {
			mask = emptyMask
		}
		muted = combine(muted, mask)
	}
	return muted
}
