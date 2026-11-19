// Package admin owns platform role definitions, scoped grants and authorization.
package admin

import (
	"context"
	"errors"
	"fmt"
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
	// ErrSnapshotExpired wraps ErrUnavailable so callers checking availability stay
	// compatible while Operations distinguishes expired/cleaned paging snapshots.
	ErrSnapshotExpired = fmt.Errorf("%w: snapshot_expired", ErrUnavailable)
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
	return scopeToken(s.School) && scopeToken(s.Group) && scopeToken(s.Module)
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
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	LabelKey     string     `json:"label_key,omitempty"`
	Capabilities []string   `json:"capabilities"`
}
type RolePage struct {
	Items      []Role `json:"items"`
	NextCursor string `json:"next_cursor"`
}
type RoleFilter struct {
	Keyword    string
	Capability string
	Cursor     string
	Limit      int
}
type RolePosition struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}
type RoleQuery struct {
	Before     *RolePosition
	Keyword    string
	Capability string
	Limit      int
}
type RoleInput struct {
	Role   Role   `json:"role"`
	Reason string `json:"reason"`
}
type Grant struct {
	Role  Role  `json:"role"`
	Scope Scope `json:"scope"`
}
type GrantTarget struct {
	RoleID string `json:"role_id"`
	Scope  Scope  `json:"scope"`
}
type GrantChangesInput struct {
	Add              []GrantTarget `json:"add"`
	Revoke           []GrantTarget `json:"revoke"`
	ExpectedRevision string        `json:"expected_revision"`
	Reason           string        `json:"reason"`
}
type BatchItemStatus string

const (
	BatchReady     BatchItemStatus = "ready"
	BatchSucceeded BatchItemStatus = "succeeded"
	BatchConflict  BatchItemStatus = "conflict"
	BatchForbidden BatchItemStatus = "forbidden"
	BatchNotFound  BatchItemStatus = "not_found"
)

type BatchGrantChangeItem struct {
	Subject          string        `json:"subject"`
	ExpectedRevision string        `json:"expected_revision"`
	Add              []GrantTarget `json:"add"`
	Revoke           []GrantTarget `json:"revoke"`
}
type BatchGrantChangesInput struct {
	DryRun bool                   `json:"dry_run"`
	Reason string                 `json:"reason"`
	Items  []BatchGrantChangeItem `json:"items"`
}
type BatchGrantChangeResultItem struct {
	Subject         string          `json:"subject"`
	Status          BatchItemStatus `json:"status"`
	Revision        string          `json:"revision"`
	CurrentRevision string          `json:"current_revision,omitempty"`
	Adding          []Grant         `json:"adding"`
	Revoking        []Grant         `json:"revoking"`
}
type BatchGrantChangesResult struct {
	DryRun bool                         `json:"dry_run"`
	Items  []BatchGrantChangeResultItem `json:"items"`
}
type AdministratorSummary struct {
	Subject  string  `json:"subject"`
	Revision string  `json:"revision"`
	Grants   []Grant `json:"grants"`
}
type AdministratorPage struct {
	Items      []AdministratorSummary `json:"items"`
	NextCursor string                 `json:"next_cursor"`
}
type AdministratorFilter struct {
	Scope      Scope
	RoleID     string
	Capability string
	Cursor     string
	Limit      int
}
type AdministratorQuery struct {
	Scopes       []Scope
	RoleID       string
	Capability   string
	AfterSubject string
	Limit        int
}
type AuthorizationMode string

const (
	PlatformAdminAll AuthorizationMode = "platform_admin_all"
	ExplicitGrants   AuthorizationMode = "explicit_grants"
)

type Effective struct {
	AuthorizationMode AuthorizationMode `json:"authorization_mode"`
	Subject           string            `json:"subject"`
	Revision          string            `json:"revision"`
	Grants            []Grant           `json:"grants"`
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
type MissingReason string

const (
	MissingSnapshotExpired MissingReason = "snapshot_expired"
	MissingUnconfigured    MissingReason = "unconfigured"
	MissingTimeout         MissingReason = "timeout"
	MissingUnavailable     MissingReason = "unavailable"
)

type Page struct {
	Partial        bool                     `json:"partial"`
	MissingModules []string                 `json:"missing_modules"`
	MissingReasons map[string]MissingReason `json:"missing_reasons"`
	QueriedModules []string                 `json:"queried_modules"`
	Source         string                   `json:"source"`
	Items          []Record                 `json:"items"`
	NextCursor     string                   `json:"next_cursor"`
}
type Filter struct {
	Sources               []string
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
	ListRoles(context.Context, RoleQuery) ([]Role, error)
	Effective(context.Context, string) (Effective, error)
	ListAdministrators(context.Context, AdministratorQuery) ([]AdministratorSummary, error)
	CreateRole(context.Context, Role) error
	Change(context.Context, string, Mutation, bool) (Revision, error)
	ApplyGrantChanges(context.Context, string, string, []GrantTarget, []GrantTarget, string, string, time.Time) (Revision, error)
	Record(context.Context, Record) error
	Replay(context.Context, string, string, string, time.Time) ([]byte, error)
	SaveReplay(context.Context, string, string, string, []byte, time.Time) error
	ReadAudit(context.Context, AuditRequest) (AuditBatch, error)
}
type Repository interface {
	// View provides one consistent, uncached authorization/query snapshot.
	View(context.Context, func(Store) error) error
	// Write serializes authorization reads, changes, receipts and audit commits.
	Write(context.Context, func(Store) error) error
}

var (
	identifier           = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	capabilityIdentifier = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9_-]*)+$`)
)

func clean(s string, n int) bool {
	return s != "" && len(s) <= n && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func nonBlank(s string, n int) bool {
	return clean(s, n) && strings.TrimSpace(s) != ""
}
func scopeToken(s string) bool {
	return clean(s, 128) && !strings.ContainsFunc(s, unicode.IsSpace)
}
func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}
func validCapability(c string) bool {
	return len(c) <= 128 && capabilityIdentifier.MatchString(c)
}
func validRole(r Role) bool {
	if !identifier.MatchString(r.ID) || !nonBlank(r.Name, 128) || (r.LabelKey != "" && !clean(r.LabelKey, 128)) || len(r.Capabilities) == 0 || len(r.Capabilities) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range r.Capabilities {
		if !validCapability(c) || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// Templates are suggestions only. Sensitive actions are never bundled into them.
func Templates() []Role {
	return []Role{
		{ID: "platform", Name: "平台管理", LabelKey: "platform.admin.role_template.platform", Capabilities: []string{"platform.authorization.read", "platform.authorization.manage", "platform.audit.read", "platform.eligibility.manage", "platform.modules"}},
		{ID: "school", Name: "学校管理", LabelKey: "platform.admin.role_template.school", Capabilities: []string{"catalog.maintain"}},
		{ID: "moderation", Name: "内容审核", LabelKey: "platform.admin.role_template.moderation", Capabilities: []string{"moderation.review"}},
		{ID: "resources", Name: "资料管理", LabelKey: "platform.admin.role_template.resources", Capabilities: []string{"resources.manage"}},
		{ID: "groups", Name: "群管理", LabelKey: "platform.admin.role_template.groups", Capabilities: []string{"groups.manage"}},
		{ID: "finance", Name: "财务管理", LabelKey: "platform.admin.role_template.finance", Capabilities: []string{"billing.configuration.read"}},
	}
}
