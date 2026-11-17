package eligibility_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
)

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
