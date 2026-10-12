package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func receiverStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	require.NoError(t, s.InitializeReceiverSchema(context.Background()))
	t.Cleanup(func() { _ = s.DB.Close() })
	return s
}
func receiverInput(v model.Receiver) model.ReceiverInput {
	in := model.ReceiverInput{UID: v.UID, Name: v.Name, Version: v.Version}
	for _, i := range v.Integrations {
		in.Integrations = append(in.Integrations, model.ReceiverIntegrationInput{ReceiverIntegration: i})
	}
	return in
}
func receiverSlackInput(uid, name string, urls ...string) model.ReceiverInput {
	in := model.ReceiverInput{UID: uid, Name: name}
	for index, url := range urls {
		letter := string(rune('a' + index))
		in.Integrations = append(in.Integrations, model.ReceiverIntegrationInput{ReceiverIntegration: model.ReceiverIntegration{UID: uid + "-" + letter, Type: "slack", Settings: json.RawMessage(`{"recipient":"C123","text":"{{ .Status }}"}`)}, SecureSettings: map[string]string{"url": url}})
	}
	return in
}

func TestReceiverConfigurationCASSecretsRevisionsAndTombstones(t *testing.T) {
	ctx := context.Background()
	s := receiverStore(t)
	in := receiverSlackInput("ops", "Operations", "https://hooks.example.test/private-hook-token")
	in.Integrations[0].SecureSettings["token"] = "bot-private-token"
	v, err := s.SaveReceiver(ctx, in)
	require.NoError(t, err)
	require.Equal(t, int64(1), v.Version)
	require.Equal(t, int64(1), v.Integrations[0].Revision)
	assert.Equal(t, int64(1), v.Integrations[0].ChannelEpoch)
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-hook-token")
	assert.NotContains(t, string(raw), "bot-private-token")
	assert.True(t, v.Integrations[0].SecureFields["url"])
	_, err = s.SaveReceiver(ctx, in)
	require.ErrorIs(t, err, ErrReceiverConflict)
	edit := receiverInput(v)
	edit.Name = "Renamed"
	renamed, err := s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	assert.Equal(t, int64(2), renamed.Version)
	assert.Equal(t, int64(1), renamed.Integrations[0].Revision)
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	snapshot, err := s.integrationSnapshotTx(ctx, tx, v.Integrations[0].UID, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	assert.Equal(t, "bot-private-token", snapshot.Secrets["token"])
	edit = receiverInput(renamed)
	edit.Integrations[0].ClearSecrets = []string{"token"}
	cleared, err := s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	assert.False(t, cleared.Integrations[0].SecureFields["token"])
	assert.Equal(t, int64(2), cleared.Integrations[0].Revision)
	assert.Equal(t, int64(2), cleared.Integrations[0].ChannelEpoch)
	// A invalid clear rolls back and does not replace the last valid revision.
	bad := receiverInput(cleared)
	bad.Integrations[0].ClearSecrets = []string{"url"}
	_, err = s.SaveReceiver(ctx, bad)
	require.ErrorIs(t, err, model.ErrInvalidReceiver)
	got, err := s.Receiver(ctx, v.UID)
	require.NoError(t, err)
	assert.Equal(t, cleared, got)
	bad = receiverInput(cleared)
	bad.Integrations[0].Settings = json.RawMessage(`{"recipient":"C123","unsupported":true}`)
	_, err = s.SaveReceiver(ctx, bad)
	require.ErrorIs(t, err, model.ErrInvalidReceiver)
	bad = receiverInput(cleared)
	bad.Integrations[0].Type = "email"
	_, err = s.SaveReceiver(ctx, bad)
	require.ErrorIs(t, err, ErrReceiverConflict)
	conflict := receiverSlackInput("other", "Renamed", "https://example.test/hook")
	_, err = s.SaveReceiver(ctx, conflict)
	require.ErrorIs(t, err, ErrReceiverConflict)
	require.ErrorIs(t, s.DeleteReceiver(ctx, v.UID, 1), ErrReceiverConflict)
	require.NoError(t, s.DeleteReceiver(ctx, v.UID, cleared.Version))
	_, err = s.Receiver(ctx, v.UID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.SaveReceiver(ctx, in)
	require.ErrorIs(t, err, ErrReceiverConflict, "tombstones prevent UID reuse")
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_integration_revisions`).Scan(&count))
	assert.Equal(t, 2, count)
}

func TestReceiverSMTPProfileBindingAndHistoricalRevision(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	p, err := s.SaveSMTPProfile(ctx, model.SMTPProfileInput{SMTPProfile: model.SMTPProfile{UID: "smtp", Name: "Mail", Settings: json.RawMessage(`{"host":"127.0.0.1:2525","from_address":"sender@example.test","auth_user":"operator","starttls_policy":"MandatoryStartTLS"}`)}, SecureSettings: map[string]string{"password": "smtp-private-password"}})
	require.NoError(t, err)
	r, err := s.SaveReceiver(ctx, model.ReceiverInput{UID: "mail", Name: "Mail contact", Integrations: []model.ReceiverIntegrationInput{{ReceiverIntegration: model.ReceiverIntegration{UID: "email", Type: "email", SMTPProfileUID: p.UID, Settings: json.RawMessage(`{"addresses":"one@example.test;two@example.test","singleEmail":true}`)}}}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), r.Integrations[0].SMTPProfileRevision)
	edit := model.SMTPProfileInput{SMTPProfile: p}
	edit.Name = "Mail renamed"
	same, err := s.SaveSMTPProfile(ctx, edit)
	require.NoError(t, err)
	assert.Equal(t, p.Revision, same.Revision)
	edit.SMTPProfile = same
	edit.SecureSettings = map[string]string{"password": "smtp-new-password"}
	next, err := s.SaveSMTPProfile(ctx, edit)
	require.NoError(t, err)
	assert.Equal(t, int64(2), next.Revision)
	unchanged, err := s.Receiver(ctx, r.UID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), unchanged.Integrations[0].SMTPProfileRevision, "profile edits never implicitly rebind receivers")
	rebind := receiverInput(unchanged)
	rebind.Integrations[0].SMTPProfileRevision = 0
	r, err = s.SaveReceiver(ctx, rebind)
	require.NoError(t, err)
	assert.Equal(t, int64(2), r.Integrations[0].SMTPProfileRevision)
	assert.Equal(t, int64(2), r.Integrations[0].Revision)
	assert.Equal(t, int64(1), r.Integrations[0].ChannelEpoch, "credential change alone keeps SMTP destination identity")
	raw, err := json.Marshal(next)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "smtp-new-password")
	require.ErrorIs(t, s.DeleteSMTPProfile(ctx, p.UID, next.Version), ErrReceiverReferenced)
	require.NoError(t, s.DeleteReceiver(ctx, r.UID, r.Version))
	require.NoError(t, s.DeleteSMTPProfile(ctx, p.UID, next.Version))
	_, err = s.SaveSMTPProfile(ctx, model.SMTPProfileInput{SMTPProfile: p})
	require.ErrorIs(t, err, ErrReceiverConflict)
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	old, err := s.profileSnapshotTx(ctx, tx, p.UID, 1)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	assert.Equal(t, "smtp-private-password", old.Secrets["password"])
}

func TestReceiverEncryptionPurposeOrganizationUIDRevisionAndRestart(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	v, err := s.SaveReceiver(ctx, receiverSlackInput("secure", "Secure", "https://example.test/url-secret"))
	require.NoError(t, err)
	var sealed []byte
	var config string
	require.NoError(t, s.DB.QueryRow(`SELECT config,sealed FROM alert_receiver_integration_revisions WHERE uid=?`, v.Integrations[0].UID).Scan(&config, &sealed))
	assert.NotContains(t, config, "url-secret")
	assert.NotContains(t, string(sealed), "url-secret")
	var value receiverConfigSnapshot
	require.NoError(t, s.openReceiver("integration", v.Integrations[0].UID, 1, sealed, &value))
	assert.Equal(t, "https://example.test/url-secret", value.Secrets["url"])
	for _, tc := range []struct {
		kind, uid string
		rev       int64
	}{{"smtp-profile", v.Integrations[0].UID, 1}, {"integration", "other", 1}, {"integration", v.Integrations[0].UID, 2}} {
		assert.ErrorIs(t, s.openReceiver(tc.kind, tc.uid, tc.rev, sealed, &value), ErrReceiverSnapshot)
	}
	wrongOrg := &Store{DB: s.DB, secretKey: s.secretKey, organizationID: 2}
	assert.ErrorIs(t, wrongOrg.openReceiver("integration", v.Integrations[0].UID, 1, sealed, &value), ErrReceiverSnapshot)
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	assert.ErrorIs(t, s.openReceiver("integration", v.Integrations[0].UID, 1, tampered, &value), ErrReceiverSnapshot)
	wrongKey := &Store{DB: s.DB, secretKey: make([]byte, 32)}
	assert.ErrorIs(t, wrongKey.InitializeReceiverSchema(ctx), ErrReceiverSnapshot)
	path := s.Path
	require.NoError(t, s.DB.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	defer reopened.DB.Close()
	require.NoError(t, reopened.InitializeReceiverSchema(ctx))
	got, err := reopened.Receiver(ctx, v.UID)
	require.NoError(t, err)
	assert.Equal(t, v, got)
}

func TestReceiverLegacyMigrationPreservesBusinessDataAndDoesNotDispatch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	for _, uid := range []string{"legacy-a", "legacy-b"} {
		token := "legacy-bearer"
		_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: model.ContactPoint{UID: uid, Name: "Duplicate legacy name", URL: "https://example.test/legacy-url-secret"}, BearerToken: &token})
		require.NoError(t, err)
	}
	legacy, err := s.ContactPoint(ctx, "legacy-a")
	require.NoError(t, err)
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: legacy})
	require.NoError(t, err)
	_, err = s.SaveNotificationPolicy(ctx, model.NotificationPolicy{Receiver: "legacy-a", Routes: []model.NotificationRoute{{Receiver: "legacy-b"}}})
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO alert_notification_outbox(event_id,contact_point_uid,rule_uid,instance_key,status,state,next_attempt_at,created_at,payload) VALUES(1,'legacy-a','rule','instance','firing','pending',1,1,'{"private":"old-payload-secret"}')`)
	require.NoError(t, err)
	var originalCP, originalPolicy, originalPayload string
	require.NoError(t, s.DB.QueryRow(`SELECT config FROM alert_contact_points WHERE uid='legacy-a'`).Scan(&originalCP))
	require.NoError(t, s.DB.QueryRow(`SELECT config FROM alert_notification_policy`).Scan(&originalPolicy))
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM alert_notification_outbox`).Scan(&originalPayload))
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	values, err := s.Receivers(ctx)
	require.NoError(t, err)
	require.Len(t, values, 2)
	assert.Equal(t, "legacy-a", values[0].UID)
	assert.Equal(t, int64(2), values[0].Version)
	assert.Equal(t, int64(2), values[0].Integrations[0].Revision)
	assert.Equal(t, "legacy", values[0].Integrations[0].ConfigVersion)
	var current string
	require.NoError(t, s.DB.QueryRow(`SELECT config FROM alert_contact_points WHERE uid='legacy-a'`).Scan(&current))
	assert.Equal(t, originalCP, current)
	require.NoError(t, s.DB.QueryRow(`SELECT config FROM alert_notification_policy`).Scan(&current))
	assert.Equal(t, originalPolicy, current)
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM alert_notification_outbox`).Scan(&current))
	assert.Equal(t, originalPayload, current)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_deliveries`).Scan(&count))
	assert.Zero(t, count)
	var encrypted []byte
	require.NoError(t, s.DB.QueryRow(`SELECT sealed FROM alert_receiver_legacy_outbox WHERE legacy_id=1`).Scan(&encrypted))
	assert.NotContains(t, string(encrypted), "old-payload-secret")
	raw, err := json.Marshal(values)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "legacy-url-secret")
	assert.NotContains(t, string(raw), "legacy-bearer")
	edit := receiverInput(values[0])
	edit.Name = "Legacy renamed"
	renamed, err := s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	assert.Equal(t, int64(2), renamed.Integrations[0].Revision)
	edit = receiverInput(values[1])
	edit.Name = "Duplicate legacy name"
	_, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err, "unchanged migrated duplicate name remains valid")
}

func TestReceiverIntegrationUIDOwnershipRemovalAndReorder(t *testing.T) {
	ctx := context.Background()
	s := receiverStore(t)
	r, err := s.SaveReceiver(ctx, receiverSlackInput("order", "Order", "https://example.test/a", "https://example.test/b"))
	require.NoError(t, err)
	edit := receiverInput(r)
	edit.Integrations[0], edit.Integrations[1] = edit.Integrations[1], edit.Integrations[0]
	r, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	assert.Equal(t, "order-b", r.Integrations[0].UID)
	for _, i := range r.Integrations {
		assert.Equal(t, int64(1), i.Revision)
	}
	other := receiverSlackInput("other", "Other", "https://example.test/c")
	other.Integrations[0].UID = r.Integrations[0].UID
	_, err = s.SaveReceiver(ctx, other)
	require.ErrorIs(t, err, ErrReceiverConflict)
	edit = receiverInput(r)
	removed := edit.Integrations[1]
	edit.Integrations = edit.Integrations[:1]
	r, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	edit = receiverInput(r)
	edit.Integrations = append(edit.Integrations, removed)
	_, err = s.SaveReceiver(ctx, edit)
	require.ErrorIs(t, err, ErrReceiverConflict, "removed integration identity cannot be reused")
	var config string
	require.NoError(t, s.DB.QueryRow(`SELECT config FROM alert_receiver_integration_revisions WHERE uid=? AND revision=1`, removed.UID).Scan(&config))
	assert.NotEmpty(t, config, "removal preserves immutable historical configuration")
}
