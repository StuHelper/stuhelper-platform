package transport

import (
	"sync"
	"time"
)

// SourceLimiter bounds anonymous reads per source within a fixed window.
// A full table rejects new sources until expired entries can be reclaimed.
type SourceLimiter struct {
	mu      sync.Mutex
	entries map[string]sourceWindow
	Now     func() time.Time
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
	if l.entries == nil {
		l.entries = map[string]sourceWindow{}
	}
	if len(l.entries) >= 4096 {
		for key, v := range l.entries {
			if !now.Before(v.start.Add(time.Minute)) {
				delete(l.entries, key)
			}
		}
	}
	v, exists := l.entries[source]
	if !exists && len(l.entries) >= 4096 {
		return false
	}
	if v.start.IsZero() || !now.Before(v.start.Add(time.Minute)) {
		v = sourceWindow{start: now}
	}
	if v.count >= 60 {
		return false
	}
	v.count++
	l.entries[source] = v
	return true
}
