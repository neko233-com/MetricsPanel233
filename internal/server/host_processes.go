package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/hostprocess"
)

func hostProcessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostprocess.ErrInvalidQuery):
		fail(w, 400, err)
	case errors.Is(err, hostprocess.ErrSnapshotExpired):
		fail(w, 410, err)
	case errors.Is(err, hostprocess.ErrProcessNotFound), errors.Is(err, hostprocess.ErrIconNotFound):
		fail(w, 404, err)
	case errors.Is(err, hostprocess.ErrProcessChanged):
		fail(w, 409, err)
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, 504, err)
	case errors.Is(err, context.Canceled):
		fail(w, 408, err)
	default:
		fail(w, 503, err)
	}
}
func validateHostProcessParameters(r *http.Request, allowed ...string) error {
	keys := map[string]bool{}
	for _, name := range allowed {
		keys[name] = true
	}
	for key, values := range r.URL.Query() {
		if !keys[key] || len(values) != 1 {
			return hostprocess.ErrInvalidQuery
		}
	}
	return nil
}
func (s *Server) hostProcessRoutes(api *http.ServeMux) {
	manager := hostprocess.New()
	api.HandleFunc("GET /api/v1/host/processes", func(w http.ResponseWriter, r *http.Request) {
		if err := validateHostProcessParameters(r, "prefix", "limit", "continue"); err != nil {
			hostProcessError(w, err)
			return
		}
		q := r.URL.Query()
		options := hostprocess.Query{Prefix: q.Get("prefix"), Continue: q.Get("continue"), PrefixSet: q.Has("prefix"), LimitSet: q.Has("limit")}
		if options.LimitSet {
			var err error
			options.Limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil {
				hostProcessError(w, hostprocess.ErrInvalidQuery)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		list, err := manager.List(ctx, options)
		if err != nil {
			hostProcessError(w, err)
			return
		}
		writeJSON(w, 200, list)
	})
	api.HandleFunc("GET /api/v1/host/processes/{pid}", func(w http.ResponseWriter, r *http.Request) {
		if err := validateHostProcessParameters(r, "startToken"); err != nil {
			hostProcessError(w, err)
			return
		}
		pid, err := strconv.ParseUint(r.PathValue("pid"), 10, 32)
		if err != nil {
			hostProcessError(w, hostprocess.ErrInvalidQuery)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		detail, err := manager.Get(ctx, int(pid), r.URL.Query().Get("startToken"))
		if err != nil {
			hostProcessError(w, err)
			return
		}
		writeJSON(w, 200, detail)
	})
	api.HandleFunc("GET /api/v1/host/process-icons/{iconKey}", func(w http.ResponseWriter, r *http.Request) {
		if err := validateHostProcessParameters(r); err != nil {
			hostProcessError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		data, source, err := manager.Icon(ctx, r.PathValue("iconKey"))
		if err != nil {
			hostProcessError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Process-Icon-Source", source)
		w.WriteHeader(200)
		_, _ = w.Write(data)
	})
}
