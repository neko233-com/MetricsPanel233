package identity

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

type Role string

const (
	RoleNone   Role = "None"
	RoleViewer Role = "Viewer"
	RoleEditor Role = "Editor"
	RoleAdmin  Role = "Admin"
)

func (r Role) rank() int {
	switch r {
	case RoleNone:
		return 0
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	default:
		return -1
	}
}

func (r Role) Valid() bool { return r.rank() >= 0 }
func (r Role) AtLeast(required Role) bool {
	return r.Valid() && required.Valid() && r.rank() >= required.rank()
}

type PrincipalType string

const (
	TypeUser           PrincipalType = "user"
	TypeServiceAccount PrincipalType = "service-account"
	TypeSystem         PrincipalType = "system"
	TypeLocal          PrincipalType = "local"
)

// Principals are supplied by instance authentication, never by request JSON or headers.
type Principal struct {
	OrgID         int64         `json:"orgId"`
	ID            int64         `json:"id"`
	Type          PrincipalType `json:"type"`
	Login         string        `json:"login"`
	Name          string        `json:"name"`
	Role          Role          `json:"role"`
	IsServerAdmin bool          `json:"isServerAdmin"`
}

var ErrUnauthenticated = errors.New("authentication required")
var ErrForbidden = errors.New("permission denied")

func (p Principal) Validate() error {
	if p.OrgID <= 0 || !p.Role.Valid() || p.ID < 0 || len(p.Login) > 256 || len(p.Name) > 256 || strings.ContainsAny(p.Login+p.Name, "\x00\r\n") {
		return ErrUnauthenticated
	}
	switch p.Type {
	case TypeUser:
		if p.ID == 0 || p.Login == "" {
			return ErrUnauthenticated
		}
	case TypeServiceAccount:
		if p.ID == 0 || p.Login == "" || p.IsServerAdmin {
			return ErrUnauthenticated
		}
	case TypeSystem:
		if p.ID != 0 || p.IsServerAdmin {
			return ErrUnauthenticated
		}
	case TypeLocal:
		if p.OrgID != 1 || p.ID != 1 || p.Role != RoleAdmin || p.IsServerAdmin {
			return ErrUnauthenticated
		}
	default:
		return ErrUnauthenticated
	}
	return nil
}

func (p Principal) RequireOrganization(orgID int64) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.OrgID != orgID {
		return ErrForbidden
	}
	return nil
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok && p.Validate() == nil
}

func HasPrincipal(ctx context.Context) bool {
	return ctx.Value(principalContextKey{}) != nil
}

func ForOrganization(ctx context.Context, orgID int64) (Principal, error) {
	if p, present := ctx.Value(principalContextKey{}).(Principal); present {
		return p, p.RequireOrganization(orgID)
	}
	if orgID == 1 {
		return LocalPrincipal(), nil
	}
	return Principal{}, ErrUnauthenticated
}

func BackgroundPrincipal(orgID int64) Principal {
	return Principal{OrgID: orgID, Type: TypeSystem, Login: "metricspanel", Name: "MetricsPanel233", Role: RoleAdmin}
}

func LocalPrincipal() Principal {
	return Principal{OrgID: 1, ID: 1, Type: TypeLocal, Login: "metricspanel", Name: "MetricsPanel233", Role: RoleAdmin}
}

// Organization 1 retains the existing resource namespace during migration.
func Namespace(orgID int64) string {
	if orgID <= 0 {
		return ""
	}
	if orgID == 1 {
		return "default"
	}
	return "org-" + strconv.FormatInt(orgID, 10)
}
