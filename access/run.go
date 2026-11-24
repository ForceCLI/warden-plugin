package access

import (
	"errors"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Options are the inputs of an audit.  Target names the target; User, as
// "field:value", limits the audit to one user; Sobject, with User and a
// type of field or object, audits every field or the object of an
// sObject instead of one target.
type Options struct {
	Type    string
	Target  string
	User    string
	Sobject string
}

// Run validates the target and audits it: for all active users, or for
// one user when options.User is set.
func Run(conn sfapi.Conn, options Options) (*Result, error) {
	resolver, err := GetResolver(options.Type)
	if err != nil {
		return nil, err
	}
	hasTarget := options.Target != ""
	hasUser := options.User != ""
	if options.Target != "" && options.Sobject != "" {
		return nil, core.NewLifecycleError("errorAccessScopesMutuallyExclusive", "target and sobject are mutually exclusive.", nil)
	}
	if !hasTarget && !hasUser {
		return nil, errors.New("An access target or user is required.")
	}
	var target *ValidatedTarget
	if options.Target != "" {
		if target, err = resolver.ValidateTarget(conn, options.Target); err != nil {
			return nil, err
		}
	} else {
		if options.Sobject == "" || (options.Type != TypeField && options.Type != TypeObject) {
			return nil, errors.New("SObject scope is only valid for field and object access.")
		}
		objectTarget, err := objectResolver.ValidateTarget(conn, options.Sobject)
		if err != nil {
			return nil, err
		}
		scoped := *objectTarget
		scoped.Type = options.Type
		target = &scoped
	}
	if options.User == "" {
		return resolver.Resolve(conn, target)
	}
	fieldMap, err := core.DescribeUserFields(conn, nil)
	if err != nil {
		return nil, err
	}
	parsedField, parsedValue, err := core.ParseUserFlag(options.User)
	if err != nil {
		return nil, err
	}
	matchField, ok := core.ResolveTargetField(parsedField, fieldMap)
	if !ok {
		return nil, errors.New(core.LifecycleMessage("errorInvalidUserMatchField", parsedField))
	}
	targets, targetErrors, err := core.ResolveTargets(conn, []core.MatchRequest{{Key: matchField + ":" + parsedValue, Field: matchField, Value: parsedValue, Order: 0}}, fieldMap)
	if err != nil {
		return nil, err
	}
	if len(targetErrors) > 0 {
		return nil, errors.New(targetErrors[0].Message)
	}
	if len(targets) == 0 {
		return nil, errors.New("User resolution failed.")
	}
	user := targets[0]
	reverseUser := ReverseUser{ID: user.ID, Name: user.ID}
	if user.Name != nil && *user.Name != "" {
		reverseUser.Name = *user.Name
	}
	if user.Username != nil {
		reverseUser.Username = *user.Username
	}
	return ResolveReverseAccess(conn, reverseUser, target)
}
