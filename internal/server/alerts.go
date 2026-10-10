package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func ruleError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRuleConflict) || errors.Is(err, store.ErrStaleEvaluation) || errors.Is(err, alerting.ErrBusy) {
		fail(w, 409, err)
	} else {
		resourceError(w, err)
	}
}
func (s *Server) alertRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/alerts/rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, rules)
	})
	api.HandleFunc("GET /api/v1/alerts/rules/{uid}", func(w http.ResponseWriter, r *http.Request) {
		rule, err := s.Store.AlertRule(r.Context(), r.PathValue("uid"))
		if err != nil {
			ruleError(w, err)
			return
		}
		writeJSON(w, 200, rule)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var rule model.AlertRule
		if err := decode(w, r, &rule); err != nil {
			fail(w, 400, err)
			return
		}
		if uid := r.PathValue("uid"); uid != "" {
			rule.UID = uid
		} else if rule.UID == "" {
			rule.UID = uuid.NewString()
		}
		rule.Defaults()
		if rule.Execution == "grafana" && rule.Expr != "" {
			rule.Execution = "promql"
			rule.Grafana = nil
		}
		if err := rule.Validate(); err != nil {
			fail(w, 400, err)
			return
		}
		if rule.Execution == "grafana" {
			if err := s.validateAlertSources(r.Context(), rule); err != nil {
				fail(w, 400, err)
				return
			}
		} else if len(rule.Grafana) > 0 && !alerting.LegacyGraphMatches(rule) {
			rule.Grafana = nil
		}
		value, err := s.Alerts.SaveRule(r.Context(), rule)
		if err != nil {
			ruleError(w, err)
			return
		}
		writeJSON(w, 200, value)
	}
	api.HandleFunc("POST /api/v1/alerts/rules", save)
	api.HandleFunc("PUT /api/v1/alerts/rules/{uid}", save)
	api.HandleFunc("DELETE /api/v1/alerts/rules/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Alerts.DeleteRule(r.Context(), r.PathValue("uid")); err != nil {
			ruleError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	api.HandleFunc("POST /api/v1/alerts/rules/{uid}/evaluate", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Alerts.Evaluate(r.Context(), r.PathValue("uid"), time.Now())
		if err != nil {
			if err.Error() == "rule is paused" {
				fail(w, 409, err)
			} else {
				ruleError(w, err)
			}
			return
		}
		writeJSON(w, 200, value)
	})
	api.HandleFunc("GET /api/v1/alerts/history", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				fail(w, 400, err)
				return
			}
		}
		if limit < 1 || limit > 1000 {
			fail(w, 400, errors.New("limit must be 1–1000"))
			return
		}
		history, err := s.Store.AlertHistory(r.Context(), r.URL.Query().Get("uid"), limit)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, history)
	})
	api.HandleFunc("GET /api/v1/provisioning/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []alerting.GrafanaRule{}
		for _, rule := range rules {
			out = append(out, alerting.ExportGrafana(rule.AlertRule))
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("GET /api/v1/provisioning/alert-rules/{uid}", func(w http.ResponseWriter, r *http.Request) {
		rule, err := s.Store.AlertRule(r.Context(), r.PathValue("uid"))
		if err != nil {
			ruleError(w, err)
			return
		}
		writeJSON(w, 200, alerting.ExportGrafana(rule.AlertRule))
	})
	grafSave := func(w http.ResponseWriter, r *http.Request) {
		var g alerting.GrafanaRule
		if err := decode(w, r, &g); err != nil {
			fail(w, 400, err)
			return
		}
		version := 0
		interval := 30
		if uid := r.PathValue("uid"); uid != "" {
			previous, err := s.Store.AlertRule(r.Context(), uid)
			if err != nil {
				ruleError(w, err)
				return
			}
			g.UID = uid
			version = previous.Version
			interval = previous.IntervalSeconds
		} else if g.UID == "" {
			g.UID = uuid.NewString()
		}
		rule, err := alerting.CompileGrafana(g, interval)
		if err != nil {
			fail(w, 400, err)
			return
		}
		rule.Version = version
		if err := s.validateAlertSources(r.Context(), rule); err != nil {
			fail(w, 400, err)
			return
		}
		value, err := s.Alerts.SaveRule(r.Context(), rule)
		if err != nil {
			ruleError(w, err)
			return
		}
		code := 201
		if r.Method == "PUT" {
			code = 200
		}
		writeJSON(w, code, alerting.ExportGrafana(value.AlertRule))
	}
	api.HandleFunc("POST /api/v1/provisioning/alert-rules", grafSave)
	api.HandleFunc("PUT /api/v1/provisioning/alert-rules/{uid}", grafSave)
	api.HandleFunc("DELETE /api/v1/provisioning/alert-rules/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Alerts.DeleteRule(r.Context(), r.PathValue("uid")); err != nil {
			ruleError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	api.HandleFunc("GET /api/v1/provisioning/folder/{folder}/rule-groups/{group}", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []alerting.GrafanaRule{}
		interval := 30
		for _, rule := range rules {
			if rule.FolderUID == r.PathValue("folder") && rule.Group == r.PathValue("group") {
				out = append(out, alerting.ExportGrafana(rule.AlertRule))
				interval = rule.IntervalSeconds
			}
		}
		if len(out) == 0 {
			resourceError(w, sql.ErrNoRows)
			return
		}
		writeJSON(w, 200, map[string]any{"title": r.PathValue("group"), "folderUid": r.PathValue("folder"), "interval": interval, "rules": out})
	})
}
func (s *Server) promAlertRoutes(mux *http.ServeMux) {
	alerts := func(rule model.AlertRuleView) []any {
		out := []any{}
		for _, v := range rule.Runtime.Instances {
			if v.State == "Normal" {
				continue
			}
			value := "NaN"
			if v.ValueText != "" {
				value = v.ValueText
			}
			if v.Value != nil {
				value = strconv.FormatFloat(*v.Value, 'g', -1, 64)
			}
			state := "pending"
			if v.State != "Pending" {
				state = "firing"
			}
			out = append(out, map[string]any{"labels": v.Labels, "annotations": rule.Annotations, "state": state, "activeAt": time.UnixMilli(v.ActiveAt).UTC().Format(time.RFC3339Nano), "value": value})
		}
		return out
	}
	mux.HandleFunc("GET /api/v1/alerts", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []any{}
		for _, rule := range rules {
			out = append(out, alerts(rule)...)
		}
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"alerts": out}})
	})
	mux.HandleFunc("GET /api/v1/rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := s.Store.AlertRules(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		groups := map[string][]any{}
		intervals := map[string]int{}
		for _, rule := range rules {
			kind := "alerting"
			state := "inactive"
			if rule.Record != "" {
				kind = "recording"
			}
			for _, v := range rule.Runtime.Instances {
				if v.State == "Pending" && state != "firing" {
					state = "pending"
				} else if v.State == "Firing" || v.State == "Recovering" || v.State == "NoData" || v.State == "Error" {
					state = "firing"
				}
			}
			if filter := r.URL.Query().Get("type"); filter != "" && filter != kind {
				continue
			}
			name := rule.Title
			if rule.Record != "" {
				name = rule.Record
			}
			item := map[string]any{"name": name, "query": rule.Expr, "labels": rule.Labels, "health": rule.Runtime.Health, "lastError": rule.Runtime.Error, "evaluationTime": float64(rule.Runtime.DurationMS) / 1000, "lastEvaluation": time.UnixMilli(rule.Runtime.LastEvaluation).UTC().Format(time.RFC3339Nano), "type": kind}
			if kind == "alerting" {
				item["state"] = state
				item["duration"] = rule.ForSeconds
				item["keepFiringFor"] = rule.KeepFiringForSeconds
				item["annotations"] = rule.Annotations
				item["alerts"] = alerts(rule)
			}
			key := rule.FolderUID + "/" + rule.Group
			groups[key] = append(groups[key], item)
			intervals[key] = rule.IntervalSeconds
		}
		keys := []string{}
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := []any{}
		for _, key := range keys {
			out = append(out, map[string]any{"name": key, "file": fmt.Sprintf("metricspanel://rules/%s", key), "interval": intervals[key], "rules": groups[key]})
		}
		writeJSON(w, 200, map[string]any{"status": "success", "data": map[string]any{"groups": out}})
	})
}
