package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/grafana/grafana-plugin-sdk-go/backend/gtime"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type grafanaImportRequest struct {
	PluginID  string                `json:"pluginId"`
	Path      string                `json:"path"`
	Overwrite bool                  `json:"overwrite"`
	Dashboard json.RawMessage       `json:"dashboard"`
	Inputs    []grafana.ImportInput `json:"inputs"`
	FolderID  int64                 `json:"folderId"`
	FolderUID string                `json:"folderUid"`
}

type grafanaSaveRequest struct {
	Dashboard json.RawMessage
	Overwrite bool
	FolderID  int64
	FolderUID string
	Message   string
	PluginID  string
	Path      string
	Import    bool
}

type grafanaDashboardDescriptor struct {
	UID              string `json:"uid"`
	PluginID         string `json:"pluginId"`
	Title            string `json:"title"`
	Imported         bool   `json:"imported"`
	ImportedURI      string `json:"importedUri"`
	ImportedURL      string `json:"importedUrl"`
	Slug             string `json:"slug"`
	DashboardID      int64  `json:"dashboardId"`
	FolderID         int64  `json:"folderId"`
	FolderUID        string `json:"folderUid"`
	ImportedRevision int64  `json:"importedRevision,omitempty"`
	Revision         int64  `json:"revision,omitempty"`
	Description      string `json:"description"`
	Path             string `json:"path"`
	Removed          bool   `json:"removed"`
}

type grafanaAPIError struct {
	code     int
	status   string
	message  string
	pluginID string
}

func (e *grafanaAPIError) Error() string { return e.message }

func grafanaError(code int, status, message string) error {
	return &grafanaAPIError{code: code, status: status, message: message}
}

func writeGrafanaError(w http.ResponseWriter, err error) {
	var api *grafanaAPIError
	if !errors.As(err, &api) {
		api = &grafanaAPIError{code: 500, message: err.Error()}
		if errors.Is(err, sql.ErrNoRows) {
			api.code, api.message = 404, "Dashboard or folder not found"
		} else if errors.Is(err, context.Canceled) {
			api.code = 408
		} else if errors.Is(err, context.DeadlineExceeded) {
			api.code = 504
		} else if errors.Is(err, store.ErrDashboardConflict) {
			api.code, api.status = 412, "version-mismatch"
		}
	}
	result := map[string]any{"message": api.message}
	if api.status != "" {
		result["status"] = api.status
	}
	if api.pluginID != "" {
		result["pluginId"] = api.pluginID
	}
	writeJSON(w, api.code, result)
}

func grafanaDashboardURL(uid, title string) string {
	return "/d/" + url.PathEscape(uid) + "/" + slug(title)
}

func (s *Server) grafanaImportRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/gnet/dashboards/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _, err := grafana.ParseCommunityReference(r.PathValue("id"))
		if err != nil {
			writeGrafanaError(w, grafanaError(400, "", err.Error()))
			return
		}
		metadata, err := s.Community.Metadata(r.Context(), id)
		if err != nil {
			var remote *grafana.CommunityHTTPError
			if errors.As(err, &remote) {
				err = grafanaError(remote.Status, "", err.Error())
			}
			writeGrafanaError(w, err)
			return
		}
		writeJSON(w, 200, metadata)
	})
	for _, action := range []string{"import", "interpolate"} {
		api.HandleFunc("POST /api/dashboards/"+action, func(w http.ResponseWriter, r *http.Request) {
			var request grafanaImportRequest
			if err := decode(w, r, &request); err != nil {
				writeGrafanaError(w, grafanaError(400, "", "bad request data: "+err.Error()))
				return
			}
			if request.PluginID == "" && (len(request.Dashboard) == 0 || string(request.Dashboard) == "null") {
				writeGrafanaError(w, grafanaError(422, "", "pluginId or dashboard must be set"))
				return
			}
			document, err := s.grafanaImportTemplate(r.Context(), request)
			if err == nil {
				document, err = grafana.Interpolate(document, request.Inputs)
			}
			if err != nil {
				if action == "interpolate" {
					err = grafanaError(500, "", "failed to interpolate dashboard: "+err.Error())
				} else if errors.Is(err, grafana.ErrDashboardInputMissing) {
					err = grafanaError(400, "", err.Error())
				}
				writeGrafanaError(w, err)
				return
			}
			if action == "interpolate" {
				document, err = grafana.StripImportMetadata(document)
				if err != nil {
					writeGrafanaError(w, err)
					return
				}
				writeJSON(w, 200, document)
				return
			}
			result, err := s.saveGrafanaDashboard(r.Context(), grafanaSaveRequest{Dashboard: document, Overwrite: request.Overwrite, FolderID: request.FolderID, FolderUID: request.FolderUID, PluginID: request.PluginID, Path: request.Path, Import: true})
			if err != nil {
				writeGrafanaError(w, err)
				return
			}
			folder, err := s.resolveDashboardFolder(r.Context(), result.Dashboard.FolderUID, 0)
			if err != nil {
				writeGrafanaError(w, err)
				return
			}
			writeJSON(w, 200, importedDashboardDescriptor(result.Dashboard, folder))
		})
	}
	api.HandleFunc("GET /api/plugins/{id}/dashboards", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.grafanaPluginDashboards(r.Context(), r.PathValue("id"))
		if err != nil {
			writeGrafanaError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
}

func importedDashboardDescriptor(d model.Dashboard, folder model.Folder) grafanaDashboardDescriptor {
	doc := classicDocument(d)
	uid, _ := doc["uid"].(string)
	revision, _ := dashboardInteger(doc, "revision")
	return grafanaDashboardDescriptor{UID: uid, PluginID: d.PluginID, Title: d.Name, Imported: true, ImportedURI: "db/" + slug(d.Name), ImportedURL: grafanaDashboardURL(uid, d.Name), Slug: slug(d.Name), DashboardID: d.GrafanaID, FolderID: folder.ID, FolderUID: folder.UID, Revision: revision, ImportedRevision: revision, Path: d.PluginPath}
}

func (s *Server) grafanaImportTemplate(ctx context.Context, request grafanaImportRequest) (json.RawMessage, error) {
	if request.PluginID == "" {
		return request.Dashboard, nil
	}
	includes, err := s.grafanaPluginDashboardIncludes(ctx, request.PluginID)
	if err != nil {
		return nil, err
	}
	for _, include := range includes {
		if include.Path == request.Path {
			return s.grafanaPluginDashboardAsset(ctx, request.PluginID, request.Path)
		}
	}
	return nil, grafanaError(404, "", "Plugin dashboard path is not declared by the installed plugin")
}

type pluginDashboardInclude struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) grafanaPluginDashboardIncludes(ctx context.Context, pluginID string) ([]pluginDashboardInclude, error) {
	metadata, err := s.Plugins.Asset(ctx, pluginID, "plugin.json")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, grafanaError(404, "", "Plugin not found")
		}
		return nil, err
	}
	var plugin struct {
		Includes []pluginDashboardInclude `json:"includes"`
	}
	if len(metadata) > grafana.CommunityBodyLimit || json.Unmarshal(metadata, &plugin) != nil {
		return nil, errors.New("invalid installed plugin metadata")
	}
	result := []pluginDashboardInclude{}
	for _, include := range plugin.Includes {
		if include.Type == "dashboard" {
			result = append(result, include)
		}
	}
	return result, nil
}

func (s *Server) grafanaPluginDashboardAsset(ctx context.Context, pluginID, path string) (json.RawMessage, error) {
	raw, err := s.Plugins.Asset(ctx, pluginID, path)
	if err != nil {
		return nil, err
	}
	if len(raw) > grafana.CommunityBodyLimit || !json.Valid(raw) || dashboardJSONObjects(raw) == nil {
		return nil, errors.New("invalid installed plugin dashboard JSON")
	}
	return json.RawMessage(raw), nil
}

func (s *Server) grafanaPluginDashboards(ctx context.Context, pluginID string) ([]grafanaDashboardDescriptor, error) {
	includes, err := s.grafanaPluginDashboardIncludes(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	existing, err := s.Store.Dashboards(ctx)
	if err != nil {
		return nil, err
	}
	result := []grafanaDashboardDescriptor{}
	matched := map[string]bool{}
	for _, include := range includes {
		raw, err := s.grafanaPluginDashboardAsset(ctx, pluginID, include.Path)
		if err != nil {
			return nil, err
		}
		doc := dashboardInputObject(raw)
		title := inputTitle(doc)
		if title == "" {
			return nil, errors.New("plugin dashboard title required")
		}
		revision, _ := dashboardInteger(doc, "revision")
		if revision == 0 {
			revision = 1
		}
		uid, _ := doc["uid"].(string)
		descriptor := grafanaDashboardDescriptor{UID: uid, PluginID: pluginID, Title: title, Path: include.Path, Revision: revision}
		for _, d := range existing {
			if d.PluginID != pluginID || (d.PluginPath != include.Path && slug(d.Name) != slug(title)) {
				continue
			}
			descriptor.UID, _ = classicDocument(d)["uid"].(string)
			descriptor.Imported = true
			descriptor.DashboardID = d.GrafanaID
			descriptor.ImportedURI = "db/" + slug(d.Name)
			descriptor.ImportedURL = grafanaDashboardURL(descriptor.UID, d.Name)
			descriptor.ImportedRevision, _ = dashboardInteger(classicDocument(d), "revision")
			if descriptor.ImportedRevision == 0 {
				descriptor.ImportedRevision = 1
			}
			matched[d.ID] = true
			break
		}
		result = append(result, descriptor)
	}
	for _, d := range existing {
		if d.PluginID == pluginID && !matched[d.ID] {
			uid, _ := classicDocument(d)["uid"].(string)
			result = append(result, grafanaDashboardDescriptor{UID: uid, Slug: slug(d.Name), DashboardID: d.GrafanaID, Removed: true})
		}
	}
	return result, nil
}

var grafanaUID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func dashboardInputObject(data json.RawMessage) map[string]any {
	root := dashboardJSONObjects(data)
	if wrapped, ok := root["dashboard"].(map[string]any); ok {
		root = wrapped
	}
	if spec, ok := root["spec"].(map[string]any); ok {
		root = spec
	}
	return root
}

func inputTitle(doc map[string]any) string {
	title, _ := doc["title"].(string)
	return strings.TrimSpace(title)
}

func dashboardInteger(doc map[string]any, key string) (int64, error) {
	if doc[key] == nil {
		return 0, nil
	}
	var value int64
	var err error
	switch number := doc[key].(type) {
	case json.Number:
		value, err = number.Int64()
	case int64:
		value = number
	case int:
		value = int64(number)
	default:
		err = errors.New("not an integer")
	}
	if err != nil || value < 0 {
		return 0, grafanaError(400, "", "dashboard "+key+" must be a nonnegative integer")
	}
	return value, nil
}

func (s *Server) saveGrafanaDashboard(ctx context.Context, request grafanaSaveRequest) (grafana.ImportResult, error) {
	result := grafana.ImportResult{}
	doc := dashboardInputObject(request.Dashboard)
	if doc == nil || !json.Valid(request.Dashboard) {
		return result, grafanaError(400, "", "dashboard must be a JSON object")
	}
	title := inputTitle(doc)
	if title == "" {
		return result, grafanaError(400, "empty-name", "Dashboard title cannot be empty")
	}
	if utf8.RuneCountInString(title) > 5000 {
		return result, grafanaError(400, "title-too-long", "Dashboard title cannot contain more than 5 000 characters")
	}
	if utf8.RuneCountInString(request.Message) > 500 {
		return result, grafanaError(400, "", "message too long, max 500 characters")
	}
	ctx = store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{Message: request.Message})
	for _, raw := range arrayServer(doc["tags"]) {
		if tag, ok := raw.(string); ok && utf8.RuneCountInString(tag) > 50 {
			return result, grafanaError(400, "tag-too-long", "dashboard tag too long, max 50 characters")
		}
	}
	uid, _ := doc["uid"].(string)
	uid = strings.TrimSpace(uid)
	if uid == "" {
		resource := dashboardJSONObjects(request.Dashboard)
		if wrapped, ok := resource["dashboard"].(map[string]any); ok {
			resource = wrapped
		}
		metadata, _ := resource["metadata"].(map[string]any)
		uid, _ = metadata["name"].(string)
		uid = strings.TrimSpace(uid)
	}
	if uid != "" && !grafanaUID.MatchString(uid) {
		return result, grafanaError(400, "", "invalid dashboard uid; use up to 40 letters, numbers, '-' or '_'")
	}
	id, err := dashboardInteger(doc, "id")
	if err != nil {
		return result, err
	}
	incomingVersion, err := dashboardInteger(doc, "version")
	if err != nil {
		return result, err
	}
	if doc["elements"] != nil && doc["version"] == nil {
		resource := dashboardJSONObjects(request.Dashboard)
		if wrapped, ok := resource["dashboard"].(map[string]any); ok {
			resource = wrapped
		}
		if metadata, ok := resource["metadata"].(map[string]any); ok {
			incomingVersion, err = dashboardInteger(metadata, "generation")
			if err != nil {
				return result, err
			}
		}
	}
	s.dashboardImportMu.Lock()
	defer s.dashboardImportMu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	var existing model.Dashboard
	found := false
	if id > 0 {
		existing, err = s.Store.DashboardByGrafanaID(ctx, id)
		if err != nil {
			return result, err
		}
		found = true
		if uid == "" {
			uid, _ = classicDocument(existing)["uid"].(string)
		}
	}
	if uid != "" {
		byUID, exists, err := s.grafanaDashboard(ctx, uid)
		if err != nil {
			return result, err
		}
		if found && exists && existing.ID != byUID.ID {
			return result, grafanaError(400, "", "A dashboard with the same uid already exists")
		}
		if !found && exists {
			existing, found = byUID, true
		}
	} else if request.PluginID != "" {
		items, err := s.Store.Dashboards(ctx)
		if err != nil {
			return result, err
		}
		for _, d := range items {
			if d.PluginID == request.PluginID && d.PluginPath == request.Path {
				existing, found = d, true
				uid, _ = classicDocument(d)["uid"].(string)
				break
			}
		}
	}
	version := int64(1)
	var revision *int64
	if found {
		current, err := dashboardInteger(classicDocument(existing), "version")
		if err != nil {
			return result, err
		}
		if !request.Overwrite && incomingVersion != current {
			return result, grafanaError(412, "version-mismatch", "The dashboard has been changed by someone else")
		}
		if existing.PluginID != "" && !request.Overwrite {
			return result, &grafanaAPIError{code: 412, status: "plugin-dashboard", message: "Dashboard belongs to plugin", pluginID: existing.PluginID}
		}
		if current == math.MaxInt64 {
			return result, grafanaError(400, "", "Dashboard version limit reached")
		}
		version = current + 1
		revision = &existing.UpdatedAt
	}
	if uid == "" {
		uid = uuid.NewString()
	}
	folderID := request.FolderID
	if request.FolderUID != "" {
		folderID = 0 // Grafana's legacy import contract gives the UID precedence.
	}
	folder, err := s.resolveDashboardFolder(ctx, request.FolderUID, folderID)
	if err != nil {
		return result, err
	}
	source, err := normalizeImportedElements(request.Dashboard)
	if err != nil {
		return result, grafanaError(400, "", err.Error())
	}
	resolved, err := s.resolveGrafanaLibraryPanels(ctx, source, folder.UID)
	if err != nil {
		return result, grafanaError(400, "", err.Error())
	}
	result, err = grafana.ImportForSave(resolved)
	if err != nil {
		return result, grafanaError(400, "", err.Error())
	}
	if found {
		result.Dashboard.ID, result.Dashboard.GrafanaID = existing.ID, existing.GrafanaID
	}
	result.Dashboard.Name = title
	result.Dashboard.FolderUID, result.Dashboard.PluginID, result.Dashboard.PluginPath = folder.UID, request.PluginID, request.Path
	// Runtime panels use the resolved library models, while the saved source keeps
	// its original resource envelope, references and unknown fields.
	savedRoot := dashboardJSONObjects(source)
	savedResource := savedRoot
	if wrapped, ok := savedRoot["dashboard"].(map[string]any); ok {
		savedResource = wrapped
	}
	savedDoc := savedResource
	if spec, ok := savedResource["spec"].(map[string]any); ok {
		savedDoc = spec
	}
	savedDoc["title"] = title
	if savedDoc["elements"] != nil {
		if savedResource["spec"] == nil {
			savedResource = map[string]any{"apiVersion": "dashboard.grafana.app/v2beta1", "kind": "Dashboard", "spec": savedDoc}
			if savedRoot["dashboard"] != nil {
				savedRoot["dashboard"] = savedResource
			} else {
				savedRoot = savedResource
			}
		}
		metadata, _ := savedResource["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["name"], metadata["generation"] = uid, version
		savedResource["metadata"] = metadata
		if _, exists := savedDoc["version"]; exists {
			savedDoc["version"] = version
		}
		if _, exists := savedDoc["uid"]; exists {
			savedDoc["uid"] = uid
		}
		if _, exists := savedDoc["id"]; exists {
			savedDoc["id"] = result.Dashboard.GrafanaID
		}
	} else {
		savedDoc["uid"], savedDoc["version"], savedDoc["id"] = uid, version, result.Dashboard.GrafanaID
		if savedResource != nil && savedResource["spec"] != nil {
			metadata, _ := savedResource["metadata"].(map[string]any)
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata["name"] = uid
			savedResource["metadata"] = metadata
		}
	}
	if request.Import {
		delete(savedDoc, "__inputs")
		delete(savedDoc, "__requires")
		if refresh, _ := savedDoc["refresh"].(string); refresh != "" {
			interval, parseErr := gtime.ParseInterval(refresh)
			if parseErr != nil || interval < 5*time.Second {
				savedDoc["refresh"] = "5s"
			}
		}
	}
	result.Dashboard.Grafana, err = json.Marshal(savedRoot)
	if err != nil {
		return result, err
	}
	if err := result.Dashboard.Validate(); err != nil {
		return result, grafanaError(400, "", err.Error())
	}
	result.Dashboard, err = s.saveDashboardWithLibraryPanels(ctx, result.Dashboard, revision)
	if errors.Is(err, store.ErrDashboardConflict) {
		return result, grafanaError(412, "version-mismatch", "The dashboard has been changed by someone else")
	}
	if err == nil {
		result.Warnings = s.filterPluginWarnings(ctx, result.Warnings)
	}
	return result, err
}

func arrayServer(value any) []any {
	array, _ := value.([]any)
	return array
}

func normalizeImportedElements(raw json.RawMessage) (json.RawMessage, error) {
	root := dashboardJSONObjects(raw)
	doc := dashboardInputObject(raw)
	definitions, oldFormat := doc["__elements"].([]any)
	if !oldFormat {
		return raw, nil
	}
	// Keep this edit in the root tree that is serialized, including resource specs.
	doc = root
	if wrapped, ok := doc["dashboard"].(map[string]any); ok {
		doc = wrapped
	}
	if spec, ok := doc["spec"].(map[string]any); ok {
		doc = spec
	}
	elements := map[string]any{}
	for _, raw := range definitions {
		element, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("library element definitions must be JSON objects")
		}
		uid, _ := element["uid"].(string)
		if uid == "" {
			return nil, errors.New("library element definition needs a UID")
		}
		elements[uid] = element
	}
	doc["__elements"] = elements
	return json.Marshal(root)
}
