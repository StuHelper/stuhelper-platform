package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
)

type roleCursor struct {
	Version int          `json:"version"`
	Binding string       `json:"binding"`
	Before  RolePosition `json:"before"`
}

type administratorCursor struct {
	Version       int    `json:"version"`
	Binding       string `json:"binding"`
	Authorization string `json:"authorization"`
	AfterSubject  string `json:"after_subject"`
}

func validReason(s string) bool {
	return clean(s, 512) && strings.TrimSpace(s) != ""
}

func validRevisionString(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, false
	}
	return n, true
}

func grantAllowed(grants []Grant, platform bool, cap string, scope Scope) bool {
	if platform {
		return true
	}
	for _, g := range grants {
		if slices.Contains(g.Role.Capabilities, cap) && g.Scope.Contains(scope) {
			return true
		}
	}
	return false
}

func hasCapabilityAnyScope(grants []Grant, platform bool, cap string) bool {
	if platform {
		return true
	}
	for _, g := range grants {
		if slices.Contains(g.Role.Capabilities, cap) {
			return true
		}
	}
	return false
}

func validateGrantTargets(add, revoke []GrantTarget) bool {
	if len(add)+len(revoke) == 0 || len(add) > 64 || len(revoke) > 64 {
		return false
	}
	type targetKey struct {
		roleID string
		scope  Scope
	}
	addSeen := map[targetKey]bool{}
	for _, t := range add {
		if !identifier.MatchString(t.RoleID) || !t.Scope.Valid() {
			return false
		}
		k := targetKey{t.RoleID, t.Scope}
		if addSeen[k] {
			return false
		}
		addSeen[k] = true
	}
	revokeSeen := map[targetKey]bool{}
	for _, t := range revoke {
		if !identifier.MatchString(t.RoleID) || !t.Scope.Valid() {
			return false
		}
		k := targetKey{t.RoleID, t.Scope}
		if revokeSeen[k] || addSeen[k] {
			return false
		}
		revokeSeen[k] = true
	}
	return true
}

func (s Service) ListRoles(ctx context.Context, p account.Principal, f RoleFilter) (RolePage, error) {
	out := RolePage{Items: []Role{}}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 1 || f.Limit > 100 || len(f.Cursor) > 4096 {
		return out, ErrInvalid
	}
	if f.Keyword != "" && (!clean(f.Keyword, 128) || strings.TrimSpace(f.Keyword) == "") {
		return out, ErrInvalid
	}
	if f.Capability != "" && !validCapability(f.Capability) {
		return out, ErrInvalid
	}
	binding := digest([]any{p.Key(), f.Keyword, f.Capability, f.Limit})
	var before *RolePosition
	if f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil {
			return out, ErrInvalid
		}
		var c roleCursor
		if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Binding != binding || c.Before.CreatedAt.IsZero() || !identifier.MatchString(c.Before.ID) {
			return out, ErrInvalid
		}
		before = &c.Before
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	var rows []Role
	err = s.Repo.View(ctx, func(st Store) error {
		if !platform {
			effective, e := st.Effective(ctx, p.Key())
			if e != nil {
				return e
			}
			if !hasCapabilityAnyScope(effective.Grants, false, "platform.authorization.read") {
				return ErrForbidden
			}
		}
		var e error
		rows, e = st.ListRoles(ctx, RoleQuery{Keyword: f.Keyword, Capability: f.Capability, Before: before, Limit: f.Limit + 1})
		return e
	})
	if err != nil {
		return out, err
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		last := rows[len(rows)-1]
		if last.CreatedAt == nil {
			return out, ErrUnavailable
		}
		b, e := json.Marshal(roleCursor{Version: 1, Binding: binding, Before: RolePosition{CreatedAt: *last.CreatedAt, ID: last.ID}})
		if e != nil {
			return out, ErrUnavailable
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	out.Items = rows
	return out, nil
}

func narrowAdministratorGrants(ctx context.Context, rows []AdministratorSummary, permitted []Scope, remaining int) error {
	for i := range rows {
		type grantKey struct {
			roleID string
			scope  Scope
		}
		seen := map[grantKey]bool{}
		narrowed := []Grant{}
		for _, g := range rows[i].Grants {
			for _, sc := range permitted {
				if err := ctx.Err(); err != nil {
					return err
				}
				if inter, ok := g.Scope.Intersection(sc); ok {
					k := grantKey{g.Role.ID, inter}
					if !seen[k] {
						if remaining == 0 {
							return ErrInvalid
						}
						remaining--
						seen[k] = true
						narrowed = append(narrowed, Grant{Role: g.Role, Scope: inter})
					}
				}
			}
		}
		slices.SortFunc(narrowed, func(a, b Grant) int {
			if c := strings.Compare(a.Role.ID, b.Role.ID); c != 0 {
				return c
			}
			if c := strings.Compare(a.Scope.School, b.Scope.School); c != 0 {
				return c
			}
			if c := strings.Compare(a.Scope.Group, b.Scope.Group); c != 0 {
				return c
			}
			return strings.Compare(a.Scope.Module, b.Scope.Module)
		})
		rows[i].Grants = narrowed
	}
	return nil
}

func (s Service) ListAdministrators(ctx context.Context, p account.Principal, f AdministratorFilter) (AdministratorPage, error) {
	out := AdministratorPage{Items: []AdministratorSummary{}}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if !f.Scope.Valid() || f.Limit < 1 || f.Limit > 100 || len(f.Cursor) > 8192 {
		return out, ErrInvalid
	}
	if f.RoleID != "" && !identifier.MatchString(f.RoleID) {
		return out, ErrInvalid
	}
	if f.Capability != "" && !validCapability(f.Capability) {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	canonical := f
	canonical.Cursor = ""
	binding := digest([]any{p.Key(), canonical})
	err = s.Repo.View(ctx, func(st Store) error {
		permitted := []Scope{}
		if platform {
			permitted = append(permitted, f.Scope)
		} else {
			effective, e := st.Effective(ctx, p.Key())
			if e != nil {
				return e
			}
			unique := map[Scope]bool{}
			for _, g := range effective.Grants {
				if slices.Contains(g.Role.Capabilities, "platform.authorization.read") {
					if inter, ok := f.Scope.Intersection(g.Scope); ok {
						unique[inter] = true
					}
				}
			}
			for sc := range unique {
				permitted = append(permitted, sc)
			}
			slices.SortFunc(permitted, func(a, b Scope) int { return strings.Compare(digest(a), digest(b)) })
		}
		if len(permitted) == 0 {
			return ErrForbidden
		}
		authHash := digest(permitted)
		afterSubject := ""
		if f.Cursor != "" {
			raw, decodeErr := base64.RawURLEncoding.DecodeString(f.Cursor)
			if decodeErr != nil {
				return ErrInvalid
			}
			var c administratorCursor
			if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.AfterSubject == "" {
				return ErrInvalid
			}
			if _, parseErr := account.Parse(c.AfterSubject); parseErr != nil || c.Binding != binding {
				return ErrInvalid
			}
			if c.Authorization != authHash {
				return ErrForbidden
			}
			afterSubject = c.AfterSubject
		}
		rows, e := st.ListAdministrators(ctx, AdministratorQuery{
			Scopes:       permitted,
			RoleID:       f.RoleID,
			Capability:   f.Capability,
			AfterSubject: afterSubject,
			Limit:        f.Limit + 1,
		})
		if e != nil {
			return e
		}
		hasMore := len(rows) > f.Limit
		if hasMore {
			rows = rows[:f.Limit]
		}
		if err := narrowAdministratorGrants(ctx, rows, permitted, 1000); err != nil {
			return err
		}
		if hasMore {
			lastSubject := rows[len(rows)-1].Subject
			b, marshalErr := json.Marshal(administratorCursor{Version: 1, Binding: binding, Authorization: authHash, AfterSubject: lastSubject})
			if marshalErr != nil {
				return ErrUnavailable
			}
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		}
		out.Items = rows
		return nil
	})
	return out, err
}

func resolveAndAuthorizeTargets(ctx context.Context, st Store, caller account.Principal, platform bool, callerGrants []Grant, subject string, add, revoke []GrantTarget, roleCache map[string]Role) ([]Grant, []Grant, error) {
	if len(add) > 0 && caller.Key() == subject {
		return nil, nil, ErrForbidden
	}
	loadGroup := func(targets []GrantTarget) ([]Grant, error) {
		resolved := make([]Grant, 0, len(targets))
		for _, t := range targets {
			if !grantAllowed(callerGrants, platform, "platform.authorization.manage", t.Scope) {
				return nil, ErrForbidden
			}
			role, cached := roleCache[t.RoleID]
			if !cached {
				var err error
				role, err = st.Role(ctx, t.RoleID)
				if err != nil {
					return nil, err
				}
				role.CreatedAt = nil
				roleCache[t.RoleID] = role
			}
			for _, cap := range role.Capabilities {
				if strings.HasPrefix(cap, "platform.") && !platform {
					return nil, ErrForbidden
				}
				if !platform && !grantAllowed(callerGrants, false, cap, t.Scope) {
					return nil, ErrForbidden
				}
			}
			resolved = append(resolved, Grant{Role: role, Scope: t.Scope})
		}
		return resolved, nil
	}
	// Authorize scopes before revealing whether a role ID exists when unauthorized,
	// matching Service.Change behavior.
	adding, err := loadGroup(add)
	if err != nil {
		return nil, nil, err
	}
	revoking, err := loadGroup(revoke)
	if err != nil {
		return nil, nil, err
	}
	return adding, revoking, nil
}

func (s Service) ChangeGrants(ctx context.Context, p account.Principal, subject, key string, in GrantChangesInput) (out Revision, err error) {
	if _, e := account.Parse(subject); e != nil || !validateGrantTargets(in.Add, in.Revoke) || !validReason(in.Reason) || !validKey(key) {
		return out, ErrInvalid
	}
	if _, ok := validRevisionString(in.ExpectedRevision); !ok {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	if len(in.Add) > 0 && p.Key() == subject {
		return out, ErrForbidden
	}
	err = s.Repo.Write(ctx, func(st Store) error {
		var callerGrants []Grant
		if !platform {
			effective, e := st.Effective(ctx, p.Key())
			if e != nil {
				return e
			}
			callerGrants = effective.Grants
		}
		if _, _, e := resolveAndAuthorizeTargets(ctx, st, p, platform, callerGrants, subject, in.Add, in.Revoke, map[string]Role{}); e != nil {
			return e
		}
		hash := digest([]any{"grant.changes", subject, in})
		at := s.now()
		b, e := st.Replay(ctx, p.Key(), key, hash, at)
		if e != nil {
			return e
		}
		if b != nil {
			return json.Unmarshal(b, &out)
		}
		out, e = st.ApplyGrantChanges(ctx, subject, in.ExpectedRevision, in.Add, in.Revoke, p.Key(), in.Reason, at)
		if e != nil {
			return e
		}
		b, _ = json.Marshal(out)
		return st.SaveReplay(ctx, p.Key(), key, hash, b, at)
	})
	return
}

func previewItemConflict(effective Effective, expectedRevision string, add, revoke []GrantTarget) (string, bool) {
	rev, ok := validRevisionString(effective.Revision)
	if !ok || effective.Revision != expectedRevision {
		return effective.Revision, false
	}
	type grantKey struct {
		roleID string
		scope  Scope
	}
	existing := map[grantKey]bool{}
	for _, g := range effective.Grants {
		existing[grantKey{g.Role.ID, g.Scope}] = true
	}
	for _, t := range revoke {
		if !existing[grantKey{t.RoleID, t.Scope}] {
			return effective.Revision, false
		}
	}
	for _, t := range add {
		if existing[grantKey{t.RoleID, t.Scope}] {
			return effective.Revision, false
		}
	}
	if len(effective.Grants)-len(revoke)+len(add) > 1000 {
		return effective.Revision, false
	}
	return strconv.FormatInt(rev+1, 10), true
}

func (s Service) BatchChangeGrants(ctx context.Context, p account.Principal, key string, in BatchGrantChangesInput) (BatchGrantChangesResult, error) {
	out := BatchGrantChangesResult{DryRun: in.DryRun, Items: []BatchGrantChangeResultItem{}}
	if !validReason(in.Reason) || len(in.Items) == 0 || len(in.Items) > 50 {
		return out, ErrInvalid
	}
	if !in.DryRun && !validKey(key) {
		return out, ErrInvalid
	}
	if in.DryRun && key != "" && !validKey(key) {
		return out, ErrInvalid
	}
	seenSubjects := map[string]bool{}
	for _, item := range in.Items {
		if _, e := account.Parse(item.Subject); e != nil || seenSubjects[item.Subject] || !validateGrantTargets(item.Add, item.Revoke) {
			return out, ErrInvalid
		}
		if _, ok := validRevisionString(item.ExpectedRevision); !ok {
			return out, ErrInvalid
		}
		seenSubjects[item.Subject] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	if in.DryRun {
		err = s.Repo.View(ctx, func(st Store) error {
			var callerGrants []Grant
			if !platform {
				effective, e := st.Effective(ctx, p.Key())
				if e != nil {
					return e
				}
				callerGrants = effective.Grants
				if !hasCapabilityAnyScope(callerGrants, false, "platform.authorization.manage") {
					return ErrForbidden
				}
			}
			roleCache := map[string]Role{}
			for _, item := range in.Items {
				res := BatchGrantChangeResultItem{Subject: item.Subject, Adding: []Grant{}, Revoking: []Grant{}}
				adding, revoking, authErr := resolveAndAuthorizeTargets(ctx, st, p, platform, callerGrants, item.Subject, item.Add, item.Revoke, roleCache)
				if errors.Is(authErr, ErrForbidden) {
					res.Status = BatchForbidden
					out.Items = append(out.Items, res)
					continue
				}
				if errors.Is(authErr, ErrNotFound) {
					res.Status = BatchNotFound
					out.Items = append(out.Items, res)
					continue
				}
				if authErr != nil {
					return authErr
				}
				res.Adding = adding
				res.Revoking = revoking
				targetEffective, e := st.Effective(ctx, item.Subject)
				if e != nil {
					return e
				}
				res.CurrentRevision = targetEffective.Revision
				nextRev, ready := previewItemConflict(targetEffective, item.ExpectedRevision, item.Add, item.Revoke)
				if !ready {
					res.Status = BatchConflict
					res.Revision = targetEffective.Revision
				} else {
					res.Status = BatchReady
					res.Revision = nextRev
				}
				out.Items = append(out.Items, res)
			}
			return nil
		})
		return out, err
	}
	err = s.Repo.Write(ctx, func(st Store) error {
		var callerGrants []Grant
		if !platform {
			effective, e := st.Effective(ctx, p.Key())
			if e != nil {
				return e
			}
			callerGrants = effective.Grants
			if !hasCapabilityAnyScope(callerGrants, false, "platform.authorization.manage") {
				return ErrForbidden
			}
		}
		roleCache := map[string]Role{}
		hash := digest([]any{"batch.grant.changes", in})
		at := s.now()
		b, e := st.Replay(ctx, p.Key(), key, hash, at)
		if e != nil {
			return e
		}
		if b != nil {
			var cached BatchGrantChangesResult
			if json.Unmarshal(b, &cached) != nil || len(cached.Items) != len(in.Items) {
				return ErrUnavailable
			}
			// Successful and conflicting items both disclose authorized grant details.
			for i, item := range in.Items {
				if cached.Items[i].Status == BatchSucceeded || cached.Items[i].Status == BatchConflict {
					if _, _, authErr := resolveAndAuthorizeTargets(ctx, st, p, platform, callerGrants, item.Subject, item.Add, item.Revoke, roleCache); authErr != nil {
						return authErr
					}
				}
			}
			out = cached
			return nil
		}
		for _, item := range in.Items {
			res := BatchGrantChangeResultItem{Subject: item.Subject, Adding: []Grant{}, Revoking: []Grant{}}
			adding, revoking, authErr := resolveAndAuthorizeTargets(ctx, st, p, platform, callerGrants, item.Subject, item.Add, item.Revoke, roleCache)
			if errors.Is(authErr, ErrForbidden) {
				res.Status = BatchForbidden
				out.Items = append(out.Items, res)
				continue
			}
			if errors.Is(authErr, ErrNotFound) {
				res.Status = BatchNotFound
				out.Items = append(out.Items, res)
				continue
			}
			if authErr != nil {
				return authErr
			}
			res.Adding = adding
			res.Revoking = revoking
			rev, applyErr := st.ApplyGrantChanges(ctx, item.Subject, item.ExpectedRevision, item.Add, item.Revoke, p.Key(), in.Reason, at)
			if errors.Is(applyErr, ErrConflict) {
				targetEffective, effErr := st.Effective(ctx, item.Subject)
				if effErr != nil {
					return effErr
				}
				res.Status = BatchConflict
				res.CurrentRevision = targetEffective.Revision
				res.Revision = targetEffective.Revision
				out.Items = append(out.Items, res)
				continue
			}
			if applyErr != nil {
				return applyErr
			}
			res.Status = BatchSucceeded
			res.CurrentRevision = item.ExpectedRevision
			res.Revision = rev.Revision
			out.Items = append(out.Items, res)
		}
		b, _ = json.Marshal(out)
		return st.SaveReplay(ctx, p.Key(), key, hash, b, at)
	})
	return out, err
}
