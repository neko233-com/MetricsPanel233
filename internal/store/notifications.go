package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrNotificationConflict = errors.New("notification configuration changed; read the current version before updating")
var ErrContactPointReferenced = errors.New("contact point is referenced by a notification policy or alert rule")

func (s *Store) InitNotifications() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_contact_points(uid TEXT PRIMARY KEY,version INTEGER NOT NULL,config TEXT NOT NULL,secrets BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS alert_notification_policy(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL,config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alert_notification_active(rule_uid TEXT NOT NULL,instance_key TEXT NOT NULL,started_at INTEGER NOT NULL,PRIMARY KEY(rule_uid,instance_key));
CREATE TABLE IF NOT EXISTS alert_notification_outbox(id INTEGER PRIMARY KEY AUTOINCREMENT,event_id INTEGER NOT NULL,contact_point_uid TEXT NOT NULL,rule_uid TEXT NOT NULL,instance_key TEXT NOT NULL,status TEXT NOT NULL,state TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,max_attempts INTEGER NOT NULL DEFAULT 8,next_attempt_at INTEGER NOT NULL,last_attempt_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,delivered_at INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',http_status INTEGER NOT NULL DEFAULT 0,payload TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS alert_notification_event_receiver ON alert_notification_outbox(event_id,contact_point_uid) WHERE event_id>0;
CREATE INDEX IF NOT EXISTS alert_notification_due ON alert_notification_outbox(state,next_attempt_at,id);
CREATE INDEX IF NOT EXISTS alert_notification_ordering ON alert_notification_outbox(contact_point_uid,rule_uid,instance_key,id,state);`)
	if err != nil {
		return err
	}
	if err := s.initSilences(); err != nil {
		return err
	}
	if err := s.initMuteTimings(); err != nil {
		return err
	}
	if err := s.initNotificationTemplates(); err != nil {
		return err
	}
	if err := s.initNotificationGroups(); err != nil {
		return err
	}
	return s.initInhibition()
}

func scanContactPoint(row interface{ Scan(...any) error }) (model.ContactPoint, error) {
	var point model.ContactPoint
	var config string
	err := row.Scan(&config)
	if err == nil {
		err = json.Unmarshal([]byte(config), &point)
	}
	return point, err
}

func (s *Store) ContactPoints(ctx context.Context) ([]model.ContactPoint, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config FROM alert_contact_points ORDER BY json_extract(config,'$.name') COLLATE NOCASE,uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ContactPoint{}
	for rows.Next() {
		v, err := scanContactPoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ContactPoint(ctx context.Context, uid string) (model.ContactPoint, error) {
	return scanContactPoint(s.DB.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, uid))
}

func (s *Store) ContactPointSecrets(ctx context.Context, uid string) (map[string]string, error) {
	var sealed []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT secrets FROM alert_contact_points WHERE uid=?`, uid).Scan(&sealed); err != nil {
		return nil, err
	}
	return s.openSecrets("contact-point:"+uid, sealed)
}

func (s *Store) SaveContactPoint(ctx context.Context, input model.ContactPointInput) (model.ContactPoint, error) {
	if input.UID == "" {
		input.UID = uuid.NewString()
	}
	input.Defaults()
	if err := input.Validate(); err != nil {
		return model.ContactPoint{}, err
	}
	if len(input.Headers) > 32 || len(input.ClearSecrets) > 33 {
		return model.ContactPoint{}, errors.New("contact point supports at most 32 secret headers")
	}
	if input.BearerToken != nil && (len(*input.BearerToken) > 16384 || strings.ContainsAny(*input.BearerToken, "\r\n\x00")) {
		return model.ContactPoint{}, errors.New("invalid webhook Bearer token")
	}
	for name, value := range input.Headers {
		if !model.ValidNotificationHeader(name, value) {
			return model.ContactPoint{}, errors.New("invalid or reserved webhook header")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ContactPoint{}, err
	}
	defer tx.Rollback()
	var previousVersion int
	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT version,secrets FROM alert_contact_points WHERE uid=?`, input.UID).Scan(&previousVersion, &sealed)
	creating := errors.Is(err, sql.ErrNoRows)
	if err != nil && !creating {
		return model.ContactPoint{}, err
	}
	if (creating && input.Version != 0) || (!creating && (input.Version == 0 || previousVersion != input.Version)) {
		return model.ContactPoint{}, ErrNotificationConflict
	}
	if err := validatePointTemplateExpressions(ctx, tx, input.ContactPoint); err != nil {
		return model.ContactPoint{}, err
	}
	if creating {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_contact_points`).Scan(&count); err != nil {
			return model.ContactPoint{}, err
		}
		if count >= 100 {
			return model.ContactPoint{}, errors.New("at most 100 contact points")
		}
	}
	secrets := map[string]string{}
	if !creating {
		secrets, err = s.openSecrets("contact-point:"+input.UID, sealed)
		if err != nil {
			return model.ContactPoint{}, err
		}
	}
	for _, key := range input.ClearSecrets {
		if strings.HasPrefix(key, "header:") {
			key = "header:" + http.CanonicalHeaderKey(strings.TrimPrefix(key, "header:"))
		}
		delete(secrets, key)
	}
	if input.BearerToken != nil {
		if *input.BearerToken == "" {
			delete(secrets, "bearer_token")
		} else {
			secrets["bearer_token"] = *input.BearerToken
		}
	}
	for name, value := range input.Headers {
		key := "header:" + http.CanonicalHeaderKey(name)
		if value == "" {
			delete(secrets, key)
		} else {
			secrets[key] = value
		}
	}
	if len(secrets) > 33 {
		return model.ContactPoint{}, errors.New("contact point supports at most 32 secret headers")
	}
	if secrets["bearer_token"] != "" && secrets["header:Authorization"] != "" {
		return model.ContactPoint{}, errors.New("configure either a Bearer token or an Authorization header")
	}
	point := input.ContactPoint
	point.Version = previousVersion + 1
	point.UpdatedAt = time.Now().UnixMilli()
	point.SecureFields = map[string]bool{}
	for key := range secrets {
		point.SecureFields[key] = true
	}
	sealed, err = s.seal("contact-point:"+input.UID, secrets)
	if err != nil {
		return model.ContactPoint{}, err
	}
	config, err := json.Marshal(point)
	if err != nil {
		return model.ContactPoint{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_contact_points(uid,version,config,secrets) VALUES(?,?,?,?) ON CONFLICT(uid) DO UPDATE SET version=excluded.version,config=excluded.config,secrets=excluded.secrets`, point.UID, point.Version, string(config), sealed)
	if err != nil {
		return model.ContactPoint{}, err
	}
	return point, tx.Commit()
}

func notificationPolicyTx(ctx context.Context, tx *sql.Tx) (model.NotificationPolicy, error) {
	p := model.NotificationPolicy{Routes: []model.NotificationRoute{}}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT config FROM alert_notification_policy WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p)
	}
	return p, err
}

func (s *Store) NotificationPolicy(ctx context.Context) (model.NotificationPolicy, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.NotificationPolicy{}, err
	}
	defer tx.Rollback()
	return notificationPolicyTx(ctx, tx)
}

func (s *Store) SaveNotificationPolicy(ctx context.Context, p model.NotificationPolicy) (model.NotificationPolicy, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	previous, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return p, err
	}
	if previous.Version != p.Version {
		return p, ErrNotificationConflict
	}
	receivers := []string{p.Receiver}
	var collectRoutes func([]model.NotificationRoute) error
	collectRoutes = func(routes []model.NotificationRoute) error {
		for _, route := range routes {
			receivers = append(receivers, route.Receiver)
			for _, names := range [][]string{route.MuteTimeIntervals, route.ActiveTimeIntervals} {
				for _, name := range names {
					var exists int
					if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_mute_timings WHERE name=?`, name).Scan(&exists); err != nil {
						return err
					}
					if exists != 1 {
						return fmt.Errorf("notification time interval %q does not exist", name)
					}
				}
			}
			if err := collectRoutes(route.Routes); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collectRoutes(p.Routes); err != nil {
		return p, err
	}
	for _, uid := range receivers {
		if uid == "" {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_contact_points WHERE uid=?`, uid).Scan(&count); err != nil {
			return p, err
		}
		if count != 1 {
			return p, fmt.Errorf("notification receiver %q does not exist", uid)
		}
	}
	if p.Routes == nil {
		p.Routes = []model.NotificationRoute{}
	}
	p.Version++
	p.UpdatedAt = time.Now().UnixMilli()
	raw, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_notification_policy(id,version,config) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,config=excluded.config`, p.Version, string(raw)); err != nil {
		return p, err
	}
	if err := syncNotificationGroupsTx(ctx, tx, p, p.UpdatedAt); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (s *Store) DeleteContactPoint(ctx context.Context, uid string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	if p.Receiver == uid {
		return ErrContactPointReferenced
	}
	var referenced func([]model.NotificationRoute) bool
	referenced = func(routes []model.NotificationRoute) bool {
		for _, route := range routes {
			if route.Receiver == uid || referenced(route.Routes) {
				return true
			}
		}
		return false
	}
	if referenced(p.Routes) {
		return ErrContactPointReferenced
	}
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_rules WHERE json_extract(config,'$.notification_receiver')=?`, uid).Scan(&references); err != nil {
		return err
	}
	if references > 0 {
		return ErrContactPointReferenced
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM alert_contact_points WHERE uid=?`, uid)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',last_error='Contact point deleted' WHERE contact_point_uid=? AND state IN ('pending','sending','suppressed')`, uid); err != nil {
		return err
	}
	return tx.Commit()
}

func notificationActive(state string) bool {
	return state == "Firing" || state == "Recovering" || state == "NoData" || state == "Error"
}

// Enqueue is part of the same transaction as the state change. A restart cannot
// persist a firing transition without its matching notification.
func enqueueAlertNotifications(ctx context.Context, tx *sql.Tx, rule model.AlertRule, event model.AlertEvent, eventID int64) error {
	was, active := notificationActive(event.From), notificationActive(event.To)
	if was == active || rule.Record != "" {
		return nil
	}
	orgID, err := notificationOrganizationTx(ctx, tx)
	if err != nil {
		return err
	}
	status := "resolved"
	startedAt := event.Timestamp
	if active {
		status = "firing"
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_active(rule_uid,instance_key,started_at) VALUES(?,?,?)`, rule.UID, event.Key, startedAt); err != nil {
			return err
		}
	} else {
		err := tx.QueryRowContext(ctx, `SELECT started_at FROM alert_notification_active WHERE rule_uid=? AND instance_key=?`, rule.UID, event.Key).Scan(&startedAt)
		if errors.Is(err, sql.ErrNoRows) {
			// Backfill recovery timing for rules that were already active before
			// notification support was installed. State changes within the same
			// active period must not move its starting timestamp.
			err = tx.QueryRowContext(ctx, `SELECT timestamp FROM alert_events WHERE uid=? AND id<? AND json_extract(payload,'$.key')=? AND json_extract(payload,'$.from') NOT IN ('Firing','Recovering','NoData','Error') AND json_extract(payload,'$.to') IN ('Firing','Recovering','NoData','Error') ORDER BY id DESC LIMIT 1`, rule.UID, eventID, event.Key).Scan(&startedAt)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_active WHERE rule_uid=? AND instance_key=?`, rule.UID, event.Key); err != nil {
			return err
		}
	}
	p, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	silences, err := activeSilencesTx(ctx, tx, event.Timestamp)
	if err != nil {
		return err
	}
	suppressor, err := newNotificationSuppressor(ctx, tx, event.Timestamp, silences)
	if err != nil {
		return err
	}
	mutedUntil, suppressionMessage, err := suppressor.check(event.Labels)
	if err != nil {
		return err
	}
	previouslySuppressed := map[string]bool{}
	if !active {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT contact_point_uid FROM alert_notification_outbox WHERE rule_uid=? AND instance_key=? AND created_at>=? AND status='firing' AND state='suppressed'`, rule.UID, event.Key, startedAt)
		if err != nil {
			return err
		}
		for rows.Next() {
			var uid string
			if err := rows.Scan(&uid); err != nil {
				rows.Close()
				return err
			}
			previouslySuppressed[uid] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		states := `state='suppressed'`
		if mutedUntil > 0 {
			states = `state IN ('pending','suppressed')`
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',next_attempt_at=0,last_error='Resolved while silenced' WHERE rule_uid=? AND instance_key=? AND created_at>=? AND status='firing' AND `+states, rule.UID, event.Key, startedAt); err != nil {
			return err
		}
	}
	opts := p.MatchingRoutes(event.Labels)
	if rule.NotificationReceiver != "" {
		opts = []model.NotificationRouteOptions{{Receiver: rule.NotificationReceiver, RouteKey: "rule/" + rule.UID}}
	}
	if !active {
		if err := resolveNotificationGroupsTx(ctx, tx, rule, event, startedAt, suppressor); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, options := range opts {
		uid := options.Receiver
		if uid == "" {
			continue
		}
		groupKey := options.GroupKey(event.Labels)
		if seen[uid+":"+groupKey] {
			continue
		}
		seen[uid+":"+groupKey] = true
		point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, uid))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if point.Disabled || (status == "resolved" && point.DisableResolveMessage) {
			continue
		}
		payload := webhookPayload(point, rule, event, status, startedAt, orgID)
		if groupKey != "" {
			if active {
				if err := enqueueNotificationGroupTx(ctx, tx, options, rule, event, startedAt, payload); err != nil {
					return err
				}
			}
			continue
		}
		id, err := insertNotificationTx(ctx, tx, eventID, uid, rule.UID, event.Key, status, payload, event.Timestamp)
		if err != nil {
			return err
		}
		if active && mutedUntil > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='suppressed',last_error=?,next_attempt_at=? WHERE id=?`, suppressionMessage, mutedUntil, id); err != nil {
				return err
			}
		} else if !active {
			cancel := mutedUntil > 0
			if previouslySuppressed[uid] && !cancel {
				var delivered int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_notification_outbox WHERE contact_point_uid=? AND rule_uid=? AND instance_key=? AND created_at>=? AND status='firing' AND state IN ('sent','sending')`, uid, rule.UID, event.Key, startedAt).Scan(&delivered); err != nil {
					return err
				}
				cancel = delivered == 0
			}
			if cancel {
				if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='canceled',last_error='Resolved while silenced',next_attempt_at=0 WHERE id=?`, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func notificationReceiverTx(ctx context.Context, tx *sql.Tx, receiver string) (string, error) {
	var uid string
	err := tx.QueryRowContext(ctx, `SELECT uid FROM alert_contact_points WHERE uid=?`, receiver).Scan(&uid)
	if err == nil {
		return uid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT uid FROM alert_contact_points WHERE json_extract(config,'$.name')=? LIMIT 2`, receiver)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if err := rows.Scan(&uid); err != nil {
			return "", err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", errors.New("notification receiver must identify one existing contact point; use its UID for duplicate names")
	}
	return uid, nil
}

func notificationOrganizationTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var orgID int64
	err := tx.QueryRowContext(ctx, `SELECT organization_id FROM store_identity WHERE id=1`).Scan(&orgID)
	return orgID, err
}

func webhookPayload(point model.ContactPoint, rule model.AlertRule, event model.AlertEvent, status string, startedAt, orgID int64) map[string]any {
	if event.Labels == nil {
		event.Labels = map[string]string{}
	}
	if event.Annotations == nil {
		event.Annotations = map[string]string{}
	}
	endsAt := "0001-01-01T00:00:00Z"
	state := "alerting"
	if status == "resolved" {
		endsAt = time.UnixMilli(event.Timestamp).UTC().Format(time.RFC3339Nano)
		state = "ok"
	}
	return map[string]any{
		"receiver": point.Name, "status": status, "version": "1", "orgId": orgID,
		"groupKey": rule.UID + ":" + event.Key, "groupLabels": map[string]string{"alertname": rule.Title},
		"commonLabels": event.Labels, "commonAnnotations": event.Annotations,
		"externalURL": "", "truncatedAlerts": 0, "title": "[" + strings.ToUpper(status) + "] " + rule.Title, "message": event.Annotations["summary"], "state": state,
		"alerts": []any{map[string]any{"status": status, "labels": event.Labels, "annotations": event.Annotations, "startsAt": time.UnixMilli(startedAt).UTC().Format(time.RFC3339Nano), "endsAt": endsAt, "generatorURL": "", "fingerprint": event.Key, "values": map[string]any{"A": event.Value}}},
	}
}

func prepareNotificationRule(ctx context.Context, tx *sql.Tx, rule *model.AlertRule) error {
	name := ""
	if rule.NotificationReceiver != "" {
		uid, err := notificationReceiverTx(ctx, tx, rule.NotificationReceiver)
		if err != nil {
			return err
		}
		rule.NotificationReceiver = uid
		point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, uid))
		if err != nil {
			return err
		}
		name = point.Name
	}
	if len(rule.Grafana) > 0 {
		var graph map[string]json.RawMessage
		if err := json.Unmarshal(rule.Grafana, &graph); err != nil {
			return err
		}
		if name == "" {
			delete(graph, "notification_settings")
		} else {
			graph["notification_settings"], _ = json.Marshal(map[string]string{"receiver": name})
		}
		var err error
		rule.Grafana, err = json.Marshal(graph)
		if err != nil {
			return err
		}
	}
	return nil
}

func insertNotificationTx(ctx context.Context, tx *sql.Tx, eventID int64, uid, rule, key, status string, payload any, at int64) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO alert_notification_outbox(event_id,contact_point_uid,rule_uid,instance_key,status,state,next_attempt_at,created_at,payload) VALUES(?,?,?,?,?,'pending',?,?,?)`, eventID, uid, rule, key, status, at, at, string(raw))
	if err != nil {
		return 0, err
	}
	// Retain the latest successful ungrouped firing for every active period.
	// Late suppression needs that durable resume anchor even after ordinary
	// history ages out. Earlier resumes and completed periods remain bounded.
	_, err = tx.ExecContext(ctx, `DELETE FROM alert_notification_outbox AS n WHERE n.id IN (SELECT id FROM alert_notification_outbox WHERE state IN ('sent','failed','canceled') ORDER BY id DESC LIMIT -1 OFFSET 10000) AND NOT (n.status='firing' AND n.state='sent' AND EXISTS(SELECT 1 FROM alert_notification_active a WHERE a.rule_uid=n.rule_uid AND a.instance_key=n.instance_key AND a.started_at<=n.created_at) AND NOT EXISTS(SELECT 1 FROM alert_notification_group_deliveries gd WHERE gd.notification_id=n.id) AND NOT EXISTS(SELECT 1 FROM alert_notification_outbox newer WHERE newer.contact_point_uid=n.contact_point_uid AND newer.rule_uid=n.rule_uid AND newer.instance_key=n.instance_key AND newer.id>n.id AND newer.status='firing'))`)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := snapshotNotificationTemplatesTx(ctx, tx, id, uid); err != nil {
		return 0, err
	}
	if err := snapshotReceiverNotificationSourceTx(ctx, tx, id); err != nil {
		return 0, err
	}
	return id, nil
}

const notificationColumns = `id,event_id,contact_point_uid,rule_uid,instance_key,status,state,attempts,max_attempts,next_attempt_at,last_attempt_at,created_at,delivered_at,last_error,http_status`

func scanNotification(row interface{ Scan(...any) error }) (model.NotificationDelivery, error) {
	var v model.NotificationDelivery
	err := row.Scan(&v.ID, &v.EventID, &v.ContactPointUID, &v.RuleUID, &v.InstanceKey, &v.Status, &v.State, &v.Attempts, &v.MaxAttempts, &v.NextAttemptAt, &v.LastAttemptAt, &v.CreatedAt, &v.DeliveredAt, &v.LastError, &v.HTTPStatus)
	return v, err
}

func (s *Store) Notifications(ctx context.Context, uid string, limit int) ([]model.NotificationDelivery, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("notification history limit must be 1–1000")
	}
	query := `SELECT ` + notificationColumns + ` FROM alert_notification_outbox`
	args := []any{}
	if uid != "" {
		query += ` WHERE contact_point_uid=?`
		args = append(args, uid)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NotificationDelivery{}
	for rows.Next() {
		v, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueTestNotification(ctx context.Context, uid string) (model.NotificationDelivery, error) {
	ctx = receiverProducerContext(ctx, s)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.NotificationDelivery{}, err
	}
	defer tx.Rollback()
	point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, uid))
	if err != nil {
		return model.NotificationDelivery{}, err
	}
	if point.Disabled {
		return model.NotificationDelivery{}, errors.New("contact point is disabled")
	}
	at := time.Now().UnixMilli()
	key := uuid.NewString()
	event := model.AlertEvent{Key: key, Timestamp: at, Labels: map[string]string{"alertname": "Test notification"}, Annotations: map[string]string{"summary": "MetricsPanel233 test notification"}}
	payload := webhookPayload(point, model.AlertRule{Title: "Test notification"}, event, "firing", at, s.OrganizationID())
	id, err := insertNotificationTx(ctx, tx, 0, uid, "", key, "test", payload, at)
	if err != nil {
		return model.NotificationDelivery{}, err
	}
	v, err := scanNotification(tx.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM alert_notification_outbox WHERE id=?`, id))
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

// A delivery lease expires after a crash. Stable delivery IDs allow receivers to
// deduplicate the unavoidable ambiguous result of an interrupted HTTP request.
func (s *Store) ClaimNotification(ctx context.Context, at int64) (model.NotificationDelivery, json.RawMessage, error) {
	ctx = receiverProducerContext(ctx, s)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	defer tx.Rollback()
	silences, err := activeSilencesTx(ctx, tx, at)
	if err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	suppressor, err := newNotificationSuppressor(ctx, tx, at, silences)
	if err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	if err := enqueueSilenceResumesTx(ctx, tx, at, suppressor); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	if err := reconcileSuppressedNotificationsTx(ctx, tx, at, suppressor); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	if err := reconcileNotificationGroupsTx(ctx, tx, at, suppressor); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	var id int64
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='failed',last_error='Delivery interrupted after final attempt',next_attempt_at=0 WHERE state='sending' AND next_attempt_at<=? AND attempts>=max_attempts`, at); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	if err := s.reconcileReceiverSourceTerminalTx(ctx, tx, at); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	var candidateRaw, candidateStatus, candidateRule, candidateKey string
	for range 32 {
		err = tx.QueryRowContext(ctx, `SELECT n.id,n.payload,n.status,n.rule_uid,n.instance_key FROM alert_notification_outbox n WHERE n.state IN ('pending','sending') AND n.next_attempt_at<=? AND NOT EXISTS(SELECT 1 FROM alert_notification_outbox earlier WHERE earlier.contact_point_uid=n.contact_point_uid AND earlier.rule_uid=n.rule_uid AND earlier.instance_key=n.instance_key AND earlier.id<n.id AND earlier.state IN ('pending','sending','suppressed')) ORDER BY n.next_attempt_at,n.id LIMIT 1`, at).Scan(&id, &candidateRaw, &candidateStatus, &candidateRule, &candidateKey)
		if err != nil {
			break
		}
		if candidateStatus == "test" {
			break
		}
		grouped, valid, err := validGroupNotificationTx(ctx, tx, id, at, suppressor)
		if err != nil {
			return model.NotificationDelivery{}, nil, err
		}
		if grouped {
			if valid {
				break
			}
			id = 0
			continue
		}
		labels, err := currentNotificationLabelsTx(ctx, tx, candidateRule, candidateKey, candidateRaw)
		if err != nil {
			return model.NotificationDelivery{}, nil, err
		}
		until, suppressionMessage, err := suppressor.check(labels)
		if err != nil {
			return model.NotificationDelivery{}, nil, err
		}
		if until == 0 {
			break
		}
		state, message, next := "suppressed", suppressionMessage, until
		if candidateStatus == "resolved" {
			state, message, next = "canceled", "Resolved while suppressed", 0
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state=?,last_error=?,next_attempt_at=? WHERE id=?`, state, message, next, id); err != nil {
			return model.NotificationDelivery{}, nil, err
		}
		id = 0
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && id == 0) {
		if commitErr := tx.Commit(); commitErr != nil {
			return model.NotificationDelivery{}, nil, commitErr
		}
		return model.NotificationDelivery{}, nil, sql.ErrNoRows
	}
	if err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='sending',attempts=attempts+1,last_attempt_at=?,next_attempt_at=? WHERE id=?`, at, at+60000, id); err != nil {
		return model.NotificationDelivery{}, nil, err
	}
	v, err := scanNotification(tx.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM alert_notification_outbox WHERE id=?`, id))
	if err != nil {
		return v, nil, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM alert_notification_outbox WHERE id=?`, id).Scan(&raw); err != nil {
		return v, nil, err
	}
	return v, json.RawMessage(raw), tx.Commit()
}

func (s *Store) FinishNotification(ctx context.Context, delivery model.NotificationDelivery, state, message string, httpStatus int, nextAt, at int64) error {
	delivered := int64(0)
	if state == "sent" {
		delivered = at
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state=?,last_error=?,http_status=?,next_attempt_at=?,delivered_at=? WHERE id=? AND state='sending' AND attempts=? AND last_attempt_at=?`, state, message, httpStatus, nextAt, delivered, delivery.ID, delivery.Attempts, delivery.LastAttemptAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count > 0 {
		if err := s.reconcileReceiverSourceTerminalTx(ctx, tx, at); err != nil {
			return err
		}
		if err := finishGroupNotificationTx(ctx, tx, delivery.ID, state, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ReleaseNotification(ctx context.Context, delivery model.NotificationDelivery) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='pending',attempts=attempts-1,next_attempt_at=? WHERE id=? AND state='sending' AND attempts=? AND last_attempt_at=?`, time.Now().UnixMilli(), delivery.ID, delivery.Attempts, delivery.LastAttemptAt)
	return err
}

func (s *Store) RetryNotification(ctx context.Context, id int64) (model.NotificationDelivery, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.NotificationDelivery{}, err
	}
	defer tx.Rollback()
	v, err := scanNotification(tx.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM alert_notification_outbox WHERE id=?`, id))
	if err != nil {
		return v, err
	}
	if v.State != "failed" {
		return v, errors.New("only a failed notification can be retried")
	}
	var renderError string
	if err := tx.QueryRowContext(ctx, `SELECT render_error FROM alert_notification_render_snapshots WHERE notification_id=?`, id).Scan(&renderError); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if renderError != "" {
		return v, errors.New("the saved template version failed to render; create a new notification after correcting the template")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_contact_points WHERE uid=?`, v.ContactPointUID).Scan(&count); err != nil {
		return v, err
	}
	if count == 0 {
		return v, sql.ErrNoRows
	}
	var groupKey string
	err = tx.QueryRowContext(ctx, `SELECT group_key FROM alert_notification_group_deliveries WHERE notification_id=?`, id).Scan(&groupKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if groupKey != "" {
		var inflight int64
		if err := tx.QueryRowContext(ctx, `SELECT inflight_id FROM alert_notification_groups WHERE group_key=?`, groupKey).Scan(&inflight); err != nil {
			return v, err
		}
		if inflight != 0 && inflight != id {
			return v, errors.New("notification group already has a pending delivery")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_notification_groups SET inflight_id=? WHERE group_key=?`, id, groupKey); err != nil {
			return v, err
		}
	}
	v.State, v.Attempts, v.NextAttemptAt, v.LastError, v.HTTPStatus = "pending", 0, time.Now().UnixMilli(), "", 0
	_, err = tx.ExecContext(ctx, `UPDATE alert_notification_outbox SET state='pending',attempts=0,next_attempt_at=?,last_error='',http_status=0 WHERE id=?`, v.NextAttemptAt, id)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
