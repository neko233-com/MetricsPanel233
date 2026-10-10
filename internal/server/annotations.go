package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func annotationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, 400, errors.New("invalid annotation id"))
		return 0, false
	}
	return id, true
}

func (s *Server) annotationScope(r *http.Request, uid string, id int64) (string, int64, error) {
	if uid == "" && id == 0 {
		return "", 0, nil
	}
	dashboards, err := s.Store.Dashboards(r.Context())
	if err != nil {
		return "", 0, err
	}
	for _, dashboard := range dashboards {
		doc := classicDocument(dashboard)
		candidate, _ := doc["uid"].(string)
		number := int64(crc32.ChecksumIEEE([]byte(candidate)))
		if (uid != "" && candidate == uid) || (uid == "" && id == number) {
			return candidate, number, nil
		}
	}
	return "", 0, sql.ErrNoRows
}

func annotationError(w http.ResponseWriter, err error) {
	if errors.Is(err, model.ErrInvalidAnnotation) {
		fail(w, 400, err)
		return
	}
	if errors.Is(err, store.ErrAnnotationConflict) {
		fail(w, 409, err)
		return
	}
	resourceError(w, err)
}

func (s *Server) annotationRoutes(api *http.ServeMux) {
	for _, prefix := range []string{"/api/annotations", "/api/v1/annotations"} {
		api.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
			params := r.URL.Query()
			q := model.AnnotationQuery{DashboardUID: params.Get("dashboardUID"), Tags: params["tags"]}
			values := map[string]*int64{"from": &q.From, "to": &q.To, "annotationId": &q.ID, "panelId": &q.PanelID, "userId": &q.UserID}
			var dashboardID int64
			values["dashboardId"] = &dashboardID
			for name, target := range values {
				if value := params.Get(name); value != "" {
					number, err := strconv.ParseInt(value, 10, 64)
					if err != nil || number < 0 {
						fail(w, 400, errors.New("invalid annotation "+name))
						return
					}
					*target = number
				}
			}
			q.Limit = 100
			if raw := params.Get("limit"); raw != "" {
				var err error
				q.Limit, err = strconv.Atoi(raw)
				if err != nil {
					fail(w, 400, err)
					return
				}
			}
			if raw := params.Get("matchAny"); raw != "" {
				var err error
				q.MatchAny, err = strconv.ParseBool(raw)
				if err != nil {
					fail(w, 400, err)
					return
				}
			}
			if dashboardID > 0 && q.DashboardUID == "" {
				var err error
				q.DashboardUID, _, err = s.annotationScope(r, "", dashboardID)
				if err != nil {
					annotationError(w, err)
					return
				}
			}
			// The workspace currently has one local principal and manual annotations.
			if kind := params.Get("type"); kind != "" && kind != "annotation" && kind != "alert" {
				fail(w, 400, errors.New("invalid annotation type"))
				return
			}
			if params.Get("type") == "alert" || (params.Get("userUID") != "" && params.Get("userUID") != "metricspanel") || params.Get("alertId") != "" || params.Get("alertUID") != "" {
				writeJSON(w, 200, []model.Annotation{})
				return
			}
			result, err := s.Store.Annotations(r.Context(), q)
			if err != nil {
				annotationError(w, err)
				return
			}
			writeJSON(w, 200, result)
		})
		api.HandleFunc("GET "+prefix+"/tags", func(w http.ResponseWriter, r *http.Request) {
			limit := 100
			if raw := r.URL.Query().Get("limit"); raw != "" {
				var err error
				limit, err = strconv.Atoi(raw)
				if err != nil {
					fail(w, 400, err)
					return
				}
			}
			result, err := s.Store.AnnotationTags(r.Context(), r.URL.Query().Get("tag"), limit)
			if err != nil {
				annotationError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"result": map[string]any{"tags": result}})
		})
		api.HandleFunc("GET "+prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
			id, ok := annotationID(w, r)
			if !ok {
				return
			}
			items, err := s.Store.Annotations(r.Context(), model.AnnotationQuery{ID: id, Limit: 1})
			if err != nil {
				annotationError(w, err)
				return
			}
			if len(items) == 0 {
				resourceError(w, sql.ErrNoRows)
				return
			}
			writeJSON(w, 200, items[0])
		})
		api.HandleFunc("POST "+prefix, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				DashboardUID   string          `json:"dashboardUID"`
				DashboardID    int64           `json:"dashboardId"`
				PanelID        int64           `json:"panelId"`
				Time           int64           `json:"time"`
				TimeEnd        int64           `json:"timeEnd"`
				Text           string          `json:"text"`
				Tags           []string        `json:"tags"`
				Data           json.RawMessage `json:"data"`
				IdempotencyKey string          `json:"idempotencyKey"`
			}
			if err := decode(w, r, &request); err != nil {
				fail(w, 400, err)
				return
			}
			uid, id, err := s.annotationScope(r, request.DashboardUID, request.DashboardID)
			if err != nil {
				annotationError(w, err)
				return
			}
			a, err := s.Store.CreateAnnotation(r.Context(), model.Annotation{DashboardUID: uid, DashboardID: id, PanelID: request.PanelID, Time: request.Time, TimeEnd: request.TimeEnd, Text: request.Text, Tags: request.Tags, Data: request.Data, UserID: 1, Login: "metricspanel"}, request.IdempotencyKey)
			if err != nil {
				annotationError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"message": "Annotation added", "id": a.ID})
		})
		for _, method := range []string{"PUT", "PATCH"} {
			api.HandleFunc(method+" "+prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
				id, ok := annotationID(w, r)
				if !ok {
					return
				}
				var p model.AnnotationPatch
				if err := decode(w, r, &p); err != nil {
					fail(w, 400, err)
					return
				}
				if r.Method == "PUT" {
					if p.Time == nil || *p.Time <= 0 || p.Text == nil || *p.Text == "" {
						fail(w, 400, errors.New("annotation replacement requires positive time and text"))
						return
					}
					if p.TimeEnd == nil || *p.TimeEnd == 0 {
						p.TimeEnd = p.Time
					}
					if p.Tags == nil {
						tags := []string{}
						p.Tags = &tags
					}
					if len(p.Data) == 0 {
						p.Data = json.RawMessage("null")
					}
				}
				if _, err := s.Store.PatchAnnotation(r.Context(), id, p); err != nil {
					annotationError(w, err)
					return
				}
				message := "Annotation updated"
				if r.Method == "PATCH" {
					message = "Annotation patched"
				}
				writeJSON(w, 200, map[string]string{"message": message})
			})
		}
		api.HandleFunc("DELETE "+prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
			id, ok := annotationID(w, r)
			if !ok {
				return
			}
			if err := s.Store.DeleteAnnotation(r.Context(), id); err != nil {
				annotationError(w, err)
				return
			}
			writeJSON(w, 200, map[string]string{"message": "Annotation deleted"})
		})
	}
	api.HandleFunc("POST /api/annotations/mass-delete", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			AnnotationID int64  `json:"annotationId"`
			DashboardUID string `json:"dashboardUID"`
			DashboardID  int64  `json:"dashboardId"`
			PanelID      int64  `json:"panelId"`
		}
		if err := decode(w, r, &cmd); err != nil {
			fail(w, 400, err)
			return
		}
		var err error
		if cmd.AnnotationID > 0 {
			err = s.Store.DeleteAnnotation(r.Context(), cmd.AnnotationID)
		} else {
			var uid string
			uid, _, err = s.annotationScope(r, cmd.DashboardUID, cmd.DashboardID)
			if err == nil {
				err = s.Store.DeletePanelAnnotations(r.Context(), uid, cmd.PanelID)
			}
		}
		if err != nil {
			annotationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"message": "Annotations deleted"})
	})
	api.HandleFunc("POST /api/annotations/graphite", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			What string          `json:"what"`
			Data string          `json:"data"`
			When int64           `json:"when"`
			Tags json.RawMessage `json:"tags"`
		}
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		tags := []string{}
		if len(request.Tags) > 0 {
			if json.Unmarshal(request.Tags, &tags) != nil {
				var legacy string
				if json.Unmarshal(request.Tags, &legacy) != nil {
					fail(w, 400, errors.New("Graphite tags must be strings"))
					return
				}
				tags = strings.Fields(legacy)
			}
		}
		when := request.When
		if when == 0 {
			when = time.Now().Unix()
		}
		if when < 0 || when > 9007199254740 {
			fail(w, 400, errors.New("invalid Graphite seconds timestamp"))
			return
		}
		text := request.What
		if request.Data != "" {
			text += "\n" + request.Data
		}
		a, err := s.Store.CreateAnnotation(r.Context(), model.Annotation{Time: when * 1000, Text: text, Tags: tags, UserID: 1, Login: "metricspanel"}, "")
		if err != nil {
			annotationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"message": "Graphite annotation added", "id": a.ID})
	})
}
