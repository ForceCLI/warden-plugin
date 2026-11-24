package provisioning

import (
	"math"
	"strconv"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// LicenseUsage compares the user licenses the new users need with what
// the org has available.
type LicenseUsage struct {
	LicenseName string `json:"licenseName"`
	// Required is the number of new users whose profile uses the license.
	Required int `json:"required"`
	// Available is nil for a license without a limit.
	Available *int `json:"available"`
	Unlimited bool `json:"unlimited"`
	Shortfall int  `json:"shortfall"`
	// Note is "unlimited", or "status <status>" for a license that is not
	// active.
	Note string `json:"note,omitempty"`
}

// toCount is Number(value ?? 0) of JavaScript for a license count.
func toCount(value any) int {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0
		}
		return int(typed)
	case int:
		return typed
	case string:
		if number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return int(number)
		}
	case bool:
		if typed {
			return 1
		}
	}
	return 0
}

func firstText(values ...any) (string, bool) {
	for _, value := range values {
		if value != nil {
			return core.JSString(value), true
		}
	}
	return "", false
}

// CalculateUserLicenseUsage counts, for each user license, the users the
// plans will create and compares the count with the licenses the org has
// left.  Plans with errors and plans for existing users are not counted.
func CalculateUserLicenseUsage(conn sfapi.Conn, plans []*UserPlan) ([]LicenseUsage, error) {
	var profileIDs []string
	requiredByProfile := map[string]int{}
	for _, plan := range plans {
		if plan.Existing != nil || len(plan.Errors) > 0 {
			continue
		}
		if profileID, _ := plan.Target.String("ProfileId"); profileID != "" {
			if _, known := requiredByProfile[profileID]; !known {
				profileIDs = append(profileIDs, profileID)
			}
			requiredByProfile[profileID]++
		}
	}
	if len(profileIDs) == 0 {
		return []LicenseUsage{}, nil
	}
	profiles, err := conn.Query("SELECT Id, UserLicenseId, UserLicense.Name, UserLicense.MasterLabel FROM Profile WHERE Id IN (" + core.SoqlIn(profileIDs) + ")")
	if err != nil {
		return nil, err
	}
	type requirement struct {
		required           int
		profileLicenseName string
		hasName            bool
	}
	var licenseIDs []string
	requiredByLicense := map[string]*requirement{}
	for _, profile := range profiles.Records {
		licenseID := text(profile, "UserLicenseId")
		if licenseID == "" {
			continue
		}
		current := requiredByLicense[licenseID]
		if current == nil {
			current = &requirement{}
			requiredByLicense[licenseID] = current
			licenseIDs = append(licenseIDs, licenseID)
		}
		current.required += requiredByProfile[text(profile, "Id")]
		if !current.hasName {
			license := nested(profile, "UserLicense")
			current.profileLicenseName, current.hasName = firstText(license["MasterLabel"], license["Name"])
		}
	}
	if len(licenseIDs) == 0 {
		return []LicenseUsage{}, nil
	}
	licenses, err := conn.Query("SELECT Id, Name, MasterLabel, TotalLicenses, UsedLicenses, Status FROM UserLicense WHERE Id IN (" + core.SoqlIn(licenseIDs) + ")")
	if err != nil {
		return nil, err
	}
	licensesByID := map[string]sfapi.Record{}
	for _, license := range licenses.Records {
		licensesByID[text(license, "Id")] = license
	}
	usage := make([]LicenseUsage, 0, len(licenseIDs))
	for _, licenseID := range licenseIDs {
		needed := requiredByLicense[licenseID]
		license := licensesByID[licenseID]
		total := toCount(license["TotalLicenses"])
		used := toCount(license["UsedLicenses"])
		unlimited := total < 0
		status, hasStatus := license["Status"].(string)
		active := strings.ToLower(status) == "active"
		entry := LicenseUsage{Required: needed.required, Unlimited: unlimited}
		if name, named := firstText(license["MasterLabel"], license["Name"]); named {
			entry.LicenseName = name
		} else if needed.hasName {
			entry.LicenseName = needed.profileLicenseName
		} else {
			entry.LicenseName = licenseID
		}
		switch {
		case unlimited:
			entry.Note = "unlimited"
		case active:
			available := total - used
			entry.Available = &available
			entry.Shortfall = max(needed.required-available, 0)
		default:
			available := 0
			entry.Available = &available
			entry.Shortfall = max(needed.required, 0)
			if !hasStatus {
				status = "unknown"
			}
			entry.Note = "status " + status
		}
		usage = append(usage, entry)
	}
	return usage, nil
}
