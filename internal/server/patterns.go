package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/analysis"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type patternCapture struct {
	Metric        string            `json:"metric"`
	Labels        map[string]string `json:"labels"`
	Range         string            `json:"range"`
	Start         int64             `json:"start"`
	End           int64             `json:"end"`
	Aggregation   string            `json:"aggregation"`
	Normalization string            `json:"normalization"`
}

func (s *Server) patternRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/patterns", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				fail(w, 400, err)
				return
			}
		}
		patterns, err := s.Store.Patterns(r.Context(), limit)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"patterns": patterns, "dimensions": 64, "storage": s.Store.Kind()})
	})
	api.HandleFunc("GET /api/v1/patterns/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.Pattern(r.Context(), r.PathValue("id"))
		if err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, p)
	})
	api.HandleFunc("DELETE /api/v1/patterns/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.DeletePattern(r.Context(), r.PathValue("id")); err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	api.HandleFunc("POST /api/v1/patterns/capture", s.capturePatterns)
	api.HandleFunc("POST /api/v1/patterns/search", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID          string            `json:"id"`
			Metric      string            `json:"metric"`
			Labels      map[string]string `json:"labels"`
			Limit       int               `json:"limit"`
			Exact       bool              `json:"exact"`
			IncludeSelf bool              `json:"include_self"`
		}
		if err := decode(w, r, &request); err != nil {
			fail(w, 400, err)
			return
		}
		if request.ID == "" {
			fail(w, 400, errors.New("reference pattern id required"))
			return
		}
		reference, err := s.Store.Pattern(r.Context(), request.ID)
		if err != nil {
			resourceError(w, err)
			return
		}
		if request.Limit == 0 {
			request.Limit = 10
		}
		query := model.PatternSearch{Reference: reference, Metric: request.Metric, Labels: request.Labels, Limit: request.Limit, Exact: request.Exact, IncludeSelf: request.IncludeSelf}
		hits, err := s.Store.SearchPatterns(r.Context(), query)
		if err != nil {
			fail(w, 400, err)
			return
		}
		engine := "sqlite-exact"
		if s.Store.Kind() == "clickhouse" {
			engine = "clickhouse-hnsw"
			if request.Exact {
				engine = "clickhouse-exact"
			}
		}
		writeJSON(w, 200, map[string]any{"reference": reference.Summary(), "hits": hits, "engine": engine, "dimensions": 64, "distance": "L2", "approximate": s.Store.Kind() == "clickhouse" && !request.Exact})
	})
}
func (s *Server) capturePatterns(w http.ResponseWriter, r *http.Request) {
	var request patternCapture
	if err := decode(w, r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	if request.End == 0 {
		request.End = time.Now().UnixMilli()
	}
	if request.Start == 0 {
		duration := 30 * time.Minute
		if request.Range != "" {
			var err error
			duration, err = time.ParseDuration(request.Range)
			if err != nil {
				fail(w, 400, err)
				return
			}
		}
		request.Start = request.End - duration.Milliseconds()
	}
	if request.Start < 0 || request.End > time.Now().Add(5*time.Minute).UnixMilli() || request.End-request.Start < 64000 || request.End-request.Start > 31*24*3600000 {
		fail(w, 400, errors.New("window must be 64 seconds–31 days, using Unix milliseconds"))
		return
	}
	if request.Normalization == "" {
		request.Normalization = "shape"
	}
	if request.Aggregation == "" {
		request.Aggregation = "last"
	}
	if request.Aggregation != "last" && request.Aggregation != "rate" {
		fail(w, 400, errors.New("pattern aggregation must be last or rate"))
		return
	}
	if request.Normalization != "shape" && request.Normalization != "raw" {
		fail(w, 400, errors.New("normalization must be shape or raw"))
		return
	}
	if err := (model.Sample{Name: request.Metric, Labels: request.Labels}).Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	step := (request.End - request.Start + 63) / 64
	result, err := s.Store.Query(r.Context(), model.Query{Metric: request.Metric, Labels: request.Labels, Start: request.Start, End: request.End, Step: step, Aggregation: request.Aggregation})
	if err != nil {
		fail(w, 400, err)
		return
	}
	patterns := []model.Pattern{}
	skipped := []string{}
	for _, series := range result.Series {
		p, err := analysis.Embed(request.Metric, series, request.Start, request.End, request.Aggregation, request.Normalization)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %s", model.LabelsJSON(series.Labels), err))
			continue
		}
		patterns = append(patterns, p)
	}
	if len(patterns) == 0 {
		fail(w, 400, fmt.Errorf("no series has sufficient data for this window (skipped %d series)", len(skipped)))
		return
	}
	if err = s.Store.SavePatterns(r.Context(), patterns); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"patterns": patterns, "skipped": skipped, "dimensions": 64})
}
