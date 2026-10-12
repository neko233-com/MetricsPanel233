package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type provisionedMuteTiming struct {
	Name          string            `json:"name"`
	TimeIntervals []json.RawMessage `json:"time_intervals"`
	Version       string            `json:"version,omitempty"`
	Provenance    string            `json:"provenance,omitempty"`
}

func exportMuteTiming(v model.MuteTiming) provisionedMuteTiming {
	return provisionedMuteTiming{Name: v.Name, TimeIntervals: v.TimeIntervals, Version: strconv.Itoa(v.Version), Provenance: "api"}
}

func (s *Server) muteTimingRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/alerting/mute-timings/preview", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Timing model.MuteTiming `json:"timing"`
			At     time.Time        `json:"at"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if input.At.IsZero() {
			fail(w, 400, errors.New("preview requires at timestamp"))
			return
		}
		matched, err := input.Timing.Matches(input.At)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"matches": matched, "at": input.At.UTC()})
	})
	list := func(w http.ResponseWriter, r *http.Request) {
		values, err := s.Store.MuteTimings(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		if strings.Contains(r.URL.Path, "/provisioning/") {
			out := []provisionedMuteTiming{}
			for _, v := range values {
				out = append(out, exportMuteTiming(v))
			}
			writeJSON(w, 200, out)
		} else {
			writeJSON(w, 200, values)
		}
	}
	get := func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Store.MuteTiming(r.Context(), r.PathValue("name"))
		if err != nil {
			notificationError(w, err)
			return
		}
		if strings.Contains(r.URL.Path, "/provisioning/") {
			writeJSON(w, 200, exportMuteTiming(value))
		} else {
			writeJSON(w, 200, value)
		}
	}
	save := func(w http.ResponseWriter, r *http.Request) {
		var value model.MuteTiming
		var decodeErr error
		grafana := strings.Contains(r.URL.Path, "/provisioning/")
		if grafana {
			var input provisionedMuteTiming
			decodeErr = decode(w, r, &input)
			value.Name, value.TimeIntervals = input.Name, input.TimeIntervals
			if decodeErr == nil && input.Version != "" {
				value.Version, decodeErr = strconv.Atoi(input.Version)
			}
		} else {
			decodeErr = decode(w, r, &value)
		}
		if decodeErr != nil {
			fail(w, 400, decodeErr)
			return
		}
		if name := r.PathValue("name"); name != "" {
			if value.Name != "" && value.Name != name {
				fail(w, 400, errors.New("mute timing name does not match URL"))
				return
			}
			value.Name = name
			if value.Version == 0 {
				if strings.HasPrefix(r.URL.Path, "/api/v1/alerting/") {
					notificationError(w, store.ErrNotificationConflict)
					return
				}
				old, err := s.Store.MuteTiming(r.Context(), name)
				if err != nil {
					notificationError(w, err)
					return
				}
				value.Version = old.Version
			}
		} else if value.Version != 0 {
			notificationError(w, store.ErrNotificationConflict)
			return
		}
		value, err := s.Store.SaveMuteTiming(r.Context(), value)
		if err != nil {
			notificationError(w, err)
			return
		}
		code := 200
		if r.Method == http.MethodPost {
			code = 201
		}
		if grafana {
			writeJSON(w, code, exportMuteTiming(value))
		} else {
			writeJSON(w, code, value)
		}
	}
	remove := func(w http.ResponseWriter, r *http.Request) {
		var version *int
		if raw := r.URL.Query().Get("version"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				fail(w, 400, errors.New("invalid mute timing version"))
				return
			}
			version = &value
		}
		if err := s.Store.DeleteMuteTimingVersion(r.Context(), r.PathValue("name"), version); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	}
	for _, base := range []string{"/api/v1/alerting/mute-timings", "/api/v1/provisioning/mute-timings"} {
		api.HandleFunc("GET "+base, list)
		api.HandleFunc("POST "+base, save)
		api.HandleFunc("GET "+base+"/{name}", get)
		api.HandleFunc("PUT "+base+"/{name}", save)
		api.HandleFunc("DELETE "+base+"/{name}", remove)
	}
	api.HandleFunc("POST /api/v1/alerting/mute-timings/{name}/preview", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			At time.Time `json:"at"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if input.At.IsZero() {
			fail(w, 400, errors.New("preview requires at timestamp"))
			return
		}
		value, err := s.Store.MuteTiming(r.Context(), r.PathValue("name"))
		if err != nil {
			notificationError(w, err)
			return
		}
		matched, err := value.Matches(input.At)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"matches": matched, "at": input.At.UTC()})
	})
	api.HandleFunc("GET /api/v1/alerting/notification-groups", func(w http.ResponseWriter, r *http.Request) {
		values, err := s.Store.NotificationGroups(r.Context())
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, values)
	})
}
