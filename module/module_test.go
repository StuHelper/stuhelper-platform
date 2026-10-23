package module

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
)

func definition(id string, deps ...Dependency) Definition {
	return Definition{Descriptor: Descriptor{ID: id, Version: "development", API: 1, Requires: deps, Capabilities: []string{id + ".read"}}}
}

func lifecycle(t *testing.T, r *Registry) *fx.App {
	t.Helper()
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) { r.RegisterLifecycle(lc) }))
	if app.Err() != nil {
		t.Fatal(app.Err())
	}
	return app
}

func TestCompositionRejectsInvalidDependencies(t *testing.T) {
	tests := []struct {
		name        string
		definitions []Definition
		enabled     []string
	}{
		{"missing", []Definition{definition("reviews", Dependency{ID: "catalog", API: 1})}, []string{"reviews"}},
		{"disabled dependency", []Definition{definition("catalog"), definition("reviews", Dependency{ID: "catalog", API: 1})}, []string{"reviews"}},
		{"incompatible", []Definition{definition("catalog"), definition("reviews", Dependency{ID: "catalog", API: 2})}, []string{"catalog", "reviews"}},
		{"cycle", []Definition{definition("a", Dependency{ID: "b", API: 1}), definition("b", Dependency{ID: "a", API: 1})}, []string{"a", "b"}},
		{"duplicate", []Definition{definition("a"), definition("a")}, []string{"a"}},
		{"unknown", []Definition{definition("a")}, []string{"missing"}},
		{"duplicate selected", []Definition{definition("a")}, []string{"a", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.definitions, tt.enabled); err == nil {
				t.Fatal("invalid composition accepted")
			}
		})
	}
}

func TestLifecycleStartupRollbackAndReverseShutdown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "rollback"}[fail], func(t *testing.T) {
			var events []string
			a, b := definition("a"), definition("b", Dependency{ID: "a", API: 1})
			a.Start = func(context.Context) error { events = append(events, "start-a"); return nil }
			a.Stop = func(context.Context) error { events = append(events, "stop-a"); return nil }
			b.Start = func(context.Context) error {
				events = append(events, "start-b")
				if fail {
					return errors.New("start rejected")
				}
				return nil
			}
			b.Stop = func(context.Context) error { events = append(events, "stop-b"); return nil }
			r, err := New([]Definition{b, a}, []string{"b", "a"})
			if err != nil {
				t.Fatal(err)
			}
			app := lifecycle(t, r)
			err = app.Start(context.Background())
			if fail {
				if err == nil {
					t.Fatal("failure swallowed")
				}
				if !reflect.DeepEqual(events, []string{"start-a", "start-b", "stop-a"}) {
					t.Fatal(events)
				}
				if r.Healthy(context.Background()) {
					t.Fatal("failed application reported ready")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !r.Healthy(context.Background()) {
					t.Fatal("running application not ready")
				}
				if err = app.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(events, []string{"start-a", "start-b", "stop-b", "stop-a"}) {
					t.Fatal(events)
				}
			}
		})
	}
}

func TestOptionalModuleAbsentAndRuntimeDependencyFailure(t *testing.T) {
	a, b := definition("catalog"), definition("reviews", Dependency{ID: "catalog", API: 1})
	var mu sync.RWMutex
	available := true
	a.Ready = func(context.Context) error {
		mu.RLock()
		defer mu.RUnlock()
		if !available {
			return io.EOF
		}
		return nil
	}
	r, err := New([]Definition{a, b, definition("community")}, []string{"catalog", "reviews"})
	if err != nil {
		t.Fatal(err)
	}
	app := lifecycle(t, r)
	if err = app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	if got := r.Capabilities(context.Background()); !reflect.DeepEqual(got, []string{"catalog.read", "reviews.read"}) {
		t.Fatal(got)
	}
	mu.Lock()
	available = false
	mu.Unlock()
	if r.Available(context.Background(), "reviews") {
		t.Fatal("required dependency outage did not block dependent")
	}
	if len(r.Capabilities(context.Background())) != 0 {
		t.Fatal("unavailable capabilities leaked")
	}
	mu.Lock()
	available = true
	mu.Unlock()
	if !r.Available(context.Background(), "reviews") {
		t.Fatal("dependency recovery not observed")
	}
}

func TestConcurrentStatusAndShutdown(t *testing.T) {
	r, err := New([]Definition{definition("a")}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	app := lifecycle(t, r)
	if err = app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				r.Statuses()
				r.Capabilities(context.Background())
			}
		})
	}
	if err = app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if r.Available(context.Background(), "a") {
		t.Fatal("stopped module available")
	}
}

func TestStartRespectsCancellation(t *testing.T) {
	d := definition("slow")
	d.Start = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	r, err := New([]Definition{d}, []string{"slow"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if lifecycle(t, r).Start(ctx) == nil {
		t.Fatal("timeout ignored")
	}
}
