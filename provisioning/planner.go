package provisioning

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

// ModeKeys are the persona keys that hold an assignment mode.
var ModeKeys = []string{"permissionSetMode", "permissionSetGroupMode", "publicGroupMode", "queueMode"}

// AssignmentListKeys are the persona keys that hold assignment lists.
var AssignmentListKeys = []string{"permissionSets", "permissionSetGroups", "publicGroups", "queues"}

var reservedUserKeys = map[string]bool{
	"personas":       true,
	"match":          true,
	"profile":        true,
	"role":           true,
	"fuzzyusername":  true,
	"related":        true,
	"relatedcontext": true,
}

var salesforceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{3}[a-zA-Z0-9]{12}(?:[a-zA-Z0-9]{3})?$`)

// IsSalesforceID reports whether a value has the form of a 15 or 18
// character Id.  It does not check the key prefix.
func IsSalesforceID(value string) bool {
	return salesforceIDPattern.MatchString(value)
}

// NormalizeMode returns the assignment mode a persona names: "additive"
// when none is named, or the named "additive" or "sync".
func NormalizeMode(value string) (string, error) {
	if value == "" {
		return "additive", nil
	}
	if value == "additive" || value == "sync" {
		return value, nil
	}
	return "", fmt.Errorf(`Invalid assignment mode "%s". Expected additive or sync.`, value)
}

// Persona is a persona definition, or the merge of the personas of a user.
// The JSON form has the keys of a persona definition; absent values are
// left out.
type Persona struct {
	PermissionSets         []string     `json:"permissionSets,omitempty"`
	PermissionSetGroups    []string     `json:"permissionSetGroups,omitempty"`
	PublicGroups           []string     `json:"publicGroups,omitempty"`
	Queues                 []string     `json:"queues,omitempty"`
	Profile                string       `json:"profile,omitempty"`
	Role                   string       `json:"role,omitempty"`
	PermissionSetMode      string       `json:"permissionSetMode,omitempty"`
	PermissionSetGroupMode string       `json:"permissionSetGroupMode,omitempty"`
	PublicGroupMode        string       `json:"publicGroupMode,omitempty"`
	QueueMode              string       `json:"queueMode,omitempty"`
	UserAttributes         *core.Object `json:"userAttributes,omitempty"`
}

// List returns the assignment list under one of AssignmentListKeys.
func (p *Persona) List(listKey string) []string {
	switch listKey {
	case "permissionSets":
		return p.PermissionSets
	case "permissionSetGroups":
		return p.PermissionSetGroups
	case "publicGroups":
		return p.PublicGroups
	case "queues":
		return p.Queues
	}
	return nil
}

func (p *Persona) setList(listKey string, values []string) {
	switch listKey {
	case "permissionSets":
		p.PermissionSets = values
	case "permissionSetGroups":
		p.PermissionSetGroups = values
	case "publicGroups":
		p.PublicGroups = values
	case "queues":
		p.Queues = values
	}
}

func (p *Persona) setMode(modeKey, value string) {
	switch modeKey {
	case "permissionSetMode":
		p.PermissionSetMode = value
	case "permissionSetGroupMode":
		p.PermissionSetGroupMode = value
	case "publicGroupMode":
		p.PublicGroupMode = value
	case "queueMode":
		p.QueueMode = value
	}
}

// PersonaFromObject reads one persona definition of a personas document.
func PersonaFromObject(definition *core.Object) Persona {
	var persona Persona
	if definition == nil {
		return persona
	}
	for _, listKey := range AssignmentListKeys {
		persona.setList(listKey, definition.Strings(listKey))
	}
	persona.Profile, _ = definition.String("profile")
	persona.Role, _ = definition.String("role")
	for _, modeKey := range ModeKeys {
		if value, present := definition.Get(modeKey); present {
			persona.setMode(modeKey, core.JSString(value))
		}
	}
	persona.UserAttributes = definition.Object("userAttributes")
	return persona
}

// CanonicalizeFieldObject returns the fields of source under the names the
// describe gives them.  A key that is not a User field is an error;
// context starts the text of that error.
func CanonicalizeFieldObject(source *core.Object, fieldMap *core.FieldMap, context string) (*core.Object, error) {
	output := core.NewObject()
	if source == nil {
		return output, nil
	}
	for _, rawKey := range source.Keys {
		meta, ok := fieldMap.Get(rawKey)
		if !ok {
			return nil, fmt.Errorf(`%s: unknown User field "%s".`, context, rawKey)
		}
		output.Set(meta.Name, source.Values[rawKey])
	}
	return output, nil
}

// ValidateExternalIDField returns the field when it can be used to match
// users.
func ValidateExternalIDField(externalID string, fieldMap *core.FieldMap) (core.FieldMeta, error) {
	return core.ValidateMatchField(externalID, fieldMap)
}

// ValidateExternalIDFieldForFlag checks the field of the --external-id
// flag.  An empty flag is valid and returns nil.
func ValidateExternalIDFieldForFlag(externalID string, fieldMap *core.FieldMap) (*core.FieldMeta, error) {
	if externalID == "" {
		return nil, nil
	}
	meta, err := ValidateExternalIDField(externalID, fieldMap)
	if err != nil {
		return nil, fmt.Errorf(`Invalid --external-id field "%s". Must be a filterable User field.`, externalID)
	}
	return &meta, nil
}

// ValidationError is a problem with one user: a message code and the
// arguments of its text.
type ValidationError struct {
	Code        string   `json:"code"`
	MessageArgs []string `json:"messageArgs"`
}

func buildValidationError(code string, messageArgs ...string) ValidationError {
	return ValidationError{Code: code, MessageArgs: nonNil(messageArgs)}
}

func resolveUserMatchField(rawMatch any, present bool, merged *core.Object, fieldMap *core.FieldMap) (string, []ValidationError) {
	if !present {
		return "", nil
	}
	name, isText := rawMatch.(string)
	if !isText {
		return "", []ValidationError{buildValidationError("errorInvalidUserMatchField", core.JSString(rawMatch))}
	}
	meta, err := ValidateExternalIDField(name, fieldMap)
	if err != nil {
		return "", []ValidationError{buildValidationError("errorInvalidUserMatchField", name)}
	}
	matchField := meta.Name
	matchValue, _ := merged.Get(matchField)
	if value, ok := matchValue.(string); !ok || value == "" {
		return matchField, []ValidationError{buildValidationError("errorUserMatchFieldEmpty", matchField)}
	}
	return matchField, nil
}

// MergeUserFields returns the persona attributes overlaid by the fields of
// the user.
func MergeUserFields(personaUserAttributes, userFields *core.Object) *core.Object {
	merged := personaUserAttributes.Clone()
	merged.Source = nil
	if userFields != nil {
		for _, key := range userFields.Keys {
			merged.Set(key, userFields.Values[key])
		}
	}
	return merged
}

func mergePersonaAttributes(validDefs []*core.Object, userOverrideFields *core.Object, fieldMap *core.FieldMap) (*core.Object, []ValidationError) {
	attributes := core.NewObject()
	var conflicts []string
	seenConflict := map[string]bool{}
	for _, def := range validDefs {
		userAttributes := def.Object("userAttributes")
		if userAttributes == nil {
			continue
		}
		for _, rawKey := range userAttributes.Keys {
			value := userAttributes.Values[rawKey]
			canonicalKey := rawKey
			if meta, ok := fieldMap.Get(rawKey); ok {
				canonicalKey = meta.Name
			}
			if existing, has := attributes.Get(canonicalKey); has && !strictEqual(existing, value) {
				conflicts = appendUnique(conflicts, seenConflict, canonicalKey)
			} else {
				attributes.Set(canonicalKey, value)
			}
		}
	}
	var validationErrors []ValidationError
	for _, conflictKey := range conflicts {
		if _, overridden := userOverrideFields.Get(conflictKey); !overridden {
			validationErrors = append(validationErrors, buildValidationError("errorPersonaConflictUserAttribute", conflictKey))
		}
	}
	if len(attributes.Keys) == 0 {
		return nil, validationErrors
	}
	return attributes, validationErrors
}

// MergePersonas merges the named personas: assignment lists are joined
// without repeats, and a profile, role or mode that the personas disagree
// on is reported.  personas is the "personas" object of a personas
// document.  A profile or role conflict is not reported when the user
// overrides that value.
func MergePersonas(names []string, personas *core.Object, userOverrideFields *core.Object, fieldMap *core.FieldMap, hasProfileOverride, hasRoleOverride bool) (Persona, []ValidationError) {
	var validationErrors []ValidationError
	var validDefs []*core.Object
	for _, name := range names {
		def := personas.Object(name)
		if def == nil {
			validationErrors = append(validationErrors, buildValidationError("errorUnknownPersona", name))
		} else {
			validDefs = append(validDefs, def)
		}
	}
	var effective Persona
	for _, listKey := range AssignmentListKeys {
		seen := map[string]bool{}
		var unioned []string
		for _, def := range validDefs {
			for _, item := range def.Strings(listKey) {
				unioned = appendUnique(unioned, seen, item)
			}
		}
		if len(unioned) > 0 {
			effective.setList(listKey, unioned)
		}
	}
	distinct := func(key string) []string {
		seen := map[string]bool{}
		var values []string
		for _, def := range validDefs {
			if value, _ := def.String(key); value != "" {
				values = appendUnique(values, seen, value)
			}
		}
		return values
	}
	profiles := distinct("profile")
	if len(profiles) > 1 && !hasProfileOverride {
		validationErrors = append(validationErrors, buildValidationError("errorPersonaConflictProfile", strings.Join(profiles, ", ")))
	} else if len(profiles) == 1 {
		effective.Profile = profiles[0]
	}
	roles := distinct("role")
	if len(roles) > 1 && !hasRoleOverride {
		validationErrors = append(validationErrors, buildValidationError("errorPersonaConflictRole", strings.Join(roles, ", ")))
	} else if len(roles) == 1 {
		effective.Role = roles[0]
	}
	for _, modeKey := range ModeKeys {
		var modes []any
		for _, def := range validDefs {
			value, present := def.Get(modeKey)
			if !present {
				continue
			}
			known := false
			for _, mode := range modes {
				known = known || strictEqual(mode, value)
			}
			if !known {
				modes = append(modes, value)
			}
		}
		if len(modes) > 1 {
			validationErrors = append(validationErrors, buildValidationError("errorPersonaConflictMode", modeKey))
		} else if len(modes) == 1 {
			effective.setMode(modeKey, core.JSString(modes[0]))
		}
	}
	userAttributes, attributeErrors := mergePersonaAttributes(validDefs, userOverrideFields, fieldMap)
	validationErrors = append(validationErrors, attributeErrors...)
	if userAttributes != nil {
		effective.UserAttributes = userAttributes
	}
	return effective, validationErrors
}

var nonAliasCharacters = regexp.MustCompile(`[^a-zA-Z0-9]`)

func sanitizeAliasPart(value any) string {
	part, _ := value.(string)
	return nonAliasCharacters.ReplaceAllString(part, "")
}

// BuildDefaultAlias returns an alias of at most 8 lower-case characters
// from the letters and digits of the first and last name, or "" when the
// names have none.  Each name gives 3 characters, and more when the other
// name is shorter than 3.
func BuildDefaultAlias(firstName, lastName any) string {
	first := sanitizeAliasPart(firstName)
	last := sanitizeAliasPart(lastName)
	if first == "" && last == "" {
		return ""
	}
	firstDeficit := max(0, 3-len(first))
	lastDeficit := max(0, 3-len(last))
	firstPart := first[:min(len(first), 3+lastDeficit)]
	lastPart := last[:min(len(last), 3+firstDeficit)]
	alias := strings.ToLower(firstPart + lastPart)
	return alias[:min(len(alias), 8)]
}

// BuildDefaultUsername returns the email followed by a dot and the domain
// label of the org, or "" when there is no email.
func BuildDefaultUsername(email any, myDomain string) string {
	if address, ok := email.(string); ok && address != "" {
		return address + "." + myDomain
	}
	return ""
}

// DeriveMyDomain returns the first label of the host name of the instance
// URL, or "" when the URL has none.
func DeriveMyDomain(instanceURL string) string {
	parsed, err := url.Parse(instanceURL)
	if err != nil || parsed.Scheme == "" {
		return ""
	}
	return strings.ToLower(strings.Split(parsed.Hostname(), ".")[0])
}

func resolveInputKey(fields *core.Object, fallback string) string {
	for _, field := range []string{"FederationIdentifier", "Username", "Email"} {
		if value, _ := fields.String(field); value != "" {
			return value
		}
	}
	return fallback
}

// FindFirstUserWithPersonas returns a name for the first user entry that
// has a personas key, and false when no entry has one.
func FindFirstUserWithPersonas(rawUsers any) (string, bool) {
	users, ok := rawUsers.([]any)
	if !ok {
		return "", false
	}
	return core.FirstUserWithPersonas(users)
}

// extractUserRef reads the profile or role key of a user.  A null or blank
// value counts as absent; a value that is not text is reported.
func extractUserRef(value any, invalidKey string, validationErrors *[]ValidationError) string {
	if value == nil {
		return ""
	}
	ref, ok := value.(string)
	if !ok {
		*validationErrors = append(*validationErrors, buildValidationError(invalidKey, core.JSString(value)))
		return ""
	}
	if strings.TrimSpace(ref) == "" {
		return ""
	}
	return ref
}

// RelatedSelection says which relationships a user may select.
type RelatedSelection struct {
	CatalogSupplied bool
	Names           map[string]bool
}

// resolveRelatedSelection reads the related key of a user.  Each problem
// is reported for that user only.
func resolveRelatedSelection(rawRelated any, related *RelatedSelection, validationErrors *[]ValidationError) []string {
	if rawRelated == nil {
		return nil
	}
	list, isList := rawRelated.([]any)
	names := make([]string, 0, len(list))
	for _, item := range list {
		name, isText := item.(string)
		isList = isList && isText
		names = append(names, name)
	}
	if !isList {
		*validationErrors = append(*validationErrors, buildValidationError("errorInvalidRelatedKey", jsonStringify(rawRelated)))
		return nil
	}
	if len(names) == 0 {
		return []string{}
	}
	if related == nil || !related.CatalogSupplied {
		*validationErrors = append(*validationErrors, buildValidationError("errorRelatedWithoutCatalog"))
		return nil
	}
	seen := map[string]bool{}
	selected := []string{}
	for _, name := range names {
		if seen[name] {
			*validationErrors = append(*validationErrors, buildValidationError("errorDuplicateRelationshipSelection", name))
			continue
		}
		seen[name] = true
		if !related.Names[name] {
			*validationErrors = append(*validationErrors, buildValidationError("errorUnknownRelationship", name))
			continue
		}
		selected = append(selected, name)
	}
	return selected
}

func extractRelatedContext(input *core.Object, validationErrors *[]ValidationError) *core.Object {
	raw, present := input.GetFold("relatedcontext")
	if !present {
		return nil
	}
	context, ok := raw.(*core.Object)
	if !ok {
		*validationErrors = append(*validationErrors, buildValidationError("errorInvalidRelatedContext"))
		return nil
	}
	return context
}

// CanonicalUser is one entry of a users definition after its fields were
// given their describe names and its personas were merged.
type CanonicalUser struct {
	// InputKey names the user in results: its FederationIdentifier,
	// Username or Email, or a text made from its position.
	InputKey         string
	Personas         []string
	EffectivePersona Persona
	// ProfileRef and RoleRef are the profile and role keys of the user,
	// or "".
	ProfileRef string
	RoleRef    string
	// MatchField is the match key of the user under its describe name,
	// or "".
	MatchField string
	// FuzzyUsername is nil when the user does not set it.
	FuzzyUsername  *bool
	Related        []string
	RelatedContext *core.Object
	// Fields are the persona attributes overlaid by the User fields of
	// the entry.
	Fields           *core.Object
	ValidationErrors []ValidationError
	// Source is the CSV row of the entry, or nil.
	Source *core.RowInfo
}

func canonicalizeValidUser(input *core.Object, names []string, personas *core.Object, fieldMap *core.FieldMap, fallback string, related *RelatedSelection) (*CanonicalUser, error) {
	candidateFields := core.NewObject()
	var validationErrors []ValidationError
	profileRef, roleRef := "", ""
	for _, key := range input.Keys {
		value := input.Values[key]
		lower := strings.ToLower(key)
		if lower == "profile" {
			profileRef = extractUserRef(value, "errorInvalidUserProfile", &validationErrors)
		} else if lower == "role" {
			roleRef = extractUserRef(value, "errorInvalidUserRole", &validationErrors)
		} else if !reservedUserKeys[lower] {
			candidateFields.Set(key, value)
		}
	}
	userFields, err := CanonicalizeFieldObject(candidateFields, fieldMap, "user personas="+strings.Join(names, "+"))
	if err != nil {
		return nil, err
	}
	_, hasProfileID := userFields.Get("ProfileId")
	_, hasRoleID := userFields.Get("UserRoleId")
	if profileRef != "" && hasProfileID {
		validationErrors = append(validationErrors, buildValidationError("errorUserProfileConflict"))
		profileRef = ""
	}
	if roleRef != "" && hasRoleID {
		validationErrors = append(validationErrors, buildValidationError("errorUserRoleConflict"))
		roleRef = ""
	}
	// A raw ProfileId or UserRoleId is an override of the user too, so it
	// also stops a persona profile or role conflict from being reported.
	hasProfileOverride := profileRef != "" || hasProfileID
	hasRoleOverride := roleRef != "" || hasRoleID
	effective, mergeErrors := MergePersonas(names, personas, userFields, fieldMap, hasProfileOverride, hasRoleOverride)
	personaFields, err := CanonicalizeFieldObject(effective.UserAttributes, fieldMap, "effective persona")
	if err != nil {
		return nil, err
	}
	merged := MergeUserFields(personaFields, userFields)
	rawMatch, hasMatch := input.GetFold("match")
	matchField, matchErrors := resolveUserMatchField(rawMatch, hasMatch, merged, fieldMap)
	rawFuzzy, hasFuzzy := input.GetFold("fuzzyusername")
	var fuzzyUsername *bool
	if fuzzy, isBoolean := rawFuzzy.(bool); isBoolean {
		fuzzyUsername = &fuzzy
	} else if hasFuzzy {
		displayValue, isText := rawFuzzy.(string)
		if !isText {
			displayValue = jsonStringify(rawFuzzy)
		}
		validationErrors = append(validationErrors, buildValidationError("errorInvalidFuzzyUsername", displayValue))
	}
	rawRelated, _ := input.GetFold("related")
	var relatedErrors []ValidationError
	selectedRelated := resolveRelatedSelection(rawRelated, related, &relatedErrors)
	rawContext := extractRelatedContext(input, &relatedErrors)
	allErrors := append(append(append(validationErrors, mergeErrors...), matchErrors...), relatedErrors...)
	inputKeyFallback := fallback
	if len(names) > 0 {
		inputKeyFallback = strings.Join(names, "+") + ":" + fallback
	}
	return &CanonicalUser{
		InputKey:         resolveInputKey(merged, inputKeyFallback),
		Personas:         nonNil(names),
		EffectivePersona: effective,
		ProfileRef:       profileRef,
		RoleRef:          roleRef,
		MatchField:       matchField,
		FuzzyUsername:    fuzzyUsername,
		Related:          selectedRelated,
		RelatedContext:   rawContext,
		Fields:           merged,
		ValidationErrors: allErrors,
		Source:           input.Source,
	}, nil
}

// ValidateAndCanonicalizeUsers reads the "users" value of a users
// definition.  A problem with one entry is recorded in the
// ValidationErrors of that user; a value that is not a list, an entry that
// is not an object and a key that is not a User field are errors of the
// whole definition.  personas is the "personas" object of the personas
// document.  When personasSupplied is false an entry needs no personas.
func ValidateAndCanonicalizeUsers(rawUsers any, personas *core.Object, fieldMap *core.FieldMap, personasSupplied bool, related *RelatedSelection) ([]*CanonicalUser, error) {
	list, ok := rawUsers.([]any)
	if !ok {
		return nil, errors.New("user-def.json must contain a users array.")
	}
	users := []*CanonicalUser{}
	failedUser := func(input *core.Object, skip func(lowerKey string) bool, fallback, code string) error {
		fields := core.NewObject()
		for _, key := range input.Keys {
			if !skip(strings.ToLower(key)) {
				fields.Set(key, input.Values[key])
			}
		}
		userFields, err := CanonicalizeFieldObject(fields, fieldMap, "user")
		if err != nil {
			return err
		}
		users = append(users, &CanonicalUser{
			InputKey:         resolveInputKey(userFields, "unknown:"+fallback),
			Personas:         []string{},
			Fields:           userFields,
			ValidationErrors: []ValidationError{buildValidationError(code)},
			Source:           input.Source,
		})
		return nil
	}
	for _, rawUser := range list {
		input, isObject := rawUser.(*core.Object)
		if !isObject || input == nil {
			return nil, errors.New("Each user entry must be an object.")
		}
		fallback := fmt.Sprint(len(users) + 1)
		if _, hasLegacyPersonaKey := input.GetFold("persona"); hasLegacyPersonaKey {
			if err := failedUser(input, func(lowerKey string) bool { return lowerKey == "persona" }, fallback, "errorLegacyPersonaKey"); err != nil {
				return nil, err
			}
			continue
		}
		rawNames, _ := input.List("personas")
		personaNames := make([]string, 0, len(rawNames))
		validNames := len(rawNames) > 0
		for _, item := range rawNames {
			name, isText := item.(string)
			validNames = validNames && isText
			personaNames = append(personaNames, name)
		}
		if !validNames {
			if !personasSupplied {
				user, err := canonicalizeValidUser(input, []string{}, personas, fieldMap, fallback, related)
				if err != nil {
					return nil, err
				}
				users = append(users, user)
				continue
			}
			if err := failedUser(input, func(lowerKey string) bool { return reservedUserKeys[lowerKey] }, fallback, "errorNoPersonas"); err != nil {
				return nil, err
			}
			continue
		}
		user, err := canonicalizeValidUser(input, personaNames, personas, fieldMap, fallback, related)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

// ValidatePersonaModes checks that every assignment mode of the personas
// is "additive" or "sync" and that every assignment list is a list.
// personas is the "personas" object of a personas document.
func ValidatePersonaModes(personas *core.Object) error {
	if personas == nil {
		return nil
	}
	for _, personaKey := range personas.Keys {
		persona, _ := personas.Values[personaKey].(*core.Object)
		for _, modeKey := range ModeKeys {
			value, present := persona.Get(modeKey)
			if !present {
				continue
			}
			if mode, isText := value.(string); !isText || (mode != "additive" && mode != "sync") {
				return fmt.Errorf(`Invalid assignment mode "%s". Expected additive or sync.`, core.JSString(value))
			}
		}
		for _, listKey := range AssignmentListKeys {
			candidate, present := persona.Get(listKey)
			if _, isList := candidate.([]any); present && !isList {
				return fmt.Errorf(`Persona "%s" has non-array %s.`, personaKey, listKey)
			}
		}
	}
	return nil
}

// PracticalRequiredUserFields are the fields an insert of a User needs,
// besides ProfileId.
var PracticalRequiredUserFields = []string{
	"Username",
	"LastName",
	"Alias",
	"TimeZoneSidKey",
	"LocaleSidKey",
	"EmailEncodingKey",
	"LanguageLocaleKey",
}

// MissingRequiredFieldsForInsert returns the required fields that fields
// has no value for.  profileIntended is true when a profile was named by
// the user or a persona; an unresolved profile is then reported as a
// missing reference and ProfileId is not reported here as well.
func MissingRequiredFieldsForInsert(fields *core.Object, profileIntended bool) []string {
	var missing []string
	for _, field := range PracticalRequiredUserFields {
		if value, _ := fields.Get(field); !truthy(value) {
			missing = append(missing, field)
		}
	}
	if value, _ := fields.Get("ProfileId"); !profileIntended && !truthy(value) {
		missing = append(missing, "ProfileId")
	}
	return missing
}
