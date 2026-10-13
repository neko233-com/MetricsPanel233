package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeSourceStore(t *testing.T, url string) (*Store, model.ContactPoint, model.Receiver) {
	return nativeSourceStoreForOrganization(t, url, 1)
}

func nativeSourceStoreForOrganization(t *testing.T, url string, organizationID int64) (*Store, model.ContactPoint, model.Receiver) {
	t.Helper()
	ctx := context.Background()
	s, err := OpenForOrganization(filepath.Join(t.TempDir(), "source.db"), organizationID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.DB.Close() })
	point, err := s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "point", Name: "Source", URL: "http://127.0.0.1:1/legacy"}})
	require.NoError(t, err)
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	r, err := s.SaveReceiver(ctx, model.ReceiverInput{UID: point.UID, Name: point.Name, Version: 1, Integrations: []model.ReceiverIntegrationInput{{ReceiverIntegration: model.ReceiverIntegration{UID: "channel", Type: "slack", Settings: json.RawMessage(`{"recipient":"C123","text":"{{ .Status }}"}`)}, SecureSettings: map[string]string{"url": url}}}})
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE alert_receiver_ownership SET mode='native',epoch=2 WHERE id=1`)
	require.NoError(t, err)
	return s, point, r
}

func TestReceiverSourcePartitionPreservesExactOrganizationAndFrozenReceiverName(t *testing.T) {
	ctx := context.Background()
	s, point, r := nativeSourceStoreForOrganization(t, "http://127.0.0.1:1/channel", 9007199254740993)
	edit := receiverInput(r)
	edit.Name = "Native name"
	_, err := s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	at := time.Now()
	_, source := nativeSourceClaim(t, s, point, at)
	parts, err := s.ReceiverNotificationPartitions(ctx, source, at.Add(3*time.Millisecond), map[string][]string{"channel": {"C123"}})
	require.NoError(t, err)
	require.Len(t, parts, 1)
	var payload struct {
		OrganizationID int64  `json:"orgId"`
		Receiver       string `json:"receiver"`
	}
	require.NoError(t, json.Unmarshal(parts[0].Payload, &payload))
	assert.Equal(t, int64(9007199254740993), payload.OrganizationID)
	assert.Equal(t, "Native name", payload.Receiver)
}

func nativeSourceClaim(t *testing.T, s *Store, point model.ContactPoint, at time.Time) (model.NotificationDelivery, *ReceiverNotificationSource) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT OR REPLACE INTO alert_notification_active(rule_uid,instance_key,started_at) VALUES('rule','instance',?)`, at.UnixMilli())
	require.NoError(t, err)
	event := model.AlertEvent{Timestamp: at.UnixMilli(), Key: "instance", Labels: map[string]string{"alertname": "private-alert", "team": "ops"}, Annotations: map[string]string{"summary": "source-private-data"}}
	id, err := insertNotificationTx(receiverProducerContext(ctx, s), tx, 0, point.UID, "rule", "instance", "firing", webhookPayload(point, model.AlertRule{UID: "rule", Title: "private-alert"}, event, "firing", at.UnixMilli(), s.OrganizationID()), at.UnixMilli())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	claim, _, err := s.ClaimNotification(ctx, at.Add(time.Millisecond).UnixMilli())
	require.NoError(t, err)
	require.Equal(t, id, claim.ID)
	source, err := s.ReceiverNotificationSource(ctx, claim, at.Add(2*time.Millisecond))
	require.NoError(t, err)
	return claim, source
}

func nativeSourceInput(t *testing.T, s *Store, source *ReceiverNotificationSource, at time.Time) []model.ReceiverDispatchInput {
	t.Helper()
	ctx := context.Background()
	parts, err := s.ReceiverNotificationPartitions(ctx, source, at, map[string][]string{"channel": {"C123"}})
	require.NoError(t, err)
	inputs := []model.ReceiverDispatchInput{}
	for index, part := range parts {
		prepared, err := s.PrepareReceiverSlack(ctx, part.Integration.UID, part.Integration.Revision, receivers.SlackContent{Recipient: part.Target, Text: "source-private-data"}, receivers.Metadata{DeliveryID: fmt.Sprintf("source:%d:%d", source.ID, index), CreatedAt: time.UnixMilli(source.snapshot.Parent.CreatedAt)})
		require.NoError(t, err)
		periods := map[string][]model.ReceiverPeriod{}
		members := map[string][]model.ReceiverMember{}
		for _, op := range prepared.Operations() {
			periods[op.Key] = part.Periods
			members[op.Key] = part.Members
		}
		inputs = append(inputs, model.ReceiverDispatchInput{Status: part.Status, DataSnapshot: part.Payload, TemplateSnapshot: marshalReceiver(part.Definitions), Deliveries: []model.ReceiverPreparedDelivery{{IntegrationUID: part.Integration.UID, Revision: part.Integration.Revision, Prepared: prepared, Periods: periods, Members: members}}})
	}
	return inputs
}

func nativeGroupedSourceClaim(t *testing.T, s *Store, point model.ContactPoint, at time.Time) (model.NotificationDelivery, *ReceiverNotificationSource) {
	t.Helper()
	ctx := context.Background()
	groupBy := []string{"team"}
	_, err := s.SaveNotificationPolicy(ctx, model.NotificationPolicy{Receiver: point.UID, NotificationTiming: model.NotificationTiming{GroupBy: &groupBy, GroupWait: "0s", GroupInterval: "1s", RepeatInterval: "0s"}})
	require.NoError(t, err)
	rule, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "rule", Title: "Source rule", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	value := 1.0
	labels := map[string]string{"alertname": "Source rule", "team": "ops"}
	runtime := model.AlertRuntime{LastEvaluation: at.UnixMilli(), Instances: []model.AlertInstance{{Key: "instance", Labels: labels, State: "Firing", Value: &value, FiringAt: at.UnixMilli(), ActiveAt: at.UnixMilli()}}}
	event := model.AlertEvent{UID: rule.UID, Key: "instance", Timestamp: at.UnixMilli(), Labels: labels, Value: &value, From: "Normal", To: "Firing"}
	require.NoError(t, s.CommitAlertEvaluation(ctx, rule.UID, rule.Version, runtime, []model.AlertEvent{event}))
	claim, _, err := s.ClaimNotification(ctx, at.Add(time.Millisecond).UnixMilli())
	require.NoError(t, err)
	source, err := s.ReceiverNotificationSource(ctx, claim, at.Add(2*time.Millisecond))
	require.NoError(t, err)
	require.Positive(t, source.snapshot.Generation)
	return claim, source
}

func mutateNativeSourceGuard(t *testing.T, s *Store, claim model.NotificationDelivery, source *ReceiverNotificationSource, change string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	switch change {
	case "epoch":
		_, err := s.DB.Exec(`UPDATE alert_receiver_ownership SET epoch=epoch+1`)
		require.NoError(t, err)
	case "parent":
		_, err := s.DB.Exec(`UPDATE alert_notification_outbox SET attempts=attempts+1,last_attempt_at=last_attempt_at+1 WHERE id=?`, claim.ID)
		require.NoError(t, err)
	case "parent_expiry":
		_, err := s.DB.Exec(`UPDATE alert_notification_outbox SET next_attempt_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), claim.ID)
		require.NoError(t, err)
	case "silence":
		_, err := s.SaveSilence(ctx, model.Silence{Matchers: []model.SilenceMatcher{{Name: "team", Value: "ops"}}, StartsAt: at.Add(-time.Second), EndsAt: at.Add(time.Hour), CreatedBy: "test", Comment: "guard"}, at)
		require.NoError(t, err)
	case "active_period":
		_, err := s.DB.Exec(`UPDATE alert_notification_active SET started_at=started_at+1`)
		require.NoError(t, err)
	case "inhibition":
		_, err := s.DB.Exec(`INSERT INTO alert_inhibition_active(rule_uid,instance_key,labels,state,started_at) VALUES('source','source','{"alertname":"inhibitor","team":"ops"}','Firing',?)`, at.UnixMilli())
		require.NoError(t, err)
		_, err = s.SaveInhibitionRule(ctx, model.InhibitionRule{UID: "late-inhibitor", Name: "Late inhibitor", SourceMatchers: []model.NotificationMatcher{{Name: "alertname", Operator: "=", Value: "inhibitor"}}, TargetMatchers: []model.NotificationMatcher{{Name: "team", Operator: "=", Value: "ops"}}, Equal: []string{"team"}})
		require.NoError(t, err)
	case "generation":
		_, err := s.DB.Exec(`UPDATE alert_notification_groups SET generation=generation+1 WHERE group_key=?`, source.snapshot.GroupKey)
		require.NoError(t, err)
	case "time_route":
		_, err := s.SaveMuteTiming(ctx, model.MuteTiming{Name: "always-muted", TimeIntervals: []json.RawMessage{json.RawMessage(`{}`)}})
		require.NoError(t, err)
		var raw string
		require.NoError(t, s.DB.QueryRow(`SELECT options FROM alert_notification_groups WHERE group_key=?`, source.snapshot.GroupKey).Scan(&raw))
		var opts model.NotificationRouteOptions
		require.NoError(t, json.Unmarshal([]byte(raw), &opts))
		opts.MuteTimeIntervals = []string{"always-muted"}
		_, err = s.DB.Exec(`UPDATE alert_notification_groups SET options=? WHERE group_key=?`, string(marshalReceiver(opts)), source.snapshot.GroupKey)
		require.NoError(t, err)
	default:
		t.Fatal("unknown source guard mutation")
	}
}

func TestReceiverSourceFreezesAllProducerTemplatesReceiverRevisionAndPrivateMetadata(t *testing.T) {
	ctx := context.Background()
	s, point, r := nativeSourceStore(t, "http://127.0.0.1:1/channel")
	template, err := s.SaveNotificationTemplate(ctx, model.NotificationTemplate{Name: "unused-by-legacy-point", Template: `{{define "future.body"}}producer-version{{end}}`})
	require.NoError(t, err)
	at := time.Now()
	claim, source := nativeSourceClaim(t, s, point, at)
	var legacyBundles int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_notification_render_snapshots WHERE notification_id=?`, claim.ID).Scan(&legacyBundles))
	assert.Zero(t, legacyBundles)
	template.Template = `{{define "future.body"}}new-version{{end}}`
	_, err = s.SaveNotificationTemplate(ctx, template)
	require.NoError(t, err)
	edit := receiverInput(r)
	edit.Name = "New receiver"
	edit.Integrations[0].Settings = json.RawMessage(`{"recipient":"C999","text":"edited"}`)
	_, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	loaded, err := s.ReceiverNotificationSource(ctx, claim, at.Add(3*time.Millisecond))
	require.NoError(t, err)
	assert.Equal(t, source.Fingerprint, loaded.Fingerprint)
	assert.Equal(t, r.Version, loaded.ReceiverVersion)
	require.Len(t, loaded.snapshot.Definitions, 1)
	assert.Contains(t, loaded.snapshot.Definitions[0].Template, "producer-version")
	assert.Equal(t, "Source", loaded.snapshot.Receiver.Name)
	assert.Equal(t, "firing", loaded.snapshot.Members[0].Status)
	assert.Equal(t, at.UnixMilli(), loaded.snapshot.Members[0].Period.StartedAt)
	metadata, err := json.Marshal(loaded)
	require.NoError(t, err)
	assert.NotContains(t, string(metadata), "source-private-data")
	assert.NotContains(t, string(metadata), "producer-version")
	var sealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT sealed FROM alert_receiver_sources WHERE notification_id=?`, claim.ID).Scan(&sealed))
	assert.NotContains(t, string(sealed), "source-private-data")
	assert.NotContains(t, string(sealed), "producer-version")
	inputs := nativeSourceInput(t, s, loaded, at.Add(4*time.Millisecond))
	require.NoError(t, s.EnqueueReceiverSourceDispatches(ctx, loaded, inputs, at.Add(5*time.Millisecond)))
	var revision int64
	require.NoError(t, s.DB.QueryRow(`SELECT revision FROM alert_receiver_deliveries`).Scan(&revision))
	assert.Equal(t, int64(1), revision)
}

func TestReceiverNativeProducerMissingCapabilityRollsBackEntireEventTransaction(t *testing.T) {
	ctx := context.Background()
	s, point, _ := nativeSourceStore(t, "http://127.0.0.1:1/channel")
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO alert_events(uid,timestamp,payload) VALUES('rule',1,'{}')`)
	require.NoError(t, err)
	_, err = insertNotificationTx(ctx, tx, 0, point.UID, "rule", "instance", "firing", map[string]any{"alerts": []any{}}, time.Now().UnixMilli())
	require.ErrorIs(t, err, ErrReceiverSourceChanged)
	require.NoError(t, tx.Rollback())
	for _, table := range []string{"alert_events", "alert_notification_outbox", "alert_receiver_sources"} {
		var count int
		require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		assert.Zero(t, count, table)
	}
}

func TestReceiverNativeProducerMethodsFreezeEvaluationAndRuleGroupResolutions(t *testing.T) {
	for _, mutation := range []string{"save_rule", "delete_rule", "save_group", "delete_group"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			s, point, _ := nativeSourceStore(t, "http://127.0.0.1:1/channel")
			_, err := s.SaveNotificationPolicy(ctx, model.NotificationPolicy{Receiver: point.UID})
			require.NoError(t, err)
			rule, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "rule", Title: "Rule", Expr: "vector(1)", Condition: "nonzero"})
			require.NoError(t, err)
			at := time.Now()
			value := 1.0
			labels := map[string]string{"alertname": "Rule", "team": "ops"}
			runtime := model.AlertRuntime{LastEvaluation: at.UnixMilli(), Instances: []model.AlertInstance{{Key: "instance", State: "Firing", Labels: labels, Value: &value, FiringAt: at.UnixMilli(), ActiveAt: at.UnixMilli()}}}
			event := model.AlertEvent{UID: rule.UID, Key: "instance", Timestamp: at.UnixMilli(), From: "Normal", To: "Firing", Labels: labels, Value: &value}
			require.NoError(t, s.CommitAlertEvaluation(ctx, rule.UID, rule.Version, runtime, []model.AlertEvent{event}))
			switch mutation {
			case "save_rule":
				rule.Paused = true
				_, err = s.SaveAlertRule(ctx, rule.AlertRule)
			case "delete_rule":
				err = s.DeleteAlertRule(ctx, rule.UID)
			case "save_group":
				changed := rule.AlertRule
				changed.Paused = true
				_, err = s.ReplaceAlertRuleGroup(ctx, rule.FolderUID, rule.Group, rule.IntervalSeconds, []model.AlertRule{changed}, "")
			case "delete_group":
				_, err = s.ReplaceAlertRuleGroup(ctx, rule.FolderUID, rule.Group, rule.IntervalSeconds, []model.AlertRule{}, "")
			}
			require.NoError(t, err)
			rows, err := s.DB.Query(`SELECT notification_id,status,owner_epoch FROM alert_receiver_sources ORDER BY notification_id`)
			require.NoError(t, err)
			defer rows.Close()
			statuses := []string{}
			for rows.Next() {
				var id, epoch int64
				var status string
				require.NoError(t, rows.Scan(&id, &status, &epoch))
				assert.Equal(t, int64(2), epoch)
				statuses = append(statuses, status)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, []string{"firing", "resolved"}, statuses, "every authorized producer freezes the source inside its alert-event transaction")
		})
	}
}

func TestReceiverNativeMissingReceiverRollsBackEvaluationRuntimeAndEvent(t *testing.T) {
	ctx := context.Background()
	s, point, _ := nativeSourceStore(t, "http://127.0.0.1:1/channel")
	_, err := s.SaveNotificationPolicy(ctx, model.NotificationPolicy{Receiver: point.UID})
	require.NoError(t, err)
	rule, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "rule", Title: "Rule", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE alert_receivers SET deleted=1 WHERE uid=?`, point.UID)
	require.NoError(t, err)
	at := time.Now()
	runtime := model.AlertRuntime{LastEvaluation: at.UnixMilli(), Instances: []model.AlertInstance{{Key: "instance", State: "Firing", Labels: map[string]string{"team": "ops"}, FiringAt: at.UnixMilli()}}}
	err = s.CommitAlertEvaluation(ctx, rule.UID, rule.Version, runtime, []model.AlertEvent{{UID: rule.UID, Key: "instance", Timestamp: at.UnixMilli(), From: "Normal", To: "Firing", Labels: map[string]string{"team": "ops"}}})
	require.ErrorIs(t, err, ErrReceiverSourceChanged)
	for _, table := range []string{"alert_events", "alert_notification_outbox", "alert_notification_active", "alert_inhibition_active"} {
		var count int
		require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		assert.Zero(t, count, table)
	}
	view, err := s.AlertRule(ctx, rule.UID)
	require.NoError(t, err)
	assert.Zero(t, view.Runtime.LastEvaluation)
	assert.Empty(t, view.Runtime.Instances)
}

func TestReceiverSourceLinkCASParentLeaseEpochAndSuppressionGuards(t *testing.T) {
	for _, change := range []string{"epoch", "parent", "silence", "active_period", "tampered_fingerprint"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, point, _ := nativeSourceStore(t, "http://127.0.0.1:1/channel")
			at := time.Now()
			claim, source := nativeSourceClaim(t, s, point, at)
			inputs := nativeSourceInput(t, s, source, at.Add(3*time.Millisecond))
			switch change {
			case "epoch":
				_, err := s.DB.Exec(`UPDATE alert_receiver_ownership SET epoch=epoch+1`)
				require.NoError(t, err)
			case "parent":
				_, err := s.DB.Exec(`UPDATE alert_notification_outbox SET attempts=attempts+1,last_attempt_at=last_attempt_at+1 WHERE id=?`, claim.ID)
				require.NoError(t, err)
			case "silence":
				_, err := s.SaveSilence(ctx, model.Silence{Matchers: []model.SilenceMatcher{{Name: "team", Value: "ops"}}, StartsAt: at.Add(-time.Second), EndsAt: at.Add(time.Hour), CreatedBy: "test", Comment: "guard"}, at)
				require.NoError(t, err)
			case "active_period":
				_, err := s.DB.Exec(`UPDATE alert_notification_active SET started_at=started_at+1`)
				require.NoError(t, err)
			case "tampered_fingerprint":
				_, err := s.DB.Exec(`UPDATE alert_receiver_sources SET fingerprint='tampered'`)
				require.NoError(t, err)
			}
			err := s.EnqueueReceiverSourceDispatches(ctx, source, inputs, at.Add(5*time.Millisecond))
			require.Error(t, err)
			var count int
			require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_dispatches`).Scan(&count))
			assert.Zero(t, count, "CAS failure must not orphan dispatches")
		})
	}
}

func TestReceiverSourcePreNetworkAndCheckpointRevalidateParentGuards(t *testing.T) {
	for _, stage := range []string{"pre_network", "checkpoint"} {
		for _, change := range []string{"epoch", "parent", "parent_expiry", "silence", "inhibition", "active_period", "generation", "time_route"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
				defer server.Close()
				ctx := context.Background()
				s, point, _ := nativeSourceStore(t, server.URL)
				at := time.Now()
				parent, source := nativeGroupedSourceClaim(t, s, point, at)
				inputs := nativeSourceInput(t, s, source, at.Add(3*time.Millisecond))
				require.NoError(t, s.EnqueueReceiverSourceDispatches(ctx, source, inputs, at.Add(4*time.Millisecond)))
				_, err := s.ClaimReceiverDelivery(ctx, at.Add(5*time.Millisecond), time.Minute)
				require.ErrorIs(t, err, sql.ErrNoRows, "standalone claims cannot execute linked sources")
				claim, err := s.ClaimReceiverSourceDelivery(ctx, source, at.Add(5*time.Millisecond))
				require.NoError(t, err)
				defer claim.Close()
				mutateNativeSourceGuard(t, s, parent, source, change, at)
				if stage == "pre_network" {
					_, err = s.SendClaimedReceiverDelivery(ctx, claim)
					require.Error(t, err)
					assert.Zero(t, hits.Load())
				} else {
					op := claim.Prepared().Operations()[0]
					err = s.CheckpointReceiverDelivery(ctx, claim, []receivers.Outcome{{OperationKey: op.Key, Sent: true, Code: "sent", Receipt: receivers.Receipt{ID: "ack"}}}, at.Add(6*time.Millisecond))
					require.Error(t, err)
				}
				var receipts int
				require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_firing_receipts`).Scan(&receipts))
				assert.Zero(t, receipts)
			})
		}
	}
}

func TestReceiverSourceEnvelopeTypedIdentityAndPreOpenMissingKeyGuard(t *testing.T) {
	s, point, _ := nativeSourceStore(t, "http://127.0.0.1:1/channel")
	at := time.Now()
	_, source := nativeSourceClaim(t, s, point, at)
	var sealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT sealed FROM alert_receiver_sources WHERE notification_id=?`, source.ID).Scan(&sealed))
	var value receiverSourceSnapshot
	require.ErrorIs(t, s.openReceiver("notification-source", "0"+fmt.Sprint(source.ID), source.OwnerEpoch, sealed, &value), ErrReceiverSnapshot, "typed UID AAD distinguishes textual identities")
	other, err := OpenForOrganization(filepath.Join(t.TempDir(), "other.db"), 9007199254740993)
	require.NoError(t, err)
	defer other.DB.Close()
	other.secretKey = append([]byte{}, s.secretKey...)
	require.ErrorIs(t, other.openReceiver("notification-source", fmt.Sprint(source.ID), source.OwnerEpoch, sealed, &value), ErrReceiverSnapshot, "organization AAD keeps exact int64 identity")
	// Leave only a sealed source row: Open must refuse to invent a replacement
	// key even when all earlier receiver snapshot tables happen to be empty.
	_, err = s.DB.Exec(`PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	for _, table := range []string{"alert_contact_points", "alert_receiver_integration_revisions", "alert_smtp_profile_revisions", "alert_receiver_dispatches", "alert_receiver_deliveries", "alert_receiver_legacy_outbox"} {
		_, err = s.DB.Exec(`DELETE FROM ` + table)
		require.NoError(t, err)
	}
	path := s.Path
	require.NoError(t, s.DB.Close())
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), "secrets.key")))
	_, err = Open(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alert_receiver_sources exist but secrets.key is missing")
	_, err = os.Stat(filepath.Join(filepath.Dir(path), "secrets.key"))
	assert.True(t, errors.Is(err, os.ErrNotExist), "missing-key guard must not create a new key")
}
