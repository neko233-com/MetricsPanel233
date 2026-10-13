package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionFixtureServer(t *testing.T, f *httpAuthenticationFixture, config SessionHTTPConfig, tls bool) (*httptest.Server, *http.Client) {
	t.Helper()
	sessions, err := NewSessionHTTPHandler(f.auth, config)
	require.NoError(t, err)
	current, err := NewCurrentUserHandler(f.auth)
	require.NoError(t, err)
	current = http.StripPrefix(strings.TrimSuffix(config.AppSubURL, "/"), current)
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(config.AppSubURL, "/"))
		if path == "/login" || path == "/logout" || strings.HasSuffix(path, "/auth-tokens/rotate") {
			sessions.ServeHTTP(w, r)
		} else {
			current.ServeHTTP(w, r)
		}
	})
	var ts *httptest.Server
	if tls {
		ts = httptest.NewTLSServer(app)
	} else {
		ts = httptest.NewServer(app)
	}
	t.Cleanup(ts.Close)
	client := ts.Client()
	client.Jar, err = cookiejar.New(nil)
	require.NoError(t, err)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return ts, client
}

func sessionHTTPCall(t *testing.T, client *http.Client, method, target, body string) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(method, target, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/141.0.0.0 Safari/537.36 Edg/141.0.3537.71")
	r.Header.Set("X-Forwarded-For", "192.0.2.233")
	response, err := client.Do(r)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, string(data)
}

func loginPayload(f *httpAuthenticationFixture) string {
	data, _ := json.Marshal(map[string]string{"user": f.user.Login, "password": httpFixturePassword})
	return string(data)
}

func authCookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("expected session cookie %s", name)
	return nil
}

func sessionSQL(t *testing.T, catalog *identity.Store, statement string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", catalog.Path())
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(statement, args...)
	require.NoError(t, err)
}

func TestSessionHTTPRealTLSCookieJarLoginDevicesRotateAndLogout(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ts, client := sessionFixtureServer(t, f, SessionHTTPConfig{AppSubURL: "/metrics", CookieSecure: true, CookieSameSite: http.SameSiteStrictMode, SessionLifetime: time.Hour, SessionIdleTimeout: 30 * time.Minute, EnableRotationRedirect: true}, true)
	response, body := sessionHTTPCall(t, client, "POST", ts.URL+"/metrics/login?redirectTo="+url.QueryEscape("/metrics/d/real/dashboard?orgId=1#overview"), loginPayload(f))
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var result map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	require.Equal(t, map[string]string{"message": "Logged in", "redirectUrl": "/metrics/d/real/dashboard?orgId=1#overview"}, result)
	credential := authCookie(t, response, f.auth.cookieName)
	hint := authCookie(t, response, sessionExpiryCookieName)
	for _, cookie := range []*http.Cookie{credential, hint} {
		assert.True(t, cookie.Secure)
		assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
		assert.Equal(t, "/metrics/", cookie.Path)
		assert.Empty(t, cookie.Domain, "the session is host-only")
		assert.Greater(t, cookie.MaxAge, 3500)
		assert.LessOrEqual(t, cookie.MaxAge, 3600)
	}
	assert.True(t, credential.HttpOnly)
	assert.False(t, hint.HttpOnly)
	assert.NotContains(t, body, credential.Value)
	assert.NotContains(t, body, httpFixturePassword)
	devices, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	original := devices[0]
	assert.Equal(t, "127.0.0.1", original.ClientIP, "actual socket address overrides no proxy headers")
	assert.Equal(t, "password", original.AuthModule)
	assert.Contains(t, original.UserAgent, "Edg/141")
	assert.Equal(t, time.UnixMilli(original.ExpiresAt).UTC().Truncate(time.Second), credential.Expires)
	response, body = sessionHTTPCall(t, client, "GET", ts.URL+"/metrics/api/user/auth-tokens", "")
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	var liveDevices []identity.GrafanaAuthToken
	require.NoError(t, json.Unmarshal([]byte(body), &liveDevices))
	require.Len(t, liveDevices, 1)
	assert.Equal(t, original.ID, liveDevices[0].ID)
	assert.True(t, liveDevices[0].IsActive)
	setIdentitySessionTime(t, f.catalog.Path(), original.ID, time.Now().Add(-time.Hour).UnixMilli())
	response, _ = sessionHTTPCall(t, client, "GET", ts.URL+"/metrics/api/login/ping", "")
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	response, body = sessionHTTPCall(t, client, "POST", ts.URL+"/metrics/api/user/auth-tokens/rotate", "")
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	require.Equal(t, "{}\n", body)
	replacement := authCookie(t, response, f.auth.cookieName)
	assert.NotEqual(t, credential.Value, replacement.Value)
	assert.Equal(t, credential.Expires, replacement.Expires, "rotation does not extend absolute expiration")
	devices, err = f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	assert.Equal(t, original.ID, devices[0].ID)
	assert.Equal(t, original.CurrentOrgID, devices[0].CurrentOrgID)
	assert.Equal(t, original.CreatedAt, devices[0].CreatedAt)
	assert.Equal(t, original.ExpiresAt, devices[0].ExpiresAt)
	assert.Equal(t, original.IdleTimeoutMS, devices[0].IdleTimeoutMS)
	response, body = sessionHTTPCall(t, client, "GET", ts.URL+"/metrics/logout", "")
	require.Equal(t, http.StatusFound, response.StatusCode, body)
	assert.Equal(t, "/metrics/login", response.Header.Get("Location"))
	for _, name := range []string{f.auth.cookieName, sessionExpiryCookieName} {
		cookie := authCookie(t, response, name)
		assert.Equal(t, -1, cookie.MaxAge)
		assert.Equal(t, "/metrics/", cookie.Path)
		assert.True(t, cookie.Secure)
	}
	appURL, _ := url.Parse(ts.URL + "/metrics/api/user")
	require.Empty(t, client.Jar.Cookies(appURL))
	for _, key := range []string{credential.Value, replacement.Value} {
		_, err := f.catalog.AuthenticateSession(context.Background(), key, 1)
		require.ErrorIs(t, err, identity.ErrUnauthenticated)
	}
	response, _ = sessionHTTPCall(t, client, "GET", ts.URL+"/metrics/api/user", "")
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}

func TestSessionHTTPOriginAndCredentialConflictsPrecedeBodyAndWrites(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{})
	require.NoError(t, err)
	for _, path := range []string{"/login", "/api/user/auth-tokens/rotate", "/logout"} {
		for _, fault := range []string{"origin", "fetch-site", "explicit-basic", "explicit-bearer", "empty-auth", "duplicate-cookie", "query-password"} {
			t.Run(path+"/"+fault, func(t *testing.T) {
				method := "POST"
				if path == "/logout" {
					method = "GET"
				}
				body := &untouchedRequestBody{}
				r := httptest.NewRequest(method, "http://example.test"+path, nil)
				r.Body = body
				r.AddCookie(&http.Cookie{Name: f.auth.cookieName, Value: key})
				want := http.StatusUnauthorized
				switch fault {
				case "origin":
					r.Header.Set("Origin", "http://foreign.test")
					want = http.StatusForbidden
				case "fetch-site":
					r.Header.Set("Sec-Fetch-Site", "cross-site")
					want = http.StatusForbidden
				case "explicit-basic":
					r.SetBasicAuth(f.user.Login, httpFixturePassword)
				case "explicit-bearer":
					r.Header.Set("Authorization", "Bearer invalid")
				case "empty-auth":
					r.Header["Authorization"] = []string{""}
				case "duplicate-cookie":
					r.AddCookie(&http.Cookie{Name: f.auth.cookieName, Value: key})
				case "query-password":
					r.URL.RawQuery = "password=do-not-copy"
					want = http.StatusBadRequest
				}
				w := currentUserResponse(h, r)
				assert.Equal(t, want, w.Code)
				assert.Zero(t, body.reads)
				assert.Empty(t, w.Result().Cookies())
			})
		}
	}
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, session, stored[0], "no session metadata or revocation changed")
}

func TestSessionHTTPLoginRejectsInvalidJSONAndRedirectBeforeIssuance(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{AppSubURL: "/metrics"})
	require.NoError(t, err)
	for _, body := range []string{`{}`, `null`, `[]`, `{"user":"admin"}`, `{"user":"admin","password":null}`, `{"user":"admin","password":"x","orgId":1}`, loginPayload(f) + `{}`, strings.Repeat(" ", 65537) + loginPayload(f)} {
		r := httptest.NewRequest("POST", "http://example.test/metrics/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := currentUserResponse(h, r)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, w.Result().Cookies())
	}
	for _, redirect := range []string{"https://foreign.test", "//foreign.test", "/metrics//foreign", "/metrics/../admin", "/metrics/%2e%2e/admin", "/metrics/%252fhost", "/metricsx/d/test", "/metrics/d/test\\evil", "/metrics/d/test?password=secret", "/metrics/d/test#//evil", "/metrics/d/test#\r\nLocation:evil"} {
		body := &untouchedRequestBody{}
		r := httptest.NewRequest("POST", "http://example.test/metrics/login?redirectTo="+url.QueryEscape(redirect), nil)
		r.Body = body
		w := currentUserResponse(h, r)
		assert.Equal(t, http.StatusBadRequest, w.Code, redirect)
		assert.Zero(t, body.reads)
		assert.Empty(t, w.Result().Cookies())
	}
	r := httptest.NewRequest("POST", "http://example.test/metrics/login", strings.NewReader(loginPayload(f)))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "redirect_to", Value: url.QueryEscape("//foreign.test")})
	w := currentUserResponse(h, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Empty(t, stored)
	for _, credentials := range []map[string]string{{"user": f.user.Login, "password": "wrong-password"}, {"user": "absent", "password": httpFixturePassword}} {
		body, _ := json.Marshal(credentials)
		r = httptest.NewRequest("POST", "http://example.test/metrics/login", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w = currentUserResponse(h, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Equal(t, "{\"message\":\"Invalid username or password\"}\n", w.Body.String())
		assert.Empty(t, w.Result().Cookies())
	}
}

func TestSessionHTTPLoginOnlyReturnsCookiesAfterMetadataTransactionSuccess(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{})
	require.NoError(t, err)
	providerSecret := "provider-secret-do-not-disclose"
	sessionSQL(t, f.catalog, `CREATE TRIGGER fail_http_session BEFORE INSERT ON browser_sessions BEGIN SELECT RAISE(ABORT,'provider-secret-do-not-disclose'); END`)
	r := httptest.NewRequest("POST", "http://example.test/login", strings.NewReader(loginPayload(f)))
	r.Header.Set("Content-Type", "application/json")
	w := currentUserResponse(h, r)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, w.Result().Cookies())
	assert.NotContains(t, w.Body.String(), providerSecret)
	assert.NotContains(t, w.Body.String(), httpFixturePassword)
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Empty(t, stored)
}

func TestSessionHTTPConcurrentRotationPreviousCredentialAndRestart(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ts, client := sessionFixtureServer(t, f, SessionHTTPConfig{}, false)
	response, body := sessionHTTPCall(t, client, "POST", ts.URL+"/login", loginPayload(f))
	require.Equal(t, http.StatusOK, response.StatusCode, body)
	original := authCookie(t, response, f.auth.cookieName).Value
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	initial := stored[0]
	setIdentitySessionTime(t, f.catalog.Path(), initial.ID, time.Now().Add(-time.Hour).UnixMilli())
	client.Jar = nil // Both actual HTTP requests carry the same preceding cookie.
	var wg sync.WaitGroup
	var outcomes [2]*http.Response
	var failures [2]error
	for i := range 2 {
		wg.Go(func() {
			r, _ := http.NewRequest("POST", ts.URL+"/api/user/auth-tokens/rotate", nil)
			r.AddCookie(&http.Cookie{Name: f.auth.cookieName, Value: original})
			outcomes[i], failures[i] = client.Do(r)
		})
	}
	wg.Wait()
	var newest string
	for i, response := range outcomes {
		require.NoError(t, failures[i])
		response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		key := authCookie(t, response, f.auth.cookieName).Value
		if key != original {
			newest = key
		}
	}
	require.NotEmpty(t, newest)
	_, err = f.catalog.AuthenticateSession(context.Background(), newest, 1)
	require.NoError(t, err)
	_, _, err = f.catalog.AuthenticateSessionWithRotation(context.Background(), original, 1, f.auth.rotationInterval)
	require.ErrorIs(t, err, identity.ErrSessionNeedsRotation, "a tab that lost the replacement must recover by rotation")
	r, _ := http.NewRequest("POST", ts.URL+"/api/user/auth-tokens/rotate", nil)
	r.AddCookie(&http.Cookie{Name: f.auth.cookieName, Value: original})
	response, err = client.Do(r)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	current := authCookie(t, response, f.auth.cookieName).Value
	require.NotEqual(t, original, current)
	_, err = f.catalog.AuthenticateSession(context.Background(), original, 1)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	stored, err = f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, initial.ID, stored[0].ID)
	assert.Equal(t, initial.ExpiresAt, stored[0].ExpiresAt)
	path := f.catalog.Path()
	require.NoError(t, f.catalog.Close())
	reopened, err := identity.Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	a, err := NewHTTPAuthenticator(reopened, HTTPAuthenticationConfig{})
	require.NoError(t, err)
	h, err := NewSessionHTTPHandler(a, SessionHTTPConfig{})
	require.NoError(t, err)
	// Logout accepts the immediately previous credential and revokes the current
	// replacement too, even after a durable catalog restart.
	w := currentUserResponse(h, cookieRequest("/logout", newest))
	require.Equal(t, http.StatusFound, w.Code)
	for _, key := range []string{newest, current} {
		_, err = reopened.AuthenticateSession(context.Background(), key, 1)
		require.ErrorIs(t, err, identity.ErrUnauthenticated)
	}
}

func TestSessionHTTPExpiryAndRedirectRotation(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{EnableRotationRedirect: true})
	require.NoError(t, err)
	for _, expired := range []string{"absolute", "idle"} {
		session, key := f.login(t, f.user)
		if expired == "absolute" {
			sessionSQL(t, f.catalog, `UPDATE browser_sessions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UnixMilli(), session.ID)
		} else {
			sessionSQL(t, f.catalog, `UPDATE browser_sessions SET last_seen=? WHERE id=?`, time.Now().Add(-time.Hour).UnixMilli(), session.ID)
		}
		r := cookieRequest("/api/user/auth-tokens/rotate", key)
		r.Method = "POST"
		w := currentUserResponse(h, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Len(t, w.Result().Cookies(), 2)
		assert.Equal(t, -1, w.Result().Cookies()[0].MaxAge)
		w = currentUserResponse(h, cookieRequest("/logout", key))
		require.Equal(t, http.StatusFound, w.Code)
		stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
		require.NoError(t, err)
		for _, device := range stored {
			if device.ID == session.ID {
				assert.Positive(t, device.RevokedAt, "logout really revokes even an expired native session")
			}
		}
	}
	session, key := f.login(t, f.user)
	setIdentitySessionTime(t, f.catalog.Path(), session.ID, time.Now().Add(-time.Hour).UnixMilli())
	w := currentUserResponse(h, cookieRequest("/user/auth-tokens/rotate?redirectTo="+url.QueryEscape("/d/real/desktop#overview"), key))
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/d/real/desktop#overview", w.Header().Get("Location"))
	for _, key := range []string{"", "invalid-cookie"} {
		w = currentUserResponse(h, cookieRequest("/user/auth-tokens/rotate", key))
		assert.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "/login", w.Header().Get("Location"))
		w = currentUserResponse(h, cookieRequest("/logout", key))
		assert.Equal(t, http.StatusFound, w.Code)
		assert.Equal(t, "/login", w.Header().Get("Location"))
	}
}

func TestSessionHTTPRealRedirectCookieTLSOriginAndTypedLogin(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ts, client := sessionFixtureServer(t, f, SessionHTTPConfig{AppSubURL: "/metrics/", CookieSecure: true, CookieSameSite: http.SameSiteNoneMode}, true)
	appURL, err := url.Parse(ts.URL + "/metrics/login")
	require.NoError(t, err)
	client.Jar.SetCookies(appURL, []*http.Cookie{{Name: "redirect_to", Value: url.QueryEscape("/metrics/d/cookie/desktop?var-site=pc"), Path: "/metrics/", Secure: true}})
	r, err := http.NewRequest("POST", appURL.String(), strings.NewReader(loginPayload(f)))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", strings.Replace(ts.URL, "https:", "http:", 1))
	r.Header.Set("X-Forwarded-Proto", "https")
	response, err := client.Do(r)
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, http.StatusForbidden, response.StatusCode, "actual TLS scheme controls Origin, not forwarded headers")
	stored, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Empty(t, stored)
	r, err = http.NewRequest("POST", appURL.String(), strings.NewReader(loginPayload(f)))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", ts.URL)
	r.Header.Set("X-Forwarded-Host", "untrusted.test")
	response, err = client.Do(r)
	require.NoError(t, err)
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, string(data), `"redirectUrl":"/metrics/d/cookie/desktop?var-site=pc"`)
	assert.Equal(t, http.SameSiteNoneMode, authCookie(t, response, f.auth.cookieName).SameSite)
	assert.Equal(t, -1, authCookie(t, response, "redirect_to").MaxAge)
	for _, cookie := range client.Jar.Cookies(appURL) {
		assert.NotEqual(t, "redirect_to", cookie.Name)
	}
	account, err := f.catalog.CreateServiceAccount(context.Background(), 1, identity.ServiceAccountInput{Name: "Same numeric account", Role: identity.RoleAdmin})
	require.NoError(t, err)
	require.Equal(t, f.user.ID, account.ID)
	_, key, err := f.catalog.CreateServiceAccountToken(context.Background(), 1, account.ID, "typed-token", 0)
	require.NoError(t, err)
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{})
	require.NoError(t, err)
	encoded, err := json.Marshal(map[string]string{"user": "api_key", "password": key})
	require.NoError(t, err)
	r = httptest.NewRequest("POST", "http://example.test/login", strings.NewReader(string(encoded)))
	r.Header.Set("Content-Type", "application/json")
	w := currentUserResponse(h, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "a real SA token is never exchanged for a human session")
	assert.Empty(t, w.Result().Cookies())
	assert.NotContains(t, w.Body.String(), key)
	stored, err = f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
}

func TestSessionHTTPConfigAndAbsentLoginPage(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	for _, config := range []SessionHTTPConfig{{AppSubURL: "metrics"}, {AppSubURL: "/metrics/../"}, {AppSubURL: "/metrics//"}, {AppSubURL: "//"}, {AppSubURL: "/metrics/./"}, {CookieSameSite: http.SameSiteNoneMode}, {CookieSameSite: http.SameSiteDefaultMode}, {SessionLifetime: -time.Hour}, {SessionLifetime: time.Hour, SessionIdleTimeout: 2 * time.Hour}, {LogoutRedirect: "https://foreign.test"}, {AppSubURL: "/metrics", LogoutRedirect: "/outside"}} {
		_, err := NewSessionHTTPHandler(f.auth, config)
		require.Error(t, err, fmt.Sprintf("%+v", config))
	}
	for _, name := range []string{sessionExpiryCookieName, "redirect_to"} {
		a, err := NewHTTPAuthenticator(f.catalog, HTTPAuthenticationConfig{CookieName: name})
		require.NoError(t, err)
		_, err = NewSessionHTTPHandler(a, SessionHTTPConfig{})
		require.Error(t, err)
	}
	for _, config := range []SessionHTTPConfig{{}, {AppSubURL: "/metrics", CookieSecure: true}} {
		a, err := NewHTTPAuthenticator(f.catalog, HTTPAuthenticationConfig{CookieName: "__Host-session"})
		require.NoError(t, err)
		_, err = NewSessionHTTPHandler(a, config)
		require.Error(t, err)
	}
	h, err := NewSessionHTTPHandler(f.auth, SessionHTTPConfig{})
	require.NoError(t, err)
	w := currentUserResponse(h, httptest.NewRequest("GET", "http://example.test/login", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "only password POST exists, no login page or SPA success")
	w = currentUserResponse(h, httptest.NewRequest("GET", "http://example.test/user/auth-tokens/rotate", nil))
	assert.Equal(t, http.StatusNotFound, w.Code, "the browser rotation redirect is opt-in")
}
