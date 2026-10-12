package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/neko233-com/MetricsPanel233/internal/plugins"
)

func (s *Server) pluginCatalogRoutes(api routeRegistrar, catalog *plugins.CatalogClient) {
	api.HandleFunc("GET /api/v1/plugins/catalog", func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query["q"]) > 1 || len(query["type"]) > 1 {
			fail(w, http.StatusBadRequest, errors.New("catalog search needs valid single q and type values"))
			return
		}
		for name := range query {
			if name != "q" && name != "type" {
				fail(w, http.StatusBadRequest, errors.New("catalog search accepts q and type only"))
				return
			}
		}
		items, err := catalog.List(r.Context(), query.Get("q"), query.Get("type"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})
	api.HandleFunc("GET /api/v1/plugin-catalog/{id}", func(w http.ResponseWriter, r *http.Request) {
		item, err := catalog.Detail(r.Context(), r.PathValue("id"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	api.HandleFunc("GET /api/v1/plugin-catalog/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		versions, err := catalog.Versions(r.Context(), r.PathValue("id"))
		if err != nil {
			catalogFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, versions)
	})
}

func catalogFailure(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, plugins.ErrCatalogInput):
		status = http.StatusBadRequest
	case errors.Is(err, plugins.ErrCatalogNotFound):
		status = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	fail(w, status, err)
}
