package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
)

// InitializeReceiverSchema is deliberately additive and dormant. Callers must
// opt in after opening the organization's store; it starts no worker and leaves
// contact points, policy routes and the legacy outbox under their existing owner.
func (s *Store) InitializeReceiverSchema(ctx context.Context) error {
	if len(s.secretKey) != 32 {
		return ErrReceiverSnapshot
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS alert_receiver_schema(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL,migrated INTEGER NOT NULL);
 INSERT OR IGNORE INTO alert_receiver_schema VALUES(1,1,0);
 CREATE TABLE IF NOT EXISTS alert_receiver_ownership(id INTEGER PRIMARY KEY CHECK(id=1),mode TEXT NOT NULL CHECK(mode IN ('legacy','native')),epoch INTEGER NOT NULL);
 INSERT OR IGNORE INTO alert_receiver_ownership VALUES(1,'legacy',1);
 CREATE TABLE IF NOT EXISTS alert_receivers(uid TEXT PRIMARY KEY,version INTEGER NOT NULL,name TEXT NOT NULL,deleted INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL,config TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS alert_smtp_profiles(uid TEXT PRIMARY KEY,version INTEGER NOT NULL,name TEXT NOT NULL,revision INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL,config TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS alert_smtp_profile_revisions(uid TEXT NOT NULL REFERENCES alert_smtp_profiles(uid),revision INTEGER NOT NULL,config TEXT NOT NULL,sealed BLOB NOT NULL,fingerprint TEXT NOT NULL,PRIMARY KEY(uid,revision));
 CREATE TABLE IF NOT EXISTS alert_receiver_integrations(uid TEXT PRIMARY KEY,receiver_uid TEXT NOT NULL REFERENCES alert_receivers(uid),type TEXT NOT NULL,config_version TEXT NOT NULL,revision INTEGER NOT NULL,channel_epoch INTEGER NOT NULL,deleted INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS alert_receiver_integration_revisions(uid TEXT NOT NULL REFERENCES alert_receiver_integrations(uid),revision INTEGER NOT NULL,channel_epoch INTEGER NOT NULL,profile_uid TEXT,profile_revision INTEGER,config TEXT NOT NULL,sealed BLOB NOT NULL,fingerprint TEXT NOT NULL,destination_key TEXT NOT NULL,PRIMARY KEY(uid,revision),FOREIGN KEY(profile_uid,profile_revision) REFERENCES alert_smtp_profile_revisions(uid,revision));
 CREATE TABLE IF NOT EXISTS alert_receiver_dispatches(id INTEGER PRIMARY KEY AUTOINCREMENT,receiver_uid TEXT NOT NULL REFERENCES alert_receivers(uid),group_key TEXT NOT NULL,generation INTEGER NOT NULL,fingerprint TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('firing','resolved','test')),state TEXT NOT NULL DEFAULT 'pending',created_at INTEGER NOT NULL,sealed BLOB NOT NULL,UNIQUE(receiver_uid,group_key,generation));
 CREATE TABLE IF NOT EXISTS alert_receiver_deliveries(id INTEGER PRIMARY KEY AUTOINCREMENT,dispatch_id INTEGER NOT NULL REFERENCES alert_receiver_dispatches(id),integration_uid TEXT NOT NULL,revision INTEGER NOT NULL,channel_epoch INTEGER NOT NULL,fingerprint TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at INTEGER NOT NULL,lease_token TEXT NOT NULL DEFAULT '',lease_until INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,completed_at INTEGER NOT NULL DEFAULT 0,sealed BLOB NOT NULL,UNIQUE(dispatch_id,integration_uid),FOREIGN KEY(integration_uid,revision) REFERENCES alert_receiver_integration_revisions(uid,revision));
 CREATE INDEX IF NOT EXISTS alert_receiver_delivery_due ON alert_receiver_deliveries(state,next_attempt_at,id);
 CREATE INDEX IF NOT EXISTS alert_receiver_delivery_order ON alert_receiver_deliveries(integration_uid,id,state);
 CREATE TABLE IF NOT EXISTS alert_receiver_send_leases(delivery_id INTEGER PRIMARY KEY REFERENCES alert_receiver_deliveries(id),claim_token TEXT NOT NULL,send_token TEXT NOT NULL,execution_count INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS alert_receiver_operations(delivery_id INTEGER NOT NULL REFERENCES alert_receiver_deliveries(id),op_key TEXT NOT NULL,target_key TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,max_attempts INTEGER NOT NULL DEFAULT 8,next_attempt_at INTEGER NOT NULL,code TEXT NOT NULL DEFAULT '',status_code INTEGER NOT NULL DEFAULT 0,receipt TEXT NOT NULL DEFAULT '',PRIMARY KEY(delivery_id,op_key));
 CREATE TABLE IF NOT EXISTS alert_receiver_firing_receipts(integration_uid TEXT NOT NULL,channel_epoch INTEGER NOT NULL,period_key TEXT NOT NULL,target_key TEXT NOT NULL,delivery_id INTEGER NOT NULL REFERENCES alert_receiver_deliveries(id),op_key TEXT NOT NULL,acknowledged_at INTEGER NOT NULL,PRIMARY KEY(integration_uid,channel_epoch,period_key,target_key));
 CREATE TABLE IF NOT EXISTS alert_receiver_legacy_outbox(legacy_id INTEGER PRIMARY KEY,receiver_uid TEXT NOT NULL REFERENCES alert_receivers(uid),state TEXT NOT NULL,sealed BLOB NOT NULL);`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS alert_receiver_sources(notification_id INTEGER PRIMARY KEY,receiver_uid TEXT NOT NULL REFERENCES alert_receivers(uid),receiver_version INTEGER NOT NULL,owner_epoch INTEGER NOT NULL,group_key TEXT NOT NULL,generation INTEGER NOT NULL,status TEXT NOT NULL,fingerprint TEXT NOT NULL,sealed BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS alert_receiver_source_dispatches(notification_id INTEGER NOT NULL REFERENCES alert_receiver_sources(notification_id),dispatch_id INTEGER NOT NULL UNIQUE REFERENCES alert_receiver_dispatches(id),PRIMARY KEY(notification_id,dispatch_id));`)
	if err == nil {
		_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS alert_receiver_source_preparations(notification_id INTEGER PRIMARY KEY REFERENCES alert_receiver_sources(notification_id),fingerprint TEXT NOT NULL)`)
	}
	if err != nil {
		return err
	}
	// The additive execution latch may already exist in an opt-in development
	// database. Upgrade it without rewriting immutable revisions or snapshots.
	columns, err := tx.QueryContext(ctx, `PRAGMA table_info(alert_receiver_send_leases)`)
	if err != nil {
		return err
	}
	hasExecutionCount := false
	for columns.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err = columns.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			break
		}
		if name == "execution_count" {
			hasExecutionCount = true
		}
	}
	if err == nil {
		err = columns.Err()
	}
	columns.Close()
	if err != nil {
		return err
	}
	if !hasExecutionCount {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE alert_receiver_send_leases ADD COLUMN execution_count INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
	}
	var version, migrated int
	if err = tx.QueryRowContext(ctx, `SELECT version,migrated FROM alert_receiver_schema WHERE id=1`).Scan(&version, &migrated); err != nil {
		return err
	}
	if version != 1 {
		return model.ErrInvalidReceiver
	}
	if migrated == 0 {
		if err = s.migrateReceiverLegacy(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_schema SET migrated=1 WHERE id=1`); err != nil {
			return err
		}
	}
	// Authenticate existing immutable data before exposing this schema. A lost key
	// must still be caught by Store.Open before it generates a replacement key.
	for _, spec := range []struct{ query, kind string }{
		{`SELECT uid,revision,sealed FROM alert_smtp_profile_revisions`, "smtp-profile"},
		{`SELECT uid,revision,sealed FROM alert_receiver_integration_revisions`, "integration"},
		{`SELECT CAST(id AS TEXT),generation,sealed FROM alert_receiver_dispatches`, "dispatch"},
		{`SELECT CAST(id AS TEXT),revision,sealed FROM alert_receiver_deliveries`, "delivery"},
		{`SELECT CAST(legacy_id AS TEXT),0,sealed FROM alert_receiver_legacy_outbox`, "legacy-outbox"},
		{`SELECT CAST(notification_id AS TEXT),owner_epoch,sealed FROM alert_receiver_sources`, "notification-source"},
	} {
		rows, e := tx.QueryContext(ctx, spec.query)
		if e != nil {
			return e
		}
		for rows.Next() {
			var uid string
			var rev int64
			var sealed []byte
			if e = rows.Scan(&uid, &rev, &sealed); e != nil {
				break
			}
			var raw json.RawMessage
			if e = s.openReceiver(spec.kind, uid, rev, sealed, &raw); e != nil {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Store) migrateReceiverLegacy(ctx context.Context, tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_contact_points'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT uid,version,config,secrets FROM alert_contact_points ORDER BY uid`)
	if err != nil {
		return err
	}
	type oldPoint struct {
		uid     string
		version int64
		raw     string
		sealed  []byte
	}
	points := []oldPoint{}
	for rows.Next() {
		var p oldPoint
		if err = rows.Scan(&p.uid, &p.version, &p.raw, &p.sealed); err != nil {
			break
		}
		points = append(points, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range points {
		var point model.ContactPoint
		if json.Unmarshal([]byte(p.raw), &point) != nil || p.version < 1 || !model.ValidReceiverIdentity(p.uid, point.Name) {
			return model.ErrInvalidReceiver
		}
		secrets, err := s.openSecrets("contact-point:"+p.uid, p.sealed)
		if err != nil {
			return ErrReceiverSnapshot
		}
		secrets["url"] = point.URL
		public := point
		public.URL = ""
		public.SecureFields = receiverSecureFields(secrets)
		i := model.ReceiverIntegration{UID: p.uid, Type: "webhook", ConfigVersion: "legacy", Revision: p.version, ChannelEpoch: 1, Disabled: point.Disabled, DisableResolveMessage: point.DisableResolveMessage, Settings: marshalReceiver(public), SecureFields: receiverSecureFields(secrets)}
		snapshot := receiverConfigSnapshot{Integration: i, Secrets: secrets, LegacyPoint: &point}
		sealed, err := s.sealReceiver("integration", p.uid, p.version, snapshot)
		if err != nil {
			return err
		}
		value := model.Receiver{UID: p.uid, Name: point.Name, Version: p.version, UpdatedAt: point.UpdatedAt, Integrations: []model.ReceiverIntegration{i}}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receivers(uid,version,name,updated_at,config) VALUES(?,?,?,?,?)`, p.uid, p.version, point.Name, point.UpdatedAt, string(marshalReceiver(value))); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_integrations(uid,receiver_uid,type,config_version,revision,channel_epoch) VALUES(?,?,'webhook','legacy',?,1)`, p.uid, p.uid, p.version); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_integration_revisions(uid,revision,channel_epoch,config,sealed,fingerprint,destination_key) VALUES(?,?,1,?,?,?,?)`, p.uid, p.version, string(marshalReceiver(i)), sealed, s.receiverHash("integration-config", snapshot), s.receiverHash("destination", point.URL)); err != nil {
			return err
		}
	}
	// The bridge is an inert encrypted audit snapshot, never a second dispatcher.
	// Legacy rows lack per-channel historical ACKs and cannot fabricate receipts.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_notification_outbox'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,contact_point_uid,state,payload FROM alert_notification_outbox ORDER BY id`)
	if err != nil {
		return err
	}
	type oldDelivery struct {
		id                  int64
		uid, state, payload string
	}
	out := []oldDelivery{}
	for rows.Next() {
		var d oldDelivery
		if err = rows.Scan(&d.id, &d.uid, &d.state, &d.payload); err != nil {
			break
		}
		out = append(out, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var hasRender int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='alert_notification_render_snapshots'`).Scan(&hasRender); err != nil {
		return err
	}
	for _, d := range out {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receivers WHERE uid=?`, d.uid).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		snapshot := map[string]any{"payload": d.payload}
		if hasRender > 0 {
			var rendered, bundle string
			err = tx.QueryRowContext(ctx, `SELECT r.rendered_payload,b.config FROM alert_notification_render_snapshots r JOIN alert_notification_template_bundles b ON b.hash=r.bundle_hash WHERE r.notification_id=?`, d.id).Scan(&rendered, &bundle)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				snapshot["rendered_payload"] = rendered
				snapshot["template_bundle"] = bundle
			}
		}
		sealed, err := s.sealReceiver("legacy-outbox", fmt.Sprint(d.id), 0, snapshot)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_legacy_outbox VALUES(?,?,?,?)`, d.id, d.uid, d.state, sealed); err != nil {
			return err
		}
	}
	return nil
}

func receiverTx(ctx context.Context, tx *sql.Tx, uid string) (model.Receiver, error) {
	var value model.Receiver
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT config FROM alert_receivers WHERE uid=? AND deleted=0`, uid).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}
func (s *Store) Receiver(ctx context.Context, uid string) (model.Receiver, error) {
	var value model.Receiver
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT config FROM alert_receivers WHERE uid=? AND deleted=0`, uid).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}
func (s *Store) Receivers(ctx context.Context) ([]model.Receiver, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config FROM alert_receivers WHERE deleted=0 ORDER BY name COLLATE NOCASE,uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []model.Receiver{}
	for rows.Next() {
		var raw string
		var v model.Receiver
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

func (s *Store) SMTPProfile(ctx context.Context, uid string) (model.SMTPProfile, error) {
	var v model.SMTPProfile
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT config FROM alert_smtp_profiles WHERE uid=? AND deleted=0`, uid).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}
func (s *Store) SMTPProfiles(ctx context.Context) ([]model.SMTPProfile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config FROM alert_smtp_profiles WHERE deleted=0 ORDER BY name COLLATE NOCASE,uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []model.SMTPProfile{}
	for rows.Next() {
		var raw string
		var v model.SMTPProfile
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *Store) profileSnapshotTx(ctx context.Context, tx *sql.Tx, uid string, revision int64) (smtpProfileSnapshot, error) {
	var v smtpProfileSnapshot
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT sealed FROM alert_smtp_profile_revisions WHERE uid=? AND revision=?`, uid, revision).Scan(&sealed)
	if err == nil {
		err = s.openReceiver("smtp-profile", uid, revision, sealed, &v)
	}
	return v, err
}
func (s *Store) integrationSnapshotTx(ctx context.Context, tx *sql.Tx, uid string, revision int64) (receiverConfigSnapshot, error) {
	var v receiverConfigSnapshot
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT sealed FROM alert_receiver_integration_revisions WHERE uid=? AND revision=?`, uid, revision).Scan(&sealed)
	if err == nil {
		err = s.openReceiver("integration", uid, revision, sealed, &v)
	}
	return v, err
}

func (s *Store) SaveSMTPProfile(ctx context.Context, input model.SMTPProfileInput) (model.SMTPProfile, error) {
	if input.UID == "" {
		input.UID = uuid.NewString()
	}
	input.Name = strings.TrimSpace(input.Name)
	if !model.ValidReceiverIdentity(input.UID, input.Name) || input.Version < 0 {
		return model.SMTPProfile{}, model.ErrInvalidReceiver
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.SMTPProfile{}, err
	}
	defer tx.Rollback()
	var version, revision int64
	var name string
	var deleted int
	err = tx.QueryRowContext(ctx, `SELECT version,revision,name,deleted FROM alert_smtp_profiles WHERE uid=?`, input.UID).Scan(&version, &revision, &name, &deleted)
	creating := errors.Is(err, sql.ErrNoRows)
	if err != nil && !creating {
		return model.SMTPProfile{}, err
	}
	if deleted != 0 || input.Version != version {
		return model.SMTPProfile{}, ErrReceiverConflict
	}
	if creating {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_smtp_profiles WHERE deleted=0`).Scan(&count); err != nil {
			return model.SMTPProfile{}, err
		}
		if count >= 100 {
			return model.SMTPProfile{}, model.ErrInvalidReceiver
		}
	}
	var names int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_smtp_profiles WHERE uid<>? AND deleted=0 AND lower(name)=lower(?)`, input.UID, input.Name).Scan(&names); err != nil {
		return model.SMTPProfile{}, err
	}
	if names > 0 && name != input.Name {
		return model.SMTPProfile{}, ErrReceiverConflict
	}
	previous := smtpProfileSnapshot{Secrets: map[string]string{}}
	if !creating {
		previous, err = s.profileSnapshotTx(ctx, tx, input.UID, revision)
		if err != nil {
			return model.SMTPProfile{}, err
		}
	}
	secret, err := receiverApplySecrets("smtp", previous.Secrets, input.SecureSettings, input.ClearSecrets)
	if err != nil {
		return model.SMTPProfile{}, err
	}
	var settings smtpProfileSettings
	if err = strictReceiverJSON(input.Settings, &settings); err != nil {
		return model.SMTPProfile{}, err
	}
	if settings.TimeoutSeconds == 0 {
		settings.TimeoutSeconds = 10
	}
	if settings.AuthMode == "" {
		settings.AuthMode = "auto"
	}
	if settings.StartTLSPolicy == "" {
		settings.StartTLSPolicy = receivers.OpportunisticStartTLS
	}
	if settings.EhloIdentity == "" {
		settings.EhloIdentity = "localhost"
	}
	snapshot := smtpProfileSnapshot{Settings: marshalReceiver(settings), Secrets: secret}
	if _, err = smtpTransport(snapshot); err != nil {
		return model.SMTPProfile{}, err
	}
	fingerprint := s.receiverHash("profile-config", snapshot)
	oldFingerprint := ""
	if !creating {
		if err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM alert_smtp_profile_revisions WHERE uid=? AND revision=?`, input.UID, revision).Scan(&oldFingerprint); err != nil {
			return model.SMTPProfile{}, err
		}
	}
	changed := fingerprint != oldFingerprint
	if changed {
		revision++
	}
	v := model.SMTPProfile{UID: input.UID, Name: input.Name, Version: version + 1, Revision: revision, UpdatedAt: time.Now().UnixMilli(), Settings: snapshot.Settings, SecureFields: receiverSecureFields(secret)}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_smtp_profiles(uid,version,name,revision,updated_at,config) VALUES(?,?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET version=excluded.version,name=excluded.name,revision=excluded.revision,updated_at=excluded.updated_at,config=excluded.config`, v.UID, v.Version, v.Name, v.Revision, v.UpdatedAt, string(marshalReceiver(v))); err != nil {
		return v, err
	}
	if changed {
		sealed, err := s.sealReceiver("smtp-profile", v.UID, revision, snapshot)
		if err != nil {
			return v, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_smtp_profile_revisions VALUES(?,?,?,?,?)`, v.UID, revision, string(v.Settings), sealed, fingerprint); err != nil {
			return v, err
		}
	}
	return v, tx.Commit()
}

func (s *Store) SaveReceiver(ctx context.Context, input model.ReceiverInput) (model.Receiver, error) {
	if input.UID == "" {
		input.UID = uuid.NewString()
	}
	input.Name = strings.TrimSpace(input.Name)
	for index := range input.Integrations {
		i := &input.Integrations[index]
		if i.UID == "" {
			i.UID = uuid.NewString()
		}
		if i.ConfigVersion == "" {
			i.ConfigVersion = "v1"
		}
	}
	if err := input.Validate(); err != nil {
		return model.Receiver{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Receiver{}, err
	}
	defer tx.Rollback()
	var version int64
	var oldName string
	var deleted int
	err = tx.QueryRowContext(ctx, `SELECT version,name,deleted FROM alert_receivers WHERE uid=?`, input.UID).Scan(&version, &oldName, &deleted)
	creating := errors.Is(err, sql.ErrNoRows)
	if err != nil && !creating {
		return model.Receiver{}, err
	}
	if deleted != 0 || input.Version != version {
		return model.Receiver{}, ErrReceiverConflict
	}
	var names int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receivers WHERE uid<>? AND deleted=0 AND lower(name)=lower(?)`, input.UID, input.Name).Scan(&names); err != nil {
		return model.Receiver{}, err
	}
	if names > 0 && oldName != input.Name {
		return model.Receiver{}, ErrReceiverConflict
	}
	if creating {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receivers WHERE deleted=0`).Scan(&count); err != nil {
			return model.Receiver{}, err
		}
		if count >= 100 {
			return model.Receiver{}, model.ErrInvalidReceiver
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receivers(uid,version,name,updated_at,config) VALUES(?,0,?,0,'{}')`, input.UID, input.Name); err != nil {
			return model.Receiver{}, err
		}
	}
	values := []model.ReceiverIntegration{}
	seen := map[string]bool{}
	for _, in := range input.Integrations {
		var owner, kind, configVersion string
		var revision, epoch int64
		var tombstone int
		err = tx.QueryRowContext(ctx, `SELECT receiver_uid,type,config_version,revision,channel_epoch,deleted FROM alert_receiver_integrations WHERE uid=?`, in.UID).Scan(&owner, &kind, &configVersion, &revision, &epoch, &tombstone)
		newIntegration := errors.Is(err, sql.ErrNoRows)
		if err != nil && !newIntegration {
			return model.Receiver{}, err
		}
		if !newIntegration && (owner != input.UID || kind != in.Type || configVersion != in.ConfigVersion || tombstone != 0 || in.Revision != revision) {
			return model.Receiver{}, ErrReceiverConflict
		}
		if newIntegration && in.Revision != 0 {
			return model.Receiver{}, ErrReceiverConflict
		}
		previous := receiverConfigSnapshot{Secrets: map[string]string{}}
		if !newIntegration {
			previous, err = s.integrationSnapshotTx(ctx, tx, in.UID, revision)
			if err != nil {
				return model.Receiver{}, err
			}
		}
		i := in.ReceiverIntegration
		i.Revision = 0
		i.ChannelEpoch = 0
		i.SecureFields = nil
		var secrets map[string]string
		var destination any
		switch i.Type {
		case "email":
			if i.ConfigVersion != "v1" || i.SMTPProfileUID == "" || len(in.SecureSettings)+len(in.ClearSecrets) > 0 {
				return model.Receiver{}, model.ErrInvalidReceiver
			}
			var settings receivers.EmailSettings
			if err = strictReceiverJSON(i.Settings, &settings); err != nil {
				return model.Receiver{}, err
			}
			var current int64
			if err = tx.QueryRowContext(ctx, `SELECT revision FROM alert_smtp_profiles WHERE uid=? AND deleted=0`, i.SMTPProfileUID).Scan(&current); err != nil {
				return model.Receiver{}, err
			}
			if i.SMTPProfileRevision == 0 {
				i.SMTPProfileRevision = current
			}
			profile, err := s.profileSnapshotTx(ctx, tx, i.SMTPProfileUID, i.SMTPProfileRevision)
			if err != nil {
				return model.Receiver{}, err
			}
			transport, err := smtpTransport(profile)
			if err != nil {
				return model.Receiver{}, err
			}
			if _, err = transport.Prepare(settings, receivers.EmailContent{Subject: "validation", TextBody: "validation"}, receivers.Metadata{DeliveryID: "configuration-validation", CreatedAt: time.Unix(1, 0)}); err != nil {
				return model.Receiver{}, model.ErrInvalidReceiver
			}
			i.Settings = marshalReceiver(settings)
			secrets = map[string]string{}
			var p smtpProfileSettings
			_ = strictReceiverJSON(profile.Settings, &p)
			destination = []any{p.Host, p.FromAddress, p.AuthUser, settings.Addresses}
		case "slack":
			if i.ConfigVersion != "v1" || i.SMTPProfileUID != "" || i.SMTPProfileRevision != 0 {
				return model.Receiver{}, model.ErrInvalidReceiver
			}
			secrets, err = receiverApplySecrets("slack", previous.Secrets, in.SecureSettings, in.ClearSecrets)
			if err != nil {
				return model.Receiver{}, err
			}
			var settings slackIntegrationSettings
			if err = strictReceiverJSON(i.Settings, &settings); err != nil {
				return model.Receiver{}, err
			}
			if settings.TimeoutSeconds == 0 {
				settings.TimeoutSeconds = 10
			}
			if settings.EndpointURL == "" {
				settings.EndpointURL = receivers.SlackAPIURL
			}
			i.Settings = marshalReceiver(settings)
			transport, err := slackTransport(receiverConfigSnapshot{Integration: i, Secrets: secrets})
			if err != nil {
				return model.Receiver{}, err
			}
			transport.Close()
			destination = []any{settings.EndpointURL, settings.Recipient, secrets["url"], secrets["token"]}
		case "webhook":
			// Legacy snapshots have no standalone webhook adapter. Keep them readable
			// and preserve name/order edits; do not accept unimplemented new configs.
			if newIntegration || i.ConfigVersion != "legacy" || len(in.SecureSettings)+len(in.ClearSecrets) > 0 || string(i.Settings) != string(previous.Integration.Settings) || i.Disabled != previous.Integration.Disabled || i.DisableResolveMessage != previous.Integration.DisableResolveMessage {
				return model.Receiver{}, model.ErrInvalidReceiver
			}
			values = append(values, previous.Integration)
			seen[i.UID] = true
			continue
		default:
			return model.Receiver{}, model.ErrInvalidReceiver
		}
		i.SecureFields = receiverSecureFields(secrets)
		snapshot := receiverConfigSnapshot{Integration: i, Secrets: secrets}
		fingerprint := s.receiverHash("integration-config", snapshot)
		destinationKey := s.receiverHash("destination", destination)
		oldFingerprint, oldDestination := "", ""
		if !newIntegration {
			if err = tx.QueryRowContext(ctx, `SELECT fingerprint,destination_key FROM alert_receiver_integration_revisions WHERE uid=? AND revision=?`, i.UID, revision).Scan(&oldFingerprint, &oldDestination); err != nil {
				return model.Receiver{}, err
			}
		}
		changed := fingerprint != oldFingerprint
		if changed {
			revision++
		}
		if destinationKey != oldDestination {
			epoch++
		}
		i.Revision = revision
		i.ChannelEpoch = epoch
		snapshot.Integration = i
		if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_integrations(uid,receiver_uid,type,config_version,revision,channel_epoch) VALUES(?,?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET revision=excluded.revision,channel_epoch=excluded.channel_epoch`, i.UID, input.UID, i.Type, i.ConfigVersion, revision, epoch); err != nil {
			return model.Receiver{}, err
		}
		if changed {
			sealed, err := s.sealReceiver("integration", i.UID, revision, snapshot)
			if err != nil {
				return model.Receiver{}, err
			}
			var profileUID, profileRevision any
			if i.SMTPProfileUID != "" {
				profileUID = i.SMTPProfileUID
				profileRevision = i.SMTPProfileRevision
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO alert_receiver_integration_revisions VALUES(?,?,?,?,?,?,?,?,?)`, i.UID, revision, epoch, profileUID, profileRevision, string(marshalReceiver(i)), sealed, fingerprint, destinationKey); err != nil {
				return model.Receiver{}, err
			}
		}
		values = append(values, i)
		seen[i.UID] = true
		if i.Disabled || i.DisableResolveMessage {
			if err = s.cancelReceiverIntegrationTx(ctx, tx, i.UID, !i.Disabled, "receiver_disabled", time.Now().UnixMilli()); err != nil {
				return model.Receiver{}, err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT uid FROM alert_receiver_integrations WHERE receiver_uid=? AND deleted=0`, input.UID)
	if err != nil {
		return model.Receiver{}, err
	}
	removed := []string{}
	for rows.Next() {
		var uid string
		if err = rows.Scan(&uid); err != nil {
			break
		}
		if !seen[uid] {
			removed = append(removed, uid)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return model.Receiver{}, err
	}
	for _, uid := range removed {
		if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_integrations SET deleted=1 WHERE uid=?`, uid); err != nil {
			return model.Receiver{}, err
		}
		if err = s.cancelReceiverIntegrationTx(ctx, tx, uid, false, "integration_deleted", time.Now().UnixMilli()); err != nil {
			return model.Receiver{}, err
		}
	}
	v := model.Receiver{UID: input.UID, Name: input.Name, Version: version + 1, UpdatedAt: time.Now().UnixMilli(), Integrations: values}
	if _, err = tx.ExecContext(ctx, `UPDATE alert_receivers SET version=?,name=?,updated_at=?,config=? WHERE uid=? AND version=? AND deleted=0`, v.Version, v.Name, v.UpdatedAt, string(marshalReceiver(v)), v.UID, version); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (s *Store) DeleteReceiver(ctx context.Context, uid string, version int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := receiverTx(ctx, tx, uid)
	if err != nil {
		return err
	}
	if version <= 0 || v.Version != version {
		return ErrReceiverConflict
	}
	for _, i := range v.Integrations {
		if err = s.cancelReceiverIntegrationTx(ctx, tx, i.UID, false, "receiver_deleted", time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE alert_receiver_integrations SET deleted=1 WHERE receiver_uid=?`, uid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE alert_receivers SET deleted=1,version=version+1 WHERE uid=? AND version=?`, uid, version); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeleteSMTPProfile(ctx context.Context, uid string, version int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM alert_smtp_profiles WHERE uid=? AND deleted=0`, uid).Scan(&current); err != nil {
		return err
	}
	if version <= 0 || version != current {
		return ErrReceiverConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_receiver_integrations i JOIN alert_receiver_integration_revisions r ON r.uid=i.uid AND r.revision=i.revision WHERE i.deleted=0 AND r.profile_uid=?`, uid).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrReceiverReferenced
	}
	if _, err = tx.ExecContext(ctx, `UPDATE alert_smtp_profiles SET deleted=1,version=version+1 WHERE uid=? AND version=?`, uid, version); err != nil {
		return err
	}
	return tx.Commit()
}
