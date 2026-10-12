package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/notificationtemplate"
)

var ErrNotificationTemplateReferenced = errors.New("notification template is referenced by another template or contact point")

func (s *Store) initNotificationTemplates() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_notification_templates(name TEXT PRIMARY KEY,version INTEGER NOT NULL,config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alert_notification_template_bundles(hash TEXT PRIMARY KEY,config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS alert_notification_render_snapshots(notification_id INTEGER PRIMARY KEY REFERENCES alert_notification_outbox(id) ON DELETE CASCADE,bundle_hash TEXT NOT NULL REFERENCES alert_notification_template_bundles(hash),rendered_payload TEXT NOT NULL DEFAULT '',render_error TEXT NOT NULL DEFAULT '',rendered_at INTEGER NOT NULL DEFAULT 0);`)
	return err
}

func scanNotificationTemplate(row interface{ Scan(...any) error }) (model.NotificationTemplate, error) {
	var v model.NotificationTemplate
	var raw string
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}
func notificationTemplatesTx(ctx context.Context, tx *sql.Tx) ([]model.NotificationTemplate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM alert_notification_templates ORDER BY name COLLATE NOCASE,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NotificationTemplate{}
	for rows.Next() {
		v, err := scanNotificationTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) NotificationTemplates(ctx context.Context) ([]model.NotificationTemplate, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return notificationTemplatesTx(ctx, tx)
}
func (s *Store) NotificationTemplate(ctx context.Context, name string) (model.NotificationTemplate, error) {
	return scanNotificationTemplate(s.DB.QueryRowContext(ctx, `SELECT config FROM alert_notification_templates WHERE name=?`, name))
}

func templateDefinitions(values []model.NotificationTemplate) []notificationtemplate.Definition {
	out := []notificationtemplate.Definition{}
	for _, v := range values {
		out = append(out, notificationtemplate.Definition{Name: v.Name, Template: v.Template, Version: v.Version})
	}
	return out
}
func validateContactTemplatesTx(ctx context.Context, tx *sql.Tx, compiled *notificationtemplate.Compiled) error {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM alert_contact_points`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		point, err := scanContactPoint(rows)
		if err != nil {
			return err
		}
		for _, expression := range []string{point.TitleTemplate, point.MessageTemplate} {
			if expression != "" {
				if err := compiled.ValidateExpression(expression); err != nil {
					return err
				}
			}
		}
	}
	return rows.Err()
}

func (s *Store) SaveNotificationTemplate(ctx context.Context, value model.NotificationTemplate) (model.NotificationTemplate, error) {
	if err := value.Validate(); err != nil {
		return value, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return value, err
	}
	defer tx.Rollback()
	existing, err := notificationTemplatesTx(ctx, tx)
	if err != nil {
		return value, err
	}
	version := 0
	index := -1
	for i, v := range existing {
		if v.Name == value.Name {
			version = v.Version
			index = i
			break
		}
	}
	if value.Version != version {
		return value, ErrNotificationConflict
	}
	value.Version = version + 1
	value.UpdatedAt = time.Now().UnixMilli()
	if index < 0 {
		existing = append(existing, value)
	} else {
		existing[index] = value
	}
	compiled, err := notificationtemplate.Compile(templateDefinitions(existing))
	if err != nil {
		return value, err
	}
	if err := validateContactTemplatesTx(ctx, tx, compiled); err != nil {
		return value, err
	}
	value.Definitions = compiled.Names[value.Name]
	raw, _ := json.Marshal(value)
	_, err = tx.ExecContext(ctx, `INSERT INTO alert_notification_templates(name,version,config) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET version=excluded.version,config=excluded.config`, value.Name, value.Version, string(raw))
	if err != nil {
		return value, err
	}
	return value, tx.Commit()
}

func (s *Store) DeleteNotificationTemplate(ctx context.Context, name string, version *int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := notificationTemplatesTx(ctx, tx)
	if err != nil {
		return err
	}
	found := false
	remaining := []model.NotificationTemplate{}
	for _, v := range existing {
		if v.Name == name {
			found = true
			if version != nil && v.Version != *version {
				return ErrNotificationConflict
			}
		} else {
			remaining = append(remaining, v)
		}
	}
	if !found {
		return sql.ErrNoRows
	}
	compiled, err := notificationtemplate.Compile(templateDefinitions(remaining))
	if err != nil {
		return ErrNotificationTemplateReferenced
	}
	if err := validateContactTemplatesTx(ctx, tx, compiled); err != nil {
		return ErrNotificationTemplateReferenced
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_notification_templates WHERE name=?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

func validatePointTemplateExpressions(ctx context.Context, tx *sql.Tx, point model.ContactPoint) error {
	if point.TitleTemplate == "" && point.MessageTemplate == "" {
		return nil
	}
	values, err := notificationTemplatesTx(ctx, tx)
	if err != nil {
		return err
	}
	compiled, err := notificationtemplate.Compile(templateDefinitions(values))
	if err != nil {
		return err
	}
	for _, expression := range []string{point.TitleTemplate, point.MessageTemplate} {
		if err := compiled.ValidateExpression(expression); err != nil {
			return err
		}
	}
	return nil
}

func snapshotNotificationTemplatesTx(ctx context.Context, tx *sql.Tx, id int64, uid string) error {
	point, err := scanContactPoint(tx.QueryRowContext(ctx, `SELECT config FROM alert_contact_points WHERE uid=?`, uid))
	if err != nil {
		return err
	}
	if point.TitleTemplate == "" && point.MessageTemplate == "" {
		return nil
	}
	values, err := notificationTemplatesTx(ctx, tx)
	if err != nil {
		return err
	}
	bundle := notificationtemplate.Bundle{Title: point.TitleTemplate, Message: point.MessageTemplate, Templates: templateDefinitions(values)}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	key := hex.EncodeToString(hash[:])
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO alert_notification_template_bundles(hash,config) VALUES(?,?)`, key, string(raw)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_notification_render_snapshots(notification_id,bundle_hash) VALUES(?,?)`, id, key); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM alert_notification_template_bundles WHERE NOT EXISTS(SELECT 1 FROM alert_notification_render_snapshots s WHERE s.bundle_hash=alert_notification_template_bundles.hash)`)
	return err
}

type NotificationRenderSnapshot struct {
	Bundle  notificationtemplate.Bundle
	Payload json.RawMessage
	Error   string
}

func (s *Store) NotificationRenderSnapshot(ctx context.Context, id int64) (NotificationRenderSnapshot, bool, error) {
	var v NotificationRenderSnapshot
	var bundle, payload string
	err := s.DB.QueryRowContext(ctx, `SELECT b.config,s.rendered_payload,s.render_error FROM alert_notification_render_snapshots s JOIN alert_notification_template_bundles b ON b.hash=s.bundle_hash WHERE s.notification_id=?`, id).Scan(&bundle, &payload, &v.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if err = json.Unmarshal([]byte(bundle), &v.Bundle); err != nil {
		return v, true, err
	}
	v.Payload = json.RawMessage(payload)
	return v, true, nil
}

func (s *Store) SaveNotificationRender(ctx context.Context, delivery model.NotificationDelivery, payload json.RawMessage, message string) (json.RawMessage, string, error) {
	_, err := s.DB.ExecContext(ctx, `UPDATE alert_notification_render_snapshots SET rendered_payload=?,render_error=?,rendered_at=? WHERE notification_id=? AND rendered_payload='' AND render_error='' AND EXISTS(SELECT 1 FROM alert_notification_outbox n WHERE n.id=? AND n.state='sending' AND n.attempts=? AND n.last_attempt_at=?)`, string(payload), message, time.Now().UnixMilli(), delivery.ID, delivery.ID, delivery.Attempts, delivery.LastAttemptAt)
	if err != nil {
		return nil, "", err
	}
	v, found, err := s.NotificationRenderSnapshot(ctx, delivery.ID)
	if err != nil {
		return nil, "", err
	}
	if !found || len(v.Payload) == 0 && v.Error == "" {
		return nil, "", errors.New("notification render lease is no longer current")
	}
	return v.Payload, v.Error, nil
}

func (s *Store) NotificationRenderRules(ctx context.Context, id int64, fallback string) ([]string, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT members FROM alert_notification_group_deliveries WHERE notification_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{fallback}, nil
	}
	if err != nil {
		return nil, err
	}
	var members []struct {
		Rule string `json:"rule"`
	}
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil, err
	}
	rules := []string{}
	for _, m := range members {
		rules = append(rules, m.Rule)
	}
	return rules, nil
}
