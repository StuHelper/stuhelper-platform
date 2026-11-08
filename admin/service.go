package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
)

type Service struct {
	// AuditProviders is an immutable host registry; a nil provider is configured but unavailable.
	AuditProviders map[string]AuditProvider
	Repo           Repository
	Identity       Identity
	Now            func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) identity(ctx context.Context, p account.Principal) (bool, error) {
	if !p.Valid() {
		return false, ErrUnauthenticated
	}
	if s.Identity == nil || s.Repo == nil {
		return false, ErrUnavailable
	}
	b, e := s.Identity.PlatformAdmin(ctx, p)
	if e != nil {
		if errors.Is(e, ErrForbidden) || errors.Is(e, ErrUnauthenticated) {
			return false, e
		}
		return false, ErrUnavailable
	}
	return b, nil
}
func allowed(ctx context.Context, st Store, p account.Principal, platform bool, cap string, scope Scope) error {
	// Platform identity bootstraps only administration of platform itself. It does
	// not implicitly grant sensitive business capabilities such as refunds.
	if platform && strings.HasPrefix(cap, "platform.") {
		return nil
	}
	effective, e := st.Effective(ctx, p.Key())
	if e != nil {
		return e
	}
	for _, g := range effective.Grants {
		if slices.Contains(g.Role.Capabilities, cap) && g.Scope.Contains(scope) {
			return nil
		}
	}
	return ErrForbidden
}

// Evaluate accepts an authenticated, trusted principal and the object's actual
// scope supplied by the owning service. No identity or scope comes from a client flag.
// Every call reads current grants. Changes committed before the call are visible.
func (s Service) Evaluate(ctx context.Context, p account.Principal, cap string, scope Scope) error {
	if !identifier.MatchString(cap) || !strings.Contains(cap, ".") || !scope.Valid() {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, e := s.identity(ctx, p)
	if e != nil {
		return e
	}
	return s.Repo.View(ctx, func(st Store) error { return allowed(ctx, st, p, platform, cap, scope) })
}
func (s Service) Role(ctx context.Context, p account.Principal, id string) (out Role, err error) {
	if !identifier.MatchString(id) {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	err = s.Repo.View(ctx, func(st Store) error {
		if e := allowed(ctx, st, p, platform, "platform.authorization.read", Global); e != nil {
			return e
		}
		var e error
		out, e = st.Role(ctx, id)
		return e
	})
	return
}
func (s Service) Effective(ctx context.Context, p account.Principal, subject string, scope Scope) (out Effective, err error) {
	if _, e := account.Parse(subject); e != nil || !scope.Valid() {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	err = s.Repo.View(ctx, func(st Store) error {
		if p.Key() != subject {
			if e := allowed(ctx, st, p, platform, "platform.authorization.read", scope); e != nil {
				return e
			}
		}
		all, e := st.Effective(ctx, subject)
		if e != nil {
			return e
		}
		out = Effective{subject, all.Revision, []Grant{}}
		for _, g := range all.Grants {
			if intersection, ok := g.Scope.Intersection(scope); ok {
				g.Scope = intersection
				out.Grants = append(out.Grants, g)
			}
		}
		return nil
	})
	return
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func (s Service) CreateRole(ctx context.Context, p account.Principal, key string, in RoleInput) (out Role, err error) {
	if !validRole(in.Role) || !clean(in.Reason, 512) || !clean(key, 128) {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	if !platform {
		return out, ErrForbidden
	}
	err = s.Repo.Write(ctx, func(st Store) error {
		hash := digest([]any{"role.create", in})
		b, e := st.Replay(ctx, p.Key(), key, hash)
		if e != nil {
			return e
		}
		if b != nil {
			return json.Unmarshal(b, &out)
		}
		if e = st.CreateRole(ctx, in.Role); e != nil {
			return e
		}
		out = in.Role
		at := s.now()
		if e = st.Record(ctx, Record{Source: "platform", Actor: p.Key(), Action: "role.create", Target: out.ID, Scope: Global, Reason: in.Reason, Result: "success", CreatedAt: at}); e != nil {
			return e
		}
		b, _ = json.Marshal(out)
		return st.SaveReplay(ctx, p.Key(), key, hash, b, at)
	})
	return
}
func (s Service) Change(ctx context.Context, p account.Principal, subject, key string, in Mutation, revoke bool) (out Revision, err error) {
	if _, e := account.Parse(subject); e != nil || !identifier.MatchString(in.RoleID) || !in.Scope.Valid() || !clean(in.Reason, 512) || !clean(key, 128) {
		return out, ErrInvalid
	}
	n, e := strconv.ParseInt(in.ExpectedRevision, 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != in.ExpectedRevision {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	platform, err := s.identity(ctx, p)
	if err != nil {
		return out, err
	}
	if !revoke && p.Key() == subject {
		return out, ErrForbidden
	}
	err = s.Repo.Write(ctx, func(st Store) error {
		if e := allowed(ctx, st, p, platform, "platform.authorization.manage", in.Scope); e != nil {
			return e
		}
		role, e := st.Role(ctx, in.RoleID)
		if e != nil {
			return e
		}
		for _, cap := range role.Capabilities {
			if strings.HasPrefix(cap, "platform.") && !platform {
				return ErrForbidden
			}
			// Delegation cannot exceed the grantor's current capabilities or scope.
			if !platform {
				if e = allowed(ctx, st, p, false, cap, in.Scope); e != nil {
					return e
				}
			}
		}
		hash := digest([]any{subject, in, revoke})
		b, e := st.Replay(ctx, p.Key(), key, hash)
		if e != nil {
			return e
		}
		if b != nil {
			return json.Unmarshal(b, &out)
		}
		out, e = st.Change(ctx, subject, in, revoke)
		if e != nil {
			return e
		}
		action := "role.grant"
		if revoke {
			action = "role.revoke"
		}
		at := s.now()
		if e = st.Record(ctx, Record{Source: "platform", Actor: p.Key(), Action: action, Target: subject, RoleID: in.RoleID, Revision: out.Revision, Scope: in.Scope, Reason: in.Reason, Result: "success", CreatedAt: at}); e != nil {
			return e
		}
		b, _ = json.Marshal(out)
		return st.SaveReplay(ctx, p.Key(), key, hash, b, at)
	})
	return
}
