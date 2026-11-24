package lifecycle

import "github.com/ForceCLI/warden-plugin/core"

var renderText = map[string]string{
	"activated":                               "Activated.",
	"addedPublicGroupMember":                  "Added %s public group memberships.",
	"addedQueueMember":                        "Added %s queue memberships.",
	"alreadyAbsent":                           "Already absent: %s.",
	"alreadyFrozen":                           "Already frozen.",
	"alreadyInactive":                         "Already inactive.",
	"alreadyUnfrozen":                         "Already unfrozen.",
	"assignedPermissionSet":                   "Assigned %s permission sets.",
	"assignedPermissionSetGroup":              "Assigned %s permission set groups.",
	"assignedPermissionSetLicense":            "Assigned %s permission set licenses.",
	"deactivated":                             "Deactivated.",
	"frozen":                                  "Froze.",
	"keptByOtherPersona":                      "Kept %s.",
	"info.licenses.header":                    "User license headroom for net-new users:",
	"info.licenses.row":                       "%s: required %s, available %s, shortfall %s%s",
	"info.noResults":                          "No active users matched this target.",
	"info.noUserResults":                      "No access grants matched this user and scope.",
	"info.permissionSetLicenses.notEvaluated": "Permission set license headroom: not evaluated.",
	"info.summary":                            "Processed %s user%s: %s changed, %s unchanged, %s failed.",
	"removedPermissionSet":                    "Removed %s permission set assignments.",
	"removedPermissionSetGroup":               "Removed %s permission set group assignments.",
	"removedPermissionSetLicense":             "Removed %s permission set license assignments.",
	"removedPublicGroupMember":                "Removed %s public group memberships.",
	"removedQueueMember":                      "Removed %s queue memberships.",
	"skippedDeactivate":                       "Skipped deactivation step.",
	"skippedFreeze":                           "Skipped freeze step.",
	"skippedPermissionSetGroups":              "Skipped permission set group removals.",
	"skippedPermissionSetLicenses":            "Skipped permission set license removals.",
	"skippedPermissionSets":                   "Skipped permission set removals.",
	"skippedProfileOwnedPermissionSets":       "Skipped %s profile-owned permission set assignments.",
	"skippedPublicGroups":                     "Skipped public group removals.",
	"skippedQueues":                           "Skipped queue removals.",
	"snapshotWritten":                         "Wrote snapshot.",
	"unfrozen":                                "Unfroze.",
	"verify.summary":                          "Verified %s users: %s conformant, %s non-conformant.",
	"verify.user":                             "%s: non-conformant",
	"verify.violation.error":                  "error: %s",
	"verify.violation.extra":                  "%s extra (sync): %s",
	"verify.violation.missing":                "%s missing: %s",
	"verify.violation.notFound":               "user not found",
	"verify.violation.profile":                "profile mismatch: %s -> %s",
	"verify.violation.role":                   "role mismatch: %s -> %s",
	"wouldActivate":                           "Would activate.",
	"wouldAddPublicGroupMember":               "Would add %s public group memberships.",
	"wouldAddQueueMember":                     "Would add %s queue memberships.",
	"wouldAssignPermissionSet":                "Would assign %s permission sets.",
	"wouldAssignPermissionSetGroup":           "Would assign %s permission set groups.",
	"wouldAssignPermissionSetLicense":         "Would assign %s permission set licenses.",
	"wouldDeactivate":                         "Would deactivate.",
	"wouldFreeze":                             "Would freeze.",
	"wouldRemovePermissionSet":                "Would remove %s permission set assignments.",
	"wouldRemovePermissionSetGroup":           "Would remove %s permission set group assignments.",
	"wouldRemovePermissionSetLicense":         "Would remove %s permission set license assignments.",
	"wouldRemovePublicGroupMember":            "Would remove %s public group memberships.",
	"wouldRemoveQueueMember":                  "Would remove %s queue memberships.",
	"wouldUnfreeze":                           "Would unfreeze.",
}

// RenderMessages is the default English text of the keys the renderers
// look up.  A key without an entry is returned as it is.
var RenderMessages MessageLookup = core.MessageTable(renderText)

// RenderMessagesFor returns the lookup of a command.  The "diff" and
// "provision" commands have their own "info.summary" line.
func RenderMessagesFor(commandID string) MessageLookup {
	summary := ""
	switch commandID {
	case "diff":
		summary = "Compared %s users: %s with drift, %s failed."
	case "provision":
		summary = "Processed %s users: %s created, %s updated, %s failed."
	}
	return func(key string, args ...string) string {
		if key == "info.summary" && summary != "" {
			return core.Format(summary, args...)
		}
		return RenderMessages(key, args...)
	}
}

var conformanceMessage MessageLookup = core.MessageTable(map[string]string{
	"verify.violation.notFound": "user not found",
	"verify.violation.error":    "error: %s",
	"verify.violation.missing":  "%s missing: %s",
	"verify.violation.extra":    "%s extra (sync): %s",
	"verify.violation.profile":  "profile mismatch: %s -> %s",
	"verify.violation.role":     "role mismatch: %s -> %s",
})
