package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrInvalidAlertGroup = errors.New("invalid alert rule group")

func ValidateAlertGroup(folder, group string, interval int) error {
	if strings.TrimSpace(folder) == "" || strings.TrimSpace(group) == "" || len(folder) > 100 || len(group) > 100 {
		return fmt.Errorf("%w: folder UID and group name must be 1–100 bytes", ErrInvalidAlertGroup)
	}
	if interval < 5 || interval > 86400 {
		return fmt.Errorf("%w: group interval must be 5–86400 seconds", ErrInvalidAlertGroup)
	}
	return nil
}

func SortAlertGroup(rules []model.AlertRuleView) {
	slices.SortFunc(rules, func(a, b model.AlertRuleView) int {
		if a.GroupIndex != b.GroupIndex {
			return a.GroupIndex - b.GroupIndex
		}
		return strings.Compare(a.UID, b.UID)
	})
}

// Compare native evaluation fields and query models, ignoring duplicate Grafana
// metadata and server timestamps. A GET/PUT round trip must not reset timers.
func comparableAlertRule(rule model.AlertRule, evaluation bool) []byte {
	rule.Version, rule.UpdatedAt = 0, 0
	if evaluation {
		rule.FolderUID, rule.Group, rule.GroupIndex, rule.IntervalSeconds, rule.Provenance = "", "", 0, 0, nil
	}
	if len(rule.Grafana) > 0 {
		var graph map[string]json.RawMessage
		if json.Unmarshal(rule.Grafana, &graph) == nil {
			for _, key := range []string{"id", "uid", "title", "orgID", "folderUID", "ruleGroup", "for", "keepFiringFor", "noDataState", "execErrState", "annotations", "labels", "isPaused", "updated", "provenance"} {
				delete(graph, key)
			}
			rule.Grafana, _ = json.Marshal(graph)
		}
	}
	raw, _ := json.Marshal(rule)
	var object any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&object)
	raw, _ = json.Marshal(object)
	return raw
}

// Nil rules update the interval only. An explicit empty slice deletes the group.
// Config, schedules, state, transition history and annotations share one commit.
func (s *Store) ReplaceAlertRuleGroup(ctx context.Context, folder, group string, interval int, rules []model.AlertRule, provenance string) ([]model.AlertRuleView, error) {
	if err := ValidateAlertGroup(folder, group, interval); err != nil {
		return nil, err
	}
	if len(rules) > 1000 || (provenance != "" && provenance != "api") {
		return nil, fmt.Errorf("%w: group allows at most 1000 rules and empty/api provenance", ErrInvalidAlertGroup)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT config,runtime FROM alert_rules ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	all := map[string]model.AlertRuleView{}
	current := []model.AlertRuleView{}
	for rows.Next() {
		view, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all[view.UID] = view
		if view.FolderUID == folder && view.Group == group {
			current = append(current, view)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	SortAlertGroup(current)
	if rules == nil {
		rules = make([]model.AlertRule, len(current))
		for i, view := range current {
			rules[i] = view.AlertRule
		}
	} else {
		rules = slices.Clone(rules)
	}
	seen, titles := map[string]bool{}, map[string]bool{}
	for i := range rules {
		rule := &rules[i]
		rule.FolderUID, rule.Group, rule.IntervalSeconds, rule.GroupIndex, rule.Provenance = folder, group, interval, i, &provenance
		rule.Defaults()
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("%w: rule %d: %v", ErrInvalidAlertGroup, i, err)
		}
		if seen[rule.UID] || titles[rule.Title] {
			return nil, fmt.Errorf("%w: duplicate UIDs or titles", ErrInvalidAlertGroup)
		}
		seen[rule.UID], titles[rule.Title] = true, true
	}
	// Rule titles are unique within a folder, including rules moved from other
	// groups. Rules omitted from the replaced group are about to be removed.
	for uid, previous := range all {
		if !seen[uid] && previous.FolderUID == folder && previous.Group != group && titles[previous.Title] {
			return nil, fmt.Errorf("%w: alert title already exists in this folder", ErrInvalidAlertGroup)
		}
	}
	// Delete first so replacements can reuse the global quota and rule titles.
	for _, previous := range current {
		if !seen[previous.UID] {
			if err := deleteAlertRuleTx(ctx, tx, previous.UID); err != nil {
				return nil, err
			}
		}
	}
	out := make([]model.AlertRuleView, 0, len(rules))
	for _, rule := range rules {
		previous, exists := all[rule.UID]
		rule.Version, rule.UpdatedAt = previous.Version, previous.UpdatedAt
		if exists && bytes.Equal(comparableAlertRule(rule, false), comparableAlertRule(previous.AlertRule, false)) {
			out = append(out, previous)
			continue
		}
		preserve := exists && bytes.Equal(comparableAlertRule(rule, true), comparableAlertRule(previous.AlertRule, true))
		view, err := saveAlertRuleTx(ctx, tx, rule, preserve)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	// Moving an existing UID also normalizes the remaining source-group order.
	type key struct{ folder, group string }
	affected := map[key]bool{}
	for _, rule := range rules {
		if previous, exists := all[rule.UID]; exists && (previous.FolderUID != folder || previous.Group != group) {
			affected[key{previous.FolderUID, previous.Group}] = true
		}
	}
	for source := range affected {
		remaining := []model.AlertRuleView{}
		for uid, previous := range all {
			if !seen[uid] && previous.FolderUID == source.folder && previous.Group == source.group {
				remaining = append(remaining, previous)
			}
		}
		SortAlertGroup(remaining)
		for i, view := range remaining {
			if view.GroupIndex != i {
				view.GroupIndex = i
				if _, err := saveAlertRuleTx(ctx, tx, view.AlertRule, true); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := pruneAlertHistory(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
