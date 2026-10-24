// Package module defines the public boundary for compiled modules and remote services.
package module

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"

	"go.uber.org/fx"
)

type Dependency struct {
	ID  string `json:"id"`
	API int    `json:"api"`
}

// Descriptor contains public operational metadata only, never provider settings.
type Descriptor struct {
	ID                 string                  `json:"id"`
	Version            string                  `json:"version"`
	API                int                     `json:"api"`
	Requires           []Dependency            `json:"requires"`
	Optional           []Dependency            `json:"optional,omitempty"`
	Capabilities       []string                `json:"capabilities"`
	CapabilityRequires map[string][]Dependency `json:"capability_requires,omitempty"`
}

// Definition's hooks must respect context cancellation. Start must clean up its
// own partial failure; Fx stops previously started modules in reverse order.
type Definition struct {
	Descriptor Descriptor
	Start      func(context.Context) error
	Stop       func(context.Context) error
	Ready      func(context.Context) error
	// Critical makes the module's core availability part of process readiness.
	Critical bool
}

type State string

const (
	Disabled State = "disabled"
	Starting State = "starting"
	Ready    State = "ready"
	Stopping State = "stopping"
	Stopped  State = "stopped"
	Failed   State = "failed"
)

type Status struct {
	ID     string `json:"id"`
	State  State  `json:"state"`
	Health string `json:"health"`
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]Definition
	states      map[string]State
	order       []string
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var capability = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z][a-z0-9.-]*$`)

// New validates the entire composition before hooks can start any work.
func New(definitions []Definition, enabled []string) (*Registry, error) {
	r := &Registry{definitions: map[string]Definition{}, states: map[string]State{}}
	owners := map[string]string{}
	for _, d := range definitions {
		m := d.Descriptor
		if !identifier.MatchString(m.ID) || m.Version == "" || m.API < 1 {
			return nil, fmt.Errorf("invalid module descriptor: %q", m.ID)
		}
		if _, ok := r.definitions[m.ID]; ok {
			return nil, fmt.Errorf("duplicate module: %s", m.ID)
		}
		seen := map[string]bool{}
		for _, dep := range append(slices.Clone(m.Requires), m.Optional...) {
			if !identifier.MatchString(dep.ID) || dep.ID == m.ID || dep.API < 1 || seen[dep.ID] {
				return nil, fmt.Errorf("invalid dependency in %s", m.ID)
			}
			seen[dep.ID] = true
		}
		for _, c := range m.Capabilities {
			if !capability.MatchString(c) {
				return nil, fmt.Errorf("invalid capability in %s", m.ID)
			}
			if owner, ok := owners[c]; ok {
				return nil, fmt.Errorf("capability %s owned by both %s and %s", c, owner, m.ID)
			}
			owners[c] = m.ID
		}
		for name, dependencies := range m.CapabilityRequires {
			if !slices.Contains(m.Capabilities, name) {
				return nil, fmt.Errorf("dependency for undeclared capability %s", name)
			}
			used := map[string]bool{}
			for _, dep := range dependencies {
				declared := append(slices.Clone(m.Requires), m.Optional...)
				if used[dep.ID] || !slices.Contains(declared, dep) {
					return nil, fmt.Errorf("undeclared or duplicate capability dependency in %s", name)
				}
				used[dep.ID] = true
			}
		}
		// Keep caller-owned slices from mutating the validated registry.
		d.Descriptor.Requires = slices.Clone(m.Requires)
		d.Descriptor.Optional = slices.Clone(m.Optional)
		d.Descriptor.Capabilities = slices.Clone(m.Capabilities)
		d.Descriptor.CapabilityRequires = make(map[string][]Dependency, len(m.CapabilityRequires))
		for name, deps := range m.CapabilityRequires {
			d.Descriptor.CapabilityRequires[name] = slices.Clone(deps)
		}
		r.definitions[m.ID], r.states[m.ID] = d, Disabled
	}
	selected := map[string]bool{}
	for _, id := range enabled {
		if _, ok := r.definitions[id]; !ok {
			return nil, fmt.Errorf("module not installed: %s", id)
		}
		if selected[id] {
			return nil, fmt.Errorf("duplicate enabled module: %s", id)
		}
		selected[id] = true
	}
	for id := range selected {
		for _, dep := range r.definitions[id].Descriptor.Optional {
			if installed, ok := r.definitions[dep.ID]; ok && installed.Descriptor.API != dep.API {
				return nil, fmt.Errorf("%s expects optional %s API %d", id, dep.ID, dep.API)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("required dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dep := range r.definitions[id].Descriptor.Requires {
			if !selected[dep.ID] {
				return fmt.Errorf("%s requires enabled module %s", id, dep.ID)
			}
			if r.definitions[dep.ID].Descriptor.API != dep.API {
				return fmt.Errorf("%s requires %s API %d", id, dep.ID, dep.API)
			}
			if err := visit(dep.ID); err != nil {
				return err
			}
		}
		visiting[id], visited[id] = false, true
		r.order = append(r.order, id)
		return nil
	}
	ids := slices.Clone(enabled)
	slices.Sort(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// RegisterLifecycle delegates ordered startup, rollback and shutdown to Fx.
// Enable/disable is configuration-driven and applied by a controlled restart.
func (r *Registry) RegisterLifecycle(lc fx.Lifecycle) {
	for _, id := range r.order {
		d := r.definitions[id]
		lc.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				r.setState(id, Starting)
				if d.Start != nil {
					if err := d.Start(ctx); err != nil {
						r.setState(id, Failed)
						return fmt.Errorf("start %s: %w", id, err)
					}
				}
				r.setState(id, Ready)
				return nil
			},
			OnStop: func(ctx context.Context) error {
				r.setState(id, Stopping)
				if d.Stop != nil {
					if err := d.Stop(ctx); err != nil {
						r.setState(id, Failed)
						return fmt.Errorf("stop %s: %w", id, err)
					}
				}
				r.setState(id, Stopped)
				return nil
			},
		})
	}
}

func (r *Registry) setState(id string, state State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[id] = state
}

func (r *Registry) Available(ctx context.Context, id string) bool {
	return r.available(ctx, id, make(map[string]bool))
}

// Remote Ready hooks read bounded health snapshots. Local hooks must be cheap.
func (r *Registry) available(ctx context.Context, id string, results map[string]bool) (available bool) {
	if ctx.Err() != nil {
		return false
	}
	if result, checked := results[id]; checked {
		return result
	}
	defer func() { results[id] = available }()
	r.mu.RLock()
	d, installed := r.definitions[id]
	state := r.states[id]
	r.mu.RUnlock()
	if !installed || state != Ready || ctx.Err() != nil {
		return false
	}
	if d.Ready != nil && d.Ready(ctx) != nil {
		return false
	}
	for _, dep := range d.Descriptor.Requires {
		if !r.available(ctx, dep.ID, results) {
			return false
		}
	}
	return ctx.Err() == nil
}

func (r *Registry) Healthy(ctx context.Context) bool {
	results := make(map[string]bool)
	for _, id := range r.order {
		if !r.available(ctx, id, results) {
			return false
		}
	}
	return ctx.Err() == nil
}

// ReadyForTraffic checks only explicitly critical modules and their core dependencies.
func (r *Registry) ReadyForTraffic(ctx context.Context) bool {
	results := make(map[string]bool)
	for _, id := range r.order {
		if r.definitions[id].Critical && !r.available(ctx, id, results) {
			return false
		}
	}
	return ctx.Err() == nil
}

func (r *Registry) CapabilityAvailable(ctx context.Context, id, name string) bool {
	return r.capabilityAvailable(ctx, id, name, make(map[string]bool))
}

func (r *Registry) capabilityAvailable(ctx context.Context, id, name string, results map[string]bool) bool {
	if !r.Owns(id, name) || !r.available(ctx, id, results) {
		return false
	}
	for _, dep := range r.definitions[id].Descriptor.CapabilityRequires[name] {
		if !r.available(ctx, dep.ID, results) {
			return false
		}
	}
	return ctx.Err() == nil
}

func (r *Registry) Capabilities(ctx context.Context) []string {
	result := []string{}
	results := make(map[string]bool)
	for _, id := range r.order {
		for _, name := range r.definitions[id].Descriptor.Capabilities {
			if r.capabilityAvailable(ctx, id, name, results) {
				result = append(result, name)
			}
		}
	}
	slices.Sort(result)
	return result
}

func (r *Registry) Owns(id, cap string) bool {
	d, ok := r.definitions[id]
	return ok && slices.Contains(d.Descriptor.Capabilities, cap)
}

func (r *Registry) Statuses() []Status {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.StatusesContext(ctx)
}

func (r *Registry) StatusesContext(ctx context.Context) []Status {
	r.mu.RLock()
	result := make([]Status, 0, len(r.states))
	for id, state := range r.states {
		result = append(result, Status{ID: id, State: state})
	}
	r.mu.RUnlock()
	availability := make(map[string]bool)
	for i := range result {
		result[i].Health = "unavailable"
		if result[i].State == Disabled {
			result[i].Health = "disabled"
		} else if r.available(ctx, result[i].ID, availability) {
			result[i].Health = "available"
		}
	}
	slices.SortFunc(result, func(a, b Status) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result
}
