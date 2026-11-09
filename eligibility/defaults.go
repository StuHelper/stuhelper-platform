package eligibility

import "context"

type Rules interface {
	Effective(context.Context, string, string) (Rule, error)
}
type Defaults struct{}

func (Defaults) Effective(_ context.Context, action, _ string) (Rule, error) {
	rule := Rule{Version: "initial-20261108", Conditions: []Condition{}}
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
	return rule, nil
}
