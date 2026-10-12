package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationLegacyCookiesCannotBypassConfiguredPolicy(t *testing.T) {
	s := authorizationServer(t)
	calls := 0
	s.Authorizer = RequestAuthorizerFunc(func(*http.Request, ProtectedRoute) (identity.Principal, error) {
		calls++
		return identity.Principal{}, identity.ErrForbidden
	})
	h := s.Handler()
	expires := time.Now().Add(time.Minute).Unix()
	for _, path := range []string{"/api/live/ws", "/public/plugins/probe/module.js"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer legacy-shared-token")
		r.AddCookie(&http.Cookie{Name: "metricspanel-live", Value: s.liveSession(expires)})
		r.AddCookie(&http.Cookie{Name: "metricspanel-plugin-assets", Value: s.assetSession(expires)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, 401, w.Code)
		p := authorizationPrincipal()
		r = r.WithContext(identity.WithPrincipal(r.Context(), p))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, 403, w.Code)
	}
	require.Equal(t, 2, calls)
}

func TestAuthorizationPrometheusFallbackAndNestedProxyRemainGuarded(t *testing.T) {
	s := authorizationServer(t)
	var calls []ProtectedRoute
	s.Authorizer = RequestAuthorizerFunc(func(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
		calls = append(calls, route)
		if route.Surface == RouteAPI {
			p, _ := identity.FromContext(r.Context())
			return p, nil
		}
		return identity.Principal{}, identity.ErrForbidden
	})
	h := s.Handler()
	for _, path := range []string{"/prometheus/api/v1/query?query=up", "/prometheus/not-registered", "/api/datasources/proxy/uid/metricspanel/api/v1/query?query=up"} {
		calls = nil
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(identity.WithPrincipal(r.Context(), authorizationPrincipal()))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, 403, w.Code, path+" "+w.Body.String())
		require.NotEmpty(t, calls)
		require.Equal(t, ProtectedRoute{RoutePrometheus, "/"}, calls[len(calls)-1])
		if strings.HasPrefix(path, "/api/") {
			require.Len(t, calls, 2)
			require.Equal(t, ProtectedRoute{RouteAPI, "/api/datasources/proxy/uid/{uid}/{rest...}"}, calls[0])
		}
	}
}

func TestAuthorizationNilKeepsExistingLocalTokenAndRoleBoundary(t *testing.T) {
	s := authorizationServer(t)
	h := s.Handler()
	for _, tc := range []struct {
		principal *identity.Principal
		token     string
		status    int
	}{
		{nil, "", 401}, {nil, "legacy-shared-token", 200},
		{pointerPrincipal(authorizationPrincipal()), "legacy-shared-token", 403},
		{pointerPrincipal(identity.LocalPrincipal()), "", 200},
	} {
		r := httptest.NewRequest("GET", "/api/v1/health", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.principal != nil {
			r = r.WithContext(identity.WithPrincipal(r.Context(), *tc.principal))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, tc.status, w.Code, w.Body.String())
	}
}
