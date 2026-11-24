package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// ContextResult holds the relatedContext values of one user after its
// lookups were resolved.
type ContextResult struct {
	Values *core.Object
	Errors []string
	// ErrorsByName maps a context name to its errors.
	ErrorsByName map[string][]string
}

func (r *ContextResult) addError(name, text string) {
	r.Errors = append(r.Errors, text)
	r.ErrorsByName[name] = append(r.ErrorsByName[name], text)
}

type contextLookup struct {
	sobject string
	field   string
	value   SourceExpr
}

// parseContextValue reads a relatedContext value: a text, number, boolean
// or null, or an object with the one key "lookup" that names an sObject,
// a field and a source expression.  The last result is false for any
// other value.
func parseContextValue(raw any) (*contextLookup, bool) {
	switch typed := raw.(type) {
	case nil, string, float64, bool:
		return nil, true
	case *core.Object:
		lookup := typed.Object("lookup")
		if len(typed.Keys) != 1 || lookup == nil || len(lookup.Keys) != 3 {
			return nil, false
		}
		sobject, _ := lookup.String("sobject")
		field, _ := lookup.String("field")
		value, _ := lookup.Get("value")
		source, valid := sourceExprOf(value)
		if sobject == "" || field == "" || !valid {
			return nil, false
		}
		if _, fromIsText := source.From.(string); source.HasFrom && !fromIsText {
			return nil, false
		}
		return &contextLookup{sobject: sobject, field: field, value: source}, true
	}
	return nil, false
}

type contextRequest struct {
	order int
	name  string
	value string
}

type contextGroup struct {
	sobject  string
	field    string
	requests []contextRequest
}

// ContextOptions is what ResolveRelatedContext resolves.
type ContextOptions struct {
	Conn         sfapi.Conn
	Users        []UserEntry
	Catalog      *Catalog
	UserFieldMap *core.FieldMap
	Cache        core.DescribeCache
	// Message is the text lookup; nil means Message.
	Message MessageFunc
}

func collectContextRequests(options ContextOptions, message MessageFunc, results map[int]*ContextResult) []*contextGroup {
	var groups []*contextGroup
	groupByKey := map[string]*contextGroup{}
	for _, entry := range options.Users {
		user := entry.User
		result := &ContextResult{Values: core.NewObject(), ErrorsByName: map[string][]string{}}
		results[entry.Order] = result
		var names []string
		seen := map[string]bool{}
		for _, relationship := range user.Related {
			def := options.Catalog.Relationships[relationship]
			if def == nil {
				continue
			}
			for _, fieldName := range def.FieldNames {
				if expr := def.Fields[fieldName]; expr.HasFrom {
					if parsed := ParseSource(expr.From); parsed.Kind == "context" {
						names = appendUnique(names, seen, parsed.Name)
					}
				}
			}
		}
		for _, name := range names {
			raw, known := user.RelatedContext.Get(name)
			if !known {
				result.addError(name, message("errorUnknownContextName", user.InputKey, name))
				continue
			}
			lookup, valid := parseContextValue(raw)
			if !valid {
				result.addError(name, message("errorInvalidLookup", name))
				continue
			}
			if lookup == nil {
				result.Values.Set(name, raw)
				continue
			}
			if lookup.value.HasFrom {
				source := ParseSource(lookup.value.From)
				_, fieldKnown := options.UserFieldMap.Get(source.Field)
				if source.Kind != "userField" || !fieldKnown {
					result.addError(name, message("errorInvalidLookup", name))
					continue
				}
			}
			resolved := ResolveSource(lookup.value, SourceContext{Relationship: name, FieldName: "lookup.value", UserFields: user.Fields, UserFieldMap: options.UserFieldMap})
			if resolved.Error != nil || resolved.Pending || isAbsent(resolved.Value) {
				result.addError(name, message("errorInvalidLookup", name))
				continue
			}
			key := strings.ToLower(lookup.sobject) + "|" + strings.ToLower(lookup.field)
			group := groupByKey[key]
			if group == nil {
				group = &contextGroup{sobject: lookup.sobject, field: lookup.field}
				groupByKey[key] = group
				groups = append(groups, group)
			}
			group.requests = append(group.requests, contextRequest{order: entry.Order, name: name, value: core.JSString(resolved.Value)})
		}
	}
	return groups
}

func resolveContextGroup(options ContextOptions, message MessageFunc, group *contextGroup, results map[int]*ContextResult) error {
	described, err := core.DescribeSobject(options.Conn, group.sobject, options.Cache)
	if err != nil {
		return err
	}
	meta, known := described.Fields.Get(group.field)
	if !described.Queryable || !IsEligibleLookupField(meta, known) {
		for _, request := range group.requests {
			results[request.order].addError(request.name, message("errorLookupFieldIneligible", group.field, group.sobject))
		}
		return nil
	}
	field := meta.Name
	prefix := "SELECT Id, " + field + " FROM " + described.Name + " WHERE " + field + " IN ("
	rejected := map[string]bool{}
	var values []string
	for _, request := range group.requests {
		values = append(values, request.value)
	}
	chunks, err := MatchQueryBatches(unique(values), prefix, func(value string) {
		rejected[value] = true
		for _, request := range group.requests {
			if request.value == value {
				results[request.order].addError(request.name, message("errorInvalidLookup", request.name))
			}
		}
	})
	if err != nil {
		return err
	}
	var kept []contextRequest
	for _, request := range group.requests {
		if !rejected[request.value] {
			kept = append(kept, request)
		}
	}
	group.requests = kept
	var rows []sfapi.Record
	for _, chunk := range chunks {
		page, err := options.Conn.Query(prefix + core.SoqlIn(chunk) + ")")
		if err != nil {
			return err
		}
		rows = append(rows, page.Records...)
	}
	normalize := func(value string) string {
		if isFalse(meta.CaseSensitive) {
			return strings.ToLower(value)
		}
		return value
	}
	index := map[string][]sfapi.Record{}
	for _, row := range rows {
		key := "undefined"
		if value, present := row[field]; present {
			key = core.JSString(value)
		}
		index[normalize(key)] = append(index[normalize(key)], row)
	}
	for _, request := range group.requests {
		matches := index[normalize(request.value)]
		result := results[request.order]
		if len(matches) == 1 && truthy(matches[0]["Id"]) {
			result.Values.Set(request.name, matches[0]["Id"])
		} else if len(matches) > 1 {
			result.addError(request.name, message("errorLookupAmbiguous", request.name, request.value))
		} else {
			result.addError(request.name, message("errorLookupNotFound", request.name, request.value))
		}
	}
	return nil
}

// ResolveRelatedContext resolves the relatedContext names that the
// selected relationships of each user use.  A literal value is taken as
// it is; a lookup becomes the Id of the one record its field value
// matches.  The lookups of all users on one sObject field share queries.
// The result maps the position of a user to its values and errors.
func ResolveRelatedContext(options ContextOptions) map[int]*ContextResult {
	message := options.Message
	if message == nil {
		message = Message
	}
	results := map[int]*ContextResult{}
	for _, group := range collectContextRequests(options, message, results) {
		if err := resolveContextGroup(options, message, group, results); err != nil {
			for _, request := range group.requests {
				results[request.order].addError(request.name, `Context lookup "`+request.name+`" failed: `+err.Error())
			}
		}
	}
	return results
}
