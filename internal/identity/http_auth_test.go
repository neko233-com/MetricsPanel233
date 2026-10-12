package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPRequestAuthenticationSelectsOnlyGrantedOrganizations(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "admin-password-233"})
	require.NoError(t, err)
	account, err := catalog.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Dashboard agent", Role: RoleEditor})
	require.NoError(t, err)
	_, serviceKey, err := catalog.CreateServiceAccountToken(ctx, 1, account.ID, "agent credential", 0)
	require.NoError(t, err)
	_, _, sessionKey, err := catalog.Login(ctx, "admin", "admin-password-233", 1, time.Hour, time.Hour)
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		setup  func(*http.Request)
		method AuthenticationMethod
		fail   bool
	}{
		{"forged headers", func(r *http.Request) { r.Header.Set("X-Grafana-Org-Id", "1"); r.Header.Set("X-Grafana-User", "admin") }, "", true},
		{"password", func(r *http.Request) { r.SetBasicAuth("admin", "admin-password-233") }, AuthenticationPassword, false},
		{"service bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+serviceKey) }, AuthenticationService, false},
		{"service basic", func(r *http.Request) { r.SetBasicAuth("api_key", serviceKey) }, AuthenticationService, false},
		{"browser cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionKey}) }, AuthenticationSession, false},
		{"foreign service org", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+serviceKey)
			r.Header.Set("X-Grafana-Org-Id", "2")
		}, "", true},
		{"foreign password org", func(r *http.Request) {
			r.SetBasicAuth("admin", "admin-password-233")
			r.Header.Set("X-Grafana-Org-Id", "2")
		}, "", true},
		{"malformed organization", func(r *http.Request) {
			r.SetBasicAuth("admin", "admin-password-233")
			r.Header.Set("X-Grafana-Org-Id", "garbage")
		}, "", true},
		{"duplicate organization", func(r *http.Request) {
			r.SetBasicAuth("admin", "admin-password-233")
			r.Header.Add("X-Grafana-Org-Id", "1")
			r.Header.Add("X-Grafana-Org-Id", "2")
		}, "", true},
		{"duplicate authorization", func(r *http.Request) {
			r.Header.Add("Authorization", "Bearer "+serviceKey)
			r.Header.Add("Authorization", "Bearer "+serviceKey)
		}, "", true},
		{"duplicate cookies", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionKey})
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionKey})
		}, "", true},
		{"bad credential cannot downgrade to cookie", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer invalid")
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionKey})
		}, "", true},
		{"blank authorization cannot downgrade to cookie", func(r *http.Request) {
			r.Header.Set("Authorization", "")
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionKey})
		}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/ds/query", nil)
			tc.setup(r)
			auth, err := catalog.AuthenticateRequest(r)
			if tc.fail {
				require.ErrorIs(t, err, ErrUnauthenticated)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.method, auth.Method)
			require.EqualValues(t, 1, auth.Principal.OrgID)
			serialized, err := json.Marshal(auth)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), serviceKey)
			require.NotContains(t, string(serialized), sessionKey)
			if tc.method == AuthenticationService {
				require.Equal(t, TypeServiceAccount, auth.Principal.Type)
				require.False(t, auth.Principal.IsServerAdmin)
			}
		})
	}
}
