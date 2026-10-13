package server

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

func (s *Server) dashboardExportRoutes(api routeRegistrar) {
	api.HandleFunc("GET /api/v1/dashboard-exports/{id}", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query["format"]) > 1 {
			fail(w, http.StatusBadRequest, errors.New("export requires one valid format"))
			return
		}
		for key := range query {
			if key != "format" {
				fail(w, http.StatusBadRequest, errors.New("export accepts format only"))
				return
			}
		}
		format := query.Get("format")
		if format == "" {
			format = "native"
		}
		if format != "native" && format != "grafana" {
			fail(w, http.StatusBadRequest, errors.New("format must be native or grafana"))
			return
		}
		dashboards, err := s.Store.Dashboards(r.Context())
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		for _, dashboard := range dashboards {
			if dashboard.ID != r.PathValue("id") {
				continue
			}
			var allowed bool
			dashboard, allowed, err = s.readableDashboard(r.Context(), dashboard, false)
			if err != nil {
				writeResourceReadError(w, err)
				return
			}
			if !allowed {
				writeResourceReadError(w, identity.ErrForbidden)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			if format == "native" {
				writeJSON(w, http.StatusOK, dashboard)
				return
			}
			if len(dashboard.Grafana) == 0 {
				writeJSON(w, http.StatusOK, classicDocument(dashboard))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(dashboard.Grafana)
			return
		}
		fail(w, http.StatusNotFound, errors.New("dashboard not found"))
	})
}
