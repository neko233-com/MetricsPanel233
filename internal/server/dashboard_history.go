package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type dashboardVersionView struct {
	model.DashboardVersion
	VersionKey       string `json:"versionKey"`
	ParentVersionKey string `json:"parentVersionKey"`
	RestoredFromKey  string `json:"restoredFromKey"`
}

func versionView(version model.DashboardVersion) dashboardVersionView {
	return dashboardVersionView{version, strconv.FormatInt(version.Version, 10), strconv.FormatInt(version.ParentVersion, 10), strconv.FormatInt(version.RestoredFrom, 10)}
}

// The CLI transports a JSON string in an ASCII header so multiline and Unicode
// save messages do not alter the dashboard document or fail HTTP header checks.
func dashboardSaveContext(r *http.Request) (context.Context, error) {
	message := ""
	if raw := r.Header.Get("X-Dashboard-Message"); raw != "" {
		if json.Unmarshal([]byte(raw), &message) != nil || utf8.RuneCountInString(message) > 500 {
			return r.Context(), errors.New("X-Dashboard-Message must be a JSON string of at most 500 characters")
		}
	}
	return store.WithDashboardHistoryMetadata(r.Context(), model.DashboardHistoryMetadata{Message: message}), nil
}

func positiveHistoryVersion(raw json.RawMessage) (int64, error) {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
	}
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, errors.New("version must be a positive integer")
	}
	version, err := strconv.ParseInt(text, 10, 64)
	if err != nil || version <= 0 {
		return 0, errors.New("version must be a positive 64-bit integer")
	}
	return version, nil
}

func historyQuery(r *http.Request) (int, int, string, error) {
	query, err := r.URL.Query(), error(nil)
	limit, start := 1000, 0
	for _, key := range []string{"limit", "start", "continueToken"} {
		if len(query[key]) > 1 {
			return 0, 0, "", errors.New("history parameters must occur once")
		}
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit > 1000 {
			return 0, 0, "", errors.New("history limit must be an integer at most 1000")
		}
	}
	if raw := query.Get("start"); raw != "" {
		start, err = strconv.Atoi(raw)
		if err != nil || start < 0 {
			return 0, 0, "", errors.New("history start must be a nonnegative offset")
		}
	}
	return limit, start, query.Get("continueToken"), nil
}

func (s *Server) historyDashboard(r *http.Request, native bool) (model.Dashboard, error) {
	if native {
		return s.Store.Dashboard(r.Context(), r.PathValue("id"))
	}
	dashboard, found, err := s.grafanaDashboard(r.Context(), r.PathValue("uid"))
	if err != nil {
		return dashboard, err
	}
	if !found {
		return dashboard, grafanaError(404, "", "Dashboard not found")
	}
	return dashboard, nil
}

func (s *Server) dashboardHistoryRoutes(api *http.ServeMux) {
	for _, native := range []bool{false, true} {
		listPath, detailPath, restorePath := "/api/dashboards/uid/{uid}/versions", "/api/dashboards/uid/{uid}/versions/{version}", "/api/dashboards/uid/{uid}/restore"
		if native {
			listPath, detailPath, restorePath = "/api/v1/dashboard-versions/{id}", "/api/v1/dashboard-versions/{id}/{version}", "/api/v1/dashboard-restore/{id}"
		}
		api.HandleFunc("GET "+listPath, func(w http.ResponseWriter, r *http.Request) {
			dashboard, err := s.historyDashboard(r, native)
			if err != nil {
				writeGrafanaError(w, err)
				return
			}
			limit, start, token, err := historyQuery(r)
			if err != nil {
				fail(w, 400, err)
				return
			}
			result, err := s.Store.DashboardVersions(r.Context(), dashboard.ID, limit, start, token)
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			if !native {
				writeJSON(w, 200, result)
				return
			}
			versions := make([]dashboardVersionView, 0, len(result.Versions))
			for _, version := range result.Versions {
				versions = append(versions, versionView(version))
			}
			writeJSON(w, 200, map[string]any{"versions": versions, "continueToken": result.ContinueToken, "currentVersionKey": strconv.FormatInt(dashboard.GrafanaVersion, 10)})
		})
		api.HandleFunc("GET "+detailPath, func(w http.ResponseWriter, r *http.Request) {
			dashboard, err := s.historyDashboard(r, native)
			if err != nil {
				writeGrafanaError(w, err)
				return
			}
			version, err := positiveHistoryVersion(rawValue(r.PathValue("version")))
			if err != nil {
				fail(w, 400, err)
				return
			}
			entry, err := s.Store.DashboardVersion(r.Context(), dashboard.ID, version)
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			if native {
				writeJSON(w, 200, map[string]any{"version": versionView(entry), "dashboard": entry.Snapshot})
				return
			}
			writeJSON(w, 200, struct {
				model.DashboardVersion
				Data any `json:"data"`
			}{entry, classicDocument(entry.Snapshot)})
		})
		api.HandleFunc("POST "+restorePath, func(w http.ResponseWriter, r *http.Request) {
			var version int64
			var revision *int64
			message := ""
			if native {
				var cmd struct {
					Version  json.RawMessage `json:"version"`
					Revision *int64          `json:"revision"`
					Message  string          `json:"message"`
				}
				if err := decode(w, r, &cmd); err != nil {
					fail(w, 400, err)
					return
				}
				var err error
				version, err = positiveHistoryVersion(cmd.Version)
				if err != nil {
					fail(w, 400, err)
					return
				}
				revision, message = cmd.Revision, cmd.Message
			} else {
				var cmd struct {
					Version int64 `json:"version"`
				}
				if err := decode(w, r, &cmd); err != nil {
					writeGrafanaError(w, grafanaError(400, "", err.Error()))
					return
				}
				version = cmd.Version
			}
			if version <= 0 || utf8.RuneCountInString(message) > 500 || revision != nil && *revision <= 0 {
				fail(w, 400, errors.New("restore requires a positive version/revision and a message of at most 500 characters"))
				return
			}
			current, err := s.historyDashboard(r, native)
			if err != nil {
				writeGrafanaError(w, err)
				return
			}
			if revision != nil && *revision != current.UpdatedAt {
				dashboardResourceError(w, store.ErrDashboardConflict)
				return
			}
			if revision == nil {
				revision = &current.UpdatedAt
			}
			restored, err := s.restoreDashboardVersion(r.Context(), current, version, revision, message)
			if err != nil {
				if native {
					dashboardResourceError(w, err)
				} else {
					writeGrafanaError(w, err)
				}
				return
			}
			if native {
				writeJSON(w, 200, restored)
				return
			}
			writeJSON(w, 200, map[string]any{"status": "success", "slug": slug(restored.Name), "version": restored.GrafanaVersion, "id": restored.GrafanaID, "uid": restored.UID(), "url": grafanaDashboardURL(restored.UID(), restored.Name), "folderUid": restored.FolderUID})
		})
	}
	api.HandleFunc("GET /api/v1/dashboard-version-exports/{id}/{version}", func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "native"
		}
		if len(r.URL.Query()["format"]) > 1 || format != "native" && format != "grafana" {
			fail(w, 400, errors.New("format must be native or grafana"))
			return
		}
		version, err := positiveHistoryVersion(rawValue(r.PathValue("version")))
		if err != nil {
			fail(w, 400, err)
			return
		}
		entry, err := s.Store.DashboardVersion(r.Context(), r.PathValue("id"), version)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		if format == "native" {
			writeJSON(w, 200, entry.Snapshot)
			return
		}
		if len(entry.Snapshot.Grafana) == 0 {
			writeJSON(w, 200, classicDocument(entry.Snapshot))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(entry.Snapshot.Grafana)
	})
	api.HandleFunc("POST /api/v1/dashboard-compare/{id}", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			BaseVersion json.RawMessage `json:"baseVersion"`
			NewVersion  json.RawMessage `json:"newVersion"`
		}
		if err := decode(w, r, &cmd); err != nil {
			fail(w, 400, err)
			return
		}
		baseVersion, err := positiveHistoryVersion(cmd.BaseVersion)
		if err != nil {
			fail(w, 400, err)
			return
		}
		newVersion, err := positiveHistoryVersion(cmd.NewVersion)
		if err != nil {
			fail(w, 400, err)
			return
		}
		base, err := s.Store.DashboardVersion(r.Context(), r.PathValue("id"), baseVersion)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		next, err := s.Store.DashboardVersion(r.Context(), r.PathValue("id"), newVersion)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		changes, err := compareDashboardVersions(base.Snapshot, next.Snapshot)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"baseVersionKey": strconv.FormatInt(baseVersion, 10), "newVersionKey": strconv.FormatInt(newVersion, 10), "changes": changes})
	})
}

func currentHistoryMetadata(current model.Dashboard, version int64) rawObject {
	metadata := rawObject{}
	var root rawObject
	if json.Unmarshal(current.Grafana, &root) == nil {
		for root != nil {
			wrapped, ok := root["dashboard"]
			if !ok {
				break
			}
			var inner rawObject
			if json.Unmarshal(wrapped, &inner) != nil || inner == nil {
				break
			}
			root = inner
		}
		if json.Unmarshal(root["metadata"], &metadata) != nil || metadata == nil {
			metadata = rawObject{}
		}
	}
	metadata["name"], metadata["generation"] = rawValue(current.UID()), rawValue(version)
	var annotations rawObject
	if json.Unmarshal(metadata["annotations"], &annotations) != nil || annotations == nil {
		annotations = rawObject{}
	}
	annotations["grafana.app/folder"] = rawValue(current.FolderUID)
	if current.PluginID != "" {
		annotations["grafana.app/managedBy"] = rawValue("plugin")
		annotations["grafana.app/managerId"] = rawValue(current.PluginID)
	}
	metadata["annotations"] = rawValue(annotations)
	return metadata
}

func restoreHistoryIdentity(raw json.RawMessage, current model.Dashboard, version int64) (json.RawMessage, error) {
	var root rawObject
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return nil, errors.New("historical dashboard source must be an object")
	}
	if wrapped, ok := root["dashboard"]; ok {
		next, err := restoreHistoryIdentity(wrapped, current, version)
		if err != nil {
			return nil, err
		}
		root["dashboard"] = next
		return json.Marshal(root)
	}
	var spec rawObject
	if source, ok := root["spec"]; ok {
		if err := json.Unmarshal(source, &spec); err != nil || spec == nil {
			return nil, errors.New("historical resource spec must be an object")
		}
	} else {
		spec = root
	}
	if _, v2 := spec["elements"]; !v2 {
		spec["id"], spec["uid"], spec["version"] = rawValue(current.GrafanaID), rawValue(current.UID()), rawValue(version)
	}
	if _, resource := root["spec"]; resource {
		// Restore the old spec in its original schema while retaining the current
		// resource's identity, ownership, and unknown metadata without float64.
		root["metadata"] = rawValue(currentHistoryMetadata(current, version))
		root["spec"] = rawValue(spec)
	}
	return json.Marshal(root)
}

func (s *Server) restoreDashboardVersion(ctx context.Context, current model.Dashboard, version int64, revision *int64, message string) (model.Dashboard, error) {
	entry, err := s.Store.DashboardVersion(ctx, current.ID, version)
	if err != nil {
		return current, err
	}
	changes, err := compareDashboardVersions(entry.Snapshot, current)
	if err != nil {
		return current, err
	}
	if len(changes) == 0 {
		return current, grafanaError(400, "", "dashboard restore would be identical to the current version")
	}
	if current.GrafanaVersion == math.MaxInt64 {
		return current, grafanaError(400, "", "dashboard version limit reached")
	}
	restored := entry.Snapshot
	restored.ID, restored.GrafanaID = current.ID, current.GrafanaID
	restored.FolderUID, restored.PluginID, restored.PluginPath = current.FolderUID, current.PluginID, current.PluginPath
	if len(restored.Grafana) > 0 {
		restored.Grafana, err = restoreHistoryIdentity(restored.Grafana, current, current.GrafanaVersion+1)
		if err != nil {
			return current, err
		}
	}
	ctx = store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{Message: message, RestoredFrom: version})
	restored, err = s.resolveDashboardLibrary(ctx, restored)
	if err != nil {
		return current, fmt.Errorf("restore library panel: %w", err)
	}
	return s.saveDashboardWithLibraryPanels(ctx, restored, revision)
}
