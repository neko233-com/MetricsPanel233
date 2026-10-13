package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readResponseResolver() store.PermissionIdentityResolver {
	return store.PermissionIdentityResolver{
		CurrentPrincipal: func(_ context.Context, p identity.Principal) (identity.Principal, error) { return p, nil },
		ValidateSubject:  func(context.Context, int64, model.ResourcePermissionSubject) (int64, error) { return 1, nil },
		TeamsForPrincipal: func(context.Context, int64, model.ResourcePermissionSubject) ([]identity.TeamGrant, error) {
			return nil, nil
		},
	}
}

// Credential/catalog and actual row checks are covered by the real HTTP tests
// in instance. This test exercises finite response buffering and header discard.
func TestResourceReadResponseBudgetNeverSendsAlreadyBufferedContent(t *testing.T) {
	s := authorizationServer(t)
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
		p, _ := identity.FromContext(r.Context())
		return p, s.BindResourceReadAuthorization(r, route, readResponseResolver())
	})
	manifest := []ProtectedRoute{}
	mux := s.authorizationMux(http.NewServeMux(), RouteAPI, &manifest)
	mux.HandleFunc("GET /api/v1/dashboards", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test-Private", "private response header")
		_, err := w.Write([]byte("BUFFERED_PRIVATE_PREFIX"))
		require.NoError(t, err)
		_, err = w.Write(bytes.Repeat([]byte("x"), resourceReadResponseBudget))
		require.Error(t, err)
	})
	r := httptest.NewRequest("GET", "/api/v1/dashboards", nil)
	r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.NotContains(t, w.Body.String(), "BUFFERED_PRIVATE_PREFIX")
	assert.Empty(t, w.Header().Get("X-Test-Private"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestResourceReadResponseRejectsWrongServerAndLatePhysicalOwner(t *testing.T) {
	for _, fault := range []string{"wrong-server", "changed-physical-owner"} {
		t.Run(fault, func(t *testing.T) {
			s := authorizationServer(t)
			other := authorizationServer(t)
			s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
				p, _ := identity.FromContext(r.Context())
				selected := s
				if fault == "wrong-server" {
					selected = other
				}
				return p, selected.BindResourceReadAuthorization(r, route, readResponseResolver())
			})
			manifest := []ProtectedRoute{}
			mux := s.authorizationMux(http.NewServeMux(), RouteAPI, &manifest)
			called := false
			mux.HandleFunc("GET /api/v1/dashboards", func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, err := s.Store.DB.Exec(`UPDATE store_identity SET organization_id=2 WHERE id=1`)
				require.NoError(t, err)
				_, _ = w.Write([]byte("[]"))
			})
			r := httptest.NewRequest("GET", "/api/v1/dashboards", nil)
			r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Equal(t, fault != "wrong-server", called)
			assert.NotEqual(t, "[]", w.Body.String())
		})
	}
}
