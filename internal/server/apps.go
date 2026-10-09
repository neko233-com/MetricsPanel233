package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func (s *Server) pluginMetadata(ctx context.Context, id string) (map[string]any, error) {
	p, err := s.Store.PluginEffective(ctx, id)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{}
	if err = json.Unmarshal(p.Metadata, &metadata); err != nil {
		return nil, err
	}
	metadata["enabled"] = p.Enabled
	metadata["signature"] = p.Signature
	metadata["module"] = "public/plugins/" + id + "/module.js"
	metadata["baseUrl"] = "public/plugins/" + id
	if p.Type == "app" {
		app, err := s.Store.AppSettings(ctx, id)
		if err != nil {
			return nil, err
		}
		metadata["jsonData"] = app.JSONData
		metadata["secureJsonFields"] = app.SecureJSONFields
		metadata["pinned"] = app.Pinned
		metadata["version"] = app.Version
		metadata["updated_at"] = app.UpdatedAt
	}
	return metadata, nil
}
func (s *Server) invalidatePlugin(ctx context.Context, id string, retry bool) {
	p, err := s.Store.Plugin(ctx, id)
	if err != nil {
		return
	}
	if p.PackageID != p.ID {
		s.Live.Invalidate("plugin", id, retry)
		return
	}
	plugins, err := s.Store.Plugins(ctx)
	if err != nil {
		return
	}
	for _, child := range plugins {
		if child.PackageID == p.PackageID {
			s.Live.Invalidate("plugin", child.ID, retry)
		}
	}
}
func (s *Server) appRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/plugins/{id}/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		pc, err := s.Plugins.AppContext(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, 403, err)
			return
		}
		health, err := s.Plugins.HealthContext(ctx, pc)
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"status": health.Status.String(), "message": health.Message})
	})
	api.HandleFunc("GET /api/v1/plugins/{id}/app-settings", func(w http.ResponseWriter, r *http.Request) {
		app, err := s.Store.AppSettings(r.Context(), r.PathValue("id"))
		if err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, app)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var input model.AppSettingsInput
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if r.Method == http.MethodPut && input.Version == nil {
			fail(w, 400, errors.New("version is required when updating application settings"))
			return
		}
		settings, err := s.Plugins.ConfigureApp(r.Context(), r.PathValue("id"), input)
		if err != nil {
			status := 400
			if errors.Is(err, store.ErrAppConflict) {
				status = 409
			}
			fail(w, status, err)
			return
		}
		s.invalidatePlugin(r.Context(), r.PathValue("id"), settings.Enabled)
		writeJSON(w, 200, map[string]any{"message": "Plugin settings updated", "settings": settings})
	}
	api.HandleFunc("POST /api/plugins/{id}/settings", save)
	api.HandleFunc("PUT /api/v1/plugins/{id}/app-settings", save)
	api.HandleFunc("/api/plugins/{id}/resources/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		pc, err := s.Plugins.AppContext(ctx, r.PathValue("id"))
		if err != nil {
			fail(w, 403, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, 400, err)
			return
		}
		target := r.PathValue("rest")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		responses, err := s.Plugins.ResourceContext(ctx, pc, &backend.CallResourceRequest{Method: r.Method, Path: r.PathValue("rest"), URL: target, Body: body, Headers: map[string][]string{"Content-Type": {r.Header.Get("Content-Type")}}})
		if err != nil {
			fail(w, 502, err)
			return
		}
		writePluginResource(w, responses)
	})
}
func writePluginResource(w http.ResponseWriter, responses []*backend.CallResourceResponse) {
	if len(responses) == 0 {
		w.WriteHeader(204)
		return
	}
	for key, values := range responses[0].Headers {
		if strings.EqualFold(key, "Set-Cookie") || strings.EqualFold(key, "Access-Control-Allow-Origin") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := responses[0].Status
	if status < 100 || status > 599 {
		status = 200
	}
	w.WriteHeader(status)
	for _, response := range responses {
		_, _ = w.Write(response.Body)
	}
}
