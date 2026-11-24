package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

func addReferenceLabel(labels *LabelMap, row sfapi.Record, apiNameField, labelField, labelType string) {
	apiName := text(row, apiNameField)
	label := apiName
	if labelField != "" {
		label = text(row, labelField)
	}
	labels.Set(text(row, "Id"), ItemLabel{ID: text(row, "Id"), APIName: apiName, Label: label, Type: labelType})
}

// referenceResolver resolves the references of one category.  Its queries
// are separate steps so that ResolveReferences can run the first query of
// every category before the second query of any.
type referenceResolver struct {
	resolved *RefMap
	steps    []func() error
	finish   func()
}

type referenceLookup struct {
	conn        sfapi.Conn
	table       string
	refs        []string
	nameField   string
	whereClause string
	idPrefixes  []string
	labelField  string
	labelType   string
}

func resolveByIDOrName(lookup referenceLookup, warnings *[]string, labels *LabelMap) *referenceResolver {
	resolver := &referenceResolver{resolved: NewRefMap()}
	fields := []string{"Id", lookup.nameField}
	if lookup.labelField != "" {
		fields = append(fields, lookup.labelField)
	}
	var ids, names []string
	for _, ref := range lookup.refs {
		if !IsSalesforceID(ref) {
			names = append(names, ref)
		} else if lookup.idPrefixes == nil || contains(lookup.idPrefixes, ref[:3]) {
			ids = append(ids, ref)
		}
	}
	query := func(condition string, key func(row sfapi.Record) string) func() error {
		return func() error {
			where := condition
			if lookup.whereClause != "" {
				where += " AND " + lookup.whereClause
			}
			page, err := lookup.conn.Query("SELECT " + strings.Join(fields, ", ") + " FROM " + lookup.table + " WHERE " + where)
			if err != nil {
				return err
			}
			for _, row := range page.Records {
				resolver.resolved.Set(key(row), text(row, "Id"))
				addReferenceLabel(labels, row, lookup.nameField, lookup.labelField, lookup.labelType)
			}
			return nil
		}
	}
	if len(ids) > 0 {
		resolver.steps = append(resolver.steps, query("Id IN ("+core.SoqlIn(ids)+")", func(row sfapi.Record) string { return text(row, "Id") }))
	}
	if len(names) > 0 {
		resolver.steps = append(resolver.steps, query(lookup.nameField+" IN ("+core.SoqlIn(names)+")", func(row sfapi.Record) string { return text(row, lookup.nameField) }))
	}
	resolver.finish = func() {
		for _, ref := range lookup.refs {
			if !resolver.resolved.Has(ref) {
				*warnings = append(*warnings, Message("warningReferenceMissing", lookup.table, ref))
			}
		}
	}
	return resolver
}

func resolveByRoleRef(conn sfapi.Conn, refs []string, warnings *[]string, labels *LabelMap) *referenceResolver {
	resolver := &referenceResolver{resolved: NewRefMap()}
	var ids, names []string
	for _, ref := range refs {
		if !IsSalesforceID(ref) {
			names = append(names, ref)
		} else if strings.HasPrefix(ref, "00E") {
			ids = append(ids, ref)
		}
	}
	if len(ids) > 0 {
		resolver.steps = append(resolver.steps, func() error {
			page, err := conn.Query("SELECT Id, DeveloperName, Name FROM UserRole WHERE Id IN (" + core.SoqlIn(ids) + ")")
			if err != nil {
				return err
			}
			for _, row := range page.Records {
				resolver.resolved.Set(text(row, "Id"), text(row, "Id"))
				addReferenceLabel(labels, row, "DeveloperName", "Name", "UserRole")
			}
			return nil
		})
	}
	if len(names) > 0 {
		resolver.steps = append(resolver.steps, func() error {
			page, err := conn.Query("SELECT Id, DeveloperName, Name FROM UserRole WHERE DeveloperName IN (" + core.SoqlIn(names) + ") OR Name IN (" + core.SoqlIn(names) + ")")
			if err != nil {
				return err
			}
			for _, row := range page.Records {
				if developerName := text(row, "DeveloperName"); developerName != "" && contains(names, developerName) {
					resolver.resolved.Set(developerName, text(row, "Id"))
				}
				if name := text(row, "Name"); name != "" && contains(names, name) {
					resolver.resolved.Set(name, text(row, "Id"))
				}
				addReferenceLabel(labels, row, "DeveloperName", "Name", "UserRole")
			}
			return nil
		})
	}
	resolver.finish = func() {
		for _, ref := range refs {
			if !resolver.resolved.Has(ref) {
				*warnings = append(*warnings, Message("warningReferenceMissing", "UserRole", ref))
			}
		}
	}
	return resolver
}

// ResolveReferences finds the Ids of the profiles, roles, permission
// sets, permission set groups, public groups and queues the personas
// name, and of the profile and role each user names.  A reference is an Id
// or a name: Name for a profile and a permission set, DeveloperName or
// Name for a role, DeveloperName for the others.  A reference that is not
// found adds a warning.  personas is the "personas" object of a personas
// document.
func ResolveReferences(conn sfapi.Conn, personas *core.Object, users []*CanonicalUser) (*ResolvedRefs, error) {
	warnings := []string{}
	labels := NewLabelMap()
	collected := map[string][]string{}
	seen := map[string]map[string]bool{}
	add := func(category, ref string) {
		if seen[category] == nil {
			seen[category] = map[string]bool{}
		}
		collected[category] = appendUnique(collected[category], seen[category], ref)
	}
	if personas != nil {
		for _, name := range personas.Keys {
			persona, _ := personas.Values[name].(*core.Object)
			if profile, _ := persona.String("profile"); profile != "" {
				add("profiles", profile)
			}
			if role, _ := persona.String("role"); role != "" {
				add("roles", role)
			}
			for _, listKey := range AssignmentListKeys {
				for _, value := range persona.Strings(listKey) {
					add(listKey, value)
				}
			}
		}
	}
	for _, user := range users {
		if user.ProfileRef != "" {
			add("profiles", user.ProfileRef)
		}
		if user.RoleRef != "" {
			add("roles", user.RoleRef)
		}
	}
	resolvers := []*referenceResolver{
		resolveByIDOrName(referenceLookup{conn: conn, table: "Profile", refs: collected["profiles"], nameField: "Name", idPrefixes: []string{"00e"}, labelType: "Profile"}, &warnings, labels),
		resolveByRoleRef(conn, collected["roles"], &warnings, labels),
		resolveByIDOrName(referenceLookup{conn: conn, table: "PermissionSet", refs: collected["permissionSets"], nameField: "Name", idPrefixes: []string{"0PS"}, labelField: "Label", labelType: "PermissionSet"}, &warnings, labels),
		resolveByIDOrName(referenceLookup{conn: conn, table: "PermissionSetGroup", refs: collected["permissionSetGroups"], nameField: "DeveloperName", idPrefixes: []string{"0PG"}, labelField: "MasterLabel", labelType: "PermissionSetGroup"}, &warnings, labels),
		resolveByIDOrName(referenceLookup{conn: conn, table: "Group", refs: collected["publicGroups"], nameField: "DeveloperName", whereClause: "Type = 'Regular'", idPrefixes: []string{"00G"}, labelField: "Name", labelType: "PublicGroup"}, &warnings, labels),
		resolveByIDOrName(referenceLookup{conn: conn, table: "Group", refs: collected["queues"], nameField: "DeveloperName", whereClause: "Type = 'Queue'", idPrefixes: []string{"00G"}, labelField: "Name", labelType: "Queue"}, &warnings, labels),
	}
	// The upstream code starts the six lookups together.  With answers
	// that arrive in request order, the first query of every category runs
	// before the second query of any, and a category adds its warnings
	// after its last query.  The rounds below give that order.
	for round := 0; round <= 2; round++ {
		for _, resolver := range resolvers {
			switch {
			case round < len(resolver.steps):
				if err := resolver.steps[round](); err != nil {
					return nil, err
				}
			case round == len(resolver.steps):
				resolver.finish()
			}
		}
	}
	return &ResolvedRefs{
		ProfilesByRef:              resolvers[0].resolved,
		RolesByRef:                 resolvers[1].resolved,
		PermissionSetIDsByRef:      resolvers[2].resolved,
		PermissionSetGroupIDsByRef: resolvers[3].resolved,
		PublicGroupIDsByRef:        resolvers[4].resolved,
		QueueIDsByRef:              resolvers[5].resolved,
		Labels:                     labels,
		Warnings:                   warnings,
	}, nil
}
