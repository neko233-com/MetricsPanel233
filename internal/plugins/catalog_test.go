package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogTransportFunc func(*http.Request) (*http.Response, error)

func (f catalogTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func catalogTestClient(t *testing.T, handler http.HandlerFunc) *CatalogClient {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	base, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	return NewCatalogClient(catalogTransportFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https", request.URL.Scheme)
		assert.Equal(t, "grafana.com", request.URL.Host)
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("Cookie"))
		assert.Equal(t, RuntimeVersion, request.Header.Get("grafana-version"))
		assert.Equal(t, runtime.GOOS, request.Header.Get("grafana-os"))
		assert.Equal(t, runtime.GOARCH, request.Header.Get("grafana-arch"))
		forward := request.Clone(request.Context())
		forward.URL.Scheme, forward.URL.Host = base.Scheme, base.Host
		return http.DefaultTransport.RoundTrip(forward)
	}))
}

const clockCatalogDetail = `{"slug":"grafana-clock-panel","name":"Clock","typeCode":"panel","version":"3.2.4","description":"Clock panel","downloads":233,"signatureType":"grafana","url":"https://github.com/grafana/clock-panel","repositoryUrl":"https://github.com/grafana/clock-panel","documentationUrl":"javascript:alert(1)","licenseUrl":"https://user:password@example.com/LICENSE","raiseAnIssueUrl":"https://github.com/grafana/clock-panel/issues","readme":"<script>unsafe()</script>","json":{"info":{"author":{"name":"Grafana Labs"}},"dependencies":{"grafanaDependency":">=11.0.0-0"}},"packages":{"any":{"downloadUrl":"/api/plugins/grafana-clock-panel/versions/3.2.4/download"}}}`

func TestCatalogSearchDetailVersionsAndCache(t *testing.T) {
	ctx := context.Background()
	var lists, details, versions atomic.Int32
	catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/plugins":
			lists.Add(1)
			assert.Equal(t, "true", r.URL.Query().Get("includeDeprecated"))
			fmt.Fprint(w, `{"items":[`+clockCatalogDetail+`,{"slug":"native-source","name":"Database","typeCode":"datasource","version":"2.0.0","description":"Database access","orgName":"Database company","downloads":234,"keywords":["sql"],"packages":{"linux-amd64":{}}},{"slug":"old-app","name":"Application","typeCode":"app","version":"1.0.0","status":"deprecated","angularDetected":true},{"slug":"grafana-image-renderer","name":"Renderer","typeCode":"renderer","version":"4.1.5","status":"deprecated"}]}`)
		case "/api/plugins/grafana-clock-panel":
			details.Add(1)
			fmt.Fprint(w, clockCatalogDetail)
		case "/api/plugins/grafana-clock-panel/versions":
			versions.Add(1)
			fmt.Fprint(w, `{"items":[{"pluginSlug":"grafana-clock-panel","version":"1.0.0","packages":{"any":{}}},{"pluginSlug":"grafana-clock-panel","version":"3.2.4","createdAt":"2026-08-25T13:03:45Z","grafanaDependency":">=11.0.0-0","packages":{"any":{}}},{"pluginSlug":"grafana-clock-panel","version":"4.0.0-beta.1","angularDetected":true,"packages":{"any":{}}}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	all, err := catalog.List(ctx, "", "")
	require.NoError(t, err)
	require.Len(t, all.Items, 4)
	assert.Equal(t, "old-app", all.Items[0].ID)
	assert.True(t, all.Items[0].Deprecated)
	assert.True(t, all.Items[0].AngularDetected)
	assert.Equal(t, "grafana-clock-panel", all.Items[1].ID)
	assert.Equal(t, "Grafana Labs", all.Items[1].Author)
	assert.Equal(t, ">=11.0.0-0", all.Items[1].GrafanaDependency)
	assert.False(t, all.Items[1].Backend)
	assert.True(t, all.Items[2].Backend)
	assert.Equal(t, "renderer", all.Items[3].Type, "official unsupported renderer remains discoverable without a compatibility claim")
	filtered, err := catalog.List(ctx, "SQL company", "datasource")
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	assert.Equal(t, "native-source", filtered.Items[0].ID)
	empty, err := catalog.List(ctx, "missing", "app")
	require.NoError(t, err)
	assert.Empty(t, empty.Items)
	raw, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[]}`, string(raw))
	assert.Equal(t, int32(1), lists.Load(), "all searches reuse the successful complete catalog")

	detail, err := catalog.Detail(ctx, "grafana-clock-panel")
	require.NoError(t, err)
	assert.Equal(t, "grafana", detail.SignatureType)
	assert.Equal(t, "https://grafana.com/grafana/plugins/grafana-clock-panel/", detail.URL)
	require.Len(t, detail.Links, 3, "unsafe links are omitted and repository URL is deduplicated")
	raw, err = json.Marshal(detail)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "unsafe()")
	assert.NotContains(t, string(raw), "javascript:")
	assert.NotContains(t, string(raw), "password")
	available, err := catalog.Versions(ctx, detail.ID)
	require.NoError(t, err)
	require.Len(t, available.Items, 3)
	assert.Equal(t, "4.0.0-beta.1", available.Items[0].Version)
	assert.True(t, available.Items[0].AngularDetected)
	assert.Equal(t, "3.2.4", available.Items[1].Version)
	assert.Equal(t, "2026-08-25T13:03:45Z", available.Items[1].CreatedAt)
	for _, version := range available.Items {
		assert.True(t, version.PlatformAvailable, "frontend-only package is platform independent")
	}
	_, err = catalog.Versions(ctx, detail.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(1), details.Load())
	assert.Equal(t, int32(1), versions.Load())

	future := time.Now().Add(catalogCacheTTL + time.Second)
	catalog.now = func() time.Time { return future }
	_, err = catalog.List(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, int32(2), lists.Load(), "expired cache is refreshed")
}

func TestCatalogVersionsUseActualHostPackages(t *testing.T) {
	var paths atomic.Int32
	catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/versions") {
			fmt.Fprintf(w, `{"items":[{"version":"3.0.0","packages":{%q:{},"any":{}}},{"version":"2.0.0","packages":{"unavailable-arch":{},"any":{}}},{"version":"1.0.0","packages":{"any":{}}}]}`, runtime.GOOS+"-"+runtime.GOARCH)
		} else {
			fmt.Fprint(w, `{"slug":"native-source","name":"Native","typeCode":"datasource","version":"3.0.0","json":{"backend":true},"packages":{"any":{}}}`)
		}
	})
	versions, err := catalog.Versions(context.Background(), "native-source")
	require.NoError(t, err)
	require.Len(t, versions.Items, 3)
	assert.True(t, versions.Items[0].PlatformAvailable)
	assert.False(t, versions.Items[1].PlatformAvailable, "a native universal archive alone does not prove host executable availability")
	assert.False(t, versions.Items[2].PlatformAvailable)
	assert.Equal(t, int32(2), paths.Load())
}

func TestCatalogRejectsInvalidInputsWithoutNetwork(t *testing.T) {
	var requests atomic.Int32
	catalog := catalogTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", 500)
	})
	for _, test := range []struct{ query, kind string }{
		{strings.Repeat("x", 257), ""}, {"a\nsearch", "panel"}, {"\x00", ""}, {string([]byte{0xff}), ""}, {"", "sql"},
	} {
		_, err := catalog.List(context.Background(), test.query, test.kind)
		require.ErrorIs(t, err, ErrCatalogInput)
	}
	for _, id := range []string{"", "../clock", "plugin?x=1", "a/b", "https://example.com", strings.Repeat("a", 129)} {
		_, err := catalog.Detail(context.Background(), id)
		require.ErrorIs(t, err, ErrCatalogInput)
		_, err = catalog.Versions(context.Background(), id)
		require.ErrorIs(t, err, ErrCatalogInput)
	}
	assert.Zero(t, requests.Load())
}

func TestCatalogFailuresAreNotCachedAndBodyIsBounded(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"upstream failure", 503, `{"message":"offline"}`, "HTTP 503"},
		{"missing", 404, `{"message":"missing"}`, "not found"},
		{"malformed JSON", 200, "not JSON", "invalid Grafana catalog JSON"},
		{"multiple JSON values", 200, `{"items":[]} {}`, "one JSON value"},
		{"missing items", 200, `{}`, "2000 items"},
		{"invalid metadata", 200, `{"items":[{"slug":"../bad","name":"Bad","typeCode":"panel"}]}`, "invalid plugin metadata"},
		{"oversized", 200, strings.Repeat(" ", catalogResponseMax+1), "exceeds 4 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			catalog := catalogTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			})
			for range 2 {
				_, err := catalog.List(context.Background(), "", "")
				require.ErrorContains(t, err, test.want)
			}
			assert.Equal(t, int32(2), requests.Load())
			assert.Empty(t, catalog.cache)
		})
	}
}

func TestCatalogCancellationAndFixedHostRedirects(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		started, stopped := make(chan struct{}), make(chan struct{})
		catalog := catalogTestClient(t, func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(stopped)
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := catalog.List(ctx, "", ""); result <- err }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("catalog request did not start")
		}
		cancel()
		select {
		case err := <-result:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(3 * time.Second):
			t.Fatal("canceled request did not finish")
		}
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream did not receive request cancellation")
		}
		assert.Empty(t, catalog.cache)
	})
	t.Run("no alternate host or HTTP", func(t *testing.T) {
		for _, target := range []string{"https://example.com/api/plugins", "http://grafana.com/api/plugins", "https://grafana.com:443/api/plugins", "https://user:pass@grafana.com/api/plugins"} {
			var requests atomic.Int32
			catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Redirect(w, r, target, http.StatusFound)
			})
			_, err := catalog.List(context.Background(), "", "")
			require.ErrorContains(t, err, "redirects must stay")
			assert.Equal(t, int32(1), requests.Load(), "redirect target must never be requested")
		}
	})
}

func TestCatalogCacheBoundsAndConcurrentReads(t *testing.T) {
	catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/api/plugins/")
		fmt.Fprintf(w, `{"slug":%q,"name":%q,"typeCode":"panel","version":"1.0.0"}`, id, id)
	})
	for index := range catalogCacheEntries + 5 {
		_, err := catalog.Detail(context.Background(), "panel-"+strconv.Itoa(index))
		require.NoError(t, err)
	}
	assert.Len(t, catalog.cache, catalogCacheEntries)
	catalog.putCache("large-one", make([]byte, catalogResponseMax))
	catalog.putCache("large-two", make([]byte, catalogResponseMax))
	assert.LessOrEqual(t, catalog.cacheBytes, catalogCacheMax)
	assert.LessOrEqual(t, len(catalog.cache), catalogCacheEntries)

	results := make(chan error, 16)
	for range 16 {
		go func() {
			detail, err := catalog.Detail(context.Background(), "concurrent-panel")
			if err == nil && detail.ID != "concurrent-panel" {
				err = fmt.Errorf("wrong detail ID %s", detail.ID)
			}
			results <- err
		}()
	}
	for range 16 {
		require.NoError(t, <-results)
	}
	assert.LessOrEqual(t, catalog.cacheBytes, catalogCacheMax)
}

func TestCatalogInvalidDetailAndVersionDataNotCached(t *testing.T) {
	for _, response := range []string{
		`{"items":[{"pluginSlug":"other-panel","version":"1.0.0"}]}`,
		`{"items":[{"version":"latest"}]}`,
		`{"items":[{"version":"1.0.0"},{"version":"1.0.0"}]}`,
		`{}`,
	} {
		var versionCalls atomic.Int32
		catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/versions") {
				versionCalls.Add(1)
				fmt.Fprint(w, response)
				return
			}
			fmt.Fprint(w, clockCatalogDetail)
		})
		for range 2 {
			_, err := catalog.Versions(context.Background(), "grafana-clock-panel")
			require.Error(t, err)
		}
		assert.Equal(t, int32(2), versionCalls.Load())
	}
	catalog := catalogTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, clockCatalogDetail)
	})
	_, err := catalog.Detail(context.Background(), "other-panel")
	require.ErrorContains(t, err, "different plugin ID")
	assert.Empty(t, catalog.cache)
}
