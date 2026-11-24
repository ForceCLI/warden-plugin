package cmd

import (
	"fmt"

	"github.com/spf13/pflag"

	"github.com/ForceCLI/warden-plugin/access"
	"github.com/ForceCLI/warden-plugin/core"
)

func init() {
	register(&command{
		id:        "warden access",
		bundle:    "warden.access",
		className: "UserAccess",
		define: func(flags *pflag.FlagSet, messages *Messages) {
			defineOrgFlags(flags)
			flags.String("type", "", messages.Get("flags.type.summary"))
			flags.String("target", "", messages.Get("flags.target.summary"))
			flags.String("user", "", messages.Get("flags.user.summary"))
			flags.String("sobject", "", messages.Get("flags.sobject.summary"))
			defineOutputFlags(flags)
			defineInteractiveFlag(flags)
		},
		run: runAccess,
	})
}

func runAccess(run *invocation) (any, error) {
	messages := loadMessages("warden.access")
	if err := run.interactiveGuard(); err != nil {
		return nil, err
	}
	if err := run.checkOption("type", "field", "object", "apex-class", "vf-page", "custom-permission", "tab", "record-type"); err != nil {
		return nil, err
	}
	context, err := run.outputContext()
	if err != nil {
		return nil, err
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	if !run.given("type") {
		return nil, sfError("Missing required flag type")
	}
	targetType, target, user, sobject := run.str("type"), run.str("target"), run.str("user"), run.str("sobject")
	userMode, hasTarget, hasSobject := user != "", target != "", sobject != ""
	switch {
	case hasTarget && hasSobject:
		return nil, sfError(messages.Get("errorUserModeScopesMutuallyExclusive"))
	case !userMode && hasSobject:
		return nil, sfError(messages.Get("errorSobjectRequiresUser"))
	case !userMode && !hasTarget:
		return nil, sfError(messages.Get("errorTargetRequired"))
	case userMode && !hasTarget && !hasSobject:
		return nil, sfError(messages.Get("errorUserModeRequiresScope"))
	}

	// An error of the library is reported with the text of this command
	// for its code, followed by the underlying error.
	translate := func(err error) error {
		if _, ok := err.(*commandError); ok {
			return err
		}
		if accessError, ok := access.AsAccessError(err); ok {
			message := messages.Get(accessError.Code, access.ErrorArgs(accessError)...)
			if cause := access.ErrorCause(accessError); cause != nil {
				message += " Underlying error: " + cause.Error()
			}
			return &commandError{name: "UserAccessError", message: message, exitCode: 1}
		}
		scope := target
		if scope == "" {
			scope = sobject
		}
		return sfError(messages.Get("errorAccessQueryFailed", targetType, scope))
	}

	resolver, err := access.GetResolver(targetType)
	if err != nil {
		return nil, translate(err)
	}
	if hasTarget {
		if _, err := resolver.ValidateTarget(conn, target); err != nil {
			return nil, translate(err)
		}
	} else {
		if targetType != "field" && targetType != "object" {
			return nil, sfError(messages.Get("errorSobjectUnsupported", targetType))
		}
		objectResolver, err := access.GetResolver("object")
		if err != nil {
			return nil, translate(err)
		}
		if _, err := objectResolver.ValidateTarget(conn, sobject); err != nil {
			return nil, translate(err)
		}
	}
	userLabel := ""
	if userMode {
		field, value, err := core.ParseUserFlag(user)
		if err != nil {
			return nil, sfError(messages.Get("errorInvalidUserValue", user))
		}
		fieldMap, err := core.DescribeUserFields(conn, nil)
		if err != nil {
			return nil, translate(err)
		}
		matchField, ok := core.ResolveTargetField(field, fieldMap)
		if !ok {
			return nil, sfError(messages.Get("errorInvalidUserMatchField", field))
		}
		targets, targetErrors, err := core.ResolveTargets(conn, []core.MatchRequest{{Key: matchField + ":" + value, Field: matchField, Value: value}}, fieldMap)
		if err != nil {
			return nil, translate(err)
		}
		if len(targetErrors) > 0 {
			return nil, sfError(targetErrors[0].Message)
		}
		if len(targets) == 0 {
			return nil, sfError(messages.Get("errorUserResolutionFailed"))
		}
		if targets[0].Name != nil {
			userLabel = *targets[0].Name
		}
		if userLabel == "" {
			userLabel = targets[0].ID
		}
	}

	result, err := access.Run(conn, access.Options{Type: targetType, Target: target, User: user, Sobject: sobject})
	if err != nil {
		return nil, translate(err)
	}
	result.Rows = access.StableOrder(result.Rows)
	columns := resolver.CsvColumns()
	if userMode {
		columns = access.ReverseCsvColumns(result.TargetType)
	}
	rows := make([]map[string]any, len(result.Rows))
	for index, row := range result.Rows {
		rows[index] = access.FlattenAccessRow(row, columns)
	}
	csv := core.SerializeCsv(rows, columns)
	if context.format == "csv" && context.outputFile == "" {
		run.warn(fmt.Sprintf("Stats: active users with access %d; profiles %d; permission sets %d; permission set groups %d",
			result.Stats.TotalActiveUsersWithAccess, result.Stats.ProfileGrants, result.Stats.PermissionSetGrants, result.Stats.PermissionSetGroupGrants))
		for _, warning := range result.Warnings {
			run.warn("Warning: " + warning)
		}
	}
	human, err := access.RenderAccessResult(result, userLabel)
	if err != nil {
		return nil, translate(err)
	}
	if err := emitResult(run.out, context, result, csv, human); err != nil {
		return nil, err
	}
	return result, nil
}
