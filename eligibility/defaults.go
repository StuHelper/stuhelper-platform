package eligibility

import (
	"context"
	"slices"
)

type Rules interface {
	Effective(context.Context, string, string) (Rule, error)
}

type Defaults struct{}

var allActions = []string{
	"content.enter",
	"read_full",
	"vote",
	"bookmark",
	"report",
	"resources.discover",
	"resources.download.public",
	"resources.download.restricted",
	"resources.purchase",
	"membership.purchase",
	"publish_course",
	"publish_mentor",
	"reply",
	"resources.upload",
	"reply_teacher",
}

func Actions() []string {
	return slices.Clone(allActions)
}

func SupportedConditions(action string) ([]Condition, error) {
	switch action {
	case "publish_course":
		return []Condition{Content, Campus, Phone, Identity, Enrollment}, nil
	case "content.enter", "read_full", "vote", "bookmark", "report", "resources.discover", "resources.download.public", "resources.download.restricted", "resources.purchase", "membership.purchase", "publish_mentor", "reply", "resources.upload", "reply_teacher":
		return []Condition{Content, Campus, Phone, Identity}, nil
	default:
		return nil, ErrRule
	}
}

func (Defaults) Effective(_ context.Context, action, _ string) (Rule, error) {
	supported, err := SupportedConditions(action)
	if err != nil {
		return Rule{}, ErrRule
	}
	rule := Rule{
		Version:             "initial-20261108",
		Conditions:          []Condition{},
		Source:              SourceInitialDefault,
		SupportedConditions: supported,
	}
	switch action {
	case "content.enter", "read_full", "vote", "bookmark", "report", "resources.discover", "resources.download.public", "resources.download.restricted":
		rule.Conditions = []Condition{Content}
	case "resources.purchase", "membership.purchase":
		rule.Conditions = []Condition{Campus}
	case "publish_course":
		rule.Conditions = []Condition{Campus, Phone, Enrollment}
	case "publish_mentor", "reply", "resources.upload":
		rule.Conditions = []Condition{Campus, Phone}
	case "reply_teacher":
		rule.Conditions = []Condition{Phone}
	default:
		return Rule{}, ErrRule
	}
	rule.GlobalConditions = slices.Clone(rule.Conditions)
	return rule, nil
}
