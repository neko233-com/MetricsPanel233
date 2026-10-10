package alerting

import (
	"context"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type GrafanaGroup struct {
	Title     string        `json:"title"`
	FolderUID string        `json:"folderUid"`
	Interval  int           `json:"interval"`
	Rules     []GrafanaRule `json:"rules"`
}

func ExportGrafanaGroup(folder, group string, interval int, views []model.AlertRuleView) GrafanaGroup {
	out := GrafanaGroup{Title: group, FolderUID: folder, Interval: interval, Rules: []GrafanaRule{}}
	for _, view := range views {
		out.Rules = append(out.Rules, ExportGrafana(view.AlertRule))
	}
	return out
}

// Rule mutation gates prevent membership changing while all affected UID gates
// are acquired together. Unrelated evaluations can continue during the commit.
func (e *Engine) ReplaceRuleGroup(ctx context.Context, folder, group string, interval int, rules []model.AlertRule, provenance string) ([]model.AlertRuleView, error) {
	e.groupMu.Lock()
	defer e.groupMu.Unlock()
	current, err := e.Store.AlertRules(ctx)
	if err != nil {
		return nil, err
	}
	uids := map[string]bool{}
	type key struct{ folder, group string }
	affected := map[key]bool{{folder, group}: true}
	wanted := map[string]bool{}
	for _, rule := range rules {
		wanted[rule.UID] = true
	}
	for _, view := range current {
		if wanted[view.UID] {
			affected[key{view.FolderUID, view.Group}] = true
		}
	}
	for _, view := range current {
		if affected[key{view.FolderUID, view.Group}] {
			uids[view.UID] = true
		}
	}
	for _, rule := range rules {
		uids[rule.UID] = true
	}
	e.mu.Lock()
	for uid := range uids {
		if e.active[uid] {
			e.mu.Unlock()
			return nil, ErrBusy
		}
	}
	for uid := range uids {
		e.active[uid] = true
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		for uid := range uids {
			delete(e.active, uid)
		}
		e.mu.Unlock()
	}()
	return e.Store.ReplaceAlertRuleGroup(ctx, folder, group, interval, rules, provenance)
}
