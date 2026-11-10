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
type Admission struct {
	School string
	Rules  Rules
	Facts  Facts
}

func (a Admission) Check(ctx context.Context, subject, school, action string) (Result, error) {
	if a.School == "" || a.Facts == nil {
		return Result{}, ErrUnavailable
	}
	if subject == "" || school != "" && school != a.School {
		return Result{}, ErrForbidden
	}
	rules := a.Rules
	if rules == nil {
		rules = Defaults{}
	}
	rule, err := rules.Effective(ctx, action, a.School)
	if err != nil || Validate(rule) != nil {
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
