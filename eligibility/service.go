package eligibility

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
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

func (s Service) Get(ctx context.Context, school, action string) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	if _, err := (Defaults{}).Effective(ctx, action, school); err != nil {
		return Rule{}, ErrRule
	}
	return s.Repo.Effective(ctx, action, school)
}
func (s Service) Set(ctx context.Context, actor, key, school, action string, in Change) (Rule, error) {
	if s.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	for _, v := range []string{actor, key, school, action, in.ExpectedVersion} {
		if v == "" || len(v) > 2048 || strings.ContainsFunc(v, unicode.IsControl) {
			return Rule{}, ErrRule
		}
	}
	if in.Conditions == nil || len(key) > 128 || len(school) > 128 || Validate(Rule{Version: in.ExpectedVersion, Conditions: in.Conditions}) != nil {
		return Rule{}, ErrRule
	}
	if _, err := (Defaults{}).Effective(ctx, action, school); err != nil {
		return Rule{}, ErrRule
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.Repo.Apply(call, Update{actor, key, school, action, in, now})
}
