// Package admin owns platform role definitions, scoped grants and authorization.
package admin

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/StuHelper/stuhelper-platform/account"
)

var (
	ErrInvalid         = errors.New("invalid_request")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrNotFound        = errors.New("not_found")
	ErrUnavailable     = errors.New("unavailable")
)

type Scope struct {
	School string `json:"school"`
	Group  string `json:"group"`
	Module string `json:"module"`
}

var Global = Scope{"*", "*", "*"}

func (s Scope) Contains(t Scope) bool {
	return (s.School == "*" || s.School == t.School) && (s.Group == "*" || s.Group == t.Group) && (s.Module == "*" || s.Module == t.Module)
}
func (s Scope) Valid() bool {
	return clean(s.School, 128) && clean(s.Group, 128) && clean(s.Module, 128)
}

// Intersection preserves all three restrictions; a group never implies a school.
func (s Scope) Intersection(t Scope) (Scope, bool) {
	values := []string{}
	for _, p := range [][2]string{{s.School, t.School}, {s.Group, t.Group}, {s.Module, t.Module}} {
		switch {
		case p[0] == "*":
			values = append(values, p[1])
		case p[1] == "*" || p[0] == p[1]:
			values = append(values, p[0])
		default:
			return Scope{}, false
		}
	}
	return Scope{values[0], values[1], values[2]}, true
}

type Role struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}
type RoleInput struct {
	Role   Role   `json:"role"`
	Reason string `json:"reason"`
}
type Grant struct {
	Role  Role  `json:"role"`
	Scope Scope `json:"scope"`
}
type Effective struct {
	Subject  string  `json:"subject"`
	Revision string  `json:"revision"`
	Grants   []Grant `json:"grants"`
}
type Mutation struct {
	RoleID           string `json:"role_id"`
	Scope            Scope  `json:"scope"`
	ExpectedRevision string `json:"expected_revision"`
	Reason           string `json:"reason"`
}
type Revision struct {
	Revision string `json:"revision"`
}
type Record struct {
	RoleID    string    `json:"role_id,omitempty"`
	Revision  string    `json:"revision,omitempty"`
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Scope     Scope     `json:"scope"`
	Reason    string    `json:"reason"`
	Result    string    `json:"result"`
	CreatedAt time.Time `json:"created_at"`
}
type Page struct {
	Source     string   `json:"source"`
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor"`
}
type Filter struct {
	Scope                 Scope
	Target, Actor, Action string
	From, Until           time.Time
	Cursor                string
	Limit                 int
}

// Identity must consult trusted current identity facts, including account status.
// It returns false,nil for an active non-platform administrator and fails closed
// for revoked identities or unavailable identity providers. Never use request roles.
type Identity interface {
	PlatformAdmin(context.Context, account.Principal) (bool, error)
}
type Store interface {
	Role(context.Context, string) (Role, error)
	Effective(context.Context, string) (Effective, error)
	CreateRole(context.Context, Role) error
	Change(context.Context, string, Mutation, bool) (Revision, error)
	Record(context.Context, Record) error
	Replay(context.Context, string, string, string) ([]byte, error)
	SaveReplay(context.Context, string, string, string, []byte, time.Time) error
	Operations(context.Context, Filter, int64) ([]Record, error)
}
type Repository interface {
	// View provides one consistent, uncached authorization/query snapshot.
	View(context.Context, func(Store) error) error
	// Write serializes authorization reads, changes, receipts and audit commits.
	Write(context.Context, func(Store) error) error
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

func clean(s string, n int) bool {
	return s != "" && len(s) <= n && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func validRole(r Role) bool {
	if !identifier.MatchString(r.ID) || !clean(r.Name, 128) || len(r.Capabilities) == 0 || len(r.Capabilities) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range r.Capabilities {
		if !identifier.MatchString(c) || !strings.Contains(c, ".") || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// Templates are suggestions only. Sensitive actions are never bundled into them.
func Templates() []Role {
	return []Role{
		{"platform", "平台管理", []string{"platform.authorization.read", "platform.authorization.manage", "platform.audit.read"}},
		{"school", "学校管理", []string{"catalog.maintain"}},
		{"moderation", "内容审核", []string{"moderation.review"}},
		{"resources", "资料管理", []string{"resources.manage"}},
		{"groups", "群管理", []string{"groups.manage"}},
		{"finance", "财务管理", []string{"billing.configuration.read"}},
	}
}
