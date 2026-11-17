// Package eligibility evaluates explicit action rules using supplied current facts.
// It has no identity, verification, catalog or network dependency.
package eligibility

import (
	"errors"
	"slices"
)

type Condition string

const (
	Content    Condition = "content_access"
	Campus     Condition = "campus_eligibility"
	Phone      Condition = "phone_bound"
	Identity   Condition = "identity_verified"
	Enrollment Condition = "course_enrollment"
)

type RuleSource string

const (
	SourceInitialDefault RuleSource = "initial_default"
	SourceGlobal         RuleSource = "global"
	SourceSchoolOverride RuleSource = "school_override"
)

type Fact uint8

const (
	Unknown Fact = iota
	Unsatisfied
	Satisfied
)

type Rule struct {
	Version             string      `json:"version"`
	Conditions          []Condition `json:"conditions"`
	Source              RuleSource  `json:"source,omitempty"`
	GlobalConditions    []Condition `json:"global_conditions,omitempty"`
	SupportedConditions []Condition `json:"supported_conditions,omitempty"`
}

type Result struct {
	Allowed     bool
	Satisfied   []Condition
	Missing     []Condition
	Unavailable []Condition
	Version     string
}

type Evaluation struct {
	Action                string      `json:"action"`
	School                string      `json:"school"`
	Allowed               bool        `json:"allowed"`
	RuleVersion           string      `json:"rule_version"`
	SatisfiedConditions   []Condition `json:"satisfied_conditions"`
	MissingConditions     []Condition `json:"missing_conditions"`
	UnavailableConditions []Condition `json:"unavailable_conditions"`
}

type EvaluationBatch struct {
	School string       `json:"school"`
	Items  []Evaluation `json:"items"`
}

func (r Result) Evaluation(school, action string) Evaluation {
	satisfied := r.Satisfied
	if satisfied == nil {
		satisfied = []Condition{}
	}
	missing := r.Missing
	if missing == nil {
		missing = []Condition{}
	}
	unavailable := r.Unavailable
	if unavailable == nil {
		unavailable = []Condition{}
	}
	return Evaluation{
		Action:                action,
		School:                school,
		Allowed:               r.Allowed,
		RuleVersion:           r.Version,
		SatisfiedConditions:   slices.Clone(satisfied),
		MissingConditions:     slices.Clone(missing),
		UnavailableConditions: slices.Clone(unavailable),
	}
}

var ErrRule = errors.New("invalid_eligibility_rule")

func Validate(rule Rule) error {
	if rule.Version == "" || len(rule.Version) > 128 || len(rule.Conditions) > 5 {
		return ErrRule
	}
	seen := map[Condition]bool{}
	for _, c := range rule.Conditions {
		switch c {
		case Content, Campus, Phone, Identity, Enrollment:
		default:
			return ErrRule
		}
		if seen[c] {
			return ErrRule
		}
		seen[c] = true
	}
	return nil
}

func ValidateAction(action string, rule Rule) error {
	if err := Validate(rule); err != nil {
		return err
	}
	supported, err := SupportedConditions(action)
	if err != nil {
		return err
	}
	for _, c := range rule.Conditions {
		if !slices.Contains(supported, c) {
			return ErrRule
		}
	}
	return nil
}

func Evaluate(rule Rule, facts map[Condition]Fact) (Result, error) {
	result := Result{
		Allowed:     true,
		Version:     rule.Version,
		Satisfied:   []Condition{},
		Missing:     []Condition{},
		Unavailable: []Condition{},
	}
	if err := Validate(rule); err != nil {
		return Result{}, err
	}
	for _, c := range rule.Conditions {
		switch facts[c] {
		case Satisfied:
			result.Satisfied = append(result.Satisfied, c)
		case Unsatisfied:
			result.Allowed = false
			result.Missing = append(result.Missing, c)
		default:
			result.Allowed = false
			result.Unavailable = append(result.Unavailable, c)
		}
	}
	return result, nil
}
