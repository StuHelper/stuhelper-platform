package eligibility_test

import (
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"testing"
)

func TestConfiguredFactsFailClosedAndDisabledConditionsAreNotRequired(t *testing.T) {
	rule := e.Rule{Version: "2", Conditions: []e.Condition{e.Campus, e.Phone}}
	r, err := e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Satisfied})
	if err != nil || r.Allowed || len(r.Unavailable) != 1 || r.Unavailable[0] != e.Phone {
		t.Fatal(r, err)
	}
	r, err = e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Satisfied, e.Phone: e.Unsatisfied})
	if err != nil || r.Allowed || len(r.Missing) != 1 {
		t.Fatal(r, err)
	}
	rule.Conditions = []e.Condition{e.Campus}
	r, err = e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Satisfied})
	if err != nil || !r.Allowed {
		t.Fatal(r, err)
	}
	rule.Conditions = []e.Condition{e.Campus, e.Campus}
	if _, err = e.Evaluate(rule, nil); err == nil {
		t.Fatal("ambiguous duplicate rule accepted")
	}
}
