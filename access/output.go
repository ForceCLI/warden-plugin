package access

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

var baseColumns = []string{
	"userId",
	"userName",
	"username",
	"assignmentType",
	"sourceId",
	"sourceName",
	"viaPermissionSetId",
	"viaPermissionSetName",
	"targetType",
	"targetName",
	"sourceApiName",
	"sourceLabel",
}

func columnsWith(extra ...string) []string {
	return append(append([]string{}, baseColumns...), extra...)
}

// FieldCsvColumns returns the CSV columns of field access rows.
func FieldCsvColumns() []string { return columnsWith("read", "edit") }

// EnabledCsvColumns returns the CSV columns of Apex class, Visualforce
// page and custom permission access rows.
func EnabledCsvColumns() []string { return columnsWith("enabled") }

// TabCsvColumns returns the CSV columns of tab access rows.
func TabCsvColumns() []string { return columnsWith("visibility") }

// RecordTypeCsvColumns returns the CSV columns of record type access rows.
func RecordTypeCsvColumns() []string { return columnsWith("visible", "default") }

// ObjectCsvColumns returns the CSV columns of object access rows.
func ObjectCsvColumns() []string {
	return columnsWith("read", "create", "edit", "delete", "viewAll", "modifyAll")
}

func csvValue(row Row, column string) any {
	if row.Access != nil {
		if value, ok := row.Access.column(column); ok {
			return value
		}
	}
	if value, ok := row.column(column); ok {
		return value
	}
	return ""
}

// FlattenAccessRow returns the values of a row by column name, for
// core.SerializeCsv.  A column of the access takes its value from the
// access; a column the row does not have is "".
func FlattenAccessRow(row Row, columns []string) map[string]any {
	flat := make(map[string]any, len(columns))
	for _, column := range columns {
		flat[column] = csvValue(row, column)
	}
	return flat
}

func formatVia(assignmentType, sourceName, viaName string) string {
	if assignmentType == AssignmentProfile {
		return "Profile: " + sourceName
	}
	if assignmentType == AssignmentPermissionSet {
		return "Permission Set: " + sourceName
	}
	if viaName != "" {
		return "PSG: " + sourceName + " / PS: " + viaName
	}
	return "PSG: " + sourceName
}

// jsLength is the length of a string in UTF-16 code units, which is what
// the JavaScript library pads columns by.
func jsLength(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func paddedTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for idx, header := range headers {
		widths[idx] = jsLength(header)
		for _, row := range rows {
			if idx < len(row) {
				widths[idx] = max(widths[idx], jsLength(row[idx]))
			}
		}
	}
	render := func(cells []string) string {
		padded := make([]string, len(cells))
		for idx, cell := range cells {
			padded[idx] = cell
			if idx < len(widths) && jsLength(cell) < widths[idx] {
				padded[idx] += strings.Repeat(" ", widths[idx]-jsLength(cell))
			}
		}
		return strings.TrimRightFunc(strings.Join(padded, "  "), unicode.IsSpace)
	}
	lines := []string{render(headers)}
	for _, row := range rows {
		lines = append(lines, render(row))
	}
	return strings.Join(lines, "\n")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func yn(value bool) string {
	if value {
		return "Y"
	}
	return "N"
}

func via(row Row) string {
	return formatVia(row.AssignmentType, row.SourceName, row.ViaPermissionSetName)
}

// RenderFieldTable writes field access rows as a text table.  showTarget
// adds a Target column, for rows of more than one field.
func RenderFieldTable(rows []Row, showTarget bool) (string, error) {
	headers := []string{"User Name", "Username", "Read", "Edit", "Via"}
	if showTarget {
		headers = append([]string{"Target"}, headers...)
	}
	cells := [][]string{}
	for _, row := range rows {
		access, ok := row.Access.(FieldAccess)
		if !ok {
			return "", errors.New("Field table received non-field access")
		}
		line := []string{row.UserName, row.Username, yesNo(access.Read), yesNo(access.Edit), via(row)}
		if showTarget {
			line = append([]string{row.TargetName}, line...)
		}
		cells = append(cells, line)
	}
	return paddedTable(headers, cells), nil
}

// RenderObjectTable writes object access rows as a text table.
func RenderObjectTable(rows []Row) (string, error) {
	cells := [][]string{}
	for _, row := range rows {
		access, ok := row.Access.(ObjectAccess)
		if !ok {
			return "", errors.New("Object table received non-object access")
		}
		cells = append(cells, []string{
			row.UserName,
			row.Username,
			yn(access.Read),
			yn(access.Create),
			yn(access.Edit),
			yn(access.Delete),
			yn(access.ViewAll),
			yn(access.ModifyAll),
			via(row),
		})
	}
	return paddedTable([]string{"User Name", "Username", "R", "C", "E", "D", "VA", "MA", "Via"}, cells), nil
}

// RenderEnabledTable writes Apex class, Visualforce page or custom
// permission access rows as a text table.
func RenderEnabledTable(rows []Row) (string, error) {
	cells := [][]string{}
	for _, row := range rows {
		access, ok := row.Access.(EnabledAccess)
		if !ok {
			return "", errors.New("Enabled table received non-enabled access")
		}
		cells = append(cells, []string{row.UserName, row.Username, yesNo(access.Enabled), via(row)})
	}
	return paddedTable([]string{"User Name", "Username", "Enabled", "Via"}, cells), nil
}

// RenderTabTable writes tab access rows as a text table.
func RenderTabTable(rows []Row) (string, error) {
	cells := [][]string{}
	for _, row := range rows {
		access, ok := row.Access.(TabAccess)
		if !ok {
			return "", errors.New("Tab table received non-tab access")
		}
		cells = append(cells, []string{row.UserName, row.Username, access.Visibility, via(row)})
	}
	return paddedTable([]string{"User Name", "Username", "Visibility", "Via"}, cells), nil
}

// RenderRecordTypeTable writes record type access rows as a text table.
func RenderRecordTypeTable(rows []Row) (string, error) {
	cells := [][]string{}
	for _, row := range rows {
		access, ok := row.Access.(RecordTypeAccess)
		if !ok {
			return "", errors.New("Record type table received non-record-type access")
		}
		isDefault := "n/a"
		if access.Default != nil {
			isDefault = yesNo(*access.Default)
		}
		cells = append(cells, []string{row.UserName, row.Username, yesNo(access.Visible), isDefault, via(row)})
	}
	return paddedTable([]string{"User Name", "Username", "Visible", "Default", "Via"}, cells), nil
}

// StableOrder returns the rows sorted by user name, user id, target
// name, assignment type, source name, name of the permission set of a
// group, access, source id and id of the permission set of a group.  The
// command sorts the rows of a result this way before it prints them.
func StableOrder(rows []Row) []Row {
	sorted := append([]Row{}, rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		for _, pair := range [][2]string{
			{a.UserName, b.UserName},
			{a.UserID, b.UserID},
			{a.TargetName, b.TargetName},
			{a.AssignmentType, b.AssignmentType},
			{a.SourceName, b.SourceName},
			{a.ViaPermissionSetName, b.ViaPermissionSetName},
			{a.accessJSON(), b.accessJSON()},
			{a.SourceID, b.SourceID},
			{a.ViaPermissionSetID, b.ViaPermissionSetID},
		} {
			if order := localeCompare(pair[0], pair[1]); order != 0 {
				return order < 0
			}
		}
		return false
	})
	return sorted
}

var targetLabels = map[string]string{
	TypeField:            "Field",
	TypeObject:           "Object",
	TypeApexClass:        "Apex Class",
	TypeVfPage:           "Visualforce Page",
	TypeCustomPermission: "Custom Permission",
	TypeTab:              "Tab",
	TypeRecordType:       "Record Type",
}

// RenderAccessResult writes a result as the text the command prints:
// heading lines, warnings, and a table of the rows in StableOrder.
// userLabel is the name of the user of a one-user audit, and "" for an
// audit of all users.
func RenderAccessResult(result *Result, userLabel string) (string, error) {
	sortedRows := StableOrder(result.Rows)
	grants := "Profiles: " + strconv.Itoa(result.Stats.ProfileGrants) +
		" | Permission Sets: " + strconv.Itoa(result.Stats.PermissionSetGrants) +
		" | Permission Set Groups: " + strconv.Itoa(result.Stats.PermissionSetGroupGrants)
	var lines []string
	if userLabel != "" {
		lines = []string{"Access for " + userLabel + ": " + result.TargetName, "Accessible grants: " + strconv.Itoa(len(sortedRows)), grants}
	} else {
		lines = []string{
			targetLabels[result.TargetType] + ": " + result.TargetName,
			"Active users with access: " + strconv.Itoa(result.Stats.TotalActiveUsersWithAccess),
			grants,
		}
	}
	if len(result.Warnings) > 0 {
		lines = append(lines, "")
		for _, warning := range result.Warnings {
			lines = append(lines, "Warning: "+warning)
		}
	}
	if len(sortedRows) > 0 {
		var table string
		var err error
		switch result.TargetType {
		case TypeField:
			table, err = RenderFieldTable(sortedRows, userLabel != "" && result.FieldAPIName == "")
		case TypeObject:
			table, err = RenderObjectTable(sortedRows)
		case TypeTab:
			table, err = RenderTabTable(sortedRows)
		case TypeRecordType:
			table, err = RenderRecordTypeTable(sortedRows)
		default:
			table, err = RenderEnabledTable(sortedRows)
		}
		if err != nil {
			return "", err
		}
		lines = append(lines, "", table)
	} else if len(result.Warnings) == 0 {
		message := infoNoResults
		if userLabel != "" {
			message = infoNoUserResults
		}
		lines = append(lines, "", message)
	}
	return strings.Join(lines, "\n"), nil
}
