// Package core holds what the access, lifecycle and provisioning packages
// share: errors, ordered JSON documents, User field maps, CSV and
// definition file reading, SOQL helpers, user matching and user targeting.
// It is a port of the shared modules of @syntax-syllogism/warden-core 0.7.0.
package core

import (
	"errors"
	"strings"
)

// WardenError is an error the library raises on purpose.  Code is stable
// and callers can replace Message by their own text for a code.
type WardenError struct {
	// Name is the class of the error: WardenError, AccessError,
	// LifecycleError, ProvisioningError, DefinitionError, CsvReadError or
	// RelatedRecordsError.
	Name    string
	Code    string
	Message string
	Data    map[string]any
}

func (e *WardenError) Error() string {
	return e.Message
}

func newError(name, code, message string, data map[string]any) *WardenError {
	return &WardenError{Name: name, Code: code, Message: message, Data: data}
}

// NewLifecycleError returns an error of the lifecycle commands.
func NewLifecycleError(code, message string, data map[string]any) *WardenError {
	return newError("LifecycleError", code, message, data)
}

// NewProvisioningError returns an error of provisioning.
func NewProvisioningError(code, message string, data map[string]any) *WardenError {
	return newError("ProvisioningError", code, message, data)
}

// NewDefinitionError returns an error of a definition file.
func NewDefinitionError(code, message string, data map[string]any) *WardenError {
	return newError("DefinitionError", code, message, data)
}

// NewRelatedRecordsError returns an error of a related-record catalog.
func NewRelatedRecordsError(code, message string, data map[string]any) *WardenError {
	return newError("RelatedRecordsError", code, message, data)
}

// NewAccessError returns an error of an access audit.
func NewAccessError(code, message string, data map[string]any) *WardenError {
	return newError("AccessError", code, message, data)
}

// AsWardenError returns err as a WardenError when it is one.
func AsWardenError(err error) (*WardenError, bool) {
	var warden *WardenError
	if errors.As(err, &warden) {
		return warden, true
	}
	return nil, false
}

// Format replaces each %s of a template by the next argument, and by
// nothing when the arguments run out.
func Format(template string, args ...string) string {
	var out strings.Builder
	index := 0
	for {
		position := strings.Index(template, "%s")
		if position < 0 {
			out.WriteString(template)
			return out.String()
		}
		out.WriteString(template[:position])
		if index < len(args) {
			out.WriteString(args[index])
		}
		index++
		template = template[position+2:]
	}
}

// MessageTable returns a lookup of message templates: the text for a code
// with its arguments, or the code itself when the table has no entry.
func MessageTable(templates map[string]string) func(code string, args ...string) string {
	return func(code string, args ...string) string {
		template, ok := templates[code]
		if !ok {
			template = code
		}
		return Format(template, args...)
	}
}

// LifecycleMessage is the default English text of the lifecycle codes.
var LifecycleMessage = MessageTable(map[string]string{
	"errorInvalidJson":                "Failed to parse JSON file %s: %s",
	"errorInvalidPersonaDefinition":   "persona-def.json must contain a personas object.",
	"errorPersonasWithoutDefinition":  `User "%s" lists personas but no --personas-def was supplied.`,
	"errorInvalidAgainstMatchField":   `Invalid against match field "%s".`,
	"errorInvalidUserValue":           `Invalid --user value "%s". Expected field:value.`,
	"errorInvalidAgainstValue":        `Invalid --against value "%s". Expected field:value.`,
	"errorInvalidUserMatchField":      `Invalid user match field "%s".`,
	"errorPersonaSourceRequiresUsers": `Persona source "%s" requires a users definition so held personas are known.`,
	"errorPersonaSourceUnsupported":   `Persona source "%s" is not supported by warden-core; the caller must provide held personas.`,
	"errorDuplicateExternalIdMatch":   `Multiple users matched %s="%s".`,
	"errorPromptDeclined":             "Operation cancelled.",
	"warningPromptTimeout":            "Warning confirmation timed out after 10 seconds.",
	"warningMissingUserLogin":         "No UserLogin row was found for this user.",
	"promptContinue":                  "Continue with this operation?",
})
