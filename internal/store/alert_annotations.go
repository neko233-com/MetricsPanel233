package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func boundedAnnotationText(text string, size int) string {
	if len(text) <= size {
		return text
	}
	text = text[:size]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func annotationState(state, reason string) string {
	if state == "Firing" {
		state = "Alerting"
	}
	switch reason {
	case "QueryError":
		reason = "Error"
	case "RuleUpdated":
		reason = "Updated"
	case "RuleDeleted":
		reason = "Deleted"
	}
	if reason != "" {
		state += " (" + reason + ")"
	}
	return state
}

// Persist a Grafana-compatible point event without an independent async write:
// state, history, tags and annotations either commit together or all roll back.
func insertAlertAnnotation(ctx context.Context, tx *sql.Tx, rule model.AlertRule, event model.AlertEvent, eventID int64) error {
	if rule.Record != "" {
		return nil
	}
	labels := map[string]string{}
	keys := []string{}
	for key, value := range event.Labels {
		if strings.HasPrefix(key, "__") || strings.HasSuffix(key, "__") {
			continue
		}
		labels[key] = value
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags, textLabels := []string{}, []string{}
	encodedLength := 2
	for _, key := range keys {
		textLabels = append(textLabels, key+"="+strconv.Quote(labels[key]))
		name, value := strings.ReplaceAll(key, ":", "_"), strings.ReplaceAll(labels[key], ":", "_")
		tag := name + ":" + value
		encoded, _ := json.Marshal(tag)
		if utf8.RuneCountInString(name) > 100 || utf8.RuneCountInString(value) > 100 || len(tag) > 256 || len(tags) >= 32 || encodedLength+len(encoded)+1 > 4096 {
			continue
		}
		tags = append(tags, tag)
		encodedLength += len(encoded) + 1
	}
	data := map[string]any{"labels": labels, "ruleUID": rule.UID, "instance": event.Key}
	if event.Reason != "" {
		data["reason"] = event.Reason
	}
	valueText := ""
	if event.To == "Error" || event.Reason == "QueryError" {
		data["error"] = boundedAnnotationText(event.Error, 4096)
		valueText = "Error"
	} else if event.To == "NoData" || event.Reason == "NoData" {
		data["noData"] = true
		valueText = "No data"
	} else if event.Value != nil {
		refID := "A"
		if len(rule.Grafana) > 0 {
			var graph struct {
				Condition string `json:"condition"`
			}
			if json.Unmarshal(rule.Grafana, &graph) == nil && graph.Condition != "" {
				refID = graph.Condition
			}
		}
		data["values"] = map[string]float64{refID: *event.Value}
		valueText = fmt.Sprintf("%s=%f", refID, *event.Value)
	} else if event.ValueText != "" {
		data["valueText"] = event.ValueText
		valueText = event.ValueText
	} else {
		data["values"] = nil
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	a := model.Annotation{Time: event.Timestamp, Text: boundedAnnotationText(fmt.Sprintf("%s {%s} - %s", rule.Title, strings.Join(textLabels, ", "), valueText), 8192), Tags: tags, Data: payload, AlertUID: rule.UID, AlertName: rule.Title, PrevState: annotationState(event.From, event.PrevReason), NewState: annotationState(event.To, event.Reason)}
	uid, panel := rule.Annotations["__dashboardUid__"], rule.Annotations["__panelId__"]
	// Older rule payloads might contain incomplete links; retain their global history.
	if number, err := strconv.ParseInt(panel, 10, 64); uid != "" && len(uid) <= 128 && err == nil && number > 0 && number <= 9007199254740991 {
		a.DashboardUID, a.PanelID = uid, number
		a.DashboardID = int64(crc32.ChecksumIEEE([]byte(uid)))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_rule_identity(uid) VALUES(?) ON CONFLICT(uid) DO NOTHING`, rule.UID); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM alert_rule_identity WHERE uid=?`, rule.UID).Scan(&a.AlertID); err != nil {
		return err
	}
	if err = a.Normalize(); err != nil {
		return err
	}
	a, err = insertAnnotation(ctx, tx, a, "", "")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO annotation_alerts(annotation_id,event_id,rule_uid,rule_id) VALUES(?,?,?,?)`, a.ID, eventID, rule.UID, a.AlertID)
	return err
}
