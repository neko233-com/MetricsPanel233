package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/live"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/querycontext"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func registryFixture(t *testing.T) (*Registry, *identity.Store, *server.Server) {
	t.Helper()
	dir := t.TempDir()
	control, err := store.Open(filepath.Join(dir, "original.db"))
	require.NoError(t, err)
	primary := server.New(control, "legacy-token", 7)
	catalog, err := identity.Open(filepath.Join(dir, "identity.db"))
	require.NoError(t, err)
	r, err := New(Config{PrimaryServer: primary, OrganizationLookup: catalog.Organization, OrganizationList: catalog.Organizations})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, r.Close(context.Background()))
		primary.CloseSQLSources()
		primary.Live.Close()
		primary.Plugins.Close()
		control.DB.Close()
		catalog.Close()
	})
	return r, catalog, primary
}

func activate(t *testing.T, r *Registry, catalog *identity.Store, name string) (identity.Organization, *server.Server) {
	t.Helper()
	ctx := context.Background()
	org, err := catalog.BeginOrganization(ctx, name)
	require.NoError(t, err)
	_, err = r.Resolve(ctx, org.ID)
	require.ErrorIs(t, err, ErrUnavailable)
	prepared, err := r.Initialize(ctx, org.ID)
	require.NoError(t, err)
	org, err = catalog.ActivateOrganization(ctx, org.ID, org.Version)
	require.NoError(t, err)
	resolved, err := r.Resolve(ctx, org.ID)
	require.NoError(t, err)
	require.Same(t, prepared, resolved)
	return org, resolved
}

func actor(orgID int64) identity.Principal {
	return identity.Principal{OrgID: orgID, ID: 233, Type: identity.TypeUser, Login: fmt.Sprintf("user-%d", orgID), Name: "Fixture", Role: identity.RoleEditor}
}

func TestOrganizationRegistryResourceSecretsAndRestartIsolation(t *testing.T) {
	r, catalog, primary := registryFixture(t)
	org, secondary := activate(t, r, catalog, "Team two")
	require.Equal(t, int64(2), org.ID)
	require.Equal(t, primary.Store.Path, r.Primary().Store.Path)
	require.NotSame(t, primary.Store, secondary.Store)
	require.NotSame(t, primary.Collector, secondary.Collector)
	require.NotSame(t, primary.Alerts, secondary.Alerts)
	require.NotSame(t, primary.Live, secondary.Live)
	require.NotSame(t, primary.Plugins, secondary.Plugins)
	require.Equal(t, primary.Plugins.Root, secondary.Plugins.Root)
	require.Equal(t, filepath.Join(r.Root(), "2", "control.db"), secondary.Store.Path)
	keys := [][]byte{}
	for _, app := range []*server.Server{primary, secondary} {
		ctx := identity.WithPrincipal(context.Background(), actor(app.OrganizationID()))
		d, err := app.Store.SaveDashboard(ctx, model.Dashboard{ID: "same", Name: actor(app.OrganizationID()).Login, Panels: []model.Panel{}})
		require.NoError(t, err)
		ds, err := app.Store.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "same", Name: "Same", Type: "mysql"}, SecureJSONData: map[string]string{"password": d.Name}})
		require.NoError(t, err)
		require.Equal(t, app.OrganizationID(), ds.OrgID)
		_, err = app.Store.CreateAnnotation(ctx, model.Annotation{DashboardUID: d.UID(), Time: 1000, Text: d.Name, Login: d.Name, UserID: 233}, "same-key")
		require.NoError(t, err)
		_, err = app.Store.SaveLibraryElement(ctx, model.LibraryElement{UID: "same", Name: d.Name, Kind: 1, Model: []byte(`{"type":"stat"}`)}, true)
		require.NoError(t, err)
		require.NoError(t, app.Store.Ingest(ctx, []model.Sample{{Name: "same_metric", Timestamp: 1000, Value: float64(app.OrganizationID())}}))
		key, err := os.ReadFile(filepath.Join(filepath.Dir(app.Store.Path), "secrets.key"))
		require.NoError(t, err)
		keys = append(keys, key)
	}
	require.NotEqual(t, keys[0], keys[1])
	verify := func(app *server.Server) {
		ctx := identity.WithPrincipal(context.Background(), actor(app.OrganizationID()))
		d, err := app.Store.Dashboard(ctx, "same")
		require.NoError(t, err)
		require.Equal(t, actor(app.OrganizationID()).Login, d.Name)
		secrets, err := app.Store.DataSourceSecrets(ctx, "same")
		require.NoError(t, err)
		require.Equal(t, d.Name, secrets["password"])
		annotations, err := app.Store.Annotations(ctx, model.AnnotationQuery{DashboardUID: d.UID()})
		require.NoError(t, err)
		require.Len(t, annotations, 1)
		require.Equal(t, d.Name, annotations[0].Text)
		versions, err := app.Store.DashboardVersions(ctx, d.ID, 100, 0, "")
		require.NoError(t, err)
		require.Len(t, versions.Versions, 1)
		require.Equal(t, d.Name, versions.Versions[0].CreatedBy)
		library, err := app.Store.LibraryElement(ctx, "same")
		require.NoError(t, err)
		require.Equal(t, app.OrganizationID(), library.OrgID)
		result, err := app.Store.Query(ctx, model.Query{Metric: "same_metric", Start: 1000, End: 2000, Step: 1000, Aggregation: "last"})
		require.NoError(t, err)
		require.Equal(t, float64(app.OrganizationID()), result.Series[0].Points[0].Value)
	}
	verify(primary)
	verify(secondary)
	_, err := r.Resolve(context.Background(), 99)
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = r.ResolvePrincipal(context.Background())
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	disabled, err := catalog.SetOrganizationState(context.Background(), org.ID, org.Version, identity.Disabled)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), org.ID)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, r.Stop(context.Background(), org.ID))
	require.Error(t, secondary.Store.DB.Ping())
	_, err = catalog.SetOrganizationState(context.Background(), org.ID, disabled.Version, identity.Active)
	require.NoError(t, err)
	require.NoError(t, r.Close(context.Background()))
	require.NoError(t, primary.Store.DB.Ping(), "legacy primary remains caller-owned")
	// Reopen the registry against the same durable organization catalog.
	restarted, err := New(r.config)
	require.NoError(t, err)
	defer restarted.Close(context.Background())
	reopened, err := restarted.ResolvePrincipal(identity.WithPrincipal(context.Background(), actor(org.ID)))
	require.NoError(t, err)
	verify(primary)
	verify(reopened)
	key, err := os.ReadFile(filepath.Join(filepath.Dir(reopened.Store.Path), "secrets.key"))
	require.NoError(t, err)
	require.Equal(t, keys[1], key)
	_, err = primary.Store.Dashboard(context.Background(), "foreign-only")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOrganizationRegistryShutdownContinuesAfterCanceledWait(t *testing.T) {
	r, catalog, _ := registryFixture(t)
	org, app := activate(t, r, catalog, "Delayed shutdown")
	done := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(done) }) })
	r.mu.Lock()
	entry := r.servers[org.ID]
	entry.done, entry.cancel = done, func() {}
	r.mu.Unlock()
	disabled, err := catalog.SetOrganizationState(context.Background(), org.ID, org.Version, identity.Disabled)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	defer cancel()
	lookup := r.config.OrganizationLookup
	r.config.OrganizationLookup = func(ctx context.Context, id int64) (identity.Organization, error) {
		org, err := lookup(ctx, id)
		if ctx == canceled {
			cancel()
		}
		return org, err
	}
	require.ErrorIs(t, r.Stop(canceled, org.ID), context.Canceled)
	_, err = catalog.SetOrganizationState(context.Background(), org.ID, disabled.Version, identity.Active)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), org.ID)
	require.ErrorIs(t, err, ErrUnavailable, "reactivation cannot race unfinished shutdown")
	require.ErrorIs(t, r.Close(canceled), context.Canceled)
	release.Do(func() { close(done) })
	require.NoError(t, r.Close(context.Background()))
	require.Error(t, app.Store.DB.Ping())
}

func TestOrganizationRegistryBackgroundOwnsWorkersAndStartupFailure(t *testing.T) {
	r, catalog, primary := registryFixture(t)
	org, app := activate(t, r, catalog, "Worker")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.RunBackground(ctx) }()
	require.Eventually(t, func() bool {
		stats, err := app.Store.Stats(context.Background())
		return err == nil && stats.Samples > 0
	}, 5*time.Second, 10*time.Millisecond)
	stats, err := primary.Store.Stats(context.Background())
	require.NoError(t, err)
	require.Zero(t, stats.Samples, "ManagePrimary=false must not start duplicate primary workers")
	_, err = catalog.SetOrganizationState(context.Background(), org.ID, org.Version, identity.Disabled)
	require.NoError(t, err)
	require.NoError(t, r.Stop(context.Background(), org.ID))
	require.Error(t, app.Store.DB.Ping())
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("registry lifecycle did not exit")
	}

	failed, _, _ := registryFixture(t)
	failed.config.OrganizationList = func(context.Context) ([]identity.Organization, error) { return nil, errors.New("catalog unavailable") }
	require.ErrorContains(t, failed.RunBackground(context.Background()), "catalog unavailable")
	_, err = failed.Resolve(context.Background(), 1)
	require.ErrorIs(t, err, ErrClosed, "failed startup must close its owned instances")
}

func TestOrganizationRegistryClickHouseNamespaceAndFactoryGuard(t *testing.T) {
	r, catalog, primary := registryFixture(t)
	var mu sync.Mutex
	var queries []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		queries = append(queries, req.URL.Query().Get("query"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer endpoint.Close()
	ch, err := store.OpenClickHouse(context.Background(), endpoint.URL, "legacy_metrics", "fixture", "fixture", 7)
	require.NoError(t, err)
	primary.Store.Backend = ch
	_, app := activate(t, r, catalog, "ClickHouse two")
	second := app.Store.Backend.(*store.ClickHouse)
	require.NotSame(t, ch, second)
	require.Equal(t, "legacy_metrics_org_2", second.Database)
	require.NoError(t, second.Ingest(context.Background(), []model.Sample{{Name: "up", Value: 2}}))
	mu.Lock()
	observed := strings.Join(queries, "\n")
	mu.Unlock()
	require.Contains(t, observed, "CREATE DATABASE IF NOT EXISTS legacy_metrics_org_2")
	require.Contains(t, observed, "INSERT INTO legacy_metrics_org_2.samples")
	// Factory accidentally returning a shared pool/backend must fail before dispatch.
	r.config.StorageFactory = func(context.Context, identity.Organization, string) (store.MetricBackend, error) { return ch, nil }
	third, err := catalog.BeginOrganization(context.Background(), "Invalid shared backend")
	require.NoError(t, err)
	_, err = r.Initialize(context.Background(), third.ID)
	require.ErrorContains(t, err, "share a backend")
	for _, invalid := range []string{"", "1metrics", "metrics;DROP", "metrics/path", "metrics`"} {
		_, err := ClickHouseDatabase(invalid, 2)
		require.Error(t, err)
	}
	_, err = canonicalRoot("relative-root")
	require.ErrorContains(t, err, "absolute path")
}

func TestOrganizationHTTPMetadataAndActorStayInTrustedInstance(t *testing.T) {
	r, catalog, _ := registryFixture(t)
	org, app := activate(t, r, catalog, "HTTP two")
	request := func(method, path, body string, trusted bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer legacy-token")
		req.Header.Set("X-Grafana-Org-Id", "2")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if trusted {
			req = req.WithContext(identity.WithPrincipal(req.Context(), actor(org.ID)))
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("GET", "/api/datasources", "", false).Code)
	w := request("GET", "/api/datasources", "", true)
	require.Equal(t, 200, w.Code, w.Body.String())
	var sources []model.DataSource
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sources))
	require.NotEmpty(t, sources)
	for _, ds := range sources {
		require.Equal(t, int64(2), ds.OrgID)
	}
	w = request("POST", "/api/annotations", `{"time":1000,"text":"org-two event"}`, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	annotations, err := app.Store.Annotations(context.Background(), model.AnnotationQuery{})
	require.NoError(t, err)
	require.Len(t, annotations, 1)
	require.Equal(t, int64(233), annotations[0].UserID)
	require.Equal(t, "user-2", annotations[0].Login)
	ctx := identity.WithPrincipal(context.Background(), actor(org.ID))
	_, err = app.Store.SaveDashboard(ctx, model.Dashboard{ID: "same", Name: "Org two", Panels: []model.Panel{}})
	require.NoError(t, err)
	selector := "?labelSelector=grafana.app%2Fget-history%3Dtrue&fieldSelector=metadata.name%3Dsame"
	w = request("GET", "/apis/dashboard.grafana.app/v1/namespaces/org-2/dashboards"+selector, "", true)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"namespace":"org-2"`)
	require.Contains(t, w.Body.String(), `"grafana.app/updatedBy":"user-2"`)
	require.Equal(t, 404, request("GET", "/apis/dashboard.grafana.app/v1/namespaces/default/dashboards"+selector, "", true).Code)
	query := `{"from":"1000","to":"2000","queries":[{"refId":"A","expr":"vector(2)","instant":true}]}`
	w = request("POST", "/apis/prometheus.datasource.grafana.app/v0alpha1/namespaces/org-2/connections/metricspanel/query", query, true)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"values"`)
	require.Equal(t, 404, request("POST", "/apis/prometheus.datasource.grafana.app/v0alpha1/namespaces/default/connections/metricspanel/query", query, true).Code)

	rule := `{"orgID":2,"uid":"same-alert","title":"Org two rule","condition":"A","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(2)","instant":true}}]}`
	w = request("POST", "/api/v1/provisioning/alert-rules", rule, true)
	require.Equal(t, 201, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"orgID":2`)
	w = request("POST", "/api/v1/provisioning/alert-rules", strings.Replace(rule, `"orgID":2`, `"orgID":1`, 1), true)
	require.Equal(t, 400, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "physical store")
	originalSource := app.Alerts.GraphSource
	observed := false
	app.Alerts.GraphSource = func(ctx context.Context, uid string, q []backend.DataQuery) (backend.Responses, string, error) {
		p, valid := identity.FromContext(ctx)
		require.True(t, valid)
		require.Equal(t, int64(2), p.OrgID)
		require.Equal(t, "2", querycontext.Headers(ctx)["X-Grafana-Org-Id"])
		observed = true
		return originalSource(ctx, uid, q)
	}
	_, err = app.Alerts.Evaluate(ctx, "same-alert", time.Now())
	require.NoError(t, err)
	require.True(t, observed)
}

func TestOrganizationLiveRealTransportSameChannelIsolation(t *testing.T) {
	r, catalog, primary := registryFixture(t)
	_, secondary := activate(t, r, catalog, "Live two")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	file := filepath.Join(t.TempDir(), "fixture")
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	output, err := exec.CommandContext(ctx, "go", "build", "-o", file, "../plugins/testdata/sdk-backend").CombinedOutput()
	require.NoError(t, err, string(output))
	archive := filepath.Join(t.TempDir(), "fixture.zip")
	output, err = exec.CommandContext(ctx, file, "--package", archive).CombinedOutput()
	require.NoError(t, err, string(output))
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)
	primary.Plugins.AllowUnsigned["metricspanel-sdk-datasource"] = true
	secondary.Plugins.AllowUnsigned["metricspanel-sdk-datasource"] = true
	_, err = primary.Plugins.Install(ctx, raw, "")
	require.NoError(t, err)
	for _, app := range []*server.Server{primary, secondary} {
		_, err := app.Store.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "same", Name: "Same", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
		require.NoError(t, err)
	}
	servers := []*httptest.Server{}
	for _, app := range []*server.Server{primary, secondary} {
		app := app
		handler := app.Handler()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Internal dispatcher supplies a catalog-selected principal. HTTP
			// headers are deliberately unrelated to this trusted context.
			handler.ServeHTTP(w, req.WithContext(identity.WithPrincipal(req.Context(), actor(app.OrganizationID()))))
		}))
		defer srv.Close()
		servers = append(servers, srv)
	}
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	var mu sync.Mutex
	counts := [2]int{}
	markers := [2][]string{}
	done := []chan error{make(chan error, 1), make(chan error, 1)}
	for index, endpoint := range servers {
		index, endpoint := index, endpoint
		go func() {
			done[index] <- live.Watch(watchCtx, live.WatchOptions{Endpoint: endpoint.URL, Channel: "ds/same/counter", Header: http.Header{"X-Grafana-Org-Id": {"999"}}}, func(event live.Event) error {
				mu.Lock()
				defer mu.Unlock()
				counts[index]++
				var marker struct {
					Marker string `json:"marker"`
				}
				if json.Unmarshal(event.Data, &marker) == nil && marker.Marker != "" {
					markers[index] = append(markers[index], marker.Marker)
				}
				return nil
			})
		}()
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return counts[0] >= 3 && counts[1] >= 3
	}, 15*time.Second, 20*time.Millisecond)
	_, err = primary.Live.Publish(identity.WithPrincipal(ctx, actor(1)), "ds/same/counter", []byte(`{"marker":"org-one-only"}`))
	require.NoError(t, err)
	_, err = secondary.Live.Publish(identity.WithPrincipal(ctx, actor(2)), "ds/same/counter", []byte(`{"marker":"org-two-only"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(markers[0]) == 1 && len(markers[1]) == 1
	}, 5*time.Second, 10*time.Millisecond)
	mu.Lock()
	firstMarkers, secondMarkers := append([]string{}, markers[0]...), append([]string{}, markers[1]...)
	before := counts[1]
	mu.Unlock()
	require.Equal(t, []string{"org-one-only"}, firstMarkers)
	require.Equal(t, []string{"org-two-only"}, secondMarkers)
	primary.Live.Invalidate("ds", "same", false)
	select {
	case err := <-done[0]:
		require.ErrorContains(t, err, "configuration changed")
	case <-time.After(5 * time.Second):
		t.Fatal("authenticated user did not receive stream invalidation")
	}
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return counts[1] >= before+2 }, 5*time.Second, 20*time.Millisecond)
	require.Len(t, secondary.Live.Snapshot().Channels, 1, "same UID invalidation must remain in the primary org hub")
	_, err = secondary.Live.Publish(identity.WithPrincipal(ctx, actor(1)), "ds/same/counter", []byte(`{}`))
	require.Error(t, err)
	stop()
	select {
	case err := <-done[1]:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Live watcher did not stop")
	}
	require.Eventually(t, func() bool { return len(secondary.Live.Snapshot().Channels) == 0 }, 5*time.Second, 10*time.Millisecond)
}
