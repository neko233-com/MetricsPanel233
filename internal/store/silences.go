package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) initSilences() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_silences(id TEXT PRIMARY KEY,starts_at INTEGER NOT NULL,ends_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS alert_silences_time ON alert_silences(ends_at,starts_at);
CREATE INDEX IF NOT EXISTS alert_silences_start ON alert_silences(starts_at,ends_at);
CREATE TABLE IF NOT EXISTS alert_notification_silence_scan(id INTEGER PRIMARY KEY CHECK(id=1),last_id INTEGER NOT NULL);
INSERT OR IGNORE INTO alert_notification_silence_scan(id,last_id) VALUES(1,0);`)
	return err
}

func scanSilence(row interface{ Scan(...any) error }, at time.Time) (model.Silence, error) {
	var silence model.Silence
	var raw string
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &silence)
		silence.At(at)
	}
	return silence, err
}

func (s *Store) Silences(ctx context.Context, at time.Time) ([]model.Silence, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT payload FROM alert_silences ORDER BY ends_at DESC,id LIMIT 10000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Silence{}
	for rows.Next() {
		v, err := scanSilence(rows, at)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Silence(ctx context.Context, id string, at time.Time) (model.Silence, error) {
	return scanSilence(s.DB.QueryRowContext(ctx, `SELECT payload FROM alert_silences WHERE id=?`, id), at)
}

func activeSilencesTx(ctx context.Context, tx *sql.Tx, at int64) ([]model.Silence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM alert_silences WHERE starts_at<=? AND ends_at>?`, at, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Silence{}
	for rows.Next() {
		v, err := scanSilence(rows, time.UnixMilli(at))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func silenceUntil(silences []model.Silence, labels map[string]string) int64 {
	until := int64(0)
	for _, silence := range silences {
		if model.SilenceMatches(silence.Matchers, labels) && (until == 0 || silence.EndsAt.UnixMilli() < until) {
			until = silence.EndsAt.UnixMilli()
		}
	}
	return until
}

func (s *Store) SaveSilence(ctx context.Context, silence model.Silence, at time.Time) (model.Silence, error) {
	if err := silence.Validate(); err != nil {
		return silence, err
	}
	if silence.ID != "" {
		if _, err := uuid.Parse(silence.ID); err != nil {
			return silence, errors.New("invalid silence ID")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return silence, err
	}
	defer tx.Rollback()
	if silence.ID == "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_silences WHERE ends_at>?`, at.UnixMilli()).Scan(&count); err != nil {
			return silence, err
		}
		if count >= 1000 {
			return silence, errors.New("at most 1000 active or pending silences")
		}
		silence.ID = uuid.NewString()
	} else {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_silences WHERE id=?`, silence.ID).Scan(&exists); err != nil {
			return silence, err
		}
		if exists != 1 {
			return silence, sql.ErrNoRows
		}
	}
	silence.StartsAt, silence.EndsAt, silence.UpdatedAt = silence.StartsAt.UTC(), silence.EndsAt.UTC(), at.UTC()
	for i := range silence.Matchers {
		if silence.Matchers[i].IsEqual == nil {
			value := true
			silence.Matchers[i].IsEqual = &value
		}
	}
	if silence.Annotations == nil {
		silence.Annotations = map[string]string{}
	}
	silence.At(at)
	raw, err := json.Marshal(silence)
	if err != nil {
		return silence, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_silences(id,starts_at,ends_at,updated_at,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET starts_at=excluded.starts_at,ends_at=excluded.ends_at,updated_at=excluded.updated_at,payload=excluded.payload`, silence.ID, silence.StartsAt.UnixMilli(), silence.EndsAt.UnixMilli(), at.UnixMilli(), string(raw))
	if err != nil {
		return silence, err
	}
	// Editing/expiring a silence wakes suppressed records immediately. The
	// dispatcher rechecks every overlapping silence before releasing them.
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET next_attempt_at=? WHERE state='suppressed'`, at.UnixMilli()); err != nil {
		return silence, err
	}
	if err := wakeNotificationGroupsTx(ctx, tx, at.UnixMilli()); err != nil {
		return silence, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_silences WHERE id IN (SELECT id FROM alert_silences WHERE ends_at<=? ORDER BY updated_at DESC LIMIT -1 OFFSET 10000)`, at.UnixMilli()); err != nil {
		return silence, err
	}
	return silence, tx.Commit()
}

func (s *Store) ExpireSilence(ctx context.Context, id string, at time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	silence, err := scanSilence(tx.QueryRowContext(ctx, `SELECT payload FROM alert_silences WHERE id=?`, id), at)
	if err != nil {
		return err
	}
	if silence.EndsAt.After(at) {
		silence.EndsAt = at.UTC()
		if silence.StartsAt.After(at) {
			silence.StartsAt = at.UTC()
		}
	}
	silence.UpdatedAt = at.UTC()
	silence.At(at)
	raw, _ := json.Marshal(silence)
	if _, err := tx.ExecContext(ctx, `UPDATE alert_silences SET starts_at=?,ends_at=?,updated_at=?,payload=? WHERE id=?`, silence.StartsAt.UnixMilli(), silence.EndsAt.UnixMilli(), at.UnixMilli(), string(raw), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET next_attempt_at=? WHERE state='suppressed'`, at.UnixMilli()); err != nil {
		return err
	}
	if err := wakeNotificationGroupsTx(ctx, tx, at.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func notificationLabels(raw string) (map[string]string, error) {
	var payload struct {
		CommonLabels map[string]string `json:"commonLabels"`
	}
	err := json.Unmarshal([]byte(raw), &payload)
	return payload.CommonLabels, err
}

func reconcileSuppressedNotificationsTx(ctx context.Context, tx *sql.Tx, at int64, silences []model.Silence) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,rule_uid,instance_key,payload FROM alert_notification_outbox WHERE state='suppressed' AND next_attempt_at<=? ORDER BY next_attempt_at,id LIMIT 128`, at)
	if err != nil {
		return err
	}
	type item struct {
		id             int64
		rule, key, raw string
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.rule, &v.key, &v.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_active WHERE rule_uid=? AND instance_key=?`, v.rule, v.key).Scan(&active); err != nil {
			return err
		}
		state, message, next := "pending", "", at
		if active == 0 {
			state, message, next = "canceled", "Resolved while silenced", 0
		} else {
			labels, err := notificationLabels(v.raw)
			if err != nil {
				return err
			}
			if until := silenceUntil(silences, labels); until > 0 {
				state, message, next = "suppressed", "Silenced", until
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state=?,last_error=?,next_attempt_at=? WHERE id=? AND state='suppressed'`, state, message, next, v.id); err != nil {
			return err
		}
	}
	return nil
}

// A silence can begin after an alert's first notification was already sent.
// Retain a single resume record for that active period; overlapping silences
// share it. A persistent cursor bounds each scan without starving later alerts.
func enqueueSilenceResumesTx(ctx context.Context, tx *sql.Tx, at int64, silences []model.Silence) error {
	if len(silences) == 0 {
		return nil
	}
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT last_id FROM alert_notification_silence_scan WHERE id=1`).Scan(&cursor); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.contact_point_uid,n.rule_uid,n.instance_key,n.payload FROM alert_notification_outbox n JOIN alert_notification_active a ON a.rule_uid=n.rule_uid AND a.instance_key=n.instance_key WHERE n.id>? AND n.status='firing' AND n.state='sent' AND n.created_at>=a.started_at AND NOT EXISTS(SELECT 1 FROM alert_notification_group_deliveries gd WHERE gd.notification_id=n.id) AND NOT EXISTS(SELECT 1 FROM alert_notification_outbox later WHERE later.contact_point_uid=n.contact_point_uid AND later.rule_uid=n.rule_uid AND later.instance_key=n.instance_key AND later.id>n.id AND later.status='firing') ORDER BY n.id LIMIT 128`, cursor)
	if err != nil {
		return err
	}
	type candidate struct {
		id                  int64
		uid, rule, key, raw string
	}
	items := []candidate{}
	for rows.Next() {
		var v candidate
		if err := rows.Scan(&v.id, &v.uid, &v.rule, &v.key, &v.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	policy, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, item := range items {
		cursor = item.id
		labels, err := notificationLabels(item.raw)
		if err != nil {
			return err
		}
		until := silenceUntil(silences, labels)
		if until == 0 {
			continue
		}
		point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, item.uid))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if point.Disabled {
			continue
		}
		rule, err := scanRule(tx.QueryRowContext(ctx, `SELECT config,runtime FROM alert_rules WHERE uid=?`, item.rule))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		receivers := policy.Receivers(labels)
		if rule.NotificationReceiver != "" {
			receivers = []string{rule.NotificationReceiver}
		}
		selected := false
		for _, uid := range receivers {
			if uid == item.uid {
				selected = true
			}
		}
		if !selected {
			continue
		}
		id, err := insertNotificationTx(ctx, tx, 0, item.uid, item.rule, item.key, "firing", json.RawMessage(item.raw), at)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='suppressed',last_error='Silenced',next_attempt_at=? WHERE id=?`, until, id); err != nil {
			return err
		}
	}
	if len(items) < 128 {
		cursor = 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_silence_scan SET last_id=? WHERE id=1`, cursor)
	return err
}
