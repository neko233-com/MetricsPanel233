package instance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resourceReadFixture struct {
	*httpAuthenticationFixture
	reader   identity.User
	key      string
	internal context.Context
	resolver store.PermissionIdentityResolver
	server   *httptest.Server
}

func newResourceReadFixture(t *testing.T) *resourceReadFixture {
	t.Helper()
	f := newHTTPAuthenticationFixture(t)
	u, err := f.catalog.CreateUser(context.Background(), identity.UserInput{Login: "resource-reader", Password: httpFixturePassword, OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	_, key := f.login(t, u)
	internal := identity.BackgroundPrincipal(1)
	resolver, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	return &resourceReadFixture{httpAuthenticationFixture: f, reader: u, key: key, internal: identity.WithPrincipal(context.Background(), internal), resolver: resolver}
}

func (f *resourceReadFixture) start(t *testing.T) {
	t.Helper()
	ready, err := f.primary.Store.MigrateResourcePermissions(f.internal, 100, f.resolver)
	require.NoError(t, err)
	require.True(t, ready.Ready)
	policy, err := NewResourceReadPolicy(f.registry, f.auth)
	require.NoError(t, err)
	authorizer, err := f.auth.NewRequestAuthorizer(policy)
	require.NoError(t, err)
	f.primary.Authorizer = authorizer
	h, err := NewProtectedOrganizationHandler(f.registry, f.auth, authorizer)
	require.NoError(t, err)
	f.server = httptest.NewServer(h)
	t.Cleanup(f.server.Close)
}

func (f *resourceReadFixture) grant(t *testing.T, kind model.PermissionResourceKind, uid string, permission model.ResourcePermissionLevel) {
	t.Helper()
	ref := model.PermissionResource{OrgID: 1, Kind: kind, UID: uid}
	doc, err := f.primary.Store.ResourcePermissions(f.internal, ref)
	require.NoError(t, err)
	_, err = f.primary.Store.ReplaceResourcePermissions(f.internal, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: f.reader.ID}, Permission: permission}}, f.resolver)
	require.NoError(t, err)
}

func (f *resourceReadFixture) get(t *testing.T, path string) (int, string) {
	t.Helper()
	r, err := http.NewRequest("GET", f.server.URL+path, nil)
	require.NoError(t, err)
	r.AddCookie(&http.Cookie{Name: "grafana_session", Value: f.key})
	response, err := f.server.Client().Do(r)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	return response.StatusCode, string(data)
}

func TestResourceReadHTTPExplicitGrantNativeIdentityPaginationMetadataAndExport(t *testing.T) {
	f := newResourceReadFixture(t)
	parent, err := f.primary.Store.CreateFolder(context.Background(), model.Folder{UID: "hidden-parent", Title: "Hidden parent name"})
	require.NoError(t, err)
	child, err := f.primary.Store.CreateFolder(context.Background(), model.Folder{UID: "visible-child", Title: "Visible child", ParentUID: parent.UID})
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(context.Background(), model.Dashboard{ID: "native-granted", Name: "Z granted dashboard", FolderUID: parent.UID, Grafana: json.RawMessage(`{"uid":"external-granted","title":"Z granted dashboard","tags":["public"],"panels":[],"opaque":18446744073709551615}`)})
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(context.Background(), model.Dashboard{ID: "native-hidden", Name: "A hidden dashboard", Grafana: json.RawMessage(`{"uid":"external-hidden","title":"A hidden dashboard","tags":["hidden-tag"],"panels":[]}`)})
	require.NoError(t, err)
	f.start(t)
	f.grant(t, model.PermissionDashboard, "native-granted", model.PermissionView)
	f.grant(t, model.PermissionFolder, child.UID, model.PermissionView)
	status, body := f.get(t, "/api/v1/dashboards")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "Z granted dashboard")
	assert.NotContains(t, body, "A hidden dashboard")
	status, body = f.get(t, "/api/search?type=dash-db&limit=1&page=1")
	require.Equal(t, http.StatusOK, status, body)
	var hits []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &hits))
	require.Len(t, hits, 1, "filter before pagination so the denied first item cannot hide the allowed item")
	assert.Equal(t, "external-granted", hits[0]["uid"])
	assert.Equal(t, "", hits[0]["folderTitle"])
	assert.Equal(t, float64(0), hits[0]["folderId"])
	status, body = f.get(t, "/api/search?type=dash-db&tag=hidden-tag")
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `[]`, body)
	status, body = f.get(t, "/api/dashboards/uid/external-granted")
	require.Equal(t, http.StatusOK, status, body)
	var document struct {
		Meta map[string]any `json:"meta"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &document))
	assert.Equal(t, false, document.Meta["canSave"])
	assert.Equal(t, false, document.Meta["canEdit"])
	assert.Equal(t, false, document.Meta["canAdmin"])
	assert.Equal(t, "", document.Meta["folderTitle"])
	assert.NotContains(t, body, "Hidden parent name")
	for _, path := range []string{"/api/dashboards/uid/external-hidden", "/api/v1/dashboard-exports/native-hidden", "/api/folders/hidden-parent"} {
		status, body = f.get(t, path)
		assert.Equal(t, http.StatusForbidden, status, path+": "+body)
		assert.NotContains(t, body, "hidden-tag")
		assert.NotContains(t, body, "Hidden parent name")
	}
	status, body = f.get(t, "/api/v1/dashboard-exports/native-granted?format=grafana")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "18446744073709551615")
	status, body = f.get(t, "/api/folders?parentUid=hidden-parent&limit=1")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "Visible child")
	assert.NotContains(t, body, "Hidden parent name")
	var folders []model.Folder
	require.NoError(t, json.Unmarshal([]byte(body), &folders))
	require.Len(t, folders, 1)
	assert.False(t, folders[0].CanEdit)
	assert.False(t, folders[0].CanSave)
	assert.False(t, folders[0].CanAdmin)
	assert.False(t, folders[0].CanDelete)
	status, body = f.get(t, "/api/folders/visible-child/parents")
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `[]`, body)
	f.grant(t, model.PermissionDashboard, "native-granted", model.PermissionEdit)
	status, body = f.get(t, "/api/dashboards/uid/external-granted")
	require.Equal(t, http.StatusOK, status, body)
	require.NoError(t, json.Unmarshal([]byte(body), &document))
	assert.Equal(t, true, document.Meta["canEdit"])
	assert.Equal(t, false, document.Meta["canAdmin"])
}

func TestResourceReadHTTPLibraryOwnerBeforeExpansionAndDeniedSurfaces(t *testing.T) {
	f := newResourceReadFixture(t)
	owner, err := f.primary.Store.CreateFolder(context.Background(), model.Folder{UID: "library-owner", Title: "Private library owner"})
	require.NoError(t, err)
	library, err := f.primary.Store.SaveLibraryElement(context.Background(), model.LibraryElement{UID: "private-library", Name: "Private library", Kind: 1, FolderUID: owner.UID, Model: json.RawMessage(`{"type":"text","title":"Secret library content","options":{"content":"Secret library content"}}`)}, true)
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(context.Background(), model.Dashboard{ID: "linked-reader", Name: "Linked dashboard", Grafana: json.RawMessage(`{"uid":"linked-external","title":"Linked dashboard","panels":[{"id":1,"type":"text","libraryPanel":{"uid":"private-library"}}]}`), Panels: []model.Panel{{ID: "linked", Title: "Reference", Aggregation: "last", Config: json.RawMessage(`{"id":1,"type":"text","libraryPanel":{"uid":"private-library"}}`)}}})
	require.NoError(t, err)
	f.start(t)
	f.grant(t, model.PermissionDashboard, "linked-reader", model.PermissionView)
	// A corrupt model would fail expansion. The unauthorized owner must be
	// rejected before reading or parsing it, without failing the whole collection.
	_, err = f.primary.Store.DB.Exec(`UPDATE library_elements SET model=? WHERE uid=?`, "{invalid", library.UID)
	require.NoError(t, err)
	status, body := f.get(t, "/api/v1/dashboards")
	require.Equal(t, http.StatusOK, status, body)
	assert.JSONEq(t, `[]`, body)
	status, body = f.get(t, "/api/dashboards/uid/linked-external")
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = f.get(t, "/api/search?type=dash-db")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "Linked dashboard", "metadata discovery does not fetch the private library model")
	assert.NotContains(t, body, "Secret library content")
	for _, path := range []string{"/api/v1/dashboard-versions/linked-reader", "/api/dashboards/uid/linked-external/versions", "/api/v1/query?query=up", "/prometheus/api/v1/query?query=up", "/api/library-elements/private-library", "/api/annotations", "/api/live/ws"} {
		status, body = f.get(t, path)
		assert.Equal(t, http.StatusForbidden, status, path+": "+body)
		assert.NotContains(t, body, "Secret library content")
	}
	_, err = f.primary.Store.DB.Exec(`UPDATE library_elements SET model=? WHERE uid=?`, string(library.Model), library.UID)
	require.NoError(t, err)
	f.grant(t, model.PermissionFolder, owner.UID, model.PermissionView)
	status, body = f.get(t, "/api/dashboards/uid/linked-external")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "Secret library content")
	policy, err := NewResourceReadPolicy(f.registry, f.auth)
	require.NoError(t, err)
	authorizer, err := f.auth.NewRequestAuthorizer(policy)
	require.NoError(t, err)
	request := cookieRequest("/api/v1/dashboards", f.key)
	request.Method = "POST"
	bodyReader := &untouchedRequestBody{}
	request.Body = bodyReader
	bound, err := f.auth.AuthenticateRequest(request)
	require.NoError(t, err)
	_, err = authorizer.AuthorizeRequest(bound, server.ProtectedRoute{Surface: server.RouteAPI, Pattern: "POST /api/v1/dashboards"})
	require.ErrorIs(t, err, identity.ErrForbidden)
	assert.Zero(t, bodyReader.reads)
}

func TestResourceReadHTTPConfiguredProviderCannotOmitResourceContext(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	policy, err := f.auth.NewRequestAuthorizer(func(*http.Request, server.ProtectedRoute, identity.Principal) error { return nil })
	require.NoError(t, err)
	f.primary.Authorizer = policy
	h, err := NewProtectedOrganizationHandler(f.registry, f.auth, policy)
	require.NoError(t, err)
	w := currentUserResponse(h, f.basic("/api/v1/dashboards"))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.NotContains(t, w.Body.String(), "panels")
	_, err = NewResourceReadPolicy(nil, f.auth)
	require.Error(t, err)
	_, err = NewResourceReadPolicy(f.registry, nil)
	require.Error(t, err)
	r := f.basic("/api/search")
	bound, err := f.auth.AuthenticateRequest(r)
	require.NoError(t, err)
	resolver, err := f.auth.PermissionResolver()
	require.NoError(t, err)
	// A caller cannot claim a selected mux registration on an unselected request.
	err = f.primary.BindResourceReadAuthorization(bound, server.ProtectedRoute{Surface: server.RouteAPI, Pattern: "GET /api/search"}, resolver)
	require.ErrorIs(t, err, identity.ErrForbidden)
	assert.False(t, strings.Contains(w.Body.String(), httpFixturePassword))
}
func TestResourceReadHTTPNestedLibraryDoesNotExposePrivateOwner(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ctx := context.Background()
	user, err := f.catalog.CreateUser(ctx, identity.UserInput{Login: "read-none", Password: httpFixturePassword, OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	internal := identity.BackgroundPrincipal(1)
	trusted, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	internalCtx := identity.WithPrincipal(ctx, internal)
	grant := []model.ResourcePermissionGrant{{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: user.ID}, Permission: model.PermissionView}}
	seed := func(kind model.PermissionResourceKind, uid string, visible bool) {
		ref := model.PermissionResource{OrgID: 1, Kind: kind, UID: uid}
		doc, err := f.primary.Store.InitializeResourcePermissions(internalCtx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, trusted)
		require.NoError(t, err)
		if visible {
			_, err = f.primary.Store.ReplaceResourcePermissions(internalCtx, ref, doc.Version, grant, trusted)
			require.NoError(t, err)
		}
	}
	for _, owner := range []string{"visible-owner", "private-owner"} {
		_, err = f.primary.Store.CreateFolder(ctx, model.Folder{UID: owner, Title: owner})
		require.NoError(t, err)
		seed(model.PermissionFolder, owner, owner == "visible-owner")
	}
	_, err = f.primary.Store.SaveLibraryElement(ctx, model.LibraryElement{UID: "private-library", Name: "Private library", Kind: 1, FolderUID: "private-owner", Model: json.RawMessage(`{"type":"text","options":{"content":"PRIVATE_LIBRARY_PAYLOAD_DO_NOT_EXPOSE"}}`)}, true)
	require.NoError(t, err)
	_, err = f.primary.Store.SaveLibraryElement(ctx, model.LibraryElement{UID: "public-library", Name: "Public library", Kind: 1, FolderUID: "visible-owner", Model: json.RawMessage(`{"type":"text","options":{"nested":{"id":2,"type":"text","libraryPanel":{"uid":"private-library"}}}}`)}, true)
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(ctx, model.Dashboard{ID: "allowed", Name: "Allowed dashboard", Grafana: json.RawMessage(`{"uid":"allowed","panels":[{"id":1,"type":"text","libraryPanel":{"uid":"public-library"}}]}`)})
	require.NoError(t, err)
	seed(model.PermissionDashboard, "allowed", true)
	policy, err := NewResourceReadPolicy(f.registry, f.auth)
	require.NoError(t, err)
	authorizer, err := f.auth.NewRequestAuthorizer(policy)
	require.NoError(t, err)
	f.primary.Authorizer = authorizer
	handler, err := NewProtectedOrganizationHandler(f.registry, f.auth, authorizer)
	require.NoError(t, err)
	_, key := f.login(t, user)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, cookieRequest("/api/dashboards/uid/allowed", key))
	require.NotContains(t, response.Body.String(), "PRIVATE_LIBRARY_PAYLOAD_DO_NOT_EXPOSE")
	require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
}

func TestResourceReadHTTPLastCredentialCallbackCannotRevokeUncheckedACL(t *testing.T) {
	f := newHTTPAuthenticationFixture(t)
	ctx := context.Background()
	user, err := f.catalog.CreateUser(ctx, identity.UserInput{Login: "read-none", Password: httpFixturePassword, OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	_, err = f.primary.Store.SaveDashboard(ctx, model.Dashboard{ID: "allowed", Name: "Allowed", Grafana: json.RawMessage(`{"uid":"allowed","panels":[{"id":1,"type":"text","options":{"content":"BUFFERED_BODY_PRIVATE_AFTER_REVOKE"}}]}`)})
	require.NoError(t, err)
	internal := identity.BackgroundPrincipal(1)
	trusted, err := NewPermissionIdentityResolver(f.catalog, PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	internalCtx := identity.WithPrincipal(ctx, internal)
	ref := model.PermissionResource{OrgID: 1, Kind: model.PermissionDashboard, UID: "allowed"}
	doc, err := f.primary.Store.InitializeResourcePermissions(internalCtx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, trusted)
	require.NoError(t, err)
	doc, err = f.primary.Store.ReplaceResourcePermissions(internalCtx, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: user.ID}, Permission: model.PermissionView}}, trusted)
	require.NoError(t, err)
	resolver, err := f.auth.PermissionResolver()
	require.NoError(t, err)
	originalCurrent := resolver.CurrentPrincipal
	var calls atomic.Int64
	var revoked atomic.Bool
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		fresh, err := originalCurrent(ctx, p)
		if err != nil {
			return fresh, err
		}
		// Fault injection is the final actor callback before the control snapshot.
		// It must commit a real revocation before any buffered response is sent.
		if calls.Add(1) == 7 {
			_, err = f.primary.Store.ReplaceResourcePermissions(internalCtx, ref, doc.Version, nil, trusted)
			if err != nil {
				return identity.Principal{}, err
			}
			revoked.Store(true)
		}
		return fresh, nil
	}
	authorizer, err := f.auth.NewRequestAuthorizer(func(r *http.Request, route server.ProtectedRoute, _ identity.Principal) error {
		if !server.ResourceReadRoute(route) {
			return identity.ErrForbidden
		}
		return f.primary.BindResourceReadAuthorization(r, route, resolver)
	})
	require.NoError(t, err)
	f.primary.Authorizer = authorizer
	handler, err := NewProtectedOrganizationHandler(f.registry, f.auth, authorizer)
	require.NoError(t, err)
	_, key := f.login(t, user)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, cookieRequest("/api/dashboards/uid/allowed", key))
	require.True(t, revoked.Load(), "fixture must really revoke in the late revalidation callback; calls=%d", calls.Load())
	require.NotContains(t, response.Body.String(), "BUFFERED_BODY_PRIVATE_AFTER_REVOKE")
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
}

func TestResourceReadHTTPLaterFenceActorCannotRevokeEarlierResource(t *testing.T) {
	f := newResourceReadFixture(t)
	ctx := context.Background()
	for _, uid := range []string{"a-first", "b-second"} {
		_, err := f.primary.Store.SaveDashboard(ctx, model.Dashboard{ID: uid, Name: uid, Grafana: json.RawMessage(`{"uid":"` + uid + `","panels":[],"private":"FIRST_RESOURCE_PRIVATE"}`)})
		require.NoError(t, err)
	}
	f.start(t)
	f.grant(t, model.PermissionDashboard, "a-first", model.PermissionView)
	f.grant(t, model.PermissionDashboard, "b-second", model.PermissionView)
	f.server.Close()
	ref := model.PermissionResource{OrgID: 1, Kind: model.PermissionDashboard, UID: "a-first"}
	doc, err := f.primary.Store.ResourcePermissions(f.internal, ref)
	require.NoError(t, err)
	resolver, err := f.auth.PermissionResolver()
	require.NoError(t, err)
	originalCurrent := resolver.CurrentPrincipal
	var calls atomic.Int64
	var revoked atomic.Bool
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		fresh, err := originalCurrent(ctx, p)
		if err != nil {
			return fresh, err
		}
		// Bind + three dashboard captures (including denied system) + response
		// credential + first/second remembered actors: mutate on the final actor.
		if calls.Add(1) == 13 {
			_, err = f.primary.Store.ReplaceResourcePermissions(f.internal, ref, doc.Version, nil, f.resolver)
			if err != nil {
				return identity.Principal{}, err
			}
			revoked.Store(true)
		}
		return fresh, nil
	}
	authorizer, err := f.auth.NewRequestAuthorizer(func(r *http.Request, route server.ProtectedRoute, _ identity.Principal) error {
		if !server.ResourceReadRoute(route) {
			return identity.ErrForbidden
		}
		return f.primary.BindResourceReadAuthorization(r, route, resolver)
	})
	require.NoError(t, err)
	f.primary.Authorizer = authorizer
	handler, err := NewProtectedOrganizationHandler(f.registry, f.auth, authorizer)
	require.NoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, cookieRequest("/api/v1/dashboards", f.key))
	require.True(t, revoked.Load(), "fixture must really revoke during the later actor callback; calls=%d", calls.Load())
	require.NotContains(t, response.Body.String(), "FIRST_RESOURCE_PRIVATE")
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
}
