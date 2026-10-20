package localauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openedCatalog(t *testing.T) *identity.Store {
	t.Helper()
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	return catalog
}

func buildHandler(t *testing.T, catalog *identity.Store, legacyToken string) *Handler {
	t.Helper()
	authenticator, err := instance.NewHTTPAuthenticator(catalog, instance.HTTPAuthenticationConfig{
		CookieName: identity.SessionCookieName, RotationInterval: time.Hour,
	})
	require.NoError(t, err)
	handler, err := New(Config{
		Catalog: catalog, Authenticator: authenticator,
		CookieName: identity.SessionCookieName, LegacyToken: legacyToken,
	})
	require.NoError(t, err)
	return handler
}

func newHandler(t *testing.T, legacyToken string) (*Handler, *identity.Store) {
	t.Helper()
	catalog := openedCatalog(t)
	return buildHandler(t, catalog, legacyToken), catalog
}

func newHandlerWithAdmin(t *testing.T, legacyToken string) (*Handler, *identity.Store) {
	t.Helper()
	catalog := openedCatalog(t)
	bootstrapAdmin(t, catalog)
	return buildHandler(t, catalog, legacyToken), catalog
}

func TestClientIPOnlyTrustsForwardedForFromConfiguredProxies(t *testing.T) {
	_, network, err := net.ParseCIDR("10.0.0.1/32")
	require.NoError(t, err)
	handler := &Handler{trusted: []*net.IPNet{network}}

	trusted := httptest.NewRequest("POST", "/login", nil)
	trusted.RemoteAddr = "10.0.0.1:1234"
	trusted.Header.Set("X-Forwarded-For", "203.0.113.9")
	assert.Equal(t, "203.0.113.9", handler.clientIP(trusted))

	untrusted := httptest.NewRequest("POST", "/login", nil)
	untrusted.RemoteAddr = "198.51.100.5:1234"
	untrusted.Header.Set("X-Forwarded-For", "203.0.113.9")
	assert.Equal(t, "198.51.100.5", handler.clientIP(untrusted))

	chain := httptest.NewRequest("POST", "/login", nil)
	chain.RemoteAddr = "10.0.0.1:1234"
	chain.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	assert.Equal(t, "203.0.113.9", handler.clientIP(chain))
}

func bootstrapAdmin(t *testing.T, catalog *identity.Store) {
	t.Helper()
	_, err := catalog.BootstrapAdmin(context.Background(), identity.UserInput{
		Login: "admin", Name: "Administrator", Email: "admin@example.com", Password: "correct-horse-233", OrgID: 1,
	})
	require.NoError(t, err)
}

func serve(h *Handler, next http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	h.Wrap(next).ServeHTTP(recorder, request)
	return recorder
}

func cookieValue(w *httptest.ResponseRecorder, name string) string {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func nextHandler(t *testing.T) (http.Handler, *int) {
	t.Helper()
	calls := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if _, ok := identity.FromContext(r.Context()); !ok {
			t.Error("next handler received no authenticated principal")
		}
		w.WriteHeader(http.StatusOK)
	}), &calls
}

func TestSetupCreatesTheAdministratorAndSession(t *testing.T) {
	handler, _ := newHandler(t, "")
	next, calls := nextHandler(t)
	require.True(t, handler.SetupRequired())
	require.NotEmpty(t, handler.SetupToken())

	status := serve(handler, next, "GET", "/api/auth/status", "", nil)
	require.Equal(t, 200, status.Code)
	assert.Contains(t, status.Body.String(), `"setupRequired":true`)

	weak := serve(handler, next, "POST", "/api/setup", `{"token":"`+handler.SetupToken()+`","login":"admin","name":"Admin","email":"admin@example.com","password":"short"}`, nil)
	require.Equal(t, 400, weak.Code)

	created := serve(handler, next, "POST", "/api/setup", `{"token":"`+handler.SetupToken()+`","login":"admin","name":"Admin","email":"admin@example.com","password":"correct-horse-233"}`, nil)
	require.Equal(t, 200, created.Code, created.Body.String())
	require.NotEmpty(t, cookieValue(created, identity.SessionCookieName))
	assert.False(t, handler.SetupRequired())
	assert.Empty(t, handler.SetupToken())

	me := serve(handler, next, "GET", "/api/auth/me", "", map[string]string{"Cookie": identity.SessionCookieName + "=" + cookieValue(created, identity.SessionCookieName)})
	require.Equal(t, 200, me.Code)
	assert.Contains(t, me.Body.String(), `"role":"Admin"`)
	assert.Contains(t, me.Body.String(), `"login":"admin"`)

	dashboards := serve(handler, next, "GET", "/api/v1/dashboards", "", map[string]string{"Cookie": identity.SessionCookieName + "=" + cookieValue(created, identity.SessionCookieName)})
	require.Equal(t, 200, dashboards.Code)
	assert.Equal(t, 1, *calls)
}

func TestLoginRejectsWrongPasswordAndEnforcesRoles(t *testing.T) {
	handler, _ := newHandlerWithAdmin(t, "")
	require.False(t, handler.SetupRequired())
	next, calls := nextHandler(t)

	wrong := serve(handler, next, "POST", "/login", `{"user":"admin","password":"wrong-password"}`, nil)
	require.Equal(t, 401, wrong.Code)

	login := serve(handler, next, "POST", "/login", `{"user":"admin","password":"correct-horse-233"}`, nil)
	require.Equal(t, 200, login.Code, login.Body.String())
	adminCookie := cookieValue(login, identity.SessionCookieName)
	require.NotEmpty(t, adminCookie)

	created := serve(handler, next, "POST", "/api/admin/users", `{"login":"viewer","name":"Viewer","email":"viewer@example.com","password":"viewer-password-233","role":"Viewer"}`, map[string]string{"Cookie": identity.SessionCookieName + "=" + adminCookie})
	require.Equal(t, 201, created.Code, created.Body.String())

	list := serve(handler, next, "GET", "/api/admin/users", "", map[string]string{"Cookie": identity.SessionCookieName + "=" + adminCookie})
	require.Equal(t, 200, list.Code)
	assert.Contains(t, list.Body.String(), `"login":"viewer"`)

	viewerLogin := serve(handler, next, "POST", "/login", `{"user":"viewer","password":"viewer-password-233"}`, nil)
	require.Equal(t, 200, viewerLogin.Code, viewerLogin.Body.String())
	viewerCookie := identity.SessionCookieName + "=" + cookieValue(viewerLogin, identity.SessionCookieName)

	read := serve(handler, next, "GET", "/api/v1/dashboards", "", map[string]string{"Cookie": viewerCookie})
	require.Equal(t, 200, read.Code)
	query := serve(handler, next, "POST", "/api/ds/query", `{}`, map[string]string{"Cookie": viewerCookie})
	require.Equal(t, 200, query.Code)
	forbidden := serve(handler, next, "POST", "/api/v1/dashboards", `{}`, map[string]string{"Cookie": viewerCookie})
	require.Equal(t, 403, forbidden.Code)
	adminOnly := serve(handler, next, "GET", "/api/admin/users", "", map[string]string{"Cookie": viewerCookie})
	require.Equal(t, 403, adminOnly.Code)
	assert.Equal(t, 2, *calls)
}

func TestGrafanaCompatibilityIdentityEndpoints(t *testing.T) {
	handler, _ := newHandlerWithAdmin(t, "")
	next, _ := nextHandler(t)
	login := serve(handler, next, "POST", "/login", `{"user":"admin","password":"correct-horse-233"}`, nil)
	require.Equal(t, 200, login.Code)
	cookie := identity.SessionCookieName + "=" + cookieValue(login, identity.SessionCookieName)

	ping := serve(handler, next, "GET", "/api/login/ping", "", map[string]string{"Cookie": cookie})
	require.Equal(t, 200, ping.Code)
	assert.Contains(t, ping.Body.String(), "Logged in")

	unauthenticated := serve(handler, next, "GET", "/api/login/ping", "", nil)
	require.Equal(t, 401, unauthenticated.Code)

	profile := serve(handler, next, "GET", "/api/user", "", map[string]string{"Cookie": cookie})
	require.Equal(t, 200, profile.Code)
	assert.Contains(t, profile.Body.String(), `"login":"admin"`)
	assert.Contains(t, profile.Body.String(), `"isGrafanaAdmin":true`)

	orgs := serve(handler, next, "GET", "/api/user/orgs", "", map[string]string{"Cookie": cookie})
	require.Equal(t, 200, orgs.Code)
	assert.Contains(t, orgs.Body.String(), `"orgId":1`)

	logout := serve(handler, next, "POST", "/api/logout", "", map[string]string{"Cookie": cookie})
	require.Equal(t, 200, logout.Code)
	revoked := serve(handler, next, "GET", "/api/login/ping", "", map[string]string{"Cookie": cookie})
	require.Equal(t, 401, revoked.Code)
}

func TestLegacyBearerTokenStillWorksForAutomation(t *testing.T) {
	handler, _ := newHandler(t, "legacy-secret-16chars")
	next, calls := nextHandler(t)
	ok := serve(handler, next, "GET", "/api/v1/dashboards", "", map[string]string{"Authorization": "Bearer legacy-secret-16chars"})
	require.Equal(t, 200, ok.Code)
	assert.Equal(t, 1, *calls)
	bad := serve(handler, next, "GET", "/api/v1/dashboards", "", map[string]string{"Authorization": "Bearer wrong-secret"})
	require.Equal(t, 401, bad.Code)
	missing := serve(handler, next, "GET", "/api/v1/dashboards", "", nil)
	require.Equal(t, 401, missing.Code)
}

func TestLoginRateLimitBlocksRepeatedFailures(t *testing.T) {
	handler, _ := newHandlerWithAdmin(t, "")
	next, _ := nextHandler(t)
	for i := 0; i < 8; i++ {
		response := serve(handler, next, "POST", "/login", `{"user":"admin","password":"wrong-password"}`, nil)
		require.Equal(t, 401, response.Code)
	}
	blocked := serve(handler, next, "POST", "/login", `{"user":"admin","password":"correct-horse-233"}`, nil)
	require.Equal(t, 429, blocked.Code)
}

func TestUserUpdateRequiresVersionsAndBlocksSelfChanges(t *testing.T) {
	handler, _ := newHandlerWithAdmin(t, "")
	next, _ := nextHandler(t)
	login := serve(handler, next, "POST", "/login", `{"user":"admin","password":"correct-horse-233"}`, nil)
	cookie := identity.SessionCookieName + "=" + cookieValue(login, identity.SessionCookieName)
	headers := map[string]string{"Cookie": cookie}

	self := serve(handler, next, "PUT", "/api/admin/users/1", `{"version":1,"membershipVersion":1,"disabled":true}`, headers)
	require.Equal(t, 400, self.Code)

	created := serve(handler, next, "POST", "/api/admin/users", `{"login":"editor","name":"Editor","email":"editor@example.com","password":"editor-password-233","role":"Editor"}`, headers)
	require.Equal(t, 201, created.Code, created.Body.String())
	var user userAdminView
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &user))

	missing := serve(handler, next, "PUT", "/api/admin/users/"+strconv.FormatInt(user.ID, 10), `{"role":"Viewer"}`, headers)
	require.Equal(t, 400, missing.Code)

	updated := serve(handler, next, "PUT", "/api/admin/users/"+strconv.FormatInt(user.ID, 10), `{"version":`+strconv.FormatInt(user.Version, 10)+`,"membershipVersion":`+strconv.FormatInt(user.MembershipVersion, 10)+`,"role":"Admin"}`, headers)
	require.Equal(t, 200, updated.Code, updated.Body.String())
	assert.Contains(t, updated.Body.String(), `"role":"Admin"`)
}
