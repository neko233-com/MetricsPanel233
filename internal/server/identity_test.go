package server_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestOrganizationHandlerRequiresTrustedMatchingPrincipal(t *testing.T) {
	db, err := store.OpenForOrganization(filepath.Join(t.TempDir(), "control.db"), 2)
	require.NoError(t, err)
	defer db.DB.Close()
	handler := server.New(db, "legacy-shared-token", 30).Handler()
	principal := identity.Principal{OrgID: 2, ID: 5, Type: identity.TypeUser, Login: "operator", Name: "Operator", Role: identity.RoleViewer}
	for _, tc := range []struct {
		name      string
		principal *identity.Principal
		status    int
	}{
		{"missing", nil, http.StatusUnauthorized},
		{"matching viewer", &principal, http.StatusOK},
		{"cross organization", func() *identity.Principal { p := principal; p.OrgID = 1; p.IsServerAdmin = true; return &p }(), http.StatusForbidden},
		{"invalid", func() *identity.Principal { p := principal; p.ID = 0; return &p }(), http.StatusUnauthorized},
		{"none role", func() *identity.Principal { p := principal; p.Role = identity.RoleNone; return &p }(), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/health", nil)
			// Neither an old shared token nor client-claimed identity headers select
			// or authorize another organization.
			r.Header.Set("Authorization", "Bearer legacy-shared-token")
			r.Header.Set("X-Grafana-Org-Id", "2")
			r.Header.Set("X-Grafana-User", "admin")
			if tc.principal != nil {
				r = r.WithContext(identity.WithPrincipal(r.Context(), *tc.principal))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
	// Instance authentication already resolved this context, so a second legacy
	// shared token is unnecessary; origin protection still applies.
	r := httptest.NewRequest("GET", "/api/v1/health", nil)
	r = r.WithContext(identity.WithPrincipal(r.Context(), principal))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r.Header.Set("Origin", "https://elsewhere.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestLegacyOrganizationDoesNotFallBackFromInvalidPrincipal(t *testing.T) {
	_, handler := setup(t, "legacy-shared-token")
	require.Equal(t, http.StatusUnauthorized, call(handler, "GET", "/api/v1/health", "", "", "").Code)
	require.Equal(t, http.StatusOK, call(handler, "GET", "/api/v1/health", "", "legacy-shared-token", "").Code)
	invalid := identity.Principal{OrgID: 1, ID: 7, Type: identity.TypeServiceAccount, Login: "agent", Role: identity.RoleAdmin, IsServerAdmin: true}
	r := httptest.NewRequest("GET", "/api/v1/health", nil)
	r.Header.Set("Authorization", "Bearer legacy-shared-token")
	r = r.WithContext(identity.WithPrincipal(r.Context(), invalid))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}
