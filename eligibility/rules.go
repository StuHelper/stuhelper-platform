// Package eligibility evaluates explicit action rules using supplied current facts.
// It has no identity, verification, catalog or network dependency.
package eligibility

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Condition string

const (
	Content    Condition = "content_access"
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
	Version             string      `json:"version"`
	Conditions          []Condition `json:"conditions"`
	SupportedConditions []Condition `json:"supported_conditions,omitempty"`
}
type Result struct {
	Allowed     bool
	Missing     []Condition
	Unavailable []Condition
	Version     string
}

var ErrRule = errors.New("invalid_eligibility_rule")

func validToken(s string, maxLen int) bool {
	return s != "" && len(s) <= maxLen && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) && !strings.ContainsFunc(s, unicode.IsSpace)
}

func Validate(rule Rule) error {
	if !validToken(rule.Version, 128) || len(rule.Conditions) > 5 {
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

// ValidateAction verifies both structural rule invariants and that every
// configured condition is supported by the target action's fact providers.
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
