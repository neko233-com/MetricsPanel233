package server

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// Grafana-compatible dashboard endpoints allow existing dashboard-as-code tools
// to manage templates. All routes share the native API's authentication policy.
func (s *Server) grafanaRoutes(api *http.ServeMux, prometheus http.Handler) {
	api.HandleFunc("POST /api/dashboards/db", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Dashboard json.RawMessage `json:"dashboard"`
			Overwrite bool            `json:"overwrite"`
			FolderID  int64           `json:"folderId"`
			FolderUID string          `json:"folderUid"`
			Message   string          `json:"message"`
		}
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		if request.FolderID != 0 || request.FolderUID != "" {
			fail(w, 400, errors.New("only the root dashboard folder is currently supported"))
			return
		}
		result, err := grafana.Import(request.Dashboard)
		if err != nil {
			fail(w, 400, err)
			return
		}
		document := classicDocument(result.Dashboard)
		uid, _ := document["uid"].(string)
		if uid == "" {
			uid = uuid.NewString()
		}
		if len(uid) > 128 || strings.ContainsAny(uid, "/?#\\") {
			fail(w, 400, errors.New("invalid dashboard uid"))
			return
		}
		existing, found, err := s.grafanaDashboard(r.Context(), uid)
		if err != nil {
			fail(w, 500, err)
			return
		}
		version := 1
		if found {
			old := classicDocument(existing)
			version = int(numeric(old["version"])) + 1
			incoming := int(numeric(document["version"]))
			if !request.Overwrite && incoming != version-1 {
				writeJSON(w, 412, map[string]string{"status": "version-mismatch", "message": "dashboard exists; provide its current version or overwrite=true"})
				return
			}
			result.Dashboard.ID = existing.ID
		}
		document["uid"] = uid
		document["version"] = version
		document["id"] = crc32.ChecksumIEEE([]byte(uid))
		result.Dashboard.Grafana, err = json.Marshal(document)
		if err != nil {
			fail(w, 500, err)
			return
		}
		saved, err := s.Store.SaveDashboard(r.Context(), result.Dashboard)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": document["id"], "uid": uid, "slug": slug(saved.Name), "url": "/d/" + url.PathEscape(uid) + "/" + slug(saved.Name), "status": "success", "version": version, "warnings": s.filterPluginWarnings(r.Context(), result.Warnings)})
	})
	api.HandleFunc("GET /api/dashboards/uid/{uid}", func(w http.ResponseWriter, r *http.Request) {
		d, found, err := s.grafanaDashboard(r.Context(), r.PathValue("uid"))
		if err != nil {
			fail(w, 500, err)
			return
		}
		if !found {
			fail(w, 404, errors.New("dashboard not found"))
			return
		}
		writeJSON(w, 200, map[string]any{"dashboard": classicDocument(d), "meta": map[string]any{"isStarred": false, "canSave": true, "canEdit": true, "canAdmin": true, "folderId": 0, "folderUid": "", "folderTitle": "General", "url": "/d/" + url.PathEscape(r.PathValue("uid")) + "/" + slug(d.Name)}})
	})
	api.HandleFunc("DELETE /api/dashboards/uid/{uid}", func(w http.ResponseWriter, r *http.Request) {
		d, found, err := s.grafanaDashboard(r.Context(), r.PathValue("uid"))
		if err != nil {
			fail(w, 500, err)
			return
		}
		if !found {
			fail(w, 404, errors.New("dashboard not found"))
			return
		}
		if d.ID == "system" {
			fail(w, 400, errors.New("system dashboard cannot be deleted"))
			return
		}
		if err = s.Store.DeleteDashboard(r.Context(), d.ID); err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"title": d.Name, "message": "Dashboard deleted", "id": classicDocument(d)["id"]})
	})
	api.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		dashboards, err := s.Store.Dashboards(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		result := []map[string]any{}
		query := strings.ToLower(r.URL.Query().Get("query"))
		for _, d := range dashboards {
			if len(d.Grafana) == 0 || !strings.Contains(strings.ToLower(d.Name), query) {
				continue
			}
			doc := classicDocument(d)
			uid, _ := doc["uid"].(string)
			if uid == "" {
				uid = d.ID
			}
			if wanted := r.URL.Query()["dashboardUIDs"]; len(wanted) > 0 && !contains(wanted, uid) {
				continue
			}
			result = append(result, map[string]any{"id": crc32.ChecksumIEEE([]byte(uid)), "uid": uid, "title": d.Name, "type": "dash-db", "url": "/d/" + url.PathEscape(uid) + "/" + slug(d.Name), "uri": "db/" + slug(d.Name), "tags": doc["tags"], "isStarred": false})
		}
		limit := 100
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
		page := 1
		if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 && n <= 1000000 {
			page = n
		}
		start := (page - 1) * limit
		if start > len(result) {
			start = len(result)
		}
		end := start + limit
		if end > len(result) {
			end = len(result)
		}
		writeJSON(w, 200, result[start:end])
	})
	s.datasourceRoutes(api, prometheus)
}

func (s *Server) grafanaDashboard(ctx context.Context, uid string) (model.Dashboard, bool, error) {
	dashboards, err := s.Store.Dashboards(ctx)
	if err != nil {
		return model.Dashboard{}, false, err
	}
	for _, d := range dashboards {
		if len(d.Grafana) > 0 {
			doc := classicDocument(d)
			if doc["uid"] == uid || d.ID == uid {
				return d, true, nil
			}
		}
	}
	return model.Dashboard{}, false, nil
}
func classicDocument(d model.Dashboard) map[string]any {
	var doc map[string]any
	_ = json.Unmarshal(d.Grafana, &doc)
	if wrapped, ok := doc["dashboard"].(map[string]any); ok {
		doc = wrapped
	}
	resourceUID := ""
	if metadata, ok := doc["metadata"].(map[string]any); ok {
		resourceUID, _ = metadata["name"].(string)
	}
	if spec, ok := doc["spec"].(map[string]any); ok {
		doc = spec
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if doc["elements"] != nil { // Public resource -> classic API response.
		panels := []any{}
		for _, p := range d.Panels {
			var config map[string]any
			_ = json.Unmarshal(p.Config, &config)
			panels = append(panels, config)
		}
		variables := []any{}
		for _, v := range d.Variables {
			var config map[string]any
			_ = json.Unmarshal(v.Config, &config)
			variables = append(variables, config)
		}
		doc = map[string]any{"title": d.Name, "panels": panels, "templating": map[string]any{"list": variables}, "schemaVersion": 41}
	}
	if doc["uid"] == nil {
		if resourceUID != "" {
			doc["uid"] = resourceUID
		} else {
			doc["uid"] = d.ID
		}
	}
	if doc["version"] == nil {
		doc["version"] = 1
	}
	return doc
}
func numeric(value any) float64 {
	if n, ok := value.(float64); ok {
		return n
	}
	return 0
}
func slug(title string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(title)), " ", "-")
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
