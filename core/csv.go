package core

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var (
	csvExtension  = regexp.MustCompile(`(?i)\.(csv|tsv)$`)
	tsvExtension  = regexp.MustCompile(`(?i)\.tsv$`)
	jsonExtension = regexp.MustCompile(`(?i)\.json$`)
	formulaPrefix = regexp.MustCompile(`^[=+\-@\t\r]`)
)

// InputFormatFromExtension returns "csv" or "json" for a path whose
// extension decides the format, and "" otherwise.
func InputFormatFromExtension(path string) string {
	switch {
	case csvExtension.MatchString(path):
		return "csv"
	case jsonExtension.MatchString(path):
		return "json"
	}
	return ""
}

// DetectInputFormat returns the override, the format of the extension, or
// "json".
func DetectInputFormat(path, override string) string {
	if override != "" {
		return override
	}
	if format := InputFormatFromExtension(path); format != "" {
		return format
	}
	return "json"
}

// NewCsvReadError reports a problem at a line of a CSV file.
func NewCsvReadError(path string, line int, detail string) *WardenError {
	return newError("CsvReadError", "errorInvalidCsv", fmt.Sprintf("%s:%d — %s", path, line, detail), map[string]any{"path": path, "line": line, "detail": detail})
}

// CsvRow is the cells of one row and the line it starts on.
type CsvRow struct {
	Cells []string
	Line  int
}

// ReadCsvRows splits CSV text into rows.  A quoted cell can hold the
// delimiter, line breaks and doubled quotes.
func ReadCsvRows(source, path string, delimiter rune) ([]CsvRow, error) {
	text := []rune(strings.TrimPrefix(source, "\xef\xbb\xbf"))
	var rows []CsvRow
	var cells []string
	var cell strings.Builder
	cellStarted, quoteClosed, inQuotes, rowTouched := false, false, false, false
	rowLine, line := 1, 1
	finishRow := func() {
		rows = append(rows, CsvRow{Cells: append(append([]string{}, cells...), cell.String()), Line: rowLine})
		cells = nil
		cell.Reset()
		cellStarted, quoteClosed, rowTouched = false, false, false
		rowLine = line
	}
	next := func(index int) rune {
		if index+1 < len(text) {
			return text[index+1]
		}
		return 0
	}
	for index := 0; index < len(text); index++ {
		character := text[index]
		if inQuotes {
			if character == '"' {
				if next(index) == '"' {
					cell.WriteRune('"')
					index++
				} else {
					inQuotes = false
					quoteClosed = true
				}
			} else {
				cell.WriteRune(character)
				if character == '\r' {
					if next(index) == '\n' {
						cell.WriteRune('\n')
						index++
					}
					line++
				} else if character == '\n' {
					line++
				}
			}
			continue
		}
		if quoteClosed {
			if character == delimiter {
				cells = append(cells, cell.String())
				cell.Reset()
				cellStarted, quoteClosed, rowTouched = false, false, true
				continue
			}
			if character == '\r' || character == '\n' {
				if character == '\r' && next(index) == '\n' {
					index++
				}
				line++
				finishRow()
				continue
			}
			return nil, NewCsvReadError(path, line, "Unexpected character after a closing quote.")
		}
		switch {
		case character == '"':
			if cellStarted {
				return nil, NewCsvReadError(path, line, "Unexpected quote in an unquoted cell.")
			}
			inQuotes, cellStarted, rowTouched = true, true, true
		case character == delimiter:
			cells = append(cells, cell.String())
			cell.Reset()
			cellStarted, rowTouched = false, true
		case character == '\r' || character == '\n':
			if character == '\r' && next(index) == '\n' {
				index++
			}
			line++
			finishRow()
		default:
			cell.WriteRune(character)
			cellStarted, rowTouched = true, true
		}
	}
	if inQuotes {
		return nil, NewCsvReadError(path, line, "Unterminated quoted cell.")
	}
	if rowTouched || len(cells) > 0 || cell.Len() > 0 {
		finishRow()
	}
	if len(text) > 0 && (text[len(text)-1] == '\r' || text[len(text)-1] == '\n') && len(rows) > 0 {
		last := rows[len(rows)-1]
		if len(last.Cells) == 1 && last.Cells[0] == "" {
			rows = rows[:len(rows)-1]
		}
	}
	return rows, nil
}

func levenshtein(left, right string) int {
	a, b := []rune(left), []rune(right)
	previous := make([]int, len(b)+1)
	for i := range previous {
		previous[i] = i
	}
	for row := 1; row <= len(a); row++ {
		diagonal := previous[0]
		previous[0] = row
		for column := 1; column <= len(b); column++ {
			above := previous[column]
			cost := 1
			if a[row-1] == b[column-1] {
				cost = 0
			}
			previous[column] = min(previous[column]+1, previous[column-1]+1, diagonal+cost)
			diagonal = above
		}
	}
	return previous[len(b)]
}

func suggestedHeader(header string, validHeaders []string) string {
	type scored struct {
		candidate string
		distance  int
	}
	var candidates []scored
	for _, candidate := range validHeaders {
		if distance := levenshtein(strings.ToLower(header), strings.ToLower(candidate)); distance <= 2 {
			candidates = append(candidates, scored{candidate, distance})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return strings.ToLower(candidates[i].candidate) < strings.ToLower(candidates[j].candidate)
	})
	return candidates[0].candidate
}

func parseCsvBoolean(value string) (bool, bool) {
	switch strings.ToLower(value) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	}
	return false, false
}

// ReadCsvUsers reads a users definition from a CSV or TSV file.  The
// columns are User field names and the three columns personas, match and
// fuzzyUsername.  The result has the shape of a JSON users definition, and
// each user carries the file and line it came from.
func ReadCsvUsers(path string, fieldMap *FieldMap, listDelimiter string) (*Object, error) {
	if listDelimiter == "" {
		return nil, NewCsvReadError(path, 1, "--csv-list-delimiter must not be empty.")
	}
	delimiter := ','
	if tsvExtension.MatchString(path) {
		delimiter = '\t'
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rows, err := ReadCsvRows(string(content), path, delimiter)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, NewCsvReadError(path, 1, "CSV must contain a header row.")
	}
	headerRow := rows[0]
	metaHeaders := map[string]string{"personas": "personas", "match": "match", "fuzzyusername": "fuzzyUsername"}
	validHeaders := []string{"personas", "match", "fuzzyUsername"}
	for _, field := range fieldMap.Fields() {
		validHeaders = append(validHeaders, field.Name)
	}
	type header struct {
		raw, name string
		isBoolean bool
	}
	seen := map[string]bool{}
	var headers []header
	for _, rawHeader := range headerRow.Cells {
		trimmed := strings.TrimSpace(rawHeader)
		key := strings.ToLower(trimmed)
		if seen[key] {
			return nil, NewCsvReadError(path, headerRow.Line, fmt.Sprintf("Duplicate column %q.", rawHeader))
		}
		seen[key] = true
		metaHeader, isMeta := metaHeaders[key]
		field, isField := fieldMap.Get(key)
		if !isMeta && !isField {
			suffix := ""
			if suggestion := suggestedHeader(trimmed, validHeaders); suggestion != "" {
				suffix = fmt.Sprintf("; did you mean %q?", suggestion)
			}
			return nil, NewCsvReadError(path, headerRow.Line, fmt.Sprintf("Unknown column %q%s", rawHeader, suffix))
		}
		if isMeta {
			headers = append(headers, header{raw: rawHeader, name: metaHeader})
		} else {
			headers = append(headers, header{raw: rawHeader, name: field.Name, isBoolean: field.IsBoolean})
		}
	}
	users := []any{}
	for _, row := range rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, NewCsvReadError(path, row.Line, fmt.Sprintf("Expected %d cells but found %d.", len(headers), len(row.Cells)))
		}
		user := NewObject()
		for index, column := range headers {
			value := row.Cells[index]
			if value == "" {
				continue
			}
			switch {
			case column.name == "personas":
				personas := []any{}
				for _, item := range strings.Split(value, listDelimiter) {
					if item = strings.TrimSpace(item); item != "" {
						personas = append(personas, item)
					}
				}
				user.Set("personas", personas)
			case column.name == "fuzzyUsername" || column.isBoolean:
				parsed, ok := parseCsvBoolean(value)
				if !ok {
					return nil, NewCsvReadError(path, row.Line, fmt.Sprintf("%s must be a boolean; got %q.", column.raw, value))
				}
				user.Set(column.name, parsed)
			default:
				user.Set(column.name, value)
			}
		}
		user.Source = &RowInfo{Path: path, Line: row.Line}
		users = append(users, user)
	}
	doc := NewObject()
	doc.Set("users", users)
	return doc, nil
}

// NeutralizeCsvFormula puts an apostrophe before a value that a
// spreadsheet would read as a formula.
func NeutralizeCsvFormula(value string) string {
	if strings.HasPrefix(value, "'") && formulaPrefix.MatchString(value[1:]) {
		return value
	}
	if formulaPrefix.MatchString(value) {
		return "'" + value
	}
	return value
}

// RestoreCsvFormula removes the apostrophe NeutralizeCsvFormula added.
func RestoreCsvFormula(value string) string {
	if strings.HasPrefix(value, "'") && formulaPrefix.MatchString(value[1:]) {
		return value[1:]
	}
	return value
}

// CsvEscape writes a value as a CSV cell.  A nil value is an empty cell.
func CsvEscape(value any) string {
	text := ""
	if value != nil {
		text = JSString(value)
	}
	escaped := strings.ReplaceAll(NeutralizeCsvFormula(text), `"`, `""`)
	if strings.ContainsAny(escaped, ",\"\n\r") {
		return `"` + escaped + `"`
	}
	return escaped
}

// SerializeCsv writes rows as CSV text with a header line and no line
// break at the end.
func SerializeCsv(rows []map[string]any, columns []string) string {
	lines := []string{strings.Join(columns, ",")}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, column := range columns {
			cells[i] = CsvEscape(row[column])
		}
		lines = append(lines, strings.Join(cells, ","))
	}
	return strings.Join(lines, "\n")
}
