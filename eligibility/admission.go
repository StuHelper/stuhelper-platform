package eligibility

import (
	"context"
	"errors"
	"slices"
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

// Preview evaluates a bounded batch of actions for an authenticated user so UI
// entrypoints can display missing conditions or retry prompts before form input.
// Temporary fact-provider failures mark the affected conditions as unavailable
// instead of masquerading as missing credentials.
func (a Admission) Preview(ctx context.Context, subject, school string, actions []string) (EvaluationBatch, error) {
	out := EvaluationBatch{School: school, Items: []Evaluation{}}
	if !validToken(school, 128) || len(actions) == 0 || len(actions) > 15 {
		return out, ErrRule
	}
	seenActions := map[string]bool{}
	for _, action := range actions {
		if seenActions[action] {
			return out, ErrRule
		}
		if _, err := SupportedConditions(action); err != nil {
			return out, ErrRule
		}
		seenActions[action] = true
	}
	if a.School == "" || a.Facts == nil {
		return out, ErrUnavailable
	}
	if subject == "" || school != a.School {
		return out, ErrForbidden
	}
	rules := a.Rules
	if rules == nil {
		rules = Defaults{}
	}
	loaded := make([]Rule, len(actions))
	generalConditions := []Condition{}
	needEnrollment := false
	for i, action := range actions {
		rule, err := rules.Effective(ctx, action, a.School)
		if err != nil || Validate(rule) != nil {
			return out, ErrUnavailable
		}
		loaded[i] = rule
		for _, cond := range rule.Conditions {
			if cond == Enrollment {
				needEnrollment = true
			} else if !slices.Contains(generalConditions, cond) {
				generalConditions = append(generalConditions, cond)
			}
		}
	}
	combinedFacts := map[Condition]Fact{}
	if len(generalConditions) > 0 {
		fetched, err := a.Facts.Facts(ctx, subject, a.School, generalConditions)
		if err != nil && ctx.Err() != nil {
			return out, ErrUnavailable
		}
		if err == nil {
			for k, v := range fetched {
				combinedFacts[k] = v
			}
		}
	}
	if needEnrollment {
		fetched, err := a.Facts.Facts(ctx, subject, a.School, []Condition{Enrollment})
		if err != nil && ctx.Err() != nil {
			return out, ErrUnavailable
		}
		if err == nil {
			if v, ok := fetched[Enrollment]; ok {
				combinedFacts[Enrollment] = v
			}
		}
	}
	for i, action := range actions {
		res, err := Evaluate(loaded[i], combinedFacts)
		if err != nil {
			return out, ErrUnavailable
		}
		out.Items = append(out.Items, res.Evaluation(a.School, action))
	}
	return out, nil
}
