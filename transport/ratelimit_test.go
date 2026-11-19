package transport_test

import (
	"fmt"
	"github.com/StuHelper/stuhelper-platform/transport"
	"testing"
	"time"
)

func TestSourceLimitRejectsExcessAndRecoversWithoutSharingQuota(t *testing.T) {
	now := time.Unix(1000, 0)
	l := transport.SourceLimiter{Now: func() time.Time { return now }}
	for range 60 {
		if !l.Allow("one") {
			t.Fatal("premature denial")
		}
	}
	if l.Allow("one") {
		t.Fatal("source limit bypassed")
	}
	if !l.Allow("two") {
		t.Fatal("unrelated source blocked")
	}
	now = now.Add(time.Minute)
	if !l.Allow("one") {
		t.Fatal("expired source not reclaimed")
	}
}

func TestSourceLimiterFullTableSkipsUnexpiredRescanAndReclaimsAtEarliestExpiry(t *testing.T) {
	now := time.Unix(2000, 0)
	l := transport.SourceLimiter{Now: func() time.Time { return now }}
	// First entry expires 30s earlier than the remaining 4095 entries.
	if !l.Allow("early") {
		t.Fatal("early source rejected")
	}
	now = now.Add(30 * time.Second)
	for i := 1; i < 4096; i++ {
		if !l.Allow(fmt.Sprintf("src-%d", i)) {
			t.Fatalf("source %d rejected before capacity", i)
		}
	}
	// Before "early" expires (t+59s), 10,000 new sources are rejected in O(1) without rescanning.
	now = time.Unix(2000, 0).Add(59 * time.Second)
	start := time.Now()
	for i := range 10000 {
		if l.Allow(fmt.Sprintf("flood-%d", i)) {
			t.Fatalf("overflow source %d admitted while table full", i)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("full-table rejection took %v (expected O(1) fast path)", elapsed)
	}
	// Existing tracked source still uses its remaining quota.
	if !l.Allow("src-1") {
		t.Fatal("existing source rejected")
	}
	// Exactly when "early" expires (t+60s), one slot is reclaimed for a new source.
	now = time.Unix(2000, 0).Add(time.Minute)
	if !l.Allow("after-early-expiry") {
		t.Fatal("expired slot was not reclaimed at earliestExpiry")
	}
}
