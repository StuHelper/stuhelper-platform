package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			remote := Remote{Expected: Descriptor{ID: "verification", Version: "development", API: 1, Capabilities: []string{"verification.status"}}, URL: server.URL, Timeout: time.Second}
			d, err := remote.Definition()
			if err != nil {
				t.Fatal(err)
			}
			err = d.Start(context.Background())
			if (err == nil) != tt.ok {
				t.Fatalf("ok=%v err=%v", tt.ok, err)
			}
			if len(d.Descriptor.Capabilities) != 1 {
				t.Fatal("remote expanded locally approved capabilities")
			}
			server.Close()
			if d.Ready(context.Background()) == nil {
				t.Fatal("outage ignored")
			}
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
	d, err := (Remote{URL: server.URL, Timeout: time.Second}).Definition()
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
