package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/alertgraph"
	"github.com/neko233-com/MetricsPanel233/internal/expressions"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Server) validateAlertSources(ctx context.Context, rule model.AlertRule) error {
	plan, err := alertgraph.Parse(rule.Grafana)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, q := range plan.Data {
		if expressions.IsSource(q.DatasourceUID) || seen[q.DatasourceUID] {
			continue
		}
		seen[q.DatasourceUID] = true
		ds, err := s.dataSource(ctx, q.DatasourceUID)
		if err != nil {
			return fmt.Errorf("query %s datasource %s: %w", q.RefID, q.DatasourceUID, err)
		}
		if ds.Type == "prometheus" || ds.Type == "grafana" {
			continue
		}
		plugin, err := s.Store.Plugin(ctx, ds.Type)
		if err != nil {
			return err
		}
		if !plugin.Backend {
			return fmt.Errorf("query %s datasource %s has no backend for alerting", q.RefID, q.DatasourceUID)
		}
	}
	return nil
}

func (s *Server) previewAlertGraph(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Grafana json.RawMessage `json:"grafana"`
		At      int64           `json:"at"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, 400, err)
		return
	}
	if input.At < 0 || input.At > 253402300799999 {
		fail(w, 400, fmt.Errorf("at must be Unix milliseconds between 0 and year 9999"))
		return
	}
	plan, err := alertgraph.Parse(input.Grafana)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err = s.validateAlertSources(r.Context(), model.AlertRule{Grafana: input.Grafana}); err != nil {
		fail(w, 400, err)
		return
	}
	at := time.Now()
	if input.At != 0 {
		at = time.UnixMilli(input.At)
	}
	result, err := s.Alerts.PreviewGraph(r.Context(), plan, at)
	if err != nil {
		resourceError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
