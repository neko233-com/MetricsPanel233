package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func notificationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotificationConflict) || errors.Is(err, store.ErrContactPointReferenced) || errors.Is(err, store.ErrNotificationTemplateReferenced) {
		fail(w, http.StatusConflict, err)
	} else if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, errors.New("notification resource not found"))
	} else {
		fail(w, http.StatusBadRequest, err)
	}
}

func (s *Server) notificationRoutes(api *http.ServeMux) {
	s.notificationProvisioningRoutes(api)
	s.silenceRoutes(api)
	s.muteTimingRoutes(api)
	s.notificationTemplateRoutes(api)
	s.inhibitionRoutes(api)
	api.HandleFunc("GET /api/v1/alerting/contact-points", func(w http.ResponseWriter, r *http.Request) {
		points, err := s.Store.ContactPoints(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, points)
	})
	api.HandleFunc("GET /api/v1/alerting/contact-points/{uid}", func(w http.ResponseWriter, r *http.Request) {
		point, err := s.Store.ContactPoint(r.Context(), r.PathValue("uid"))
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, point)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var input model.ContactPointInput
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if uid := r.PathValue("uid"); uid != "" {
			if input.UID != "" && input.UID != uid {
				fail(w, 400, errors.New("contact point UID does not match the URL"))
				return
			}
			input.UID = uid
			if input.Version == 0 {
				fail(w, 409, store.ErrNotificationConflict)
				return
			}
		} else if input.Version != 0 {
			fail(w, 409, store.ErrNotificationConflict)
			return
		}
		point, err := s.Store.SaveContactPoint(r.Context(), input)
		if err != nil {
			notificationError(w, err)
			return
		}
		code := 200
		if r.Method == http.MethodPost {
			code = 201
		}
		writeJSON(w, code, point)
	}
	api.HandleFunc("POST /api/v1/alerting/contact-points", save)
	api.HandleFunc("PUT /api/v1/alerting/contact-points/{uid}", save)
	api.HandleFunc("DELETE /api/v1/alerting/contact-points/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.DeleteContactPoint(r.Context(), r.PathValue("uid")); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	api.HandleFunc("POST /api/v1/alerting/contact-points/{uid}/test", func(w http.ResponseWriter, r *http.Request) {
		delivery, err := s.Store.EnqueueTestNotification(r.Context(), r.PathValue("uid"))
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, delivery)
	})
	api.HandleFunc("GET /api/v1/alerting/notification-policy", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.NotificationPolicy(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, p)
	})
	api.HandleFunc("PUT /api/v1/alerting/notification-policy", func(w http.ResponseWriter, r *http.Request) {
		var p model.NotificationPolicy
		if err := decode(w, r, &p); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := s.Store.SaveNotificationPolicy(r.Context(), p)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, p)
	})
	api.HandleFunc("GET /api/v1/alerting/notifications", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil {
				fail(w, 400, errors.New("invalid notification limit"))
				return
			}
			limit = n
		}
		values, err := s.Store.Notifications(r.Context(), r.URL.Query().Get("contact_point_uid"), limit)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, values)
	})
	api.HandleFunc("POST /api/v1/alerting/notifications/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			fail(w, 400, errors.New("invalid notification ID"))
			return
		}
		v, err := s.Store.RetryNotification(r.Context(), id)
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 202, v)
	})
}
