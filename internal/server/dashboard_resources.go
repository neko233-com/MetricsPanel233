package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func dashboardResourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, 404, err)
	case errors.Is(err, store.ErrResourceConflict):
		writeJSON(w, 412, map[string]string{"message": err.Error(), "status": "version-mismatch"})
	case errors.Is(err, store.ErrResourceExists), errors.Is(err, store.ErrLibraryConnected), errors.Is(err, store.ErrFolderAlertRules), errors.Is(err, store.ErrDashboardConflict), errors.Is(err, alerting.ErrBusy):
		fail(w, 409, err)
	default:
		fail(w, 400, err)
	}
}
func (s *Server) resolveDashboardFolder(ctx context.Context, uid string, id int64) (model.Folder, error) {
	if uid != "" {
		f, err := s.Store.Folder(ctx, uid)
		if err != nil {
			return f, err
		}
		if id != 0 && id != f.ID {
			return f, errors.New("folderId and folderUid refer to different folders")
		}
		return f, nil
	}
	return s.Store.FolderByID(ctx, id)
}
func (s *Server) dashboardResourceRoutes(api routeRegistrar) {
	api.HandleFunc("GET /api/v1/folders", func(w http.ResponseWriter, r *http.Request) {
		folders, err := s.Store.Folders(r.Context())
		if err != nil {
			resourceError(w, err)
			return
		}
		folders, err = s.filterReadableFolders(r.Context(), folders)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		writeJSON(w, 200, folders)
	})
	api.HandleFunc("GET /api/folders", func(w http.ResponseWriter, r *http.Request) {
		folders, err := s.Store.Folders(r.Context())
		if err != nil {
			resourceError(w, err)
			return
		}
		folders, err = s.filterReadableFolders(r.Context(), folders)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		parent := model.RootFolderUID(r.URL.Query().Get("parentUid"))
		if _, err = s.Store.Folder(r.Context(), parent); err != nil {
			dashboardResourceError(w, err)
			return
		}
		out := []model.Folder{}
		for _, f := range folders {
			if f.ParentUID == parent {
				out = append(out, f)
			}
		}
		page, limit := 1, 1000
		if raw := r.URL.Query().Get("page"); raw != "" {
			page, err = strconv.Atoi(raw)
			if err != nil || page < 1 || page > 1000000 {
				fail(w, 400, errors.New("page must be 1–1000000"))
				return
			}
		}
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 1000 {
				fail(w, 400, errors.New("limit must be 1–1000"))
				return
			}
		}
		start := (page - 1) * limit
		if start >= len(out) {
			out = []model.Folder{}
		} else {
			end := start + limit
			if end > len(out) {
				end = len(out)
			}
			out = out[start:end]
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("POST /api/folders", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			UID         string `json:"uid"`
			Title       string `json:"title"`
			Description string `json:"description"`
			ParentUID   string `json:"parentUid"`
		}
		if err := decode(w, r, &cmd); err != nil {
			fail(w, 400, err)
			return
		}
		f, err := s.Store.CreateFolder(r.Context(), model.Folder{UID: cmd.UID, Title: cmd.Title, Description: cmd.Description, ParentUID: cmd.ParentUID})
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, f)
	})
	api.HandleFunc("GET /api/folders/{uid}", func(w http.ResponseWriter, r *http.Request) {
		f, err := s.Store.Folder(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		f, allowed, err := s.readableFolder(r.Context(), f)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		if !allowed {
			writeResourceReadError(w, identity.ErrForbidden)
			return
		}
		writeJSON(w, 200, f)
	})
	api.HandleFunc("PUT /api/folders/{uid}", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			Title       *string `json:"title"`
			Description *string `json:"description"`
			Version     int64   `json:"version"`
			Overwrite   bool    `json:"overwrite"`
		}
		if err := decode(w, r, &cmd); err != nil {
			fail(w, 400, err)
			return
		}
		f, err := s.Store.Folder(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		if cmd.Title != nil {
			f.Title = *cmd.Title
		}
		if cmd.Description != nil {
			f.Description = *cmd.Description
		}
		f.Version = cmd.Version
		f, err = s.Store.UpdateFolder(r.Context(), f, cmd.Overwrite)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, f)
	})
	api.HandleFunc("POST /api/folders/{uid}/move", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			ParentUID string `json:"parentUid"`
		}
		if err := decode(w, r, &cmd); err != nil {
			fail(w, 400, err)
			return
		}
		f, err := s.Store.MoveFolder(r.Context(), r.PathValue("uid"), cmd.ParentUID)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, f)
	})
	api.HandleFunc("DELETE /api/folders/{uid}", func(w http.ResponseWriter, r *http.Request) {
		force := false
		if raw := r.URL.Query().Get("forceDeleteRules"); raw != "" {
			var err error
			force, err = strconv.ParseBool(raw)
			if err != nil {
				fail(w, 400, err)
				return
			}
		}
		if err := s.Alerts.DeleteFolder(r.Context(), r.PathValue("uid"), force); err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"message": "Folder deleted"})
	})
	api.HandleFunc("GET /api/folders/{uid}/parents", func(w http.ResponseWriter, r *http.Request) {
		f, err := s.Store.Folder(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		f, allowed, err := s.readableFolder(r.Context(), f)
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
		if !allowed {
			writeResourceReadError(w, identity.ErrForbidden)
			return
		}
		out := []model.Folder{}
		seen := map[string]bool{f.UID: true}
		for f.ParentUID != "" {
			if len(seen) >= 64 || seen[f.ParentUID] {
				writeResourceReadError(w, store.ErrPermissionAncestry)
				return
			}
			seen[f.ParentUID] = true
			f, err = s.Store.Folder(r.Context(), f.ParentUID)
			if err != nil {
				dashboardResourceError(w, err)
				return
			}
			f, allowed, err = s.readableFolder(r.Context(), f)
			if err != nil {
				writeResourceReadError(w, err)
				return
			}
			if allowed {
				out = append(out, f)
			}
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("GET /api/library-elements", s.listLibraryElements)
	api.HandleFunc("GET /api/library-elements/{uid}", func(w http.ResponseWriter, r *http.Request) {
		e, err := s.Store.LibraryElement(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": e})
	})
	byName := func(w http.ResponseWriter, r *http.Request) {
		elements, err := s.Store.LibraryElements(r.Context())
		if err != nil {
			resourceError(w, err)
			return
		}
		name := r.PathValue("tail")
		out := []model.LibraryElement{}
		for _, e := range elements {
			if e.Name == name {
				out = append(out, e)
			}
		}
		writeJSON(w, 200, map[string]any{"result": out})
	}
	api.HandleFunc("GET /api/library-elements/{uid}/{tail}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("uid") == "name" {
			byName(w, r)
			return
		}
		if r.PathValue("tail") != "connections" {
			fail(w, 404, errors.New("resource not found"))
			return
		}
		connections, err := s.Store.LibraryConnections(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"result": connections})
	})
	api.HandleFunc("GET /api/v1/library-models/{uid}", func(w http.ResponseWriter, r *http.Request) {
		e, err := s.Store.LibraryElement(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(e.Model)
	})
	api.HandleFunc("POST /api/library-elements", s.saveLibraryElement)
	api.HandleFunc("PATCH /api/library-elements/{uid}", s.saveLibraryElement)
	api.HandleFunc("DELETE /api/library-elements/{uid}", func(w http.ResponseWriter, r *http.Request) {
		e, err := s.Store.LibraryElement(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		if err = s.Store.DeleteLibraryElement(r.Context(), e.UID); err != nil {
			dashboardResourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": e.ID, "message": "Library element deleted"})
	})
	api.HandleFunc("PUT /api/v1/dashboard-folders/{id}", s.moveDashboardFolder)
	api.HandleFunc("POST /api/v1/dashboard-library/{id}", s.insertDashboardLibrary)
	api.HandleFunc("POST /api/v1/dashboard-library/{id}/unlink", s.unlinkDashboardLibrary)
}
func (s *Server) listLibraryElements(w http.ResponseWriter, r *http.Request) {
	elements, err := s.Store.LibraryElements(r.Context())
	if err != nil {
		resourceError(w, err)
		return
	}
	params := r.URL.Query()
	page, perPage := 1, 100
	for _, p := range []struct {
		name string
		dst  *int
	}{{"page", &page}, {"perPage", &perPage}} {
		if raw := params.Get(p.name); raw != "" {
			*p.dst, err = strconv.Atoi(raw)
			if err != nil || *p.dst < 1 || *p.dst > 1000 {
				fail(w, 400, errors.New(p.name+" must be 1–1000"))
				return
			}
		}
	}
	if kind := params.Get("kind"); kind != "" && kind != "1" {
		fail(w, 400, errors.New("kind must be 1"))
		return
	}
	folders := strings.Split(params.Get("folderFilterUIDs"), ",")
	types := strings.Split(params.Get("typeFilter"), ",")
	folderIDs := strings.Split(params.Get("folderFilter"), ",")
	contains := func(items []string, value string) bool {
		for _, item := range items {
			if item == value {
				return true
			}
		}
		return false
	}
	out := []model.LibraryElement{}
	for _, e := range elements {
		if query := strings.ToLower(params.Get("searchString")); query != "" && !strings.Contains(strings.ToLower(e.Name), query) {
			continue
		}
		if uid := params.Get("excludeUid"); uid != "" && e.UID == uid {
			continue
		}
		if params.Get("folderFilterUIDs") != "" && !contains(folders, e.FolderUID) && !(e.FolderUID == "" && contains(folders, "general")) {
			continue
		}
		if params.Get("folderFilter") != "" && !contains(folderIDs, strconv.FormatInt(e.FolderID, 10)) {
			continue
		}
		if params.Get("typeFilter") != "" && !contains(types, e.Type) {
			continue
		}
		out = append(out, e)
	}
	if direction := params.Get("sortDirection"); direction == "desc" {
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) > strings.ToLower(out[j].Name) })
	} else if direction != "" && direction != "asc" {
		fail(w, 400, errors.New("sortDirection must be asc or desc"))
		return
	}
	total := len(out)
	start := (page - 1) * perPage
	if start >= total {
		out = []model.LibraryElement{}
	} else {
		end := start + perPage
		if end > total {
			end = total
		}
		out = out[start:end]
	}
	writeJSON(w, 200, map[string]any{"result": map[string]any{"totalCount": total, "elements": out, "page": page, "perPage": perPage}})
}
func (s *Server) saveLibraryElement(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		UID       string          `json:"uid"`
		Name      *string         `json:"name"`
		Kind      int64           `json:"kind"`
		FolderUID *string         `json:"folderUid"`
		FolderID  *int64          `json:"folderId"`
		Version   int64           `json:"version"`
		Model     json.RawMessage `json:"model"`
	}
	if err := decode(w, r, &cmd); err != nil {
		fail(w, 400, err)
		return
	}
	create := r.Method == "POST"
	e := model.LibraryElement{UID: cmd.UID, Kind: cmd.Kind, Version: cmd.Version, Model: cmd.Model}
	if !create {
		var err error
		e, err = s.Store.LibraryElement(r.Context(), r.PathValue("uid"))
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		if cmd.UID != "" && cmd.UID != e.UID {
			fail(w, 400, errors.New("library UID is immutable"))
			return
		}
		e.Version = cmd.Version
		if cmd.Kind != 0 {
			e.Kind = cmd.Kind
		}
		if len(cmd.Model) > 0 {
			e.Model = cmd.Model
		}
	}
	if cmd.Name != nil {
		e.Name = *cmd.Name
	}
	if cmd.FolderUID != nil || cmd.FolderID != nil {
		uid := ""
		id := int64(0)
		if cmd.FolderUID != nil {
			uid = *cmd.FolderUID
		}
		if cmd.FolderID != nil {
			id = *cmd.FolderID
		}
		f, err := s.resolveDashboardFolder(r.Context(), uid, id)
		if err != nil {
			dashboardResourceError(w, err)
			return
		}
		e.FolderUID = f.UID
	}
	e, err := s.Store.SaveLibraryElement(r.Context(), e, create)
	if err != nil {
		dashboardResourceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"result": e})
}
