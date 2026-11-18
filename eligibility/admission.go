package eligibility

import (
	"context"
	"errors"
)

var ErrUnavailable = errors.New("eligibility_unavailable")
var ErrForbidden = errors.New("eligibility_forbidden")

type Facts interface {
	Facts(context.Context, string, string, []Condition) (map[Condition]Fact, error)
}

// Admission evaluates independently named actions without any business-module types.
// Rules and Facts must both be explicitly wired (use Defaults{} in tests when static
// defaults are intended). An empty target school evaluates against the server-owned
// Admission.School so directory and favorite callers can resolve the served school
// before checking object ownership; any explicit school mismatch is rejected.
type Admission struct {
	School string
	Rules  Rules
	Facts  Facts
}

func (a Admission) Check(ctx context.Context, subject, school, action string) (Result, error) {
	if !ValidSchool(a.School) || a.School == "*" || a.Rules == nil || a.Facts == nil {
		return Result{}, ErrUnavailable
	}
	if !validActor(subject) || school != "" && school != a.School {
		return Result{}, ErrForbidden
	}
	rule, err := a.Rules.Effective(ctx, action, a.School)
	if err != nil || ValidateAction(action, rule) != nil {
		return Result{}, ErrUnavailable
	}
	facts := map[Condition]Fact{}
	if len(rule.Conditions) > 0 {
		facts, err = a.Facts.Facts(ctx, subject, a.School, rule.Conditions)
		if err != nil {
			return Result{}, ErrUnavailable
		}
	}
	result, err := Evaluate(rule, facts)
	if err != nil || len(result.Unavailable) > 0 {
		return result, ErrUnavailable
	}
	if !result.Allowed {
		return result, ErrForbidden
	}
	return result, nil
}
