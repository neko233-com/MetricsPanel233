package instance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/stretchr/testify/require"
)

// The credential is valid at dispatch entry but needs rotation at the route's
// second credential check, as may happen when the rotation deadline passes.
func TestProtectedOrganizationHTTPRotationAfterAuthenticationIsUnauthorized(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	policy, err := f.auth.NewRequestAuthorizer(func(_ *http.Request, route server.ProtectedRoute, _ identity.Principal) error {
		if route.Pattern == "GET /api/v1/health" {
			return nil
		}
		return identity.ErrForbidden
	})
	require.NoError(t, err)
	f.primary.Authorizer = policy
	lookup := f.registry.config.OrganizationLookup
	f.registry.config.OrganizationLookup = func(ctx context.Context, orgID int64) (identity.Organization, error) {
		setIdentitySessionTime(t, f.catalog.Path(), session.ID, time.Now().Add(-time.Hour).UnixMilli())
		return lookup(ctx, orgID)
	}
	h, err := NewProtectedOrganizationHandler(f.registry, f.auth, policy)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, cookieRequest("/api/v1/health", key))
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestProtectedOrganizationHTTPOriginBeforeCatalogAndDeviceMutation(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	policy, err := f.auth.NewRequestAuthorizer(func(*http.Request, server.ProtectedRoute, identity.Principal) error { return nil })
	require.NoError(t, err)
	f.primary.Authorizer = policy
	lookup := f.registry.config.OrganizationLookup
	calls := 0
	f.registry.config.OrganizationLookup = func(ctx context.Context, orgID int64) (identity.Organization, error) {
		calls++
		return lookup(ctx, orgID)
	}
	h, err := NewProtectedOrganizationHandler(f.registry, f.auth, policy)
	require.NoError(t, err)
	for _, path := range []string{"/api/v1/health", "/api/live/ws", "/public/plugins/any/module.js", "/prometheus/api/v1/query"} {
		r := cookieRequest(path, key)
		r.Header.Set("Origin", "http://foreign.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, http.StatusForbidden, w.Code)
	}
	require.Zero(t, calls)
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, session.LastSeen, stored[0].LastSeen)
	require.False(t, stored[0].TokenSeen)
}

func TestHTTPAuthenticationExactIssuerCannotBorrowRequestMetadata(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	bound, err := f.auth.AuthenticateRequest(cookieRequest("/api/user/auth-tokens", key))
	require.NoError(t, err)
	p, present := identity.FromContext(bound.Context())
	require.True(t, present)
	other, err := NewHTTPAuthenticator(f.catalog, HTTPAuthenticationConfig{})
	require.NoError(t, err)
	_, err = other.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, ok := other.Metadata(bound.Context())
	require.False(t, ok)
	metadata, ok := f.auth.Metadata(bound.Context())
	require.True(t, ok)
	require.Equal(t, session.ID, metadata.SessionID)
	forged := p
	forged.ID++
	ctx := identity.WithPrincipal(bound.Context(), forged)
	_, err = f.auth.Revalidate(ctx, forged)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, ok = f.auth.Metadata(ctx)
	require.False(t, ok)
}
