package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
)

type boundDashboardRequest struct {
	Dashboard json.RawMessage   `json:"dashboard"`
	Name      string            `json:"name"`
	UID       string            `json:"uid"`
	Inputs    map[string]string `json:"inputs"`
	FolderUID string            `json:"folderUid"`
	FolderID  int64             `json:"folderId"`
}

type communityImportRequest struct {
	ID        int               `json:"id"`
	URL       string            `json:"url"`
	Revision  int               `json:"revision"`
	Name      string            `json:"name"`
	UID       string            `json:"uid"`
	Inputs    map[string]string `json:"inputs"`
	FolderUID string            `json:"folderUid"`
	FolderID  int64             `json:"folderId"`
}

func (s *Server) dashboardCatalogRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/dashboards/community/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _, err := grafana.ParseCommunityReference(r.PathValue("id"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		revision := 0
		if text := r.URL.Query().Get("revision"); text != "" {
			revision, err = strconv.Atoi(text)
			if err != nil || revision <= 0 {
				fail(w, 400, errors.New("revision must be a positive integer"))
				return
			}
		}
		result, err := s.Community.Load(r.Context(), id, revision)
		if err != nil {
			communityLoadError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	api.HandleFunc("POST /api/v1/import/grafana-preview", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Dashboard json.RawMessage `json:"dashboard"`
		}
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		title, inputs, err := grafana.PreviewInputs(request.Dashboard)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"dashboard": request.Dashboard, "title": title, "inputs": inputs})
	})
	api.HandleFunc("POST /api/v1/import/grafana-bound", func(w http.ResponseWriter, r *http.Request) {
		var request boundDashboardRequest
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		s.saveBoundDashboard(w, r, request, nil)
	})
	api.HandleFunc("POST /api/v1/import/grafana-community", func(w http.ResponseWriter, r *http.Request) {
		var request communityImportRequest
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		if (request.ID == 0) == (request.URL == "") || request.Revision < 0 {
			fail(w, 400, errors.New("provide exactly one community dashboard ID or URL, with an optional positive revision"))
			return
		}
		id := request.ID
		if request.URL != "" {
			var embedded int
			var err error
			id, embedded, err = grafana.ParseCommunityReference(request.URL)
			if err != nil {
				fail(w, 400, err)
				return
			}
			if embedded != 0 {
				if request.Revision != 0 && request.Revision != embedded {
					fail(w, 400, errors.New("requested revision conflicts with the download URL"))
					return
				}
				request.Revision = embedded
			}
		}
		if id <= 0 || id > 2147483647 {
			fail(w, 400, errors.New("dashboard ID must be a positive 32-bit integer"))
			return
		}
		catalog, err := s.Community.Load(r.Context(), id, request.Revision)
		if err != nil {
			communityLoadError(w, err)
			return
		}
		s.saveBoundDashboard(w, r, boundDashboardRequest{Dashboard: catalog.Dashboard, Name: request.Name, UID: request.UID, Inputs: request.Inputs, FolderUID: request.FolderUID, FolderID: request.FolderID}, map[string]any{"id": catalog.ID, "revision": catalog.Revision, "url": catalog.URL})
	})
}

func (s *Server) saveBoundDashboard(w http.ResponseWriter, r *http.Request, request boundDashboardRequest, source any) {
	_, inputs, err := grafana.PreviewInputs(request.Dashboard)
	if err != nil {
		fail(w, 400, err)
		return
	}
	for _, input := range inputs {
		value, provided := request.Inputs[input.Name]
		if !provided || input.Type != "datasource" {
			continue
		}
		datasource, err := s.dataSource(r.Context(), value)
		if err != nil {
			fail(w, 400, fmt.Errorf("input %q references an unknown datasource UID", input.Name))
			return
		}
		if datasource.Type != input.PluginID {
			fail(w, 400, fmt.Errorf("input %q requires datasource type %q", input.Name, input.PluginID))
			return
		}
	}
	s.dashboardImportMu.Lock()
	defer s.dashboardImportMu.Unlock()
	if request.UID == "" {
		request.UID = uuid.NewString()
	} else {
		_, found, err := s.grafanaDashboard(r.Context(), request.UID)
		if err != nil {
			fail(w, 500, err)
			return
		}
		if found {
			fail(w, 409, errors.New("dashboard UID already exists; choose a new UID"))
			return
		}
	}
	bound, err := grafana.BindDashboard(request.Dashboard, request.Inputs, request.Name, request.UID)
	if err != nil {
		fail(w, 400, err)
		return
	}
	folderID := request.FolderID
	if request.FolderUID != "" {
		folderID = 0
	}
	folder, err := s.resolveDashboardFolder(r.Context(), request.FolderUID, folderID)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	bound, err = normalizeImportedElements(bound)
	sourceDocument := append(json.RawMessage(nil), bound...)
	if err == nil {
		bound, err = s.resolveGrafanaLibraryPanels(r.Context(), bound, folder.UID)
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	result, err := grafana.Import(bound)
	if err != nil {
		fail(w, 400, err)
		return
	}
	result.Dashboard.Grafana = sourceDocument
	result.Dashboard.FolderUID = folder.UID
	result.Dashboard, err = s.saveDashboardWithLibraryPanels(r.Context(), result.Dashboard, nil)
	if err != nil {
		fail(w, 500, err)
		return
	}
	result.Warnings = s.filterPluginWarnings(r.Context(), result.Warnings)
	if source == nil {
		writeJSON(w, 200, result)
	} else {
		writeJSON(w, 200, map[string]any{"dashboard": result.Dashboard, "warnings": result.Warnings, "source": source})
	}
}

func communityLoadError(w http.ResponseWriter, err error) {
	var remote *grafana.CommunityHTTPError
	status := 502
	if errors.As(err, &remote) && remote.Status == 404 {
		status = 404
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = 504
	} else if errors.Is(err, context.Canceled) {
		status = 408
	} else if err.Error() == "community dashboard revision does not exist" || err.Error() == "invalid community dashboard ID or revision" {
		status = 400
	}
	fail(w, status, err)
}
