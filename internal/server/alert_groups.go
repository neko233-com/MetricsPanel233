package server

import (
	"database/sql"
	"net/http"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func (s *Server) alertGroupRoutes(api *http.ServeMux) {
	const path = "/api/v1/provisioning/folder/{folder}/rule-groups/{group}"
	api.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		folder, group := r.PathValue("folder"), r.PathValue("group")
		views, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		matches := []model.AlertRuleView{}
		for _, view := range views {
			if view.FolderUID == folder && view.Group == group {
				matches = append(matches, view)
			}
		}
		if len(matches) == 0 {
			resourceError(w, sql.ErrNoRows)
			return
		}
		store.SortAlertGroup(matches)
		writeJSON(w, 200, alerting.ExportGrafanaGroup(folder, group, matches[0].IntervalSeconds, matches))
	})
	api.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
		var input alerting.GrafanaGroup
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		folder, group := r.PathValue("folder"), r.PathValue("group")
		if err := store.ValidateAlertGroup(folder, group, input.Interval); err != nil {
			fail(w, 400, err)
			return
		}
		var rules []model.AlertRule
		if input.Rules != nil {
			rules = make([]model.AlertRule, 0, len(input.Rules))
			for _, graph := range input.Rules {
				graph.FolderUID, graph.RuleGroup = folder, group
				if graph.UID == "" {
					graph.UID = uuid.NewString()
				}
				rule, err := alerting.CompileGrafana(graph, input.Interval)
				if err != nil {
					fail(w, 400, err)
					return
				}
				if err := s.validateAlertSources(r.Context(), rule); err != nil {
					fail(w, 400, err)
					return
				}
				rules = append(rules, rule)
			}
		}
		provenance := "api"
		if _, disabled := r.Header["X-Disable-Provenance"]; disabled {
			provenance = ""
		}
		views, err := s.Alerts.ReplaceRuleGroup(r.Context(), folder, group, input.Interval, rules, provenance)
		if err != nil {
			ruleError(w, err)
			return
		}
		writeJSON(w, 200, alerting.ExportGrafanaGroup(folder, group, input.Interval, views))
	})
	api.HandleFunc("DELETE "+path, func(w http.ResponseWriter, r *http.Request) {
		_, err := s.Alerts.ReplaceRuleGroup(r.Context(), r.PathValue("folder"), r.PathValue("group"), 5, []model.AlertRule{}, "api")
		if err != nil {
			ruleError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
