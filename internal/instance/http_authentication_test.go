package instance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const httpFixturePassword = "request-authentication-test-password-233"

func setIdentitySessionTime(t *testing.T, path string, sessionID, rotatedAt int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, rotatedAt, sessionID)
	require.NoError(t, err)
}

type httpAuthenticationFixture struct {
	registry *Registry
	catalog  *identity.Store
	primary  *server.Server
	auth     *HTTPAuthenticator
	user     identity.User
}

func newHTTPAuthenticationFixture(t *testing.T) *httpAuthenticationFixture {
	t.Helper()
	r, catalog, primary := registryFixture(t)
	user, err := catalog.BootstrapAdmin(context.Background(), identity.UserInput{Login: "admin", Name: "HTTP administrator", Password: httpFixturePassword})
	require.NoError(t, err)
	auth, err := NewHTTPAuthenticator(catalog, HTTPAuthenticationConfig{})
	require.NoError(t, err)
	return &httpAuthenticationFixture{r, catalog, primary, auth, user}
}

func (f *httpAuthenticationFixture) login(t *testing.T, user identity.User) (identity.Session, string) {
	t.Helper()
	_, session, secret, err := f.catalog.Login(context.Background(), user.Login, httpFixturePassword, user.CurrentOrgID, time.Hour, 30*time.Minute)
	require.NoError(t, err)
	return session, secret
}

func (f *httpAuthenticationFixture) basic(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
	r.SetBasicAuth(f.user.Login, httpFixturePassword)
	return r
}

func cookieRequest(path, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
	r.AddCookie(&http.Cookie{Name: "grafana_session", Value: secret})
	return r
}

func TestHTTPAuthenticationCredentialMethodsAndRedaction(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, cookie := f.login(t, f.user)
	account, err := f.catalog.CreateServiceAccount(context.Background(), 1, identity.ServiceAccountInput{Name: "Automation", Role: identity.RoleNone})
	require.NoError(t, err)
	_, token, err := f.catalog.CreateServiceAccountToken(context.Background(), 1, account.ID, "agent", 0)
	require.NoError(t, err)
	bearer := httptest.NewRequest("GET", "http://example.test/api/v1/health", nil)
	bearer.Header.Set("Authorization", "bearer "+token)
	apiKey := httptest.NewRequest("GET", "http://example.test/api/v1/health", nil)
	apiKey.SetBasicAuth("api_key", token)
	for _, tc := range []struct {
		name   string
		r      *http.Request
		typeOf identity.PrincipalType
		method AuthenticationMethod
		id     int64
	}{
		{"password", f.basic("/api/user"), identity.TypeUser, AuthenticationPassword, 0},
		{"cookie", cookieRequest("/api/user", cookie), identity.TypeUser, AuthenticationSession, session.ID},
		{"bearer", bearer, identity.TypeServiceAccount, AuthenticationToken, 0},
		{"api-key-basic", apiKey, identity.TypeServiceAccount, AuthenticationToken, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, err := f.auth.AuthenticateRequest(tc.r)
			require.NoError(t, err)
			p, ok := identity.FromContext(bound.Context())
			require.True(t, ok)
			assert.Equal(t, tc.typeOf, p.Type)
			metadata, ok := f.auth.Metadata(bound.Context())
			require.True(t, ok)
			assert.Equal(t, AuthenticationMetadata{tc.method, tc.id}, metadata)
			encoded := fmt.Sprintf("%+v %#v", bound.Context().Value(requestCredentialKey{}), bound.Context().Value(requestCredentialKey{}))
			for _, secret := range []string{httpFixturePassword, cookie, token} {
				assert.NotContains(t, encoded, secret)
			}
			fresh, err := f.auth.Revalidate(bound.Context(), p)
			require.NoError(t, err)
			assert.Equal(t, p, fresh)
		})
	}
}

func TestHTTPAuthenticationOrganizationSelectorsDoNotGrantMembership(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	org, _ := activate(t, f.registry, f.catalog, "Second organization")
	_, err := f.catalog.AddOrganizationUser(context.Background(), org.ID, f.user.ID, identity.RoleNone)
	require.NoError(t, err)
	for _, tc := range []struct {
		path, header string
		want         int64
	}{
		{"/apis/dashboard.grafana.app/v1beta1/namespaces/default/dashboards?orgId=2", "2", 1},
		{"/apis/dashboard.grafana.app/v1beta1/namespaces/org-2/dashboards?orgId=1", "1", 2},
		{"/api/v1/health?orgId=2&targetOrgId=1", "1", 2},
		{"/api/v1/health?orgId=bad&targetOrgId=2", "1", 2},
		{"/api/v1/health?orgId=9223372036854775808&targetOrgId=-1", "2", 2},
		{"/api/v1/health", "not-a-number", 1},
	} {
		r := f.basic(tc.path)
		r.Header.Set("X-Grafana-Org-Id", tc.header)
		bound, err := f.auth.AuthenticateRequest(r)
		require.NoError(t, err, tc.path)
		p, _ := identity.FromContext(bound.Context())
		assert.Equal(t, tc.want, p.OrgID)
		if p.OrgID == org.ID {
			assert.Equal(t, identity.RoleNone, p.Role)
		}
	}
	r := f.basic("/api/v1/health?orgId=233")
	_, err = f.auth.AuthenticateRequest(r)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	// A service token remains pinned even when the human administrator is a
	// member of the requested organization; same numeric ID does not change type.
	a, err := f.catalog.CreateServiceAccount(context.Background(), 1, identity.ServiceAccountInput{Name: "Pinned", Role: identity.RoleAdmin})
	require.NoError(t, err)
	_, key, err := f.catalog.CreateServiceAccountToken(context.Background(), 1, a.ID, "token", 0)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	r.URL.RawQuery = "orgId=2"
	_, err = f.auth.AuthenticateRequest(r)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestHTTPAuthenticationRejectsAmbientFallbackAndForgedContext(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	_, cookie := f.login(t, f.user)
	for _, header := range []string{"", "Basic invalid", "Bearer invalid-secret", "Bearer", "Bearer one two", "Digest value"} {
		r := cookieRequest("/api/user", cookie)
		r.Header["Authorization"] = []string{header}
		_, err := f.auth.AuthenticateRequest(r)
		require.ErrorIs(t, err, identity.ErrUnauthenticated)
	}
	r := cookieRequest("/api/user", cookie)
	r.AddCookie(&http.Cookie{Name: "grafana_session", Value: cookie})
	_, err := f.auth.AuthenticateRequest(r)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	r = f.basic("/api/user")
	r.Header.Add("Authorization", "Bearer other")
	_, err = f.auth.AuthenticateRequest(r)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	r = httptest.NewRequest("GET", "http://example.test/api/user", nil)
	r = r.WithContext(identity.WithPrincipal(r.Context(), identity.LocalPrincipal()))
	_, err = f.auth.AuthenticateRequest(r)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	// An otherwise valid credential replaces an injected system principal.
	r = f.basic("/api/user")
	r = r.WithContext(identity.WithPrincipal(r.Context(), identity.BackgroundPrincipal(1)))
	bound, err := f.auth.AuthenticateRequest(r)
	require.NoError(t, err)
	p, _ := identity.FromContext(bound.Context())
	assert.Equal(t, identity.TypeUser, p.Type)
	_, err = f.auth.Revalidate(identity.WithPrincipal(bound.Context(), identity.BackgroundPrincipal(1)), identity.BackgroundPrincipal(1))
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	other, err := NewHTTPAuthenticator(f.catalog, HTTPAuthenticationConfig{})
	require.NoError(t, err)
	_, err = other.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.auth.Revalidate(identity.WithPrincipal(context.Background(), p), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestHTTPAuthenticationBindingRevalidatesRevocationAndRole(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	u, err := f.catalog.CreateUser(context.Background(), identity.UserInput{Login: "member", Password: httpFixturePassword, Role: identity.RoleEditor, OrgID: 1})
	require.NoError(t, err)
	session, key := f.login(t, u)
	bound, err := f.auth.AuthenticateRequest(cookieRequest("/api/user", key))
	require.NoError(t, err)
	p, _ := identity.FromContext(bound.Context())
	m, err := f.catalog.Membership(context.Background(), 1, u.ID)
	require.NoError(t, err)
	_, err = f.catalog.ChangeOrganizationUserRole(context.Background(), 1, u.ID, m.Version, identity.RoleNone)
	require.NoError(t, err)
	fresh, err := f.auth.Revalidate(bound.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, identity.RoleNone, fresh.Role)
	// Mutating a later request's selectors/credentials cannot change the binding.
	bound.Header.Set("Authorization", "Bearer fake")
	bound.URL.RawQuery = "orgId=233"
	fresh, err = f.auth.Revalidate(bound.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, int64(1), fresh.OrgID)
	require.NoError(t, f.catalog.RevokeSession(context.Background(), u.ID, session.ID))
	_, err = f.auth.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	// Basic binding is rechecked against the password credential, not user ID.
	r := httptest.NewRequest("GET", "http://example.test/api/user", nil)
	r.SetBasicAuth(u.Login, httpFixturePassword)
	bound, err = f.auth.AuthenticateRequest(r)
	require.NoError(t, err)
	p, _ = identity.FromContext(bound.Context())
	u, err = f.catalog.User(context.Background(), u.ID)
	require.NoError(t, err)
	_, err = f.catalog.ChangePassword(context.Background(), u.ID, u.Version, "changed-password-233")
	require.NoError(t, err)
	_, err = f.auth.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestHTTPAuthenticationOriginPrecedesSessionMetadata(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	for _, tc := range []struct{ origin, site string }{
		{"http://foreign.test", ""}, {"https://example.test", ""}, {"null", ""},
		{"http://example.test/path", ""}, {"http://u:p@example.test", ""}, {"", "cross-site"},
	} {
		r := cookieRequest("/api/user", key)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		_, err := f.auth.AuthenticateRequest(r)
		require.ErrorIs(t, err, identity.ErrForbidden)
	}
	current, err := f.catalog.Sessions(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Len(t, current, 1)
	assert.Equal(t, session.LastSeen, current[0].LastSeen)
	assert.False(t, current[0].TokenSeen, "foreign requests did not mark the issued credential as seen")
	r := cookieRequest("/api/user", key)
	r.Header.Set("Origin", "http://example.test")
	r.Header.Set("X-Forwarded-Host", "foreign.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	_, err = f.auth.AuthenticateRequest(r)
	require.NoError(t, err, "untrusted proxy headers cannot override actual transport/host")
}

func TestHTTPAuthenticationRotationGateAndFreshRoutePolicy(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	session, key := f.login(t, f.user)
	_, err := f.auth.AuthenticateRequest(cookieRequest("/api/user", key))
	require.NoError(t, err)
	// The real catalog's rotation state persists across authenticator instances.
	// Use an independent sqlite connection because Store keeps its DB private.
	setIdentitySessionTime(t, f.catalog.Path(), session.ID, time.Now().Add(-time.Hour).UnixMilli())
	_, err = f.auth.AuthenticateRequest(cookieRequest("/api/user", key))
	require.ErrorIs(t, err, identity.ErrSessionNeedsRotation)
	_, _, replacement, err := f.catalog.RotateSession(context.Background(), key)
	require.NoError(t, err)
	bound, err := f.auth.AuthenticateRequest(cookieRequest("/api/user", replacement))
	require.NoError(t, err)
	authorizer, err := f.auth.NewRequestAuthorizer(func(r *http.Request, route server.ProtectedRoute, p identity.Principal) error {
		fromContext, ok := identity.FromContext(r.Context())
		require.True(t, ok)
		assert.Equal(t, p, fromContext)
		if route.Pattern != "GET /api/v1/health" {
			return identity.ErrForbidden
		}
		return nil
	})
	require.NoError(t, err)
	p, err := authorizer.AuthorizeRequest(bound, server.ProtectedRoute{Surface: server.RouteAPI, Pattern: "GET /api/v1/health"})
	require.NoError(t, err)
	assert.Equal(t, f.user.ID, p.ID)
	_, err = authorizer.AuthorizeRequest(bound, server.ProtectedRoute{Surface: server.RouteAPI, Pattern: "DELETE /api/v1/targets/{id}"})
	require.ErrorIs(t, err, identity.ErrForbidden)
}

func TestProtectedOrganizationHTTPDispatchIsolationAndLegacyRefusal(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	readPolicy, err := NewResourceReadPolicy(f.registry, f.auth)
	require.NoError(t, err)
	policy, err := f.auth.NewRequestAuthorizer(readPolicy)
	require.NoError(t, err)
	h, err := NewProtectedOrganizationHandler(f.registry, f.auth, policy)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.basic("/api/v1/dashboards"))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "authenticated users must never reach a legacy handler")
	f.primary.Authorizer = policy
	f.registry.config.ConfigureServer = func(_ context.Context, child *server.Server, _ identity.Organization) error {
		child.Authorizer = policy
		return nil
	}
	org, child := activate(t, f.registry, f.catalog, "HTTP child")
	_, err = f.catalog.AddOrganizationUser(context.Background(), org.ID, f.user.ID, identity.RoleNone)
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(context.Background(), model.Dashboard{ID: "same-uid", Name: "primary-only"})
	require.NoError(t, err)
	_, err = child.Store.SaveDashboard(context.Background(), model.Dashboard{ID: "same-uid", Name: "child-only"})
	require.NoError(t, err)
	for _, selected := range []*server.Server{f.primary, child} {
		internal := identity.BackgroundPrincipal(selected.OrganizationID())
		resolver, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
		require.NoError(t, err)
		ready, err := selected.Store.MigrateResourcePermissions(identity.WithPrincipal(context.Background(), internal), 100, resolver)
		require.NoError(t, err)
		require.True(t, ready.Ready)
		if selected == child {
			ref := model.PermissionResource{OrgID: org.ID, Kind: model.PermissionDashboard, UID: "same-uid"}
			doc, err := child.Store.ResourcePermissions(context.Background(), ref)
			require.NoError(t, err)
			_, err = child.Store.ReplaceResourcePermissions(identity.WithPrincipal(context.Background(), internal), ref, doc.Version, []model.ResourcePermissionGrant{{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: f.user.ID}, Permission: model.PermissionView}}, resolver)
			require.NoError(t, err)
		}
	}
	for _, tc := range []struct{ org, own, other string }{{"1", "primary-only", "child-only"}, {"2", "child-only", "primary-only"}} {
		r := f.basic("/api/v1/dashboards")
		r.Header.Set("X-Grafana-Org-Id", tc.org)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), tc.own)
		assert.NotContains(t, w.Body.String(), tc.other)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, f.basic("/api/v1/targets"))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	// A different valid authorizer is also refused, rather than accepted by type.
	otherPolicy, err := f.auth.NewRequestAuthorizer(func(*http.Request, server.ProtectedRoute, identity.Principal) error { return nil })
	require.NoError(t, err)
	child.Authorizer = otherPolicy // No concurrent handlers in this fixture.
	r := f.basic("/api/v1/dashboards?orgId=2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHTTPAuthenticationConfigurationAndUnavailableErrorRedaction(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	_, err := NewHTTPAuthenticator(nil, HTTPAuthenticationConfig{})
	require.Error(t, err)
	for _, c := range []HTTPAuthenticationConfig{{CookieName: "bad\nname"}, {RotationInterval: time.Second}, {RotationInterval: -time.Minute}} {
		_, err := NewHTTPAuthenticator(f.catalog, c)
		require.Error(t, err)
	}
	_, err = f.auth.NewRequestAuthorizer(nil)
	require.Error(t, err)
	var nilPolicy *CredentialRequestAuthorizer
	_, err = NewProtectedOrganizationHandler(f.registry, f.auth, nilPolicy)
	require.Error(t, err)
	for _, cause := range []error{errors.New("password=" + httpFixturePassword), identity.ErrUnauthenticated, identity.ErrForbidden, identity.ErrSessionNeedsRotation} {
		w := httptest.NewRecorder()
		writeAuthenticationError(w, cause)
		assert.NotContains(t, w.Body.String(), httpFixturePassword)
		assert.True(t, strings.HasPrefix(w.Header().Get("Content-Type"), "application/json"))
	}
}

func TestHTTPAuthenticationPermissionResolverUsesBoundCredentialAndTypedGrants(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ctx := context.Background()
	u, err := f.catalog.CreateUser(ctx, identity.UserInput{Login: "explicit-grant", Password: httpFixturePassword, Role: identity.RoleNone, OrgID: 1})
	require.NoError(t, err)
	_, key := f.login(t, u)
	_, err = f.primary.Store.SaveDashboard(ctx, model.Dashboard{ID: "private-dashboard", Name: "Private"})
	require.NoError(t, err)
	internal := identity.BackgroundPrincipal(1)
	internalCtx := identity.WithPrincipal(ctx, internal)
	trusted, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	ref := model.PermissionResource{OrgID: 1, Kind: model.PermissionDashboard, UID: "private-dashboard"}
	doc, err := f.primary.Store.InitializeResourcePermissions(internalCtx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, trusted)
	require.NoError(t, err)
	bound, err := f.auth.AuthenticateRequest(cookieRequest("/api/user", key))
	require.NoError(t, err)
	p, _ := identity.FromContext(bound.Context())
	resolver, err := f.auth.PermissionResolver()
	require.NoError(t, err)
	forgedRole := p
	forgedRole.Role = identity.RoleAdmin
	effective, err := f.primary.Store.EffectiveResourcePermission(bound.Context(), ref, forgedRole, resolver)
	require.NoError(t, err)
	assert.Equal(t, model.ResourcePermissionLevel(0), effective.Permission, "request role fields cannot grant default Admin access")
	_, err = f.primary.Store.ReplaceResourcePermissions(internalCtx, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID}, Permission: model.PermissionEdit}}, trusted)
	require.NoError(t, err)
	effective, err = f.primary.Store.EffectiveResourcePermission(bound.Context(), ref, p, resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionEdit, effective.Permission)
	// Grant matching is typed even when human and account ID sequences overlap.
	for i := int64(1); i <= u.ID; i++ {
		account, err := f.catalog.CreateServiceAccount(ctx, 1, identity.ServiceAccountInput{Name: fmt.Sprintf("Account %d", i), Role: identity.RoleNone})
		require.NoError(t, err)
		if account.ID != u.ID {
			continue
		}
		_, serviceKey, err := f.catalog.CreateServiceAccountToken(ctx, 1, account.ID, "token", 0)
		require.NoError(t, err)
		r := httptest.NewRequest("GET", "http://example.test/api/user", nil)
		r.Header.Set("Authorization", "Bearer "+serviceKey)
		serviceRequest, err := f.auth.AuthenticateRequest(r)
		require.NoError(t, err)
		service, _ := identity.FromContext(serviceRequest.Context())
		effective, err = f.primary.Store.EffectiveResourcePermission(serviceRequest.Context(), ref, service, resolver)
		require.NoError(t, err)
		assert.Equal(t, model.ResourcePermissionLevel(0), effective.Permission)
	}
	require.NoError(t, f.catalog.Logout(ctx, key))
	_, err = f.primary.Store.EffectiveResourcePermission(bound.Context(), ref, p, resolver)
	require.ErrorIs(t, err, identity.ErrUnauthenticated, "an old bound request does not outlive actual cookie revocation")
}

func TestHTTPAuthenticationPolicyFenceReachesMutationHandler(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ctx := context.Background()
	dashboard, err := f.primary.Store.SaveDashboard(ctx, model.Dashboard{ID: "policy-fence", Name: "Before"})
	require.NoError(t, err)
	internal := identity.BackgroundPrincipal(1)
	trusted, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	ref := model.PermissionResource{OrgID: 1, Kind: model.PermissionDashboard, UID: dashboard.ID}
	_, err = f.primary.Store.InitializeResourcePermissions(identity.WithPrincipal(ctx, internal), ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, trusted)
	require.NoError(t, err)
	_, key := f.login(t, f.user)
	request, err := f.auth.AuthenticateRequest(cookieRequest("/api/v1/dashboards/policy-fence", key))
	require.NoError(t, err)
	type policyFenceKey struct{}
	authorizer, err := f.auth.NewRequestAuthorizer(func(r *http.Request, _ server.ProtectedRoute, p identity.Principal) error {
		resolver, err := f.auth.PermissionResolver()
		if err != nil {
			return err
		}
		fence, err := f.primary.Store.CapturePermissionMutation(r.Context(), []model.PermissionResource{ref}, resolver)
		if err != nil {
			return err
		}
		effective, err := fence.Effective(ref)
		if err != nil {
			return err
		}
		if effective.Permission < model.PermissionEdit {
			return identity.ErrForbidden
		}
		*r = *r.WithContext(context.WithValue(r.Context(), policyFenceKey{}, fence))
		return nil
	})
	require.NoError(t, err)
	p, err := authorizer.AuthorizeRequest(request, server.ProtectedRoute{Surface: server.RouteAPI, Pattern: "PUT /api/v1/dashboards/{id}"})
	require.NoError(t, err)
	// The downstream mutation sees the same request, its original credential
	// capability, the fresh principal and the opaque policy-created snapshot.
	fence, present := request.Context().Value(policyFenceKey{}).(store.PermissionMutationFence)
	require.True(t, present, "authorization context must reach the downstream handler")
	metadata, present := f.auth.Metadata(request.Context())
	require.True(t, present)
	assert.Equal(t, AuthenticationSession, metadata.Method)
	fresh, err := f.auth.Revalidate(request.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, p, fresh)
	dashboard.Name = "After policy"
	_, err = f.primary.Store.SaveDashboardWithPermissions(request.Context(), dashboard, &dashboard.UpdatedAt, model.ResourcePermissionSeed{}, fence)
	require.NoError(t, err)
	saved, err := f.primary.Store.Dashboard(ctx, dashboard.ID)
	require.NoError(t, err)
	assert.Equal(t, "After policy", saved.Name)
}

func TestHTTPAuthenticationBasicProofIsPrivateAndCannotBeReusedAsNewCredentials(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ctx := context.Background()
	bound, err := f.auth.AuthenticateRequest(f.basic("/api/user"))
	require.NoError(t, err)
	credential, ok := bound.Context().Value(requestCredentialKey{}).(requestCredential)
	require.True(t, ok)
	require.NotNil(t, credential.passwordProof)
	assert.Empty(t, credential.secret, "the actual Basic password is discarded after its first verification")
	assert.Empty(t, credential.login)
	p, present := identity.FromContext(bound.Context())
	require.True(t, present)
	name := "Current profile after password verification"
	updated, err := f.catalog.UpdateUser(ctx, f.user.ID, f.user.Version, identity.UserProfileUpdate{Name: &name})
	require.NoError(t, err)
	fresh, err := f.auth.Revalidate(bound.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, name, fresh.Name)
	// A new request must verify its own password, even if trusted test code
	// supplied the old authenticated context. There is no cross-request cache.
	wrong := f.basic("/api/user").WithContext(bound.Context())
	wrong.SetBasicAuth(f.user.Login, "incorrect-new-request-password")
	_, err = f.auth.AuthenticateRequest(wrong)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	updated, err = f.catalog.ChangePassword(ctx, updated.ID, updated.Version, "replacement-proof-password-233")
	require.NoError(t, err)
	_, err = f.auth.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.catalog.ChangePassword(ctx, updated.ID, updated.Version, httpFixturePassword)
	require.NoError(t, err)
	_, err = f.auth.Revalidate(bound.Context(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated, "the observed invalidated proof cannot be revived")
	newRequest, err := f.auth.AuthenticateRequest(f.basic("/api/user"))
	require.NoError(t, err)
	newCredential := newRequest.Context().Value(requestCredentialKey{}).(requestCredential)
	assert.NotSame(t, credential.passwordProof, newCredential.passwordProof)
}
