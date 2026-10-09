package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrRuleConflict = errors.New("rule version changed; read the current rule before updating")
var ErrStaleEvaluation = errors.New("rule changed or a newer evaluation was already committed")

func scanRule(row interface{ Scan(...any) error }) (model.AlertRuleView, error) {
	var v model.AlertRuleView
	var rule, runtime string
	if err := row.Scan(&rule, &runtime); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(rule), &v.AlertRule); err != nil {
		return v, err
	}
	err := json.Unmarshal([]byte(runtime), &v.Runtime)
	return v, err
}
func (s *Store) AlertRules(ctx context.Context) ([]model.AlertRuleView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config,runtime FROM alert_rules ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AlertRuleView{}
	for rows.Next() {
		v, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AlertRule(ctx context.Context, uid string) (model.AlertRuleView, error) {
	return scanRule(s.DB.QueryRowContext(ctx, `SELECT config,runtime FROM alert_rules WHERE uid=?`, uid))
}

// Scheduling reads only small indexed control fields, never the persisted query
// graphs or instance payloads. Oldest evaluations go first to prevent starvation.
func (s *Store) DueAlertRules(ctx context.Context, at int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.uid FROM alert_rules r JOIN alert_schedule s ON s.uid=r.uid WHERE s.paused=0 AND r.last_evaluation<=?-s.interval_seconds*1000 ORDER BY r.last_evaluation,r.uid`, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// SaveAlertRule uses optimistic concurrency. A zero version creates only; an
// update must carry the version returned by GET, including for pause changes.
func (s *Store) SaveAlertRule(ctx context.Context, rule model.AlertRule) (model.AlertRuleView, error) {
	rule.Defaults()
	if err := rule.Validate(); err != nil {
		return model.AlertRuleView{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.AlertRuleView{}, err
	}
	defer tx.Rollback()
	previous, err := scanRule(tx.QueryRowContext(ctx, `SELECT config,runtime FROM alert_rules WHERE uid=?`, rule.UID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.AlertRuleView{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if rule.Version != 0 {
			return model.AlertRuleView{}, ErrRuleConflict
		}
	} else if previous.Version != rule.Version || rule.Version == 0 {
		return model.AlertRuleView{}, ErrRuleConflict
	}
	if rule.Version == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_rules`).Scan(&count); err != nil {
			return model.AlertRuleView{}, err
		}
		if count >= 1000 {
			return model.AlertRuleView{}, errors.New("at most 1000 alert/recording rules")
		}
	}
	rule.Version++
	rule.UpdatedAt = time.Now().UnixMilli()
	reason := "RuleUpdated"
	health := "unknown"
	if rule.Paused {
		reason = "Paused"
		health = "paused"
	}
	for _, instance := range previous.Runtime.Instances {
		if instance.State != "Normal" {
			if err := insertAlertEvent(ctx, tx, model.AlertEvent{UID: rule.UID, Key: instance.Key, Labels: instance.Labels, From: instance.State, To: "Normal", Timestamp: rule.UpdatedAt, Reason: reason}); err != nil {
				return model.AlertRuleView{}, err
			}
		}
	}
	runtime := model.AlertRuntime{Health: health, Instances: []model.AlertInstance{}}
	config, _ := json.Marshal(rule)
	state, _ := json.Marshal(runtime)
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_rules(uid,version,config,runtime,last_evaluation) VALUES(?,?,?,?,0) ON CONFLICT(uid) DO UPDATE SET version=excluded.version,config=excluded.config,runtime=excluded.runtime,last_evaluation=0`, rule.UID, rule.Version, string(config), string(state))
	if err != nil {
		return model.AlertRuleView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_schedule(uid,interval_seconds,paused) VALUES(?,?,?) ON CONFLICT(uid) DO UPDATE SET interval_seconds=excluded.interval_seconds,paused=excluded.paused`, rule.UID, rule.IntervalSeconds, rule.Paused); err != nil {
		return model.AlertRuleView{}, err
	}
	if err = pruneAlertHistory(ctx, tx); err != nil {
		return model.AlertRuleView{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.AlertRuleView{}, err
	}
	return model.AlertRuleView{AlertRule: rule, Runtime: runtime}, nil
}
func insertAlertEvent(ctx context.Context, tx *sql.Tx, event model.AlertEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_events(uid,timestamp,payload) VALUES(?,?,?)`, event.UID, event.Timestamp, string(payload))
	return err
}
func pruneAlertHistory(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM alert_events WHERE id<=(SELECT coalesce(max(id),0)-100000 FROM alert_events)`)
	return err
}
func (s *Store) CommitAlertEvaluation(ctx context.Context, uid string, version int, runtime model.AlertRuntime, events []model.AlertEvent) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload, err := json.Marshal(runtime)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE alert_rules SET runtime=?,last_evaluation=? WHERE uid=? AND version=? AND last_evaluation<?`, string(payload), runtime.LastEvaluation, uid, version, runtime.LastEvaluation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrStaleEvaluation
	}
	for _, event := range events {
		if err = insertAlertEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	// Bound the durable history without deleting the current rule state.
	if len(events) > 0 {
		if err = pruneAlertHistory(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) DeleteAlertRule(ctx context.Context, uid string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := scanRule(tx.QueryRowContext(ctx, `SELECT config,runtime FROM alert_rules WHERE uid=?`, uid))
	if err != nil {
		return err
	}
	for _, instance := range previous.Runtime.Instances {
		if instance.State != "Normal" {
			if err := insertAlertEvent(ctx, tx, model.AlertEvent{UID: uid, Key: instance.Key, Labels: instance.Labels, From: instance.State, To: "Normal", Timestamp: time.Now().UnixMilli(), Reason: "RuleDeleted"}); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM alert_rules WHERE uid=?`, uid); err != nil {
		return err
	}
	if err = pruneAlertHistory(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AlertHistory(ctx context.Context, uid string, limit int) ([]model.AlertEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("history limit must be 1–1000")
	}
	query := `SELECT id,payload FROM alert_events`
	args := []any{}
	if uid != "" {
		query += ` WHERE uid=?`
		args = append(args, uid)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AlertEvent{}
	for rows.Next() {
		var v model.AlertEvent
		var payload string
		var id int64
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			return nil, err
		}
		v.ID = id
		out = append(out, v)
	}
	return out, rows.Err()
}
