package eligibility

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrConflict = errors.New("eligibility_conflict")

type Configuration struct {
	School  string          `json:"school"`
	Version string          `json:"version"`
	Rules   map[string]Rule `json:"rules"`
}

type Change struct {
	ExpectedVersion string      `json:"expected_version"`
	Conditions      []Condition `json:"conditions"`
	Reason          string      `json:"reason,omitempty"`
}

type Reset struct {
	ExpectedVersion string `json:"expected_version"`
	Reason          string `json:"reason"`
}

type Update struct {
	Actor, Key, School, Action string
	Change                     Change
	At                         time.Time
}

type ResetUpdate struct {
	Actor, Key, School, Action string
	Reset                      Reset
	At                         time.Time
}

type Repository interface {
	Rules
	Apply(context.Context, Update) (Rule, error)
}

type ResetRepository interface {
	Reset(context.Context, ResetUpdate) (Rule, error)
}

type SchoolRepository interface {
	SchoolRules(context.Context, string) (Configuration, error)
}

type Service struct {
	Repo Repository
	Now  func() time.Time
}

func validToken(s string, maxLen int) bool {
	return s != "" && len(s) <= maxLen && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) == s
}

func validReason(s string) bool {
	return s != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) != ""
}

func enrichRule(action, school string, r Rule) Rule {
	if r.Conditions == nil {
		r.Conditions = []Condition{}
	}
	if r.SupportedConditions == nil {
		if supported, err := SupportedConditions(action); err == nil {
			r.SupportedConditions = supported
		}
	}
	if r.Source == "" {
		if school == "*" {
			r.Source = SourceGlobal
		} else {
			r.Source = SourceSchoolOverride
		}
	}
	if r.GlobalConditions == nil && school == "*" {
		r.GlobalConditions = slices.Clone(r.Conditions)
	}
	return r
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) Get(ctx context.Context, school, action string) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if !validToken(school, 128) {
		return Rule{}, ErrRule
	}
	if _, err := (Defaults{}).Effective(ctx, action, school); err != nil {
		return Rule{}, ErrRule
	}
	r, err := s.Repo.Effective(ctx, action, school)
	if err != nil {
		return Rule{}, err
	}
	return enrichRule(action, school, r), nil
}

func (s Service) GetSchool(ctx context.Context, school string) (Configuration, error) {
	if s.Repo == nil {
		return Configuration{}, ErrUnavailable
	}
	if !validToken(school, 128) {
		return Configuration{}, ErrRule
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if sr, ok := s.Repo.(SchoolRepository); ok {
		return sr.SchoolRules(call, school)
	}
	out := Configuration{School: school, Rules: map[string]Rule{}}
	for _, action := range Actions() {
		r, err := s.Repo.Effective(call, action, school)
		if err != nil {
			return Configuration{}, err
		}
		r = enrichRule(action, school, r)
		out.Version = r.Version
		out.Rules[action] = r
	}
	return out, nil
}

func (s Service) Set(ctx context.Context, actor, key, school, action string, in Change) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if !validToken(actor, 2048) || !validToken(key, 128) || !validToken(school, 128) || !validToken(action, 128) || !validToken(in.ExpectedVersion, 128) {
		return Rule{}, ErrRule
	}
	if in.Reason != "" && !validReason(in.Reason) {
		return Rule{}, ErrRule
	}
	if in.Conditions == nil || ValidateAction(action, Rule{Version: in.ExpectedVersion, Conditions: in.Conditions}) != nil {
		return Rule{}, ErrRule
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.Repo.Apply(call, Update{Actor: actor, Key: key, School: school, Action: action, Change: in, At: s.now()})
	if err != nil {
		return Rule{}, err
	}
	return enrichRule(action, school, out), nil
}

func (s Service) Reset(ctx context.Context, actor, key, school, action string, in Reset) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	// Global scope "*" holds the fallback baseline and cannot be deleted as a school override.
	if school == "*" || !validToken(actor, 2048) || !validToken(key, 128) || !validToken(school, 128) || !validToken(action, 128) || !validToken(in.ExpectedVersion, 128) || !validReason(in.Reason) {
		return Rule{}, ErrRule
	}
	if _, err := SupportedConditions(action); err != nil {
		return Rule{}, ErrRule
	}
	rr, ok := s.Repo.(ResetRepository)
	if !ok {
		return Rule{}, ErrUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := rr.Reset(call, ResetUpdate{Actor: actor, Key: key, School: school, Action: action, Reset: in, At: s.now()})
	if err != nil {
		return Rule{}, err
	}
	return enrichRule(action, school, out), nil
}
