package core

import (
	"fmt"
	"os"
)

// ProvisioningMessage is replaced by the provisioning package with its
// full table; core uses three of its codes.
var ProvisioningMessage = MessageTable(map[string]string{
	"errorInvalidJson":               "Failed to parse JSON file %s: %s",
	"errorInvalidPersonaDefinition":  "persona-def.json must contain a personas object.",
	"errorPersonasWithoutDefinition": `User "%s" lists personas but no --personas-def was supplied.`,
})

// DefinitionMessages are the texts of the errors of definition reading,
// which a caller can replace.
type DefinitionMessages struct {
	PersonasWithoutDefinition func(userKey string) string
	InvalidJSON               func(path, detail string) string
}

// DefaultDefinitionMessages uses the provisioning texts.
func DefaultDefinitionMessages() DefinitionMessages {
	return DefinitionMessages{
		PersonasWithoutDefinition: func(userKey string) string {
			return ProvisioningMessage("errorPersonasWithoutDefinition", userKey)
		},
		InvalidJSON: func(path, detail string) string { return ProvisioningMessage("errorInvalidJson", path, detail) },
	}
}

// ReadJSONFile reads and decodes a JSON file.  A file that cannot be read
// or decoded is a DefinitionError with the code errorInvalidJson.
func ReadJSONFile(path string, invalidJSON func(path, detail string) string) (any, error) {
	fail := func(err error) error {
		return NewDefinitionError("errorInvalidJson", invalidJSON(path, err.Error()), map[string]any{"path": path, "detail": err.Error()})
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fail(err)
	}
	value, err := ParseJSON(content)
	if err != nil {
		return nil, fail(err)
	}
	return value, nil
}

// ReadOptions say how a users definition file is read.
type ReadOptions struct {
	// InputFormat is "json", "csv" or "" to decide by extension.
	InputFormat      string
	CsvListDelimiter string
	// FieldMap is the User field map, needed for CSV.
	FieldMap *FieldMap
}

// ReadUsersDefinition reads a users definition from a JSON, CSV or TSV
// file.
func ReadUsersDefinition(path string, options ReadOptions, invalidJSON func(path, detail string) string) (*Object, error) {
	if invalidJSON == nil {
		invalidJSON = DefaultDefinitionMessages().InvalidJSON
	}
	if DetectInputFormat(path, options.InputFormat) == "json" {
		value, err := ReadJSONFile(path, invalidJSON)
		if err != nil {
			return nil, err
		}
		return ParseUsersDefinition(value)
	}
	if options.FieldMap == nil {
		return nil, NewDefinitionError("errorMissingUserFieldMap", "A User field map is required to read CSV user definitions.", map[string]any{"path": path})
	}
	delimiter := options.CsvListDelimiter
	if delimiter == "" {
		delimiter = ";"
	}
	return ReadCsvUsers(path, options.FieldMap, delimiter)
}

// DefinitionSource names where the definitions of a command come from: a
// document that was already read, or a path.
type DefinitionSource struct {
	UsersDoc         *Object
	PersonasDoc      *Object
	RelatedDoc       *Object
	UsersPath        string
	PersonasPath     string
	RelatedPath      string
	InputFormat      string
	CsvListDelimiter string
	// PersonasSupplied overrides whether a personas definition counts as
	// supplied; nil means it is when a document or a path is given.
	PersonasSupplied *bool
}

// Definitions are the documents a command works from.
type Definitions struct {
	UsersDoc         *Object
	PersonasDoc      *Object
	PersonasSupplied bool
	// RelatedDoc is nil when no catalog was supplied.
	RelatedDoc *Object
}

func emptyPersonas() *Object {
	doc := NewObject()
	doc.Set("personas", NewObject())
	return doc
}

// ResolveDefinitions returns the documents of a source, reading the paths
// it names.
func ResolveDefinitions(source DefinitionSource, fieldMap *FieldMap, messages DefinitionMessages) (*Definitions, error) {
	result := &Definitions{UsersDoc: source.UsersDoc, PersonasDoc: source.PersonasDoc, RelatedDoc: source.RelatedDoc}
	if result.UsersDoc == nil {
		if source.UsersPath == "" {
			return nil, NewDefinitionError("errorInvalidJson", messages.InvalidJSON("users-def", "missing path"), nil)
		}
		usersDoc, err := ReadUsersDefinition(source.UsersPath, ReadOptions{InputFormat: source.InputFormat, CsvListDelimiter: source.CsvListDelimiter, FieldMap: fieldMap}, messages.InvalidJSON)
		if err != nil {
			return nil, err
		}
		result.UsersDoc = usersDoc
	}
	if result.PersonasDoc == nil {
		result.PersonasDoc = emptyPersonas()
		if source.PersonasPath != "" {
			value, err := ReadJSONFile(source.PersonasPath, messages.InvalidJSON)
			if err != nil {
				return nil, err
			}
			if result.PersonasDoc, err = ParsePersonaDefinitions(value); err != nil {
				return nil, err
			}
		}
	}
	if result.RelatedDoc == nil && source.RelatedPath != "" {
		value, err := ReadJSONFile(source.RelatedPath, messages.InvalidJSON)
		if err != nil {
			return nil, err
		}
		if result.RelatedDoc, err = ParseRelatedCatalog(value); err != nil {
			return nil, err
		}
	}
	result.PersonasSupplied = source.PersonasDoc != nil || source.PersonasPath != ""
	if source.PersonasSupplied != nil {
		result.PersonasSupplied = *source.PersonasSupplied
	}
	return result, nil
}

// FirstUserWithPersonas returns a name for the first user that has a
// personas key: its FederationIdentifier, Username or Email, or its
// position counted from 1.  The second result is false when no user has
// the key.
func FirstUserWithPersonas(users []any) (string, bool) {
	for index, item := range users {
		user, ok := item.(*Object)
		if !ok {
			continue
		}
		if _, has := user.GetFold("personas"); !has {
			continue
		}
		for _, field := range []string{"FederationIdentifier", "Username", "Email"} {
			if value, _ := user.GetFold(field); value != nil {
				if text, ok := value.(string); ok && text != "" {
					return text, true
				}
			}
		}
		return fmt.Sprint(index + 1), true
	}
	return "", false
}

// LoadValidatedDefinitions resolves the definitions and checks the users
// and personas documents.  Users that list personas without a personas
// definition are an error.
func LoadValidatedDefinitions(source DefinitionSource, fieldMap *FieldMap, messages DefinitionMessages) (*Definitions, error) {
	definitions, err := ResolveDefinitions(source, fieldMap, messages)
	if err != nil {
		return nil, err
	}
	if _, err := ParseUsersDefinition(definitions.UsersDoc); err != nil {
		return nil, err
	}
	if _, err := ParsePersonaDefinitions(definitions.PersonasDoc); err != nil {
		return nil, err
	}
	if !definitions.PersonasSupplied {
		users, _ := definitions.UsersDoc.List("users")
		if userKey, found := FirstUserWithPersonas(users); found {
			return nil, NewDefinitionError("errorPersonasWithoutDefinition", messages.PersonasWithoutDefinition(userKey), nil)
		}
	}
	return definitions, nil
}
