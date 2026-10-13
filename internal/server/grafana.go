package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// Grafana-compatible dashboard endpoints allow existing dashboard-as-code tools
// to manage templates. All routes share the native API's authentication policy.
func (s *Server) grafanaRoutes(api routeRegistrar, prometheus http.Handler) {
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
		result, err := s.saveGrafanaDashboard(r.Context(), grafanaSaveRequest{Dashboard: request.Dashboard, Overwrite: request.Overwrite, FolderID: request.FolderID, FolderUID: request.FolderUID, Message: request.Message})
		if err != nil {
			writeGrafanaError(w, err)
			return
		}
		document := classicDocument(result.Dashboard)
		uid, _ := document["uid"].(string)
		writeJSON(w, 200, map[string]any{"id": result.Dashboard.GrafanaID, "uid": uid, "slug": slug(result.Dashboard.Name), "url": grafanaDashboardURL(uid, result.Dashboard.Name), "status": "success", "version": document["version"], "warnings": result.Warnings})
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
		var allowed bool
		d, allowed, err = s.readableDashboard(r.Context(), d, true)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		if !allowed {
			writeResourceReadError(w, identity.ErrForbidden)
			return
		}
		d, err = s.resolveDashboardLibrary(r.Context(), d)
		if err != nil {
			if s.Authorizer != nil {
				writeResourceReadError(w, err)
			} else {
				writeGrafanaError(w, err)
			}
			return
		}
		folder, err := s.resolveDashboardFolder(r.Context(), d.FolderUID, 0)
		if err != nil {
			writeGrafanaError(w, err)
			return
		}
		folder, allowed, err = s.readableFolder(r.Context(), folder)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		if !allowed {
			folder = model.Folder{UID: d.FolderUID}
		}
		canEdit, canAdmin := true, true
		if state, err := s.resourceReadAccess(r.Context()); err != nil {
			writeResourceReadError(w, err)
			return
		} else if state != nil {
			permission, err := s.Store.EffectiveResourcePermission(r.Context(), model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionDashboard, UID: d.ID}, state.principal, state.resolver)
			if err != nil {
				writeResourceReadError(w, err)
				return
			}
			canEdit, canAdmin = permission.Permission.Includes(model.PermissionEdit), permission.Permission.Includes(model.PermissionAdmin)
		}
		classic, err := json.Marshal(classicDocument(d))
		if err == nil {
			classic, err = s.resolveGrafanaLibraryPanels(r.Context(), classic, d.FolderUID)
		}
		if err != nil {
			if s.Authorizer != nil {
				writeResourceReadError(w, err)
			} else {
				writeGrafanaError(w, err)
			}
			return
		}
		writeJSON(w, 200, map[string]any{"dashboard": json.RawMessage(classic), "meta": map[string]any{"isStarred": false, "canSave": canEdit, "canEdit": canEdit, "canAdmin": canAdmin, "folderId": folder.ID, "folderUid": folder.UID, "folderTitle": folder.Title, "pluginId": d.PluginID, "url": grafanaDashboardURL(r.PathValue("uid"), d.Name)}})
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
		result, err := s.grafanaSearch(r)
		if err != nil {
			if s.Authorizer != nil {
				writeResourceReadError(w, err)
			} else {
				writeGrafanaError(w, err)
			}
			return
		}
		writeJSON(w, 200, result)
	})
	s.grafanaImportRoutes(api)
	s.datasourceRoutes(api, prometheus)
}

func (s *Server) grafanaDashboard(ctx context.Context, uid string) (model.Dashboard, bool, error) {
	dashboards, err := s.Store.Dashboards(ctx)
	if err != nil {
		return model.Dashboard{}, false, err
	}
	for _, d := range dashboards {
		doc := classicDocument(d)
		if doc["uid"] == uid {
			return d, true, nil
		}
	}
	for _, d := range dashboards {
		if d.ID == uid {
			return d, true, nil
		}
	}
	return model.Dashboard{}, false, nil
}

func (s *Server) grafanaSearch(r *http.Request) ([]map[string]any, error) {
	params := r.URL.Query()
	limit, page := 1000, 1
	if n, err := strconv.Atoi(params.Get("limit")); err == nil && n > 0 {
		if n > 5000 {
			return nil, grafanaError(422, "", "Limit is above maximum allowed (5000), use page parameter to access hits beyond limit")
		}
		limit = n
	}
	if n, err := strconv.Atoi(params.Get("page")); err == nil && n > 0 && n <= 1000000 {
		page = n
	}
	wantedUIDs := params["dashboardUIDs"]
	if len(wantedUIDs) == 0 {
		wantedUIDs = params["dashboardUID"]
	}
	parseIDs := func(values []string) []int64 {
		var ids []int64
		for _, value := range values {
			if id, err := strconv.ParseInt(value, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	}
	wantedIDs, folderIDs := parseIDs(params["dashboardIds"]), parseIDs(params["folderIds"])
	folderUIDs := params["folderUIDs"]
	if len(wantedUIDs) > 0 && len(wantedIDs) > 0 || len(folderUIDs) > 0 && len(folderIDs) > 0 {
		return nil, grafanaError(400, "", "search supports UIDs or IDs, not both")
	}
	for i, uid := range folderUIDs {
		folderUIDs[i] = model.RootFolderUID(uid)
	}
	result := []map[string]any{}
	if params.Get("starred") == "true" || params.Get("deleted") == "true" {
		return result, nil
	}
	folders, err := s.Store.Folders(r.Context())
	if err != nil {
		return nil, err
	}
	byUID := map[string]model.Folder{"": {Title: "General", URL: "/dashboards"}}
	for _, folder := range folders {
		byUID[folder.UID] = folder
	}
	visibleFolders, err := s.filterReadableFolders(r.Context(), folders)
	if err != nil {
		return nil, err
	}
	visibleByUID := map[string]model.Folder{"": byUID[""]}
	for _, folder := range visibleFolders {
		visibleByUID[folder.UID] = folder
	}
	visibleParent := func(uid string) model.Folder {
		if folder, ok := visibleByUID[uid]; ok {
			return folder
		}
		return model.Folder{UID: uid}
	}
	query, kind := strings.ToLower(params.Get("query")), params.Get("type")
	matches := func(uid, title, parent string, id int64) bool {
		if !strings.Contains(strings.ToLower(title), query) || len(wantedUIDs) > 0 && !contains(wantedUIDs, uid) || len(wantedIDs) > 0 && !containsID(wantedIDs, id) {
			return false
		}
		folder := byUID[parent]
		return (len(folderUIDs) == 0 || contains(folderUIDs, parent)) && (len(folderIDs) == 0 || containsID(folderIDs, folder.ID))
	}
	hit := func(uid, title, typ, location string, id int64, parent model.Folder, tags any) map[string]any {
		if tags == nil {
			tags = []string{}
		}
		return map[string]any{"id": id, "uid": uid, "orgId": s.OrganizationID(), "title": title, "type": typ, "url": location, "uri": "db/" + slug(title), "slug": slug(title), "tags": tags, "isStarred": false, "isDeleted": false, "sortMeta": 0, "folderId": parent.ID, "folderUid": parent.UID, "folderTitle": parent.Title, "folderUrl": parent.URL}
	}
	if (kind == "" || kind == "dash-folder") && len(params["tag"]) == 0 {
		for _, folder := range visibleFolders {
			if matches(folder.UID, folder.Title, folder.ParentUID, folder.ID) {
				result = append(result, hit(folder.UID, folder.Title, "dash-folder", folder.URL, folder.ID, visibleParent(folder.ParentUID), nil))
			}
		}
	}
	if kind == "" || kind == "dash-db" {
		dashboards, err := s.Store.Dashboards(r.Context())
		if err != nil {
			return nil, err
		}
		for _, candidate := range dashboards {
			d, allowed, err := s.readableDashboard(r.Context(), candidate, false)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			doc := classicDocument(d)
			uid, _ := doc["uid"].(string)
			parent := model.RootFolderUID(d.FolderUID)
			if !matches(uid, d.Name, parent, d.GrafanaID) {
				continue
			}
			tags := []string{}
			if raw, ok := doc["tags"].([]any); ok {
				for _, value := range raw {
					if tag, ok := value.(string); ok {
						tags = append(tags, tag)
					}
				}
			}
			allTags := true
			for _, tag := range params["tag"] {
				allTags = allTags && contains(tags, tag)
			}
			if allTags {
				sort.Strings(tags)
				result = append(result, hit(uid, d.Name, "dash-db", grafanaDashboardURL(uid, d.Name), d.GrafanaID, visibleParent(parent), tags))
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if params.Get("sort") == "" && result[i]["type"] != result[j]["type"] {
			return result[i]["type"] == "dash-folder"
		}
		iTitle, jTitle := strings.ToLower(result[i]["title"].(string)), strings.ToLower(result[j]["title"].(string))
		if params.Get("sort") == "alpha-desc" {
			return iTitle > jTitle
		}
		return iTitle < jTitle
	})
	start := (page - 1) * limit
	if start >= len(result) {
		return []map[string]any{}, nil
	}
	end := min(start+limit, len(result))
	return result[start:end], nil
}

func containsID(values []int64, value int64) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func nativeClassicDocument(d model.Dashboard) map[string]any {
	panels := []any{}
	for i, panel := range d.Panels {
		config := dashboardJSONObjects(panel.Config)
		if config == nil || config["type"] == nil || config["targets"] == nil {
			positionConfig := config
			visual := panel.Visualization
			if visual == "" {
				visual = "timeseries"
			}
			expressions := panel.Expressions
			if len(expressions) == 0 && panel.Expr != "" {
				expressions = []string{panel.Expr}
			}
			if len(expressions) == 0 && panel.Metric != "" {
				expression := panel.Metric
				keys := make([]string, 0, len(panel.Labels))
				for key := range panel.Labels {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				matchers := make([]string, 0, len(keys))
				for _, key := range keys {
					matchers = append(matchers, key+"="+strconv.Quote(panel.Labels[key]))
				}
				if len(matchers) > 0 {
					expression += "{" + strings.Join(matchers, ",") + "}"
				}
				switch panel.Aggregation {
				case "rate":
					expression = "rate(" + expression + "[$__rate_interval])"
				case "avg", "sum", "min", "max":
					expression = panel.Aggregation + "(" + expression + ")"
				}
				expressions = []string{expression}
			}
			targets := []any{}
			for j, expression := range expressions {
				refID := string(rune('A' + j))
				if j >= 26 {
					refID = "Q" + strconv.Itoa(j+1)
				}
				targets = append(targets, map[string]any{"refId": refID, "expr": expression, "range": true, "datasource": map[string]any{"uid": "metricspanel", "type": "prometheus"}})
			}
			unit := panel.Unit
			if unit == "seconds" {
				unit = "s"
			} else if unit == "count" {
				unit = "short"
			}
			config = map[string]any{"id": i + 1, "title": panel.Title, "type": visual, "datasource": map[string]any{"uid": "metricspanel", "type": "prometheus"}, "targets": targets, "gridPos": map[string]int{"h": 8, "w": 12, "x": i % 2 * 12, "y": i / 2 * 8}, "fieldConfig": map[string]any{"defaults": map[string]any{"unit": unit}, "overrides": []any{}}, "options": map[string]any{}}
			for key, value := range positionConfig {
				config[key] = value
			}
		}
		if config["id"] == nil {
			config["id"] = i + 1
		}
		if config["title"] == nil {
			config["title"] = panel.Title
		}
		if config["type"] == nil {
			config["type"] = panel.Visualization
		}
		panels = append(panels, config)
	}
	variables := []any{}
	for _, variable := range d.Variables {
		config := dashboardJSONObjects(variable.Config)
		if config == nil {
			options := []any{}
			for _, value := range variable.Options {
				options = append(options, map[string]any{"text": value, "value": value, "selected": value == variable.Current})
			}
			config = map[string]any{"name": variable.Name, "type": variable.Type, "query": variable.Query, "current": map[string]any{"text": variable.Current, "value": variable.Current}, "options": options, "multi": variable.Multi, "includeAll": variable.IncludeAll}
		}
		variables = append(variables, config)
	}
	annotations := []any{}
	if len(d.Annotations) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(d.Annotations))
		decoder.UseNumber()
		_ = decoder.Decode(&annotations)
	}
	return map[string]any{"title": d.Name, "panels": panels, "templating": map[string]any{"list": variables}, "annotations": map[string]any{"list": annotations}, "schemaVersion": 41, "tags": []string{}, "time": map[string]string{"from": "now-30m", "to": "now"}}
}

func classicDocument(d model.Dashboard) map[string]any {
	doc := dashboardJSONObjects(d.Grafana)
	if wrapped, ok := doc["dashboard"].(map[string]any); ok {
		doc = wrapped
	}
	resourceUID := ""
	var resourceVersion any
	if metadata, ok := doc["metadata"].(map[string]any); ok {
		resourceUID, _ = metadata["name"].(string)
		resourceVersion = metadata["generation"]
	}
	if spec, ok := doc["spec"].(map[string]any); ok {
		doc = spec
	}
	if doc == nil {
		doc = nativeClassicDocument(d)
	}
	if doc["elements"] != nil { // Public resource -> classic API response.
		version := doc["version"]
		if version == nil {
			version = resourceVersion
		}
		panels := []any{}
		for _, p := range d.Panels {
			panels = append(panels, dashboardJSONObjects(p.Config))
		}
		variables := []any{}
		for _, v := range d.Variables {
			variables = append(variables, dashboardJSONObjects(v.Config))
		}
		doc = map[string]any{"title": d.Name, "panels": panels, "templating": map[string]any{"list": variables}, "schemaVersion": 41, "version": version}
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
	if d.GrafanaID > 0 {
		doc["id"] = d.GrafanaID
	}
	if d.GrafanaVersion > 0 {
		doc["version"] = d.GrafanaVersion
	}
	return doc
}

func dashboardJSONObjects(data json.RawMessage) map[string]any {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&object)
	return object
}

func dashboardInputVersion(data json.RawMessage) any {
	object := dashboardJSONObjects(data)
	if wrapped, ok := object["dashboard"].(map[string]any); ok {
		object = wrapped
	}
	if spec, ok := object["spec"].(map[string]any); ok {
		object = spec
	}
	return object["version"]
}

func numeric(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		value, err := n.Float64()
		if err == nil {
			return value
		}
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
