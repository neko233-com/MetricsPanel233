package instance

import (
	"errors"
	"net/http"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/server"
)

// NewResourceReadPolicy is an opt-in, limited policy for the eight implemented
// current dashboard/folder read registrations. It denies history, query/plugin/
// Live operations and every mutation. It is not the complete production policy.
// The original single-workspace CLI server does not install this adapter.
func NewResourceReadPolicy(registry *Registry, authenticator *HTTPAuthenticator) (RoutePermissionPolicy, error) {
	if registry == nil || authenticator == nil || authenticator.catalog == nil {
		return nil, errors.New("an organization registry and authenticator are required")
	}
	resolver, err := authenticator.PermissionResolver()
	if err != nil {
		return nil, err
	}
	return func(r *http.Request, route server.ProtectedRoute, p identity.Principal) error {
		if !server.ResourceReadRoute(route) {
			return identity.ErrForbidden
		}
		selected, err := registry.Resolve(r.Context(), p.OrgID)
		if err != nil {
			return err
		}
		if selected.OrganizationID() != p.OrgID {
			return identity.ErrForbidden
		}
		return selected.BindResourceReadAuthorization(r, route, resolver)
	}, nil
}
