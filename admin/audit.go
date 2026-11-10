package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
)

// AuditPosition is a strict descending key (time, source, ID). IDs must be
// immutable and unique within a provider. Strings use bytewise ordering.
type AuditPosition struct {
	Time       time.Time
	Source, ID string
}

// AuditRequest is constructed only after authorization. Providers must apply the
// union of Scopes and all filters BEFORE limiting, using their own repository.
// Snapshot pins membership: later inserts, including backdated rows, must not
// enter it. Expired snapshots return an error, never a newly created snapshot.
// No provider may return material contents, payment payloads or raw audit JSON.
type AuditRequest struct {
	Principal             account.Principal
	Scopes                []Scope
	Actor, Target, Action string
	From, Until           time.Time
	Before                *AuditPosition
	Snapshot              string
	Limit                 int
}
type AuditBatch struct {
	Records  []Record
	Snapshot string
}

// AuditProvider is implemented by module-owned public query adapters and wired
// by backend. ReadAudit respects cancellation, returns at most Limit records in
// descending key order, and returns a nonempty snapshot token (max 256 bytes).
// Token validity must account for retention; expired data cannot imply completeness.
type AuditProvider interface {
	ReadAudit(context.Context, AuditRequest) (AuditBatch, error)
}

const maxAuditProviders = 16

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type auditCursor struct {
	Authorization string
	Version       int
	Binding       string
	Until         time.Time
	Before        *AuditPosition
	Snapshots     map[string]string
	Missing       []string
}

func position(r Record) AuditPosition { return AuditPosition{r.CreatedAt, r.Source, r.ID} }
func comparePosition(a, b AuditPosition) int {
	if c := a.Time.Compare(b.Time); c != 0 {
		return c
	}
	if c := strings.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}
func validateFilter(f Filter) error {
	if !f.Scope.Valid() || f.Limit < 1 || f.Limit > 100 || len(f.Cursor) > 16384 || len(f.Sources) > maxAuditProviders {
		return ErrInvalid
	}
	for _, v := range []struct {
		s string
		n int
	}{{f.Actor, 2048}, {f.Target, 2048}, {f.Action, 128}} {
		if v.s != "" && !clean(v.s, v.n) {
			return ErrInvalid
		}
	}
	if !f.From.IsZero() && !f.Until.IsZero() && !f.From.Before(f.Until) {
		return ErrInvalid
	}
	return nil
}
func (s Service) selectedProviders(f Filter) ([]string, error) {
	if len(s.AuditProviders) >= maxAuditProviders {
		return nil, ErrUnavailable
	}
	names := []string{"platform"}
	for name := range s.AuditProviders {
		if !moduleName.MatchString(name) || name == "platform" {
			return nil, ErrUnavailable
		}
		names = append(names, name)
	}
	if len(f.Sources) > 0 {
		selected := []string{}
		for _, name := range f.Sources {
			if !slices.Contains(names, name) || slices.Contains(selected, name) {
				return nil, ErrInvalid
			}
			selected = append(selected, name)
		}
		names = selected
	}
	sort.Strings(names)
	return names, nil
}

// authorizationScopes limits provider input before pagination. If privileges
// change during paging, the old cursor is rejected rather than mixing datasets.
func (s Service) authorizationScopes(ctx context.Context, p account.Principal, f Filter, names []string) (map[string][]Scope, error) {
	platform, err := s.identity(ctx, p)
	if err != nil {
		return nil, err
	}
	granted := []Scope{}
	if platform {
		granted = append(granted, Global)
	} else {
		err = s.Repo.View(ctx, func(st Store) error {
			e, err := st.Effective(ctx, p.Key())
			if err != nil {
				return err
			}
			for _, g := range e.Grants {
				if slices.Contains(g.Role.Capabilities, "platform.audit.read") {
					granted = append(granted, g.Scope)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := map[string][]Scope{}
	for _, name := range names {
		requested := f.Scope
		// platform may own a grant change concerning another business module.
		if name != "platform" {
			var ok bool
			requested, ok = requested.Intersection(Scope{"*", "*", name})
			if !ok {
				continue
			}
		}
		unique := map[Scope]bool{}
		for _, g := range granted {
			if scope, ok := requested.Intersection(g); ok {
				unique[scope] = true
			}
		}
		for scope := range unique {
			out[name] = append(out[name], scope)
		}
		slices.SortFunc(out[name], func(a, b Scope) int { return strings.Compare(digest(a), digest(b)) })
	}
	if len(out) == 0 {
		return nil, ErrForbidden
	}
	if len(f.Sources) > 0 {
		for _, name := range names {
			if len(out[name]) == 0 {
				return nil, ErrForbidden
			}
		}
	}
	return out, nil
}
func decodeAuditCursor(f Filter, binding, authorization string, names []string, now time.Time) (auditCursor, error) {
	until := f.Until
	if until.IsZero() || until.After(now) {
		until = now
	}
	c := auditCursor{Version: 1, Binding: binding, Authorization: authorization, Until: until, Snapshots: map[string]string{}, Missing: []string{}}
	if f.Cursor == "" {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
	if err != nil {
		return c, ErrInvalid
	}
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Before == nil || c.Snapshots == nil || c.Missing == nil || c.Until.IsZero() || c.Until.After(now) || (!f.Until.IsZero() && c.Until.After(f.Until)) {
		return c, ErrInvalid
	}
	if c.Binding != binding {
		return c, ErrInvalid
	}
	if c.Authorization != authorization {
		return c, ErrForbidden
	}
	if c.Before.Time.After(c.Until) || !clean(c.Before.ID, 256) || !slices.Contains(names, c.Before.Source) {
		return c, ErrInvalid
	}
	if len(c.Snapshots) > maxAuditProviders || len(c.Missing) > maxAuditProviders {
		return c, ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range c.Missing {
		if !slices.Contains(names, name) || seen[name] {
			return c, ErrInvalid
		}
		seen[name] = true
	}
	for name, token := range c.Snapshots {
		if !slices.Contains(names, name) || !clean(token, 256) {
			return c, ErrInvalid
		}
	}
	for _, name := range names {
		if !seen[name] && c.Snapshots[name] == "" {
			return c, ErrInvalid
		}
	}
	return c, nil
}
func validateBatch(name string, q AuditRequest, b AuditBatch) bool {
	if !clean(b.Snapshot, 256) || (q.Snapshot != "" && b.Snapshot != q.Snapshot) || len(b.Records) > q.Limit {
		return false
	}
	for i, r := range b.Records {
		if len(r.RoleID) > 128 || len(r.Revision) > 128 || (name != "platform" && (r.RoleID != "" || r.Revision != "")) {
			return false
		}
		if r.Source != name || !clean(r.ID, 256) || !clean(r.Actor, 2048) || !clean(r.Target, 2048) || !clean(r.Action, 128) || len(r.Reason) > 512 || (r.Reason != "" && !clean(r.Reason, 512)) || !r.Scope.Valid() || !slices.Contains([]string{"success", "denied", "failed", "unknown"}, r.Result) || r.CreatedAt.IsZero() {
			return false
		}
		if r.CreatedAt.Before(q.From) || !r.CreatedAt.Before(q.Until) || (q.Actor != "" && r.Actor != q.Actor) || (q.Target != "" && r.Target != q.Target) || (q.Action != "" && r.Action != q.Action) {
			return false
		}
		permitted := false
		for _, scope := range q.Scopes {
			if scope.Contains(r.Scope) {
				permitted = true
				break
			}
		}
		if !permitted {
			return false
		}
		if q.Before != nil && comparePosition(position(r), *q.Before) >= 0 {
			return false
		}
		if i > 0 && comparePosition(position(b.Records[i-1]), position(r)) <= 0 {
			return false
		}
	}
	return true
}
func (s Service) Operations(ctx context.Context, p account.Principal, f Filter) (Page, error) {
	out := Page{Source: "platform", Items: []Record{}, MissingModules: []string{}, QueriedModules: []string{}}
	if err := validateFilter(f); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	names, err := s.selectedProviders(f)
	if err != nil {
		return out, err
	}
	scopes, err := s.authorizationScopes(ctx, p, f, names)
	if err != nil {
		return out, err
	}
	for _, name := range names {
		if len(scopes[name]) > 0 {
			out.QueriedModules = append(out.QueriedModules, name)
		}
	}
	canonical := f
	canonical.Cursor = ""
	canonical.Sources = names
	binding := digest([]any{p.Key(), canonical})
	c, err := decodeAuditCursor(f, binding, digest(scopes), out.QueriedModules, s.now())
	if err != nil {
		return out, err
	}
	records := []Record{}
	// At most 16 providers, with 250 ms per provider and 101 records per read.
	// Calls are sequential to bound in-flight work; adapters must honor context.
	for _, name := range out.QueriedModules {
		if slices.Contains(c.Missing, name) {
			continue
		}
		q := AuditRequest{Principal: p, Scopes: scopes[name], Actor: f.Actor, Target: f.Target, Action: f.Action, From: f.From, Until: c.Until, Before: c.Before, Snapshot: c.Snapshots[name], Limit: f.Limit + 1}
		call, stop := context.WithTimeout(ctx, 250*time.Millisecond)
		var batch AuditBatch
		if name == "platform" {
			err = s.Repo.View(call, func(st Store) error { var e error; batch, e = st.ReadAudit(call, q); return e })
		} else if provider := s.AuditProviders[name]; provider != nil {
			batch, err = provider.ReadAudit(call, q)
		} else {
			err = ErrUnavailable
		}
		if call.Err() != nil {
			err = call.Err()
		}
		stop()
		if err != nil || !validateBatch(name, q, batch) {
			c.Missing = append(c.Missing, name)
			continue
		}
		c.Snapshots[name] = batch.Snapshot
		records = append(records, batch.Records...)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	slices.SortFunc(records, func(a, b Record) int { return -comparePosition(position(a), position(b)) })
	if len(records) > f.Limit {
		records = records[:f.Limit]
		last := position(records[len(records)-1])
		c.Before = &last
		b, e := json.Marshal(c)
		if e != nil {
			return out, ErrUnavailable
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		if len(out.NextCursor) > 16384 {
			return out, ErrUnavailable
		}
	}
	out.Items = records
	sort.Strings(c.Missing)
	out.MissingModules = c.Missing
	out.Partial = len(c.Missing) > 0
	return out, nil
}
