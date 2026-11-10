package transport_test

import (
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
