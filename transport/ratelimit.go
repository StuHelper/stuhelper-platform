package transport

import (
	"sync"
	"time"
)

// SourceLimiter bounds anonymous reads per source within a fixed window.
// A full table rejects new sources in O(1) until the earliest entry expires.
type SourceLimiter struct {
	mu             sync.Mutex
	entries        map[string]sourceWindow
	earliestExpiry time.Time
	Now            func() time.Time
	Limit          int
	Window         time.Duration
	MaxEntries     int
}
type sourceWindow struct {
	start time.Time
	count int
}

func (l *SourceLimiter) Allow(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	limit := l.Limit
	if limit <= 0 {
		limit = 60
	}
	window := l.Window
	if window <= 0 {
		window = time.Minute
	}
	maxEntries := l.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	if l.entries == nil {
		l.entries = map[string]sourceWindow{}
	}
	if len(l.entries) >= maxEntries && (l.earliestExpiry.IsZero() || !now.Before(l.earliestExpiry)) {
		var nextEarliest time.Time
		for key, v := range l.entries {
			exp := v.start.Add(window)
			if !now.Before(exp) {
				delete(l.entries, key)
				continue
			}
			if nextEarliest.IsZero() || exp.Before(nextEarliest) {
				nextEarliest = exp
			}
		}
		l.earliestExpiry = nextEarliest
	}
	v, exists := l.entries[source]
	if !exists && len(l.entries) >= maxEntries {
		return false
	}
	if v.start.IsZero() || !now.Before(v.start.Add(window)) {
		v = sourceWindow{start: now}
	}
	if v.count >= limit {
		return false
	}
	v.count++
	l.entries[source] = v
	exp := v.start.Add(window)
	if l.earliestExpiry.IsZero() || exp.Before(l.earliestExpiry) {
		l.earliestExpiry = exp
	}
	return true
}
