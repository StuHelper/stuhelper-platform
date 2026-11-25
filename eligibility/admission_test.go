package eligibility_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
)

type staticFacts struct {
	facts map[e.Condition]e.Fact
	err   error
}

func (f staticFacts) Facts(_ context.Context, _, _ string, conditions []e.Condition) (map[e.Condition]e.Fact, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[e.Condition]e.Fact{}
	for _, c := range conditions {
		if v, ok := f.facts[c]; ok {
			out[c] = v
		}
	}
	return out, nil
}

type staticRule struct {
	rule e.Rule
	err  error
}

func (r staticRule) Effective(context.Context, string, string) (e.Rule, error) {
	return r.rule, r.err
}

func TestAdmissionFailsClosedWhenRulesUnwiredAndPreservesServedSchoolScope(t *testing.T) {
	ctx := context.Background()
	satisfied := staticFacts{facts: map[e.Condition]e.Fact{
		e.Content:  e.Satisfied,
		e.Campus:   e.Satisfied,
		e.Phone:    e.Satisfied,
		e.Identity: e.Satisfied,
	}}

	// A-02: nil Rules must fail closed with ErrUnavailable rather than falling back to Defaults.
	if _, err := (e.Admission{School: "buaa", Facts: satisfied}).Check(ctx, "user-1", "buaa", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when Rules is nil, got %v", err)
	}
	if _, err := (e.Admission{School: "   ", Rules: e.Defaults{}, Facts: satisfied}).Check(ctx, "user-1", "buaa", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for blank School, got %v", err)
	}
	if _, err := (e.Admission{School: "*", Rules: e.Defaults{}, Facts: satisfied}).Check(ctx, "user-1", "*", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for wildcard served School, got %v", err)
	}
	if _, err := (e.Admission{School: "buaa", Rules: e.Defaults{}}).Check(ctx, "user-1", "buaa", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when Facts is nil, got %v", err)
	}

	adm := e.Admission{School: "buaa", Rules: e.Defaults{}, Facts: satisfied}

	// Blank or whitespace subject must be rejected with ErrForbidden.
	for _, badSubject := range []string{"", "   ", " user-1", "user-1\x00", "user-1\xff"} {
		if _, err := adm.Check(ctx, badSubject, "buaa", "content.enter"); !errors.Is(err, e.ErrForbidden) {
			t.Fatalf("subject %q: expected ErrForbidden, got %v", badSubject, err)
		}
	}

	// Empty target school resolves against server-owned Admission.School (used by catalog/favorites).
	res, err := adm.Check(ctx, "user-1", "", "content.enter")
	if err != nil || !res.Allowed {
		t.Fatalf("expected empty school to evaluate against Admission.School, got %+v, %v", res, err)
	}

	// Explicit foreign or whitespace school must be rejected with ErrForbidden.
	for _, otherSchool := range []string{"other", " ", "buaa "} {
		if _, err := adm.Check(ctx, "user-1", otherSchool, "content.enter"); !errors.Is(err, e.ErrForbidden) {
			t.Fatalf("school %q: expected ErrForbidden, got %v", otherSchool, err)
		}
	}

	// Unsupported condition on non-enrollment action must fail closed with ErrUnavailable.
	badRule := staticRule{rule: e.Rule{Version: "2", Conditions: []e.Condition{e.Condition("course_enrollment")}}}
	if _, err := (e.Admission{School: "buaa", Rules: badRule, Facts: satisfied}).Check(ctx, "user-1", "buaa", "content.enter"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for unsupported condition on content.enter, got %v", err)
	}

	// Missing condition returns ErrForbidden with Missing populated.
	missingPhone := staticFacts{facts: map[e.Condition]e.Fact{e.Campus: e.Satisfied, e.Phone: e.Unsatisfied}}
	res, err = (e.Admission{School: "buaa", Rules: e.Defaults{}, Facts: missingPhone}).Check(ctx, "user-1", "buaa", "resources.upload")
	if !errors.Is(err, e.ErrForbidden) || res.Allowed || !slices.Equal(res.Missing, []e.Condition{e.Phone}) {
		t.Fatalf("expected ErrForbidden with missing phone, got %+v, %v", res, err)
	}

	// Unknown fact fails closed with ErrUnavailable.
	unknownFact := staticFacts{facts: map[e.Condition]e.Fact{e.Campus: e.Satisfied}}
	res, err = (e.Admission{School: "buaa", Rules: e.Defaults{}, Facts: unknownFact}).Check(ctx, "user-1", "buaa", "resources.upload")
	if !errors.Is(err, e.ErrUnavailable) || !slices.Equal(res.Unavailable, []e.Condition{e.Phone}) {
		t.Fatalf("expected ErrUnavailable with unavailable phone, got %+v, %v", res, err)
	}
}

type mapFacts struct {
	facts map[e.Condition]e.Fact
	err   error
}

func (m mapFacts) Facts(_ context.Context, _, _ string, conditions []e.Condition) (map[e.Condition]e.Fact, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := map[e.Condition]e.Fact{}
	for _, c := range conditions {
		if v, ok := m.facts[c]; ok {
			out[c] = v
		}
	}
	return out, nil
}

func TestAdmissionCheckBoundariesAndMissingFacts(t *testing.T) {
	ctx := context.Background()

	// Unconfigured School or Facts fails closed with ErrUnavailable.
	if _, err := (e.Admission{Facts: mapFacts{}}).Check(ctx, "user-1", "buaa", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for empty School, got %v", err)
	}
	if _, err := (e.Admission{School: "buaa"}).Check(ctx, "user-1", "buaa", "resources.upload"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for nil Facts, got %v", err)
	}

	adm := e.Admission{
		School: "buaa",
		Rules:  e.Defaults{},
		Facts: mapFacts{facts: map[e.Condition]e.Fact{
			e.Campus: e.Satisfied,
			e.Phone:  e.Unsatisfied,
		}},
	}

	// Empty subject or mismatched school returns ErrForbidden.
	if _, err := adm.Check(ctx, "", "buaa", "resources.upload"); !errors.Is(err, e.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for empty subject, got %v", err)
	}
	if _, err := adm.Check(ctx, "user-1", "other", "resources.upload"); !errors.Is(err, e.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for cross-school check, got %v", err)
	}

	// Unknown action returns ErrUnavailable.
	if _, err := adm.Check(ctx, "user-1", "buaa", "unknown.action"); !errors.Is(err, e.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for unknown action, got %v", err)
	}

	// Unsatisfied condition returns ErrForbidden along with exact Missing slice.
	res, err := adm.Check(ctx, "user-1", "buaa", "resources.upload")
	if !errors.Is(err, e.ErrForbidden) || res.Allowed || !slices.Equal(res.Missing, []e.Condition{e.Phone}) {
		t.Fatalf("unexpected denied result: %+v, err=%v", res, err)
	}

	// Satisfied action (resources.purchase only requires Campus) succeeds.
	res, err = adm.Check(ctx, "user-1", "buaa", "resources.purchase")
	if err != nil || !res.Allowed || res.Version == "" {
		t.Fatalf("unexpected allowed result: %+v, err=%v", res, err)
	}

	// Unknown fact (Content omitted from mapFacts) fails closed with ErrUnavailable.
	res, err = adm.Check(ctx, "user-1", "buaa", "content.enter")
	if !errors.Is(err, e.ErrUnavailable) || !slices.Equal(res.Unavailable, []e.Condition{e.Content}) {
		t.Fatalf("expected ErrUnavailable for unknown fact, got %+v, err=%v", res, err)
	}
}
