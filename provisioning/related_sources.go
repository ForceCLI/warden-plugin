package provisioning

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
)

const (
	userSourcePrefix    = "user."
	contextSourcePrefix = "context."
)

// ParsedSource is a source text split into its kind: "userField" with
// Field, "userId", "context" with Name, or "invalid".
type ParsedSource struct {
	Kind  string
	Field string
	Name  string
}

// ParseSource splits the "from" text of a source expression.
func ParseSource(from any) ParsedSource {
	source, ok := from.(string)
	if !ok {
		return ParsedSource{Kind: "invalid"}
	}
	trimmed := strings.TrimSpace(source)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, contextSourcePrefix) {
		if name := trimmed[len(contextSourcePrefix):]; name != "" {
			return ParsedSource{Kind: "context", Name: name}
		}
		return ParsedSource{Kind: "invalid"}
	}
	if !strings.HasPrefix(lower, userSourcePrefix) {
		return ParsedSource{Kind: "invalid"}
	}
	field := trimmed[len(userSourcePrefix):]
	if field == "" || strings.Contains(field, ".") {
		return ParsedSource{Kind: "invalid"}
	}
	if strings.ToLower(field) == "id" {
		return ParsedSource{Kind: "userId"}
	}
	return ParsedSource{Kind: "userField", Field: field}
}

// isAbsent reports whether a value is null or the empty text.  0 and
// false are values.
func isAbsent(value any) bool {
	return value == nil || value == ""
}

// SourceContext is what a source expression is resolved against.
type SourceContext struct {
	Relationship   string
	FieldName      string
	UserFields     *core.Object
	UserFieldMap   *core.FieldMap
	RelatedContext *core.Object
	SavedUserID    string
}

// ResolvedSource is the outcome of ResolveSource: a value, a value that
// is known only after the User is saved (Pending), or an error.
type ResolvedSource struct {
	Value   any
	Pending bool
	Error   *ValidationError
}

// ResolveSource returns the value of a source expression for one user.
// The form of the source and the User field it names were checked with
// the catalog, so an error here is a source without a value for this
// user.
func ResolveSource(expr SourceExpr, ctx SourceContext) ResolvedSource {
	if !expr.HasFrom {
		return ResolvedSource{Value: expr.Value}
	}
	parsed := ParseSource(expr.From)
	switch parsed.Kind {
	case "userId":
		if ctx.SavedUserID != "" {
			return ResolvedSource{Value: ctx.SavedUserID}
		}
		return ResolvedSource{Pending: true}
	case "context":
		value, known := ctx.RelatedContext.Get(parsed.Name)
		if !known {
			failure := buildValidationError("errorUnknownContextName", ctx.Relationship, parsed.Name)
			return ResolvedSource{Error: &failure}
		}
		if isAbsent(value) {
			failure := buildValidationError("errorRelatedSourceEmpty", ctx.Relationship, ctx.FieldName, "context."+parsed.Name)
			return ResolvedSource{Error: &failure}
		}
		return ResolvedSource{Value: value}
	case "userField":
		canonical := parsed.Field
		if meta, ok := ctx.UserFieldMap.Get(parsed.Field); ok {
			canonical = meta.Name
		}
		value, _ := ctx.UserFields.Get(canonical)
		if isAbsent(value) {
			failure := buildValidationError("errorRelatedSourceEmpty", ctx.Relationship, ctx.FieldName, canonical)
			return ResolvedSource{Error: &failure}
		}
		return ResolvedSource{Value: value}
	}
	failure := buildValidationError("errorRelatedInvalidSourceValue", ctx.Relationship, ctx.FieldName, core.JSString(expr.From))
	return ResolvedSource{Error: &failure}
}
