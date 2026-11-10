package eligibility

import (
	"context"
	"sync"
	"time"
)

// CachedRules keeps effective rules for at most five seconds. All local writes
// invalidate the cache, including writes whose commit result is unknown.
type CachedRules struct {
	Repo    Repository
	Now     func() time.Time
	mu      sync.Mutex
	epoch   uint64
	entries map[[2]string]cachedRule
}
type cachedRule struct {
	rule  Rule
	until time.Time
}

func (c *CachedRules) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func copyRule(r Rule) Rule { r.Conditions = append([]Condition{}, r.Conditions...); return r }
func (c *CachedRules) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	c.entries = nil
}
func (c *CachedRules) Effective(ctx context.Context, action, school string) (Rule, error) {
	if err := ctx.Err(); err != nil {
		return Rule{}, err
	}
	if c.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	key := [2]string{action, school}
	now := c.now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	epoch := c.epoch
	c.mu.Unlock()
	if ok && now.Before(entry.until) {
		return copyRule(entry.rule), nil
	}
	r, err := c.Repo.Effective(ctx, action, school)
	if err != nil {
		return Rule{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == epoch {
		if len(c.entries) >= 4096 {
			c.entries = nil
		}
		if c.entries == nil {
			c.entries = map[[2]string]cachedRule{}
		}
		c.entries[key] = cachedRule{copyRule(r), now.Add(5 * time.Second)}
	}
	return r, nil
}
func (c *CachedRules) Apply(ctx context.Context, in Update) (Rule, error) {
	defer c.Invalidate()
	if c.Repo == nil {
		return Rule{}, ErrUnavailable
	}
	return c.Repo.Apply(ctx, in)
}
