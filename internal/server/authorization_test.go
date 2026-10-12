package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

type unreadAuthorizationBody struct{ reads int }

func (b *unreadAuthorizationBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("a denied handler read the body")
}
func (*unreadAuthorizationBody) Close() error { return nil }

func authorizationServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	s := New(db, "legacy-shared-token", 30)
	t.Cleanup(func() { s.Live.Close(); s.Plugins.Close(); require.NoError(t, db.DB.Close()) })
	return s
}

func authorizationPrincipal() identity.Principal {
	return identity.Principal{OrgID: 1, ID: 23, Type: identity.TypeUser, Login: "reader", Role: identity.RoleNone}
}

func TestEveryRegisteredProtectedRouteChecksPolicyBeforeHandler(t *testing.T) {
	s := authorizationServer(t)
	var observed ProtectedRoute
	var selectedPattern string
	calls := 0
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
		calls++
		observed, selectedPattern = route, r.Pattern
		return identity.Principal{}, identity.ErrForbidden
	})
	handler, manifest := s.handlerWithRouteManifest()
	require.Greater(t, len(manifest), 100)
	seen := map[ProtectedRoute]bool{}
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range manifest {
		require.False(t, seen[route], "duplicate registration: %+v", route)
		seen[route] = true
		method, path, hasMethod := strings.Cut(route.Pattern, " ")
		if !hasMethod {
			method, path = http.MethodPatch, route.Pattern
		}
		path = parameter.ReplaceAllStringFunc(path, func(value string) string {
			if value == "{namespace}" {
				return "default"
			}
			if strings.Contains(value, "...") {
				return "probe/resource"
			}
			return "probe"
		})
		if route.Surface == RoutePrometheus {
			if path == "/" {
				path = "/unknown-probe-operation"
			}
			path = "/prometheus" + path
		}
		t.Run(string(route.Surface)+" "+route.Pattern, func(t *testing.T) {
			body := &unreadAuthorizationBody{}
			r := httptest.NewRequest(method, path, body)
			r.Header.Set("Content-Type", "application/json")
			r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
			calls = 0
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Equal(t, 1, calls)
			require.Equal(t, route, observed)
			require.Equal(t, route.Pattern, selectedPattern)
			require.Zero(t, body.reads)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		})
	}
}

func TestRoutePolicyGetsDecodedPathValuesAndFreshPrincipal(t *testing.T) {
	s := authorizationServer(t)
	incoming := authorizationPrincipal()
	incoming.Role = identity.RoleAdmin
	current := incoming
	current.Role, current.Name = identity.RoleViewer, "Current name"
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
		require.Equal(t, ProtectedRoute{RouteAPI, "GET /api/probe/{uid}/{rest...}"}, route)
		require.Equal(t, "a/b", r.PathValue("uid"))
		require.Equal(t, "first/second", r.PathValue("rest"))
		return current, nil
	})
	manifest := []ProtectedRoute{}
	mux := s.authorizationMux(http.NewServeMux(), RouteAPI, &manifest)
	mux.HandleFunc("GET /api/probe/{uid}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := identity.FromContext(r.Context())
		require.True(t, ok)
		require.Equal(t, current, p)
		require.Equal(t, "a/b", r.PathValue("uid"))
		w.WriteHeader(http.StatusNoContent)
	})
	// HEAD inherits GET's exact operation; it is not classified as a write.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "/api/probe/a%2Fb/first/second", nil)
		r = r.WithContext(identity.WithPrincipal(r.Context(), incoming))
		w := httptest.NewRecorder()
		s.protect(mux).ServeHTTP(w, r)
		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	}
}

func TestRoutePolicyCannotChangeIdentityAndDoesNotExposeProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal identity.Principal
		err       error
		status    int
	}{
		{"revoked", identity.Principal{}, identity.ErrUnauthenticated, http.StatusUnauthorized},
		{"denied", identity.Principal{}, identity.ErrForbidden, http.StatusForbidden},
		{"provider failure", identity.Principal{}, errors.New("secret-token-password"), http.StatusInternalServerError},
		{"cancelled", identity.Principal{}, context.Canceled, http.StatusInternalServerError},
		{"different user", func() identity.Principal { p := authorizationPrincipal(); p.ID++; return p }(), nil, http.StatusForbidden},
		{"different organization", func() identity.Principal { p := authorizationPrincipal(); p.OrgID++; return p }(), nil, http.StatusForbidden},
		{"different kind", identity.Principal{OrgID: 1, ID: 23, Type: identity.TypeServiceAccount, Login: "agent", Role: identity.RoleNone}, nil, http.StatusForbidden},
		{"invalid principal", identity.Principal{}, nil, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := authorizationServer(t)
			s.Authorizer = RequestAuthorizerFunc(func(*http.Request, ProtectedRoute) (identity.Principal, error) { return tc.principal, tc.err })
			manifest := []ProtectedRoute{}
			mux := s.authorizationMux(http.NewServeMux(), RouteAPI, &manifest)
			called := false
			mux.HandleFunc("POST /api/probe", func(http.ResponseWriter, *http.Request) { called = true })
			r := httptest.NewRequest(http.MethodPost, "/api/probe", nil)
			r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
			w := httptest.NewRecorder()
			s.protect(mux).ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.False(t, called)
			require.NotContains(t, w.Body.String(), "secret-token-password")
		})
	}
}

func TestConfiguredPolicyNeverUsesLegacyOrInternalPrincipalFallback(t *testing.T) {
	s := authorizationServer(t)
	calls := 0
	s.Authorizer = RequestAuthorizerFunc(func(*http.Request, ProtectedRoute) (identity.Principal, error) {
		calls++
		return authorizationPrincipal(), nil
	})
	handler := s.Handler()
	for _, p := range []*identity.Principal{nil, pointerPrincipal(identity.LocalPrincipal()), pointerPrincipal(identity.BackgroundPrincipal(1))} {
		for _, path := range []string{"/api/v1/health", "/prometheus/api/v1/query", "/api/live/ws", "/public/plugins/probe/module.js", "/metrics"} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Authorization", "Bearer legacy-shared-token")
			if p != nil {
				r = r.WithContext(identity.WithPrincipal(r.Context(), *p))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, http.StatusUnauthorized, w.Code, path+" "+w.Body.String())
		}
	}
	require.Zero(t, calls)
	// Public bootstrap files remain reachable for the future login application.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, w.Code)
	_, err := io.ReadAll(w.Result().Body)
	require.NoError(t, err)
}

func pointerPrincipal(p identity.Principal) *identity.Principal { return &p }

func TestConfiguredPolicyAllowsAnExplicitNoneGrantAndRejectsMissingProvider(t *testing.T) {
	s := authorizationServer(t)
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
		if route != (ProtectedRoute{RouteAPI, "GET /api/v1/health"}) {
			return identity.Principal{}, identity.ErrForbidden
		}
		p, _ := identity.FromContext(r.Context())
		return p, nil
	})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// A typed nil provider must fail closed, rather than panic or use local mode.
	s.Authorizer = RequestAuthorizerFunc(nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}

func TestConfiguredPolicyChecksOriginBeforeCredentialRevalidation(t *testing.T) {
	s := authorizationServer(t)
	calls := 0
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, _ ProtectedRoute) (identity.Principal, error) {
		calls++
		p, _ := identity.FromContext(r.Context())
		return p, nil
	})
	handler := s.Handler()
	for _, path := range []string{"/api/v1/health", "/prometheus/api/v1/query", "/metrics", "/api/live/ws", "/public/plugins/probe/module.js"} {
		t.Run(path, func(t *testing.T) {
			calls = 0
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Origin", "https://foreign.example")
			r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Zero(t, calls, "cross-origin rejection must precede credential or permission callbacks")
		})
	}
}
