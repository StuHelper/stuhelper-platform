package application

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/internal/api/gen"
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
	client := http.Client{Timeout: time.Second}
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
}
