package server

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

func silenceFilterMatches(silence model.Silence, filters []*labels.Matcher) bool {
	for _, filter := range filters {
		found := false
		for _, matcher := range silence.Matchers {
			m := matcher.NotificationMatcher()
			operator := map[labels.MatchType]string{labels.MatchEqual: "=", labels.MatchNotEqual: "!=", labels.MatchRegexp: "=~", labels.MatchNotRegexp: "!~"}[filter.Type]
			if m.Name == filter.Name && m.Value == filter.Value && m.Operator == operator {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *Server) silenceRoutes(api routeRegistrar) {
	list := func(w http.ResponseWriter, r *http.Request) {
		states := map[string]bool{"active": true, "pending": true, "expired": true}
		for state := range states {
			if value := r.URL.Query().Get(state); value != "" {
				v, err := strconv.ParseBool(value)
				if err != nil {
					fail(w, 400, errors.New("silence state filters must be booleans"))
					return
				}
				states[state] = v
			}
		}
		var filters []*labels.Matcher
		if values := r.URL.Query()["filter"]; len(values) > 0 {
			if len(values) > 32 || len(strings.Join(values, ",")) > 8192 {
				fail(w, 400, errors.New("too many silence filters"))
				return
			}
			var err error
			filters, err = parser.NewParser(parser.Options{}).ParseMetricSelector("{" + strings.Join(values, ",") + "}")
			if err != nil {
				fail(w, 400, errors.New("invalid silence filter expression"))
				return
			}
		}
		silences, err := s.Store.Silences(r.Context(), time.Now())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []model.Silence{}
		for _, silence := range silences {
			if states[silence.Status.State] && silenceFilterMatches(silence, filters) {
				out = append(out, silence)
			}
		}
		order := map[string]int{"active": 0, "pending": 1, "expired": 2}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.Status.State != b.Status.State {
				return order[a.Status.State] < order[b.Status.State]
			}
			if a.Status.State == "pending" {
				return a.StartsAt.Before(b.StartsAt)
			}
			if a.Status.State == "expired" {
				return a.EndsAt.After(b.EndsAt)
			}
			return a.EndsAt.Before(b.EndsAt)
		})
		writeJSON(w, 200, out)
	}
	save := func(w http.ResponseWriter, r *http.Request) {
		var input model.Silence
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		value, err := s.Store.SaveSilence(r.Context(), input, time.Now())
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"silenceID": value.ID})
	}
	get := func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Store.Silence(r.Context(), r.PathValue("id"), time.Now())
		if err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, value)
	}
	expire := func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.ExpireSilence(r.Context(), r.PathValue("id"), time.Now()); err != nil {
			notificationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"expired": true})
	}
	api.HandleFunc("GET /api/v1/alerting/silences", list)
	api.HandleFunc("POST /api/v1/alerting/silences", save)
	api.HandleFunc("GET /api/v1/alerting/silences/{id}", get)
	api.HandleFunc("DELETE /api/v1/alerting/silences/{id}", expire)
	api.HandleFunc("GET /api/alertmanager/grafana/api/v2/silences", list)
	api.HandleFunc("POST /api/alertmanager/grafana/api/v2/silences", save)
	api.HandleFunc("GET /api/alertmanager/grafana/api/v2/silence/{id}", get)
	api.HandleFunc("DELETE /api/alertmanager/grafana/api/v2/silence/{id}", expire)
	api.HandleFunc("POST /api/v1/alerting/silences/preview", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Matchers []model.SilenceMatcher `json:"matchers"`
			Labels   map[string]string      `json:"labels"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if err := model.ValidateSilenceMatchers(input.Matchers); err != nil {
			fail(w, 400, err)
			return
		}
		if err := model.ValidateLabels(input.Labels); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"matches": model.SilenceMatches(input.Matchers, input.Labels)})
	})
}
