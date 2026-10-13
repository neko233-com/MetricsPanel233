package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func currentUserHandler(t *testing.T, f *httpAuthenticationFixture) http.Handler {
	t.Helper()
	h, err := NewCurrentUserHandler(f.auth)
	require.NoError(t, err)
	return h
}

func currentUserResponse(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func revokeRequest(key, payload string) *http.Request {
	r := httptest.NewRequest("POST", "http://example.test/api/user/revoke-auth-token", strings.NewReader(payload))
	r.AddCookie(&http.Cookie{Name: "grafana_session", Value: key})
	return r
}

func TestCurrentUserHTTPRealServerProfileOrganizationsAndDevices(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	org, _ := activate(t, f.registry, f.catalog, "Second team")
	_, err := f.catalog.AddOrganizationUser(context.Background(), org.ID, f.user.ID, identity.RoleEditor)
	require.NoError(t, err)
	_, device, secret, err := f.catalog.LoginWithMetadata(context.Background(), f.user.Login, httpFixturePassword, 1, time.Hour, time.Hour, identity.SessionMetadataInput{
		RemoteAddr: "[::ffff:192.0.2.233]:12345",
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71",
	})
	require.NoError(t, err)
	_, otherSecret := f.login(t, f.user)
	h := currentUserHandler(t, f)
	ts := httptest.NewServer(h)
	defer ts.Close()
	client := ts.Client()
	get := func(path string) (int, []byte) {
		r, err := http.NewRequest("GET", ts.URL+path, nil)
		require.NoError(t, err)
		r.AddCookie(&http.Cookie{Name: "grafana_session", Value: secret})
		response, err := client.Do(r)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		assert.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))
		for _, private := range []string{secret, otherSecret, httpFixturePassword, "Mozilla/5.0", "mp233_session_", "token_hash", "previous_token_hash"} {
			assert.NotContains(t, string(body), private)
		}
		return response.StatusCode, body
	}
	status, body := get("/api/user?orgId=2&userId=999")
	require.Equal(t, http.StatusOK, status)
	var profile identity.GrafanaUserProfile
	require.NoError(t, json.Unmarshal(body, &profile))
	assert.Equal(t, f.user.ID, profile.ID)
	assert.Equal(t, f.user.UID, profile.UID)
	assert.Equal(t, int64(1), profile.OrgID, "temporary request context does not mutate the persisted profile organization")
	assert.Equal(t, time.UnixMilli(f.user.CreatedAt).UTC(), profile.CreatedAt)
	status, body = get("/api/user/orgs")
	require.Equal(t, http.StatusOK, status)
	var orgs []identity.GrafanaUserOrganization
	require.NoError(t, json.Unmarshal(body, &orgs))
	require.Len(t, orgs, 2)
	assert.Equal(t, org.ID, orgs[1].OrgID)
	assert.Equal(t, identity.RoleEditor, orgs[1].Role)
	status, body = get("/api/user/auth-tokens?authTokenId=999")
	require.Equal(t, http.StatusOK, status)
	var devices []identity.GrafanaAuthToken
	require.NoError(t, json.Unmarshal(body, &devices))
	require.Len(t, devices, 2)
	var current int
	for _, token := range devices {
		if token.IsActive {
			current++
			assert.Equal(t, device.ID, token.ID)
			assert.Equal(t, "192.0.2.233", token.ClientIP)
			assert.Equal(t, "Edge", token.Browser)
			assert.Equal(t, "141.0", token.BrowserVersion)
			assert.Equal(t, "Windows", token.OS)
			assert.Equal(t, "", token.AuthModule)
		}
	}
	assert.Equal(t, 1, current)
	status, _ = get("/api/login/ping")
	assert.Equal(t, http.StatusOK, status)
}

func TestCurrentUserHTTPRevocationOwnershipAndActualActiveSession(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	active, activeKey := f.login(t, f.user)
	second, secondKey := f.login(t, f.user)
	foreign, err := f.catalog.CreateUser(context.Background(), identity.UserInput{Login: "foreign", Password: httpFixturePassword, Role: identity.RoleViewer, OrgID: 1})
	require.NoError(t, err)
	foreignSession, foreignKey := f.login(t, foreign)
	h := currentUserHandler(t, f)
	for _, target := range []int64{foreignSession.ID, 999999} {
		w := currentUserResponse(h, revokeRequest(activeKey, fmt.Sprintf(`{"authTokenId":%d}`, target)))
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, "{\"message\":\"Auth token not found\"}\n", w.Body.String(), "foreign and absent identifiers have the same response")
	}
	w := currentUserResponse(h, revokeRequest(activeKey, fmt.Sprintf(`{"authTokenId":%d}`, active.ID)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	_, err = f.catalog.AuthenticateSession(context.Background(), activeKey, 1)
	require.NoError(t, err)
	w = currentUserResponse(h, revokeRequest(activeKey, fmt.Sprintf(`{"authTokenId":%d}`, second.ID)))
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = currentUserResponse(h, cookieRequest("/api/login/ping", secondKey))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = currentUserResponse(h, cookieRequest("/api/login/ping", foreignKey))
	assert.Equal(t, http.StatusOK, w.Code)
	// Basic authentication has no active browser device; it can revoke its own
	// remaining browser session, with no request field that overrides active ID.
	r := httptest.NewRequest("POST", "http://example.test/api/user/revoke-auth-token", strings.NewReader(fmt.Sprintf(`{"authTokenId":%d}`, active.ID)))
	r.SetBasicAuth(f.user.Login, httpFixturePassword)
	w = currentUserResponse(h, r)
	assert.Equal(t, http.StatusOK, w.Code)
	w = currentUserResponse(h, cookieRequest("/api/login/ping", activeKey))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

type untouchedRequestBody struct{ reads int }

func (b *untouchedRequestBody) Read([]byte) (int, error) { b.reads++; return 0, errorsNewUnreadBody }
func (*untouchedRequestBody) Close() error               { return nil }

var errorsNewUnreadBody = fmt.Errorf("body must not be read before authorization")

func TestCurrentUserHTTPRejectsServiceIdentityBeforeReadingBody(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	account, err := f.catalog.CreateServiceAccount(context.Background(), 1, identity.ServiceAccountInput{Name: "Same numeric identity", Role: identity.RoleAdmin})
	require.NoError(t, err)
	assert.Equal(t, f.user.ID, account.ID)
	_, key, err := f.catalog.CreateServiceAccountToken(context.Background(), 1, account.ID, "agent", 0)
	require.NoError(t, err)
	h := currentUserHandler(t, f)
	for _, path := range []string{"/api/user", "/api/user/orgs", "/api/user/auth-tokens"} {
		r := httptest.NewRequest("GET", "http://example.test"+path, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := currentUserResponse(h, r)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.NotContains(t, w.Body.String(), f.user.Login)
	}
	body := &untouchedRequestBody{}
	r := httptest.NewRequest("POST", "http://example.test/api/user/revoke-auth-token", nil)
	r.Body = body
	r.Header.Set("Authorization", "Bearer "+key)
	w := currentUserResponse(h, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Zero(t, body.reads)
	r = httptest.NewRequest("GET", "http://example.test/api/login/ping", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w = currentUserResponse(h, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCurrentUserHTTPInvalidInputDoesNotRevokeAndUnknownRoutesStayAbsent(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	_, key := f.login(t, f.user)
	h := currentUserHandler(t, f)
	for _, payload := range []string{
		`{}`, `{"authTokenId":0}`, `{"authTokenId":-1}`, `{"authTokenId":"1"}`, `{"authTokenId":1.0}`,
		`{"authTokenId":9223372036854775808}`, `{"authTokenId":1}{}`, `{"authTokenId":1,"activeSessionID":0}`,
		`null`, strings.Repeat(" ", 65537) + `{}`, `{"authTokenId":`,
	} {
		w := currentUserResponse(h, revokeRequest(key, payload))
		assert.Equal(t, http.StatusBadRequest, w.Code)
		_, err := f.catalog.AuthenticateSession(context.Background(), key, 1)
		require.NoError(t, err)
	}
	for _, path := range []string{"/api/admin/users", "/api/user/preferences", "/login"} {
		w := currentUserResponse(h, cookieRequest(path, key))
		assert.Equal(t, http.StatusNotFound, w.Code)
	}
	body := &untouchedRequestBody{}
	r := httptest.NewRequest("POST", "http://example.test/api/user/revoke-auth-token", nil)
	r.Body = body
	r.Header.Set("Authorization", "Bearer invalid")
	w := currentUserResponse(h, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Zero(t, body.reads)
}

func TestCurrentUserHTTPRestartPreservesCookieAndRejectsDisabledIdentity(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	u, err := f.catalog.CreateUser(context.Background(), identity.UserInput{Login: "restart-user", Password: httpFixturePassword, Role: identity.RoleNone, OrgID: 1})
	require.NoError(t, err)
	_, key := f.login(t, u)
	path := f.catalog.Path()
	require.NoError(t, f.catalog.Close())
	catalog, err := identity.Open(path)
	require.NoError(t, err)
	defer catalog.Close()
	auth, err := NewHTTPAuthenticator(catalog, HTTPAuthenticationConfig{})
	require.NoError(t, err)
	h, err := NewCurrentUserHandler(auth)
	require.NoError(t, err)
	w := currentUserResponse(h, cookieRequest("/api/user", key))
	assert.Equal(t, http.StatusOK, w.Code, "None role still has its own authenticated profile")
	u, err = catalog.SetUserDisabled(context.Background(), u.ID, u.Version, true)
	require.NoError(t, err)
	w = currentUserResponse(h, cookieRequest("/api/user", key))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.NotContains(t, w.Body.String(), u.Login)
}
