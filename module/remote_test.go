package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteCompatibilityAndOutage(t *testing.T) {
	for _, tt := range []struct {
		name, id    string
		api, status int
		caps        []string
		ok          bool
	}{
		{"compatible", "verification", 1, 200, []string{"verification.status", "private.extra"}, true},
		{"wrong identity", "billing", 1, 200, []string{"verification.status"}, false},
		{"wrong api", "verification", 2, 200, []string{"verification.status"}, false},
		{"missing capability", "verification", 1, 200, []string{}, false},
		{"unauthorized", "verification", 1, 401, []string{"verification.status"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/module" {
					t.Errorf("unexpected request path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(Descriptor{ID: tt.id, Version: "synthetic", API: tt.api, Capabilities: tt.caps})
			}))
			defer server.Close()
			remote := Remote{Expected: Descriptor{ID: "verification", Version: "development", API: 1, Capabilities: []string{"verification.status"}}, URL: server.URL, Timeout: time.Second, ProbeInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
			d, err := remote.Definition()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Stop(context.Background()) })
			err = d.Start(context.Background())
			if (err == nil) != tt.ok {
				t.Fatalf("ok=%v err=%v", tt.ok, err)
			}
			if len(d.Descriptor.Capabilities) != 1 {
				t.Fatal("remote expanded locally approved capabilities")
			}
			server.Close()
			eventually(t, func() bool { return d.Ready(context.Background()) != nil })
		})
	}
}

func TestRemoteRejectsUnsafeOriginAndOversizePayload(t *testing.T) {
	for _, origin := range []string{"http://example.org", "https://user:password@example.org", "https://example.org?token=x", "https://example.org/path", "file:///tmp/module"} {
		if _, err := (Remote{URL: origin, Timeout: time.Second}).Definition(); err == nil {
			t.Errorf("accepted unsafe origin %s", origin)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 65537))) }))
	defer server.Close()
	d, err := (Remote{URL: server.URL, Timeout: time.Second, ProbeInterval: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}).Definition()
	if err != nil {
		t.Fatal(err)
	}
	if d.Start(context.Background()) == nil {
		t.Fatal("oversize response accepted")
	}
}

func TestRemoteDoesNotFollowRedirect(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	d, err := (Remote{URL: source.URL, Timeout: time.Second}).Definition()
	if err != nil {
		t.Fatal(err)
	}
	if d.Start(context.Background()) == nil || hit {
		t.Fatal("redirect followed")
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("health monitor did not converge")
}

func TestRemoteDegradedStartupRecoveryAndBoundedProbes(t *testing.T) {
	var healthy atomic.Bool
	var calls atomic.Int64
	desc := Descriptor{ID: "verification", Version: "test", API: 1, Capabilities: []string{"verification.read"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(desc)
	}))
	defer server.Close()
	d, err := (Remote{Expected: desc, URL: server.URL, Timeout: time.Second, ProbeInterval: 20 * time.Millisecond, MaxBackoff: 40 * time.Millisecond}).Definition()
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Start(context.Background()); err != nil {
		t.Fatal("transient outage blocked startup", err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	if !errors.Is(d.Ready(context.Background()), ErrRemoteUnavailable) {
		t.Fatal("outage admitted traffic")
	}
	before := calls.Load()
	for range 1000 {
		_ = d.Ready(context.Background())
	}
	if calls.Load()-before > 1 {
		t.Fatal("requests amplified remote probes")
	}
	healthy.Store(true)
	eventually(t, func() bool { return d.Ready(context.Background()) == nil })
	healthy.Store(false)
	eventually(t, func() bool { return d.Ready(context.Background()) != nil })
	if err = d.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.Ready(context.Background()) == nil {
		t.Fatal("stopped monitor remains ready")
	}
}

func TestRemoteCredentialRotationAndMissingConfiguration(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "service-token")
	remote := Remote{Expected: Descriptor{ID: "verification", Version: "test", API: 1}, TokenFile: tokenFile, Timeout: time.Second, ProbeInterval: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	var expected atomic.Value
	expected.Store("first")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(remote.Expected)
	}))
	defer server.Close()
	remote.URL = server.URL
	d, err := remote.Definition()
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(d.Start(context.Background()), ErrRemoteConfiguration) {
		t.Fatal("missing credentials accepted")
	}
	if err = os.WriteFile(tokenFile, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	expected.Store("second")
	eventually(t, func() bool { return errors.Is(d.Ready(context.Background()), ErrRemoteConfiguration) })
	if err = os.WriteFile(tokenFile, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.Ready(context.Background()) == nil })
}

func TestRemoteSlowStartupBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	d, err := (Remote{Expected: Descriptor{ID: "slow", Version: "test", API: 1}, URL: server.URL, Timeout: 5 * time.Second}).Definition()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop(context.Background()) })
	if time.Since(started) > time.Second {
		t.Fatal("slow remote blocked process startup")
	}
	if d.Ready(context.Background()) == nil {
		t.Fatal("unverified remote admitted traffic")
	}
}
