package server

import (
	"context"
	"fmt"

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
