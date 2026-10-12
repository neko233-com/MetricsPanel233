package plugins

import (
	"context"
	"errors"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

// Principal resolves only an authenticated internal context. Legacy organization
// 1 callers retain their local identity; a second organization never gets an
// implicit administrator and malformed supplied identities never fall back.
func (m *Manager) Principal(ctx context.Context) (identity.Principal, error) {
	p, valid := identity.FromContext(ctx)
	if !identity.HasPrincipal(ctx) && m.Store.OrganizationID() == 1 {
		p, valid = identity.LocalPrincipal(), true
	}
	if !valid {
		return p, identity.ErrUnauthenticated
	}
	if err := p.RequireOrganization(m.Store.OrganizationID()); err != nil {
		return p, err
	}
	if p.Role == identity.RoleNone {
		return p, identity.ErrForbidden
	}
	return p, nil
}

func (m *Manager) contextIdentity(ctx context.Context) (int64, string, *backend.User, error) {
	p, err := m.Principal(ctx)
	if err != nil {
		return 0, "", nil, err
	}
	return p.OrgID, identity.Namespace(p.OrgID), &backend.User{Login: p.Login, Name: p.Name, Role: string(p.Role)}, nil
}

func (m *Manager) validateContext(ctx context.Context, pc backend.PluginContext) error {
	p, err := m.Principal(ctx)
	if err != nil {
		return err
	}
	if pc.OrgID != p.OrgID || pc.Namespace != identity.Namespace(p.OrgID) || pc.User == nil || pc.User.Login != p.Login || pc.User.Role != string(p.Role) {
		return errors.New("plugin context differs from the authenticated organization identity")
	}
	return nil
}
