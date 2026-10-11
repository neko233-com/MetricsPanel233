package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResourceCLIAndRoundTrips(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "resources")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, "resources", 30)
	defer srv.Plugins.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	_, err = capture(t, "folders", "create", "--id", "team", "--name", "Team", "--server", h.URL)
	require.NoError(t, err)
	folder, err := capture(t, "folders", "get", "--id", "team", "--server", h.URL)
	require.NoError(t, err)
	folderPath := filepath.Join(t.TempDir(), "folder.json")
	require.NoError(t, os.WriteFile(folderPath, folder, 0600))
	_, err = capture(t, "folders", "save", "--id", "team", "--file", folderPath, "--server", h.URL)
	require.NoError(t, err)
	_, err = capture(t, "folders", "save", "--id", "team", "--file", folderPath, "--server", h.URL)
	require.ErrorContains(t, err, "HTTP 412: resource version changed")
	libraryPath := filepath.Join(t.TempDir(), "library.json")
	require.NoError(t, os.WriteFile(libraryPath, []byte(`{"uid":"shared","name":"Shared","kind":1,"folderUid":"team","model":{"type":"text","title":"Shared","options":{"content":"hello"},"opaque":900719925474099312345}}`), 0600))
	_, err = capture(t, "library", "save", "--file", libraryPath, "--server", h.URL)
	require.NoError(t, err)
	library, err := capture(t, "library", "get", "--id", "shared", "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(library), "900719925474099312345")
	require.NoError(t, os.WriteFile(libraryPath, library, 0600))
	_, err = capture(t, "library", "save", "--id", "shared", "--file", libraryPath, "--server", h.URL)
	require.NoError(t, err)
	model, err := capture(t, "library", "model", "--id", "shared", "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(model), "900719925474099312345")
	d, err := s.Dashboard(context.Background(), "system")
	require.NoError(t, err)
	_, err = capture(t, "dashboards", "library-add", "--id", d.ID, "--library-uid", "shared", "--revision", strconv.FormatInt(d.UpdatedAt, 10), "--server", h.URL)
	require.NoError(t, err)
	d, err = s.Dashboard(context.Background(), "system")
	require.NoError(t, err)
	require.Len(t, d.Panels, 4)
	connections, err := capture(t, "library", "connections", "--id", "shared", "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(connections), `"connectionUid": "system"`)
	_, err = capture(t, "dashboards", "library-unlink", "--id", d.ID, "--panel", d.Panels[3].ID, "--revision", strconv.FormatInt(d.UpdatedAt, 10), "--server", h.URL)
	require.NoError(t, err)
	_, err = capture(t, "library", "delete", "--id", "shared", "--server", h.URL)
	require.NoError(t, err)
	_, err = capture(t, "folders", "delete", "--id", "team", "--server", h.URL)
	require.NoError(t, err)
	discovery, err := capture(t, "schema")
	require.NoError(t, err)
	for _, field := range []string{"library_panels", "folders", "api_cli", "/api/dashboards/import", "/api/gnet/plugins"} {
		require.Contains(t, string(discovery), field)
	}
}

func TestGenericAPICLIPreservesBodiesAndAuthenticatesText(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "native-token")
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer native-token", r.Header.Get("Authorization"))
		if r.URL.Path == "/api/plugins/clock/markdown/README" {
			w.Header().Set("Content-Type", "text/markdown")
			w.Write([]byte("# Clock\nA real response.\n"))
			return
		}
		require.Equal(t, "PATCH", r.Method)
		var fields map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fields))
		require.Equal(t, "900719925474099312345", string(fields["opaque"]))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fields)
	}))
	defer h.Close()
	input := filepath.Join(t.TempDir(), "api.json")
	require.NoError(t, os.WriteFile(input, []byte(`{"opaque":900719925474099312345}`), 0600))
	result, err := capture(t, "api", "--method", "PATCH", "--path", "/api/library-elements/shared", "--file", input, "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(result), "900719925474099312345")
	result, err = capture(t, "api", "--path", "/api/plugins/clock/markdown/README", "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(result), "text/markdown")
	require.Contains(t, string(result), "# Clock")
	for _, path := range []string{"https://evil.example/api/private", "//evil.example/api/private", "/outside", "/api/test#fragment"} {
		_, err = capture(t, "api", "--path", path, "--server", h.URL)
		require.ErrorContains(t, err, "local /api/")
	}
}
