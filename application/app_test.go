package application

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/internal/api/gen"
	"github.com/StuHelper/stuhelper-platform/module"
	"github.com/StuHelper/stuhelper-platform/transport"
)

func TestListenerFailureStopsApplication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	app, err := New(Config{Address: listener.Addr().String(), Enabled: []string{"platform"}}, Modules{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if app.Start(ctx) == nil {
		t.Fatal("busy listener accepted")
	}
}

func TestPublicFrameworkAndUnavailableIdentity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	app, err := New(Config{Address: address, Enabled: []string{"platform"}}, Modules{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	transport := &http.Transport{}
	client := http.Client{Timeout: time.Second, Transport: transport}
	// Close test-owned keep-alive/speculative connections before stopping the server.
	t.Cleanup(client.CloseIdleConnections)
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/system/capabilities"} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Errorf("%s: %d", path, response.StatusCode)
		}
		if path == "/api/v1/system/capabilities" {
			var body gen.Capabilities
			if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !body.ApiVersion.Valid() || len(body.Capabilities) != 2 {
				t.Fatal(body)
			}
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	response, err := client.Get("http://" + address + "/api/v1/admin/modules")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var problem gen.Problem
	if err = json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 503 || problem.Code != "identity_unavailable" || problem.RequestId == "" {
		t.Fatal(response.StatusCode, problem)
	}
	_, _ = io.Copy(io.Discard, response.Body)
}

func TestSlowHTTPHandlerDoesNotSkipModuleStopHooksAndDeduplicatesCapabilityProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	var readyCalls atomic.Int32
	var moduleStopped atomic.Bool
	enteredHandler := make(chan struct{})
	releaseHandler := make(chan struct{})
	defer close(releaseHandler)

	worker := module.Definition{
		Descriptor: module.Descriptor{
			ID:           "worker",
			Version:      "development",
			API:          1,
			Requires:     []module.Dependency{{ID: "platform", API: 1}},
			Capabilities: []string{"worker.slow"},
		},
		Stop: func(context.Context) error {
			moduleStopped.Store(true)
			return nil
		},
	}
	slowRoute := transport.Route{
		Method:     "GET",
		Path:       "/api/v1/worker/slow",
		Module:     "worker",
		Capability: "worker.slow",
		Access:     transport.Public,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(enteredHandler)
			<-releaseHandler
		}),
	}
	app, err := New(Config{Address: address, Enabled: []string{"platform", "worker"}}, Modules{
		PlatformReady: func(context.Context) error {
			readyCalls.Add(1)
			return nil
		},
		Definitions: []module.Definition{worker},
		Routes:      []transport.Route{slowRoute},
	})
	if err != nil {
		t.Fatal(err)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer startCancel()
	if err = app.Start(startCtx); err != nil {
		t.Fatal(err)
	}

	// A-06: single GET /api/v1/system/capabilities should evaluate PlatformReady only once.
	readyCalls.Store(0)
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	client := http.Client{Timeout: 2 * time.Second, Transport: tr}
	resp, err := client.Get("http://" + address + "/api/v1/system/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := readyCalls.Load(); got != 1 {
		t.Fatalf("expected 1 PlatformReady probe per capabilities request, got %d", got)
	}

	// A-04: start a slow active HTTP request and verify app.Stop still runs worker.Stop.
	go func() {
		_, _ = client.Get("http://" + address + "/api/v1/worker/slow")
	}()
	<-enteredHandler
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stopCancel()
	_ = app.Stop(stopCtx)
	if !moduleStopped.Load() {
		t.Fatalf("slow HTTP drain exhausted Fx stop context and skipped module Stop hook")
	}
}
