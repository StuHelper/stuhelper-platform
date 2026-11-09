package eligibility_test

import (
	"context"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"reflect"
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

func TestContentAdmissionFactIsIndependentFromCampusQualification(t *testing.T) {
	rule := e.Rule{Version: "content-v1", Conditions: []e.Condition{e.Content}}
	result, err := e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Unsatisfied, e.Content: e.Satisfied})
	if err != nil || !result.Allowed {
		t.Fatal("teacher content fact incorrectly required student qualification", result, err)
	}
	result, err = e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Satisfied, e.Content: e.Unsatisfied})
	if err != nil || result.Allowed || len(result.Missing) != 1 {
		t.Fatal("out-of-scope campus fact admitted content", result, err)
	}
	result, err = e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: e.Satisfied})
	if err != nil || result.Allowed || len(result.Unavailable) != 1 {
		t.Fatal("unknown content fact became success", result, err)
	}
}

func TestPurchaseDefaultsRequireCampusQualification(t *testing.T) {
	for _, action := range []string{"resources.purchase", "membership.purchase"} {
		rule, err := (e.Defaults{}).Effective(context.Background(), action, "school")
		if err != nil || !reflect.DeepEqual(rule.Conditions, []e.Condition{e.Campus}) {
			t.Fatal(action, rule, err)
		}
		for _, fact := range []e.Fact{e.Unknown, e.Unsatisfied, e.Satisfied} {
			result, err := e.Evaluate(rule, map[e.Condition]e.Fact{e.Campus: fact, e.Content: e.Satisfied})
			if err != nil || result.Allowed != (fact == e.Satisfied) {
				t.Fatal(action, result, err)
			}
		}
	}
}
