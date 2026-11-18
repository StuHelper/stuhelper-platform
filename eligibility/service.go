package eligibility

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrConflict = errors.New("eligibility_conflict")

type Configuration struct {
	Version string                            `json:"version"`
	Rules   map[string]map[string][]Condition `json:"rules"`
}
type Change struct {
	ExpectedVersion string      `json:"expected_version"`
	Conditions      []Condition `json:"conditions"`
}
type Update struct {
	Actor, Key, School, Action string
	Change                     Change
	At                         time.Time
}
type Repository interface {
	Rules
	Apply(context.Context, Update) (Rule, error)
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

func (s Service) Get(ctx context.Context, school, action string) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if !ValidSchool(school) {
		return Rule{}, ErrRule
	}
	supported, err := SupportedConditions(action)
	if err != nil {
		return Rule{}, ErrRule
	}
	rule, err := s.Repo.Effective(ctx, action, school)
	if err != nil {
		return Rule{}, err
	}
	if rule.Conditions == nil {
		rule.Conditions = []Condition{}
	}
	rule.SupportedConditions = supported
	return rule, nil
}
func (s Service) Set(ctx context.Context, actor, key, school, action string, in Change) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if !validActor(actor) || !validIdempotencyKey(key) || !ValidSchool(school) || in.Conditions == nil {
		return Rule{}, ErrRule
	}
	supported, err := SupportedConditions(action)
	if err != nil {
		return Rule{}, ErrRule
	}
	if ValidateAction(action, Rule{Version: in.ExpectedVersion, Conditions: in.Conditions}) != nil {
		return Rule{}, ErrRule
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rule, err := s.Repo.Apply(call, Update{actor, key, school, action, in, now})
	if err != nil {
		return Rule{}, err
	}
	if rule.Conditions == nil {
		rule.Conditions = []Condition{}
	}
	rule.SupportedConditions = supported
	return rule, nil
}
