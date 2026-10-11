package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
)

type nativeCatalogInstall func(context.Context, string, string) (model.Plugin, error)

// install is injectable for deterministic signed-package tests. Production uses
// the same official downloader and signature installer as the native CLI.
func (s *Server) nativePluginRoutes(api *http.ServeMux, catalog *plugins.CatalogClient, install nativeCatalogInstall) {
	if install == nil {
		install = s.Plugins.CatalogInstall
	}
	api.HandleFunc("GET /api/gnet/plugins", func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values["includeDeprecated"]) > 1 {
			fail(w, 400, errors.New("includeDeprecated must be a single boolean"))
			return
		}
		for key := range values {
			if key != "includeDeprecated" {
				fail(w, 400, errors.New("plugin catalog accepts includeDeprecated only"))
				return
			}
		}
		deprecated := false
		if text := values.Get("includeDeprecated"); text != "" {
			deprecated, err = strconv.ParseBool(text)
			if err != nil {
				fail(w, 400, errors.New("includeDeprecated must be a boolean"))
				return
			}
		}
		raw, err := catalog.NativeList(r.Context(), deprecated)
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, 200, raw)
	})
	api.HandleFunc("GET /api/gnet/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		raw, err := catalog.NativeDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, 200, raw)
	})
	api.HandleFunc("GET /api/gnet/plugins/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		raw, err := catalog.NativeVersions(r.Context(), r.PathValue("id"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, 200, raw)
	})
	api.HandleFunc("GET /api/gnet/plugins/{id}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		raw, err := catalog.NativeVersion(r.Context(), r.PathValue("id"), r.PathValue("version"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, 200, raw)
	})
	api.HandleFunc("POST /api/plugins/{id}/install", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !model.PluginID.MatchString(id) {
			fail(w, 400, errors.New("valid plugin ID required"))
			return
		}
		var input struct {
			Version string `json:"version"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if input.Version == "" {
			version, err := catalog.LatestVersion(r.Context(), id)
			if err != nil {
				catalogFailure(w, err)
				return
			}
			input.Version = version
		}
		if len(input.Version) > 100 {
			fail(w, 400, errors.New("exact semantic version required"))
			return
		}
		if _, err := semver.StrictNewVersion(input.Version); err != nil {
			fail(w, 400, errors.New("exact semantic version required"))
			return
		}
		if _, err := install(r.Context(), id, input.Version); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, []byte{})
	})
	api.HandleFunc("POST /api/plugins/{id}/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Plugins.Uninstall(r.Context(), r.PathValue("id")); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				resourceError(w, err)
			} else {
				fail(w, 409, err)
			}
			return
		}
		writeJSON(w, 200, []byte{})
	})
	api.HandleFunc("GET /api/plugins/{id}/markdown/{document}", func(w http.ResponseWriter, r *http.Request) {
		body, err := s.Plugins.Markdown(r.Context(), r.PathValue("id"), r.PathValue("document"))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				resourceError(w, err)
			} else if errors.Is(err, plugins.ErrCatalogInput) {
				fail(w, 400, err)
			} else {
				fail(w, 500, err)
			}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(body)
	})
	api.HandleFunc("GET /api/plugins/errors", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Plugins.InspectionErrors(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, items)
	})
}

func (s *Server) nativeInstalledPluginList(w http.ResponseWriter, r *http.Request, catalog *plugins.CatalogClient) {
	query := r.URL.Query()
	items, err := s.Store.Plugins(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := []map[string]any{}
	for _, p := range items {
		if kind := query.Get("type"); kind != "" && kind != p.Type {
			continue
		}
		// Core plugins are served by this binary and are not installable packages.
		if query.Get("core") == "1" {
			continue
		}
		if p.PackageID != p.ID {
			if embedded := query.Get("embedded"); embedded == "0" || (strings.HasPrefix(embedded, "include-") && p.Type != strings.TrimPrefix(embedded, "include-")) {
				continue
			}
		}
		metadata, err := s.nativePluginMetadata(r.Context(), p.ID, catalog)
		if err != nil {
			fail(w, 500, err)
			return
		}
		if builtin, _ := metadata["builtIn"].(bool); builtin {
			continue
		}
		if query.Get("enabled") == "1" && metadata["enabled"] != true {
			continue
		}
		out = append(out, metadata)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["name"].(string)
		b, _ := out[j]["name"].(string)
		return a < b
	})
	writeJSON(w, 200, out)
}

func (s *Server) nativePluginMetadata(ctx context.Context, id string, catalog *plugins.CatalogClient) (map[string]any, error) {
	metadata, err := s.pluginMetadata(ctx, id)
	if err != nil {
		return nil, err
	}
	signature, err := s.Plugins.SignatureInfo(ctx, id)
	if err != nil {
		return nil, err
	}
	metadata["signature"], metadata["signatureType"], metadata["signatureOrg"] = signature.Status, signature.Type, signature.Org
	if metadata["pinned"] == nil {
		metadata["pinned"] = false
	}
	if metadata["autoEnabled"] == nil {
		metadata["autoEnabled"] = false
	}
	metadata["latestVersion"], metadata["hasUpdate"] = "", false
	if latest := catalog.CachedLatestVersion(id); latest != "" {
		metadata["latestVersion"] = latest
		info, _ := metadata["info"].(map[string]any)
		version, _ := info["version"].(string)
		old, oldErr := semver.NewVersion(version)
		next, nextErr := semver.NewVersion(latest)
		metadata["hasUpdate"] = oldErr == nil && nextErr == nil && next.GreaterThan(old)
	}
	metadata["defaultNavUrl"] = "/plugins/" + id + "/"
	if metadata["type"] == "app" && metadata["enabled"] == true {
		metadata["defaultNavUrl"] = "/a/" + id + "/"
	}
	if metadata["angularDetected"] == nil {
		angular, _ := metadata["angular"].(map[string]any)
		metadata["angularDetected"] = angular["detected"] == true
	}
	return metadata, nil
}
