package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) initNotificationGroups() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_notification_groups(group_key TEXT PRIMARY KEY,receiver TEXT NOT NULL,options TEXT NOT NULL,group_labels TEXT NOT NULL,created_at INTEGER NOT NULL,last_sent_at INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL,dirty INTEGER NOT NULL DEFAULT 1,generation INTEGER NOT NULL DEFAULT 1,inflight_id INTEGER NOT NULL DEFAULT 0,last_hash TEXT NOT NULL DEFAULT '',resume_needed INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS alert_notification_groups_due ON alert_notification_groups(inflight_id,next_at);
CREATE TABLE IF NOT EXISTS alert_notification_group_members(group_key TEXT NOT NULL REFERENCES alert_notification_groups(group_key) ON DELETE CASCADE,rule_uid TEXT NOT NULL,instance_key TEXT NOT NULL,started_at INTEGER NOT NULL,status TEXT NOT NULL,payload TEXT NOT NULL,notified INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(group_key,rule_uid,instance_key));
CREATE INDEX IF NOT EXISTS alert_notification_group_instance ON alert_notification_group_members(rule_uid,instance_key);
CREATE TABLE IF NOT EXISTS alert_notification_group_deliveries(notification_id INTEGER PRIMARY KEY REFERENCES alert_notification_outbox(id) ON DELETE CASCADE,group_key TEXT NOT NULL,generation INTEGER NOT NULL,members TEXT NOT NULL,payload_hash TEXT NOT NULL,muted_until INTEGER NOT NULL DEFAULT 0);`)
	return err
}

type notificationGroup struct {
	Key, Receiver, Options, Labels, LastHash              string
	CreatedAt, LastSentAt, NextAt, Generation, InflightID int64
	Dirty, ResumeNeeded                                   bool
}

func scanNotificationGroup(row interface{ Scan(...any) error }) (notificationGroup, error) {
	var g notificationGroup
	err := row.Scan(&g.Key, &g.Receiver, &g.Options, &g.Labels, &g.CreatedAt, &g.LastSentAt, &g.NextAt, &g.Dirty, &g.Generation, &g.InflightID, &g.LastHash, &g.ResumeNeeded)
	return g, err
}

const groupColumns = `group_key,receiver,options,group_labels,created_at,last_sent_at,next_at,dirty,generation,inflight_id,last_hash,resume_needed`

type groupMember struct {
	Rule      string `json:"rule"`
	Key       string `json:"key"`
	StartedAt int64  `json:"started_at"`
	Status    string `json:"status"`
	Raw       string `json:"-"`
	Notified  bool   `json:"-"`
}

func enqueueNotificationGroupTx(ctx context.Context, tx *sql.Tx, opts model.NotificationRouteOptions, rule model.AlertRule, event model.AlertEvent, startedAt int64, payload any) error {
	key := opts.GroupKey(event.Labels)
	if key == "" {
		return errors.New("notification grouping requires explicit options")
	}
	optsJSON, _ := json.Marshal(opts)
	labelsJSON, _ := json.Marshal(opts.GroupLabels(event.Labels))
	wait, _ := model.NotificationDuration(opts.GroupWait)
	g, err := scanNotificationGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM alert_notification_groups WHERE group_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_notification_groups(group_key,receiver,options,group_labels,created_at,next_at) VALUES(?,?,?,?,?,?)`, key, opts.Receiver, string(optsJSON), string(labelsJSON), event.Timestamp, event.Timestamp+wait.Milliseconds()); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		next := g.NextAt
		interval, _ := model.NotificationDuration(opts.GroupInterval)
		if !g.Dirty {
			next = max(event.Timestamp, g.LastSentAt+interval.Milliseconds())
			if g.LastSentAt == 0 {
				next = event.Timestamp + wait.Milliseconds()
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET options=?,group_labels=?,dirty=1,generation=generation+1,next_at=? WHERE group_key=?`, string(optsJSON), string(labelsJSON), next, key); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_notification_group_members(group_key,rule_uid,instance_key,started_at,status,payload) VALUES(?,?,?,?, 'firing',?) ON CONFLICT(group_key,rule_uid,instance_key) DO UPDATE SET started_at=excluded.started_at,status='firing',payload=excluded.payload,notified=CASE WHEN started_at=excluded.started_at THEN notified ELSE 0 END`, key, rule.UID, event.Key, startedAt, string(raw))
	return err
}

func resolveNotificationGroupsTx(ctx context.Context, tx *sql.Tx, rule model.AlertRule, event model.AlertEvent, startedAt int64, silences []model.Silence) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.`+strings.ReplaceAll(groupColumns, ",", ",g.")+` FROM alert_notification_groups g JOIN alert_notification_group_members m ON m.group_key=g.group_key WHERE m.rule_uid=? AND m.instance_key=? AND m.started_at=?`, rule.UID, event.Key, startedAt)
	if err != nil {
		return err
	}
	groups := []notificationGroup{}
	for rows.Next() {
		g, err := scanNotificationGroup(rows)
		if err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	timings, err := muteTimingsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		var opts model.NotificationRouteOptions
		if err := json.Unmarshal([]byte(g.Options), &opts); err != nil {
			return err
		}
		muted, err := routeTimingMuted(opts, timings, time.UnixMilli(event.Timestamp))
		if err != nil {
			return err
		}
		muted = muted || silenceUntil(silences, event.Labels) > 0
		if muted {
			if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=? AND started_at=?`, g.Key, rule.UID, event.Key, startedAt); err != nil {
				return err
			}
		} else {
			point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, g.Receiver))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if point.DisableResolveMessage {
				if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=?`, g.Key, rule.UID, event.Key); err != nil {
					return err
				}
			} else {
				raw, _ := json.Marshal(webhookPayload(point, rule, event, "resolved", startedAt))
				if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_group_members SET status='resolved',payload=? WHERE group_key=? AND rule_uid=? AND instance_key=?`, string(raw), g.Key, rule.UID, event.Key); err != nil {
					return err
				}
			}
		}
		interval, _ := model.NotificationDuration(opts.GroupInterval)
		next := g.NextAt
		if !g.Dirty {
			next = max(event.Timestamp, g.LastSentAt+interval.Milliseconds())
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET dirty=1,generation=generation+1,next_at=?,resume_needed=CASE WHEN ? THEN 0 ELSE resume_needed END WHERE group_key=?`, next, muted, g.Key); err != nil {
			return err
		}
	}
	return nil
}

func groupMembersTx(ctx context.Context, tx *sql.Tx, key string) ([]groupMember, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rule_uid,instance_key,started_at,status,payload,notified FROM alert_notification_group_members WHERE group_key=? ORDER BY rule_uid,instance_key`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []groupMember{}
	for rows.Next() {
		var v groupMember
		if err := rows.Scan(&v.Rule, &v.Key, &v.StartedAt, &v.Status, &v.Raw, &v.Notified); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func effectiveRepeat(opts model.NotificationRouteOptions) time.Duration {
	repeat, _ := model.NotificationDuration(opts.RepeatInterval)
	interval, _ := model.NotificationDuration(opts.GroupInterval)
	if repeat > 0 && interval > 0 {
		repeat = ((repeat + interval - 1) / interval) * interval
	}
	return repeat
}

func nextTimingCheck(opts model.NotificationRouteOptions, at int64) int64 {
	if len(opts.MuteTimeIntervals) == 0 && len(opts.ActiveTimeIntervals) == 0 {
		return 0
	}
	return time.UnixMilli(at).Truncate(time.Minute).Add(time.Minute).UnixMilli()
}
func earlierCheck(a, b int64) int64 {
	if a == 0 || b > 0 && b < a {
		return b
	}
	return a
}

func nextPendingSilenceTx(ctx context.Context, tx *sql.Tx, at int64) (int64, error) {
	var next int64
	err := tx.QueryRowContext(ctx, `SELECT coalesce(min(starts_at),0) FROM alert_silences WHERE starts_at>? AND ends_at>starts_at`, at).Scan(&next)
	return next, err
}

func payloadForGroup(g notificationGroup, members []groupMember) (map[string]any, string, error) {
	alerts := []any{}
	commonLabels := map[string]string{}
	commonAnnotations := map[string]string{}
	status := "resolved"
	fingerprints := []string{}
	for index, member := range members {
		var raw struct {
			CommonLabels      map[string]string `json:"commonLabels"`
			CommonAnnotations map[string]string `json:"commonAnnotations"`
			Alerts            []json.RawMessage `json:"alerts"`
		}
		if err := json.Unmarshal([]byte(member.Raw), &raw); err != nil {
			return nil, "", err
		}
		for _, alert := range raw.Alerts {
			var value any
			if err := json.Unmarshal(alert, &value); err != nil {
				return nil, "", err
			}
			alerts = append(alerts, value)
		}
		if member.Status == "firing" {
			status = "firing"
		}
		if index == 0 {
			for key, value := range raw.CommonLabels {
				commonLabels[key] = value
			}
			for key, value := range raw.CommonAnnotations {
				commonAnnotations[key] = value
			}
		} else {
			for key, value := range commonLabels {
				if other, ok := raw.CommonLabels[key]; !ok || other != value {
					delete(commonLabels, key)
				}
			}
			for key, value := range commonAnnotations {
				if other, ok := raw.CommonAnnotations[key]; !ok || other != value {
					delete(commonAnnotations, key)
				}
			}
		}
		fingerprints = append(fingerprints, fmt.Sprintf("%s/%s/%d/%s", member.Rule, member.Key, member.StartedAt, member.Status))
	}
	var groupLabels map[string]string
	if err := json.Unmarshal([]byte(g.Labels), &groupLabels); err != nil {
		return nil, "", err
	}
	sort.Strings(fingerprints)
	sum := sha256.Sum256([]byte(strings.Join(fingerprints, "\n")))
	hash := hex.EncodeToString(sum[:])
	state := "ok"
	if status == "firing" {
		state = "alerting"
	}
	return map[string]any{"receiver": g.Receiver, "status": status, "version": "1", "orgId": 1, "groupKey": g.Key, "groupLabels": groupLabels, "commonLabels": commonLabels, "commonAnnotations": commonAnnotations, "externalURL": "", "truncatedAlerts": 0, "alerts": alerts, "title": fmt.Sprintf("[%s:%d] %s", strings.ToUpper(status), len(alerts), commonLabels["alertname"]), "message": commonAnnotations["summary"], "state": state}, hash, nil
}

func reconcileNotificationGroupsTx(ctx context.Context, tx *sql.Tx, at int64, silences []model.Silence) error {
	// Repair a lease's bookkeeping if a final attempt was interrupted.
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=0,next_at=CASE WHEN dirty=1 THEN ? ELSE 0 END WHERE inflight_id IN (SELECT id FROM alert_notification_outbox WHERE state IN ('failed','canceled'))`, at); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+groupColumns+` FROM alert_notification_groups WHERE inflight_id=0 AND next_at>0 AND next_at<=? ORDER BY next_at,group_key LIMIT 64`, at)
	if err != nil {
		return err
	}
	groups := []notificationGroup{}
	for rows.Next() {
		g, err := scanNotificationGroup(rows)
		if err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	timings, err := muteTimingsTx(ctx, tx)
	if err != nil {
		return err
	}
	pendingSilence, err := nextPendingSilenceTx(ctx, tx, at)
	if err != nil {
		return err
	}
	for _, g := range groups {
		var opts model.NotificationRouteOptions
		if err := json.Unmarshal([]byte(g.Options), &opts); err != nil {
			return err
		}
		point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, g.Receiver))
		if errors.Is(err, sql.ErrNoRows) || err == nil && point.Disabled {
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET dirty=0,next_at=0 WHERE group_key=?`, g.Key); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		all, err := groupMembersTx(ctx, tx, g.Key)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_groups WHERE group_key=? AND inflight_id=0`, g.Key); err != nil {
				return err
			}
			continue
		}
		visible := []groupMember{}
		mutedUntil := int64(0)
		timingMuted, err := routeTimingMuted(opts, timings, time.UnixMilli(at))
		if err != nil {
			return err
		}
		for _, member := range all {
			if member.Status == "resolved" && !member.Notified {
				if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=? AND started_at=?`, g.Key, member.Rule, member.Key, member.StartedAt); err != nil {
					return err
				}
				continue
			}
			if member.Status == "firing" {
				var active int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_active WHERE rule_uid=? AND instance_key=? AND started_at=?`, member.Rule, member.Key, member.StartedAt).Scan(&active); err != nil {
					return err
				}
				if active == 0 {
					if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=?`, g.Key, member.Rule, member.Key); err != nil {
						return err
					}
					continue
				}
				valid, raw, err := currentGroupMemberTx(ctx, tx, g, member, at)
				if err != nil {
					return err
				}
				if !valid {
					if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=?`, g.Key, member.Rule, member.Key); err != nil {
						return err
					}
					continue
				}
				member.Raw = raw
			}
			labels, err := notificationLabels(member.Raw)
			if err != nil {
				return err
			}
			until := silenceUntil(silences, labels)
			if timingMuted {
				until = time.UnixMilli(at).Truncate(time.Minute).Add(time.Minute).UnixMilli()
			}
			if until > 0 {
				if member.Status == "resolved" {
					if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=?`, g.Key, member.Rule, member.Key); err != nil {
						return err
					}
				} else if mutedUntil == 0 || until < mutedUntil {
					mutedUntil = until
				}
				continue
			}
			visible = append(visible, member)
		}
		if len(visible) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET next_at=?,dirty=?,resume_needed=? WHERE group_key=?`, mutedUntil, mutedUntil > 0, mutedUntil > 0, g.Key); err != nil {
				return err
			}
			if mutedUntil == 0 {
				if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_groups WHERE group_key=? AND NOT EXISTS(SELECT 1 FROM alert_notification_group_members m WHERE m.group_key=?)`, g.Key, g.Key); err != nil {
					return err
				}
			}
			continue
		}
		payload, hash, err := payloadForGroup(g, visible)
		if err != nil {
			return err
		}
		payload["receiver"] = point.Name
		repeat := effectiveRepeat(opts)
		repeatDue := repeat > 0 && g.LastSentAt > 0 && at >= g.LastSentAt+repeat.Milliseconds()
		if hash == g.LastHash && (!g.ResumeNeeded || mutedUntil > 0) && !repeatDue {
			next := earlierCheck(earlierCheck(mutedUntil, nextTimingCheck(opts, at)), pendingSilence)
			if repeat > 0 {
				nextRepeat := g.LastSentAt + repeat.Milliseconds()
				if next == 0 || nextRepeat < next {
					next = nextRepeat
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET dirty=0,next_at=?,resume_needed=? WHERE group_key=?`, next, mutedUntil > 0, g.Key); err != nil {
				return err
			}
			continue
		}
		interval, _ := model.NotificationDuration(opts.GroupInterval)
		if g.LastSentAt > 0 && at < g.LastSentAt+interval.Milliseconds() {
			next := g.LastSentAt + interval.Milliseconds()
			if mutedUntil > at && mutedUntil < next {
				next = mutedUntil
			}
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET next_at=?,resume_needed=? WHERE group_key=?`, next, g.ResumeNeeded || mutedUntil > 0, g.Key); err != nil {
				return err
			}
			continue
		}
		id, err := insertNotificationTx(ctx, tx, 0, g.Receiver, visible[0].Rule, visible[0].Key, payload["status"].(string), payload, at)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(visible)
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_notification_group_deliveries(notification_id,group_key,generation,members,payload_hash,muted_until) VALUES(?,?,?,?,?,?)`, id, g.Key, g.Generation, string(raw), hash, mutedUntil); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=?,dirty=0 WHERE group_key=?`, id, g.Key); err != nil {
			return err
		}
	}
	return nil
}

func finishGroupNotificationTx(ctx context.Context, tx *sql.Tx, id int64, state string, at int64) error {
	var key, raw, hash string
	var generation, mutedUntil int64
	err := tx.QueryRowContext(ctx, `SELECT group_key,generation,members,payload_hash,muted_until FROM alert_notification_group_deliveries WHERE notification_id=?`, id).Scan(&key, &generation, &raw, &hash, &mutedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "pending" {
		return nil
	}
	g, err := scanNotificationGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM alert_notification_groups WHERE group_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "sent" {
		dirty := g.Generation != generation
		next := int64(0)
		if dirty {
			next = at
		}
		_, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=0,dirty=?,next_at=? WHERE group_key=? AND inflight_id=?`, dirty, next, key, id)
		return err
	}
	var members []groupMember
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return err
	}
	for _, member := range members {
		if member.Status == "resolved" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=? AND started_at=? AND status='resolved'`, key, member.Rule, member.Key, member.StartedAt); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_group_members SET notified=1 WHERE group_key=? AND rule_uid=? AND instance_key=? AND started_at=?`, key, member.Rule, member.Key, member.StartedAt); err != nil {
				return err
			}
		}
	}
	var opts model.NotificationRouteOptions
	if err := json.Unmarshal([]byte(g.Options), &opts); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_group_members WHERE group_key=?`, key).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_groups WHERE group_key=? AND inflight_id=?`, key, id)
		return err
	}
	repeat := effectiveRepeat(opts)
	interval, _ := model.NotificationDuration(opts.GroupInterval)
	dirty := g.Generation != generation
	pendingSilence, err := nextPendingSilenceTx(ctx, tx, at)
	if err != nil {
		return err
	}
	next := earlierCheck(earlierCheck(mutedUntil, nextTimingCheck(opts, at)), pendingSilence)
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_group_members WHERE group_key=? AND status='firing'`, key).Scan(&active); err != nil {
		return err
	}
	if active > 0 && repeat > 0 {
		nextRepeat := at + repeat.Milliseconds()
		if next == 0 || nextRepeat < next {
			next = nextRepeat
		}
	}
	if dirty {
		next = max(at, at+interval.Milliseconds())
	}
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=0,last_sent_at=?,last_hash=?,dirty=?,next_at=?,resume_needed=? WHERE group_key=? AND inflight_id=?`, at, hash, dirty, next, mutedUntil > 0, key, id)
	return err
}

func validGroupNotificationTx(ctx context.Context, tx *sql.Tx, id, at int64, silences []model.Silence) (bool, bool, error) {
	var key, raw string
	err := tx.QueryRowContext(ctx, `SELECT group_key,members FROM alert_notification_group_deliveries WHERE notification_id=?`, id).Scan(&key, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return true, false, err
	}
	g, err := scanNotificationGroup(tx.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM alert_notification_groups WHERE group_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',last_error='Notification group removed' WHERE id=?`, id); err != nil {
			return true, false, err
		}
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	var opts model.NotificationRouteOptions
	if err := json.Unmarshal([]byte(g.Options), &opts); err != nil {
		return true, false, err
	}
	timings, err := muteTimingsTx(ctx, tx)
	if err != nil {
		return true, false, err
	}
	var members []groupMember
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return true, false, err
	}
	invalid := false
	for _, member := range members {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_active WHERE rule_uid=? AND instance_key=? AND started_at=?`, member.Rule, member.Key, member.StartedAt).Scan(&active); err != nil {
			return true, false, err
		}
		if member.Status == "firing" && active == 0 {
			invalid = true
		}
		if member.Status == "firing" {
			current, _, err := currentGroupMemberTx(ctx, tx, g, member, at)
			if err != nil {
				return true, false, err
			}
			if !current {
				invalid = true
			}
		}
		var payload string
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=? AND started_at=? AND status=?`, key, member.Rule, member.Key, member.StartedAt, member.Status).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
			invalid = true
		} else if err != nil {
			return true, false, err
		} else {
			labels, err := notificationLabels(payload)
			if err != nil {
				return true, false, err
			}
			if silenceUntil(silences, labels) > 0 {
				invalid = true
			}
		}
	}
	if !invalid {
		muted, err := routeTimingMuted(opts, timings, time.UnixMilli(at))
		if err != nil {
			return true, false, err
		}
		invalid = muted
	}
	if !invalid {
		return true, true, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',last_error='Group snapshot superseded by current alert or silence state',next_attempt_at=0 WHERE id=?`, id); err != nil {
		return true, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=0,dirty=1,generation=generation+1,next_at=? WHERE group_key=? AND inflight_id=?`, at, key, id)
	return true, false, err
}

// Policy changes populate new grouped routes from current state without making
// synthetic alert history transitions. Existing members retain delivery state.
func syncNotificationGroupsTx(ctx context.Context, tx *sql.Tx, p model.NotificationPolicy, at int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT config,runtime FROM alert_rules`)
	if err != nil {
		return err
	}
	views := []model.AlertRuleView{}
	for rows.Next() {
		v, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return err
		}
		views = append(views, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, view := range views {
		if view.Paused || view.Record != "" || view.NotificationReceiver != "" {
			continue
		}
		for _, instance := range view.Runtime.Instances {
			if !notificationActive(instance.State) {
				continue
			}
			started := instance.FiringAt
			if started == 0 {
				started = instance.ActiveAt
			}
			if started == 0 {
				started = at
			}
			err := tx.QueryRowContext(ctx, `SELECT started_at FROM alert_notification_active WHERE rule_uid=? AND instance_key=?`, view.UID, instance.Key).Scan(&started)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_active(rule_uid,instance_key,started_at) VALUES(?,?,?)`, view.UID, instance.Key, started); err != nil {
				return err
			}
			for _, opts := range p.MatchingRoutes(instance.Labels) {
				key := opts.GroupKey(instance.Labels)
				if key == "" || opts.Receiver == "" {
					continue
				}
				point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, opts.Receiver))
				if err != nil {
					return err
				}
				wanted[key+"/"+view.UID+"/"+instance.Key] = true
				event := model.AlertEvent{UID: view.UID, Key: instance.Key, Labels: instance.Labels, Annotations: instance.Annotations, Value: instance.Value, Timestamp: at, To: instance.State}
				if err := enqueueNotificationGroupTx(ctx, tx, opts, view.AlertRule, event, started, webhookPayload(point, view.AlertRule, event, "firing", started)); err != nil {
					return err
				}
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT group_key,rule_uid,instance_key FROM alert_notification_group_members`)
	if err != nil {
		return err
	}
	type stale struct{ k, r, i string }
	obsolete := []stale{}
	for rows.Next() {
		var v stale
		if err := rows.Scan(&v.k, &v.r, &v.i); err != nil {
			rows.Close()
			return err
		}
		if !wanted[v.k+"/"+v.r+"/"+v.i] {
			obsolete = append(obsolete, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range obsolete {
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_group_members WHERE group_key=? AND rule_uid=? AND instance_key=?`, v.k, v.r, v.i); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_groups WHERE inflight_id=0 AND NOT EXISTS(SELECT 1 FROM alert_notification_group_members m WHERE m.group_key=alert_notification_groups.group_key)`); err != nil {
		return err
	}
	return wakeNotificationGroupsTx(ctx, tx, at)
}

func wakeNotificationGroupsTx(ctx context.Context, tx *sql.Tx, at int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET next_at=CASE WHEN last_sent_at>0 THEN ? ELSE next_at END,dirty=1,generation=generation+1`, at)
	return err
}

func currentGroupMemberTx(ctx context.Context, tx *sql.Tx, g notificationGroup, m groupMember, at int64) (bool, string, error) {
	view, err := scanRule(tx.QueryRowContext(ctx, `SELECT config,runtime FROM alert_rules WHERE uid=?`, m.Rule))
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if view.Paused || view.Record != "" || view.NotificationReceiver != "" {
		return false, "", nil
	}
	p, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return false, "", err
	}
	for _, instance := range view.Runtime.Instances {
		if instance.Key != m.Key || !notificationActive(instance.State) {
			continue
		}
		selected := false
		for _, opts := range p.MatchingRoutes(instance.Labels) {
			if opts.GroupKey(instance.Labels) == g.Key {
				selected = true
				break
			}
		}
		if !selected {
			return false, "", nil
		}
		point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, g.Receiver))
		if err != nil {
			return false, "", err
		}
		event := model.AlertEvent{UID: view.UID, Key: instance.Key, Labels: instance.Labels, Annotations: instance.Annotations, Value: instance.Value, Timestamp: at, To: instance.State}
		raw, err := json.Marshal(webhookPayload(point, view.AlertRule, event, "firing", m.StartedAt))
		return true, string(raw), err
	}
	return false, "", nil
}
