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

// ValidSchool checks that school is "*" or a non-blank, control-free UTF-8 identifier up to 128 bytes.
func ValidSchool(school string) bool {
	return validToken(school, 128)
}

func validActor(actor string) bool {
	return actor != "" && len(actor) <= 2048 && utf8.ValidString(actor) && !strings.ContainsFunc(actor, unicode.IsControl) && strings.TrimSpace(actor) == actor
}

func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func validReason(s string) bool {
	return s != "" && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) && strings.TrimSpace(s) != ""
}

func enrichRule(action, school string, r Rule) (Rule, error) {
	if r.Conditions == nil {
		r.Conditions = []Condition{}
	}
	supported, err := SupportedConditions(action)
	if err != nil {
		return Rule{}, err
	}
	r.SupportedConditions = supported
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
	return r, nil
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
	if !ValidSchool(school) {
		return Rule{}, ErrRule
	}
	if !ValidAction(action) {
		return Rule{}, ErrRule
	}
	r, err := s.Repo.Effective(ctx, action, school)
	if err != nil {
		return Rule{}, err
	}
	return enrichRule(action, school, r)
}

func (s Service) GetSchool(ctx context.Context, school string) (Configuration, error) {
	if s.Repo == nil {
		return Configuration{}, ErrUnavailable
	}
	if !ValidSchool(school) {
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
		r, err = enrichRule(action, school, r)
		if err != nil {
			return Configuration{}, err
		}
		out.Version = r.Version
		out.Rules[action] = r
	}
	return out, nil
}

func (s Service) Set(ctx context.Context, actor, key, school, action string, in Change) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if !validActor(actor) || !validIdempotencyKey(key) || !ValidSchool(school) || !validToken(action, 128) || !validToken(in.ExpectedVersion, 128) {
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
	return enrichRule(action, school, out)
}

func (s Service) Reset(ctx context.Context, actor, key, school, action string, in Reset) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	// Global scope "*" holds the fallback baseline and cannot be deleted as a school override.
	if school == "*" || !validActor(actor) || !validIdempotencyKey(key) || !ValidSchool(school) || !validToken(action, 128) || !validToken(in.ExpectedVersion, 128) || !validReason(in.Reason) {
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
	return enrichRule(action, school, out)
}
