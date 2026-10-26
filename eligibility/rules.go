// Package eligibility evaluates explicit action rules using supplied current facts.
// It has no identity, verification, catalog or network dependency.
package eligibility

import "errors"

type Condition string

const (
	Campus     Condition = "campus_eligibility"
	Phone      Condition = "phone_bound"
	Identity   Condition = "identity_verified"
	Enrollment Condition = "course_enrollment"
)

type Fact uint8

const (
	Unknown Fact = iota
	Unsatisfied
	Satisfied
)

type Rule struct {
	Version    string      `json:"version"`
	Conditions []Condition `json:"conditions"`
}
type Result struct {
	Allowed     bool
	Missing     []Condition
	Unavailable []Condition
	Version     string
}

var ErrRule = errors.New("invalid_eligibility_rule")

func Validate(rule Rule) error {
	if rule.Version == "" || len(rule.Version) > 128 || len(rule.Conditions) > 4 {
		return ErrRule
	}
	seen := map[Condition]bool{}
	for _, c := range rule.Conditions {
		switch c {
		case Campus, Phone, Identity, Enrollment:
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
func Evaluate(rule Rule, facts map[Condition]Fact) (Result, error) {
	result := Result{Allowed: true, Version: rule.Version, Missing: []Condition{}, Unavailable: []Condition{}}
	if err := Validate(rule); err != nil {
		return Result{}, err
	}
	for _, c := range rule.Conditions {
		switch facts[c] {
		case Satisfied:
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
