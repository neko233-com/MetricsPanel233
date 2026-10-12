package server

import (
	"context"
	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

func (s *Server) organizationPrincipal(ctx context.Context) (identity.Principal, error) {
	p, valid := identity.FromContext(ctx)
	if !identity.HasPrincipal(ctx) && s.Store.OrganizationID() == 1 {
		p, valid = identity.LocalPrincipal(), true
	}
	if !valid {
		return p, identity.ErrUnauthenticated
	}
	if err := p.RequireOrganization(s.Store.OrganizationID()); err != nil {
		return p, err
	}
	return p, nil
}
