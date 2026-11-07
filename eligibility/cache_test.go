package eligibility

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cacheRepo struct {
	rule             Rule
	calls            int
	failure          bool
	entered, release chan struct{}
}

func (r *cacheRepo) Effective(ctx context.Context, _, _ string) (Rule, error) {
	r.calls++
	if r.failure {
		return Rule{}, ErrUnavailable
	}
	rule := copyRule(r.rule)
	if r.entered != nil {
		close(r.entered)
		<-r.release
	}
	return rule, nil
}
func (r *cacheRepo) Apply(_ context.Context, in Update) (Rule, error) {
	if r.failure {
		return Rule{}, ErrUnavailable
	}
	r.rule = Rule{Version: "2", Conditions: in.Change.Conditions}
	return copyRule(r.rule), nil
}
func TestRuleCacheExpiryInvalidationAndFailure(t *testing.T) {
	now := time.Now()
	repo := &cacheRepo{rule: Rule{Version: "1", Conditions: []Condition{Content}}}
	c := &CachedRules{Repo: repo, Now: func() time.Time { return now }}
	ctx := context.Background()
	a, _ := c.Effective(ctx, "content.enter", "school")
	a.Conditions[0] = Phone
	b, _ := c.Effective(ctx, "content.enter", "school")
	if repo.calls != 1 || b.Conditions[0] != Content {
		t.Fatal("cache mutated", b)
	}
	_, err := c.Apply(ctx, Update{Change: Change{Conditions: []Condition{Phone}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = c.Effective(ctx, "content.enter", "school")
	if b.Version != "2" || repo.calls != 2 {
		t.Fatal("write did not invalidate", b)
	}
	repo.failure = true
	now = now.Add(5 * time.Second)
	if _, err = c.Effective(ctx, "content.enter", "school"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stale rule survived failure", err)
	}
	repo.failure = false
	_, _ = c.Effective(ctx, "content.enter", "school")
	repo.failure = true
	_, _ = c.Apply(ctx, Update{})
	if _, err = c.Effective(ctx, "content.enter", "school"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown commit kept cached rule", err)
	}
}
func TestRuleCacheDoesNotReinsertInvalidatedInFlightRead(t *testing.T) {
	repo := &cacheRepo{rule: Rule{Version: "1", Conditions: []Condition{Content}}, entered: make(chan struct{}), release: make(chan struct{})}
	c := &CachedRules{Repo: repo}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = c.Effective(context.Background(), "content.enter", "school") }()
	<-repo.entered
	c.Invalidate()
	close(repo.release)
	<-done
	repo.entered = nil
	repo.rule.Version = "2"
	v, err := c.Effective(context.Background(), "content.enter", "school")
	if err != nil || v.Version != "2" {
		t.Fatal(v, err)
	}
}
