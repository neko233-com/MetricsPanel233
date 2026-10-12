package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inhibitionConfig(t *testing.T, s *store.Store) model.InhibitionRule {
	t.Helper()
	v, err := s.SaveInhibitionRule(context.Background(), model.InhibitionRule{UID: "root-causes", Name: "Root causes", SourceMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "source"}}, TargetMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "target"}}, Equal: []string{"cluster"}})
	require.NoError(t, err)
	return v
}
func inhibitionAlert(t *testing.T, s *store.Store, uid string) model.AlertRule {
	t.Helper()
	v, err := s.SaveAlertRule(context.Background(), model.AlertRule{UID: uid, Title: uid, Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	return v.AlertRule
}
func inhibitionRuntime(t *testing.T, s *store.Store, rule model.AlertRule, at time.Time, instances []model.AlertInstance, emitEvents bool) {
	t.Helper()
	previous, err := s.AlertRule(context.Background(), rule.UID)
	require.NoError(t, err)
	old := map[string]model.AlertInstance{}
	for _, v := range previous.Runtime.Instances {
		old[v.Key] = v
	}
	events := []model.AlertEvent{}
	for i := range instances {
		v := &instances[i]
		if v.Labels == nil {
			v.Labels = map[string]string{}
		}
		v.Labels["alertname"] = rule.Title
		if v.State == "" {
			v.State = "Firing"
		}
		v.ActiveAt = old[v.Key].ActiveAt
		if v.ActiveAt == 0 {
			v.ActiveAt = at.UnixMilli()
		}
		v.FiringAt = v.ActiveAt
		v.UpdatedAt = at.UnixMilli()
		from := old[v.Key].State
		if from == "" {
			from = "Normal"
		}
		if emitEvents && from != v.State {
			events = append(events, model.AlertEvent{UID: rule.UID, Key: v.Key, Labels: v.Labels, Annotations: v.Annotations, From: from, To: v.State, Timestamp: at.UnixMilli()})
		}
	}
	require.NoError(t, s.CommitAlertEvaluation(context.Background(), rule.UID, previous.Version, model.AlertRuntime{LastEvaluation: at.UnixMilli(), Health: "ok", Instances: instances}, events))
}
func inhibitionInstance(key, role, cluster, state string) model.AlertInstance {
	return model.AlertInstance{Key: key, State: state, Labels: map[string]string{"role": role, "cluster": cluster}}
}
func inhibitionReceiver(t *testing.T) (*httptest.Server, chan map[string]any) {
	t.Helper()
	ch := make(chan map[string]any, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.NotEmpty(t, r.Header.Get("X-Metricspanel-Notification-Id"))
		ch <- body
		w.WriteHeader(204)
	}))
	return server, ch
}
func targetOnlyPolicy(t *testing.T, s *store.Store) {
	t.Helper()
	p, err := s.NotificationPolicy(context.Background())
	require.NoError(t, err)
	p.Receiver = ""
	p.Routes = []model.NotificationRoute{{Receiver: "webhook", Matchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "target"}}}}
	_, err = s.SaveNotificationPolicy(context.Background(), p)
	require.NoError(t, err)
}

func TestInhibitionMultipleSourcesSilencedSourceRestartAndConcurrentResume(t *testing.T) {
	ctx := context.Background()
	webhook, received := inhibitionReceiver(t)
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "inhibition.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	addNotificationPoint(t, s, webhook.URL)
	targetOnlyPolicy(t, s)
	inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target := inhibitionAlert(t, s, "target")
	at := time.Now().Add(time.Hour)
	_, err = s.SaveSilence(ctx, model.Silence{Matchers: []model.SilenceMatcher{{Name: "role", Value: "source"}}, StartsAt: at.Add(-time.Second), EndsAt: at.Add(time.Hour), CreatedBy: "ops", Comment: "Source notification is muted"}, at)
	require.NoError(t, err)
	inhibitionRuntime(t, s, source, at, []model.AlertInstance{inhibitionInstance("s1", "source", "a", "Firing"), inhibitionInstance("s2", "source", "a", "Error")}, true)
	inhibitionRuntime(t, s, target, at.Add(time.Millisecond), []model.AlertInstance{inhibitionInstance("t1", "target", "a", "Firing")}, true)
	e := &Engine{Store: s}
	processGroup(t, e, at.Add(time.Second), false)
	require.Empty(t, received)
	history, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, history, 1)
	stableID := history[0].ID
	assert.Equal(t, "Inhibited", history[0].LastError)
	assert.Zero(t, history[0].Attempts)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	processGroup(t, e, at.Add(2*time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(3*time.Second), []model.AlertInstance{inhibitionInstance("s1", "source", "a", "Normal"), inhibitionInstance("s2", "source", "a", "NoData")}, true)
	processGroup(t, e, at.Add(4*time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(5*time.Second), []model.AlertInstance{inhibitionInstance("s1", "source", "a", "Normal"), inhibitionInstance("s2", "source", "a", "Normal")}, true)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := e.ProcessNotification(ctx, at.Add(6*time.Second))
			assert.NoError(t, err)
		}()
	}
	workers.Wait()
	require.Len(t, received, 1)
	assert.Equal(t, "firing", (<-received)["status"])
	history, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, stableID, history[0].ID)
	assert.Equal(t, "sent", history[0].State)
	assert.Equal(t, 1, history[0].Attempts)
	events, err := s.AlertHistory(ctx, target.UID, 100)
	require.NoError(t, err)
	require.Len(t, events, 1, "resume must not fabricate a state transition")
}

func TestInhibitionRecoveryWhileMutedNeverReplaysAfterRestart(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "initially-inhibited", true: "already-notified"}[late], func(t *testing.T) {
			ctx := context.Background()
			webhook, received := inhibitionReceiver(t)
			defer webhook.Close()
			path := filepath.Join(t.TempDir(), "recovery.db")
			s, err := store.Open(path)
			require.NoError(t, err)
			addNotificationPoint(t, s, webhook.URL)
			targetOnlyPolicy(t, s)
			inhibitionConfig(t, s)
			source := inhibitionAlert(t, s, "source")
			target := inhibitionAlert(t, s, "target")
			at := time.Now().Add(time.Hour)
			e := &Engine{Store: s}
			if !late {
				inhibitionRuntime(t, s, source, at, []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
			}
			inhibitionRuntime(t, s, target, at.Add(time.Millisecond), []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
			if late {
				processGroup(t, e, at.Add(time.Second), true)
				<-received
				inhibitionRuntime(t, s, source, at.Add(2*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
			}
			processGroup(t, e, at.Add(3*time.Second), false)
			inhibitionRuntime(t, s, target, at.Add(4*time.Second), []model.AlertInstance{inhibitionInstance("t", "target", "a", "Normal")}, true)
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			defer s.DB.Close()
			e.Store = s
			require.NoError(t, s.DeleteAlertRule(ctx, source.UID))
			processGroup(t, e, at.Add(10*time.Second), false)
			require.Empty(t, received)
			rows, err := s.Notifications(ctx, "", 100)
			require.NoError(t, err)
			for _, v := range rows {
				if v.State != "sent" {
					assert.Equal(t, "canceled", v.State)
				}
			}
		})
	}
}

func TestInhibitionCurrentLabelsWithoutTransitionRulesDisableDeleteReset(t *testing.T) {
	ctx := context.Background()
	webhook, received := inhibitionReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	addNotificationPoint(t, s, webhook.URL)
	targetOnlyPolicy(t, s)
	config := inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target := inhibitionAlert(t, s, "target")
	at := time.Now().Add(time.Hour)
	e := &Engine{Store: s}
	inhibitionRuntime(t, s, source, at, []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	inhibitionRuntime(t, s, target, at.Add(time.Millisecond), []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
	processGroup(t, e, at.Add(time.Second), false)
	// Active source changes its equal label without producing any history event.
	inhibitionRuntime(t, s, source, at.Add(2*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "b", "Firing")}, false)
	processGroup(t, e, at.Add(3*time.Second), true)
	<-received
	inhibitionRuntime(t, s, source, at.Add(4*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, false)
	processGroup(t, e, at.Add(5*time.Second), false)
	config.Disabled = true
	config, err := s.SaveInhibitionRule(ctx, config)
	require.NoError(t, err)
	processGroup(t, e, at.Add(6*time.Second), true)
	<-received
	config.Disabled = false
	config, err = s.SaveInhibitionRule(ctx, config)
	require.NoError(t, err)
	processGroup(t, e, at.Add(7*time.Second), false)
	_, err = s.SaveAlertRule(ctx, source)
	require.NoError(t, err)
	processGroup(t, e, at.Add(8*time.Second), true)
	<-received
	sourceView, err := s.AlertRule(ctx, source.UID)
	require.NoError(t, err)
	inhibitionRuntime(t, s, sourceView.AlertRule, at.Add(9*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	processGroup(t, e, at.Add(10*time.Second), false)
	require.NoError(t, s.DeleteInhibitionRule(ctx, config.UID, config.Version))
	processGroup(t, e, at.Add(11*time.Second), true)
	<-received
	processGroup(t, e, at.Add(12*time.Second), false)
	events, err := s.AlertHistory(ctx, target.UID, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestInhibitionOverlapSilenceAndPreSendFrozenTemplate(t *testing.T) {
	ctx := context.Background()
	webhook, received := inhibitionReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	point := addNotificationPoint(t, s, webhook.URL)
	targetOnlyPolicy(t, s)
	inhibitionConfig(t, s)
	point.TitleTemplate = "OLD {{.Status}}"
	_, err := s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	source := inhibitionAlert(t, s, "source")
	target := inhibitionAlert(t, s, "target")
	at := time.Now().Add(time.Hour)
	e := &Engine{Store: s}
	inhibitionRuntime(t, s, target, at, []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
	delivery, raw, err := s.ClaimNotification(ctx, at.UnixMilli())
	require.NoError(t, err)
	prepared, frozen, err := e.prepareTemplateNotification(ctx, delivery, raw)
	require.NoError(t, err)
	require.True(t, frozen)
	inhibitionRuntime(t, s, source, at.Add(time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	valid, err := s.ValidateNotificationClaim(ctx, delivery, at.Add(time.Second).UnixMilli())
	require.NoError(t, err)
	assert.False(t, valid)
	silence, err := s.SaveSilence(ctx, model.Silence{Matchers: []model.SilenceMatcher{{Name: "role", Value: "target"}}, StartsAt: at, EndsAt: at.Add(20 * time.Second), CreatedBy: "ops", Comment: "overlap"}, at)
	require.NoError(t, err)
	inhibitionRuntime(t, s, source, at.Add(2*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(3*time.Second), false)
	point, err = s.ContactPoint(ctx, point.UID)
	require.NoError(t, err)
	point.TitleTemplate = "NEW"
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	require.NoError(t, s.ExpireSilence(ctx, silence.ID, at.Add(4*time.Second)))
	processGroup(t, e, at.Add(4*time.Second), true)
	body := <-received
	assert.Equal(t, "OLD firing", body["title"])
	var captured map[string]any
	require.NoError(t, json.Unmarshal(prepared, &captured))
	assert.Equal(t, captured, body, "frozen template/payload must survive suppression")
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, delivery.ID, rows[0].ID)
	assert.Equal(t, 1, rows[0].Attempts)
	processGroup(t, e, at.Add(5*time.Second), false)
}

func TestInhibitionGroupedMixedMembersWaitAndFrozenRebuild(t *testing.T) {
	ctx := context.Background()
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	e := groupEngine(t, s, webhook.URL, "3s", "1s", "")
	p, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	p.Receiver = ""
	p.Routes = []model.NotificationRoute{{Receiver: "webhook", Matchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "target"}}}}
	_, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target, err := s.AlertRule(ctx, "service")
	require.NoError(t, err)
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	inhibitionRuntime(t, s, target.AlertRule, at, []model.AlertInstance{inhibitionInstance("muted", "target", "a", "Firing"), inhibitionInstance("visible", "target", "b", "Firing")}, true)
	delivery, _, err := s.ClaimNotification(ctx, at.Add(3*time.Second).UnixMilli())
	require.NoError(t, err)
	require.NoError(t, s.ReleaseNotification(ctx, delivery))
	inhibitionRuntime(t, s, source, at.Add(4*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	// A frozen two-member delivery is canceled. A new snapshot contains only b.
	processGroup(t, e, at.Add(5*time.Second), false)
	processGroup(t, e, at.Add(5*time.Second), true)
	first := <-received
	require.Len(t, first.Alerts, 1)
	assert.Equal(t, "b", first.Alerts[0].Labels["cluster"])
	inhibitionRuntime(t, s, target.AlertRule, at.Add(6*time.Second), []model.AlertInstance{inhibitionInstance("muted", "target", "a", "Normal"), inhibitionInstance("visible", "target", "b", "Firing")}, true)
	processGroup(t, e, at.Add(7*time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(8*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(9*time.Second), false)
	require.Empty(t, received, "resolved inhibited member cannot return in a group resume")
	// A later active period is still inhibited until the source clears, and a
	// source wake cannot shorten the initial group_wait on a new group.
	inhibitionRuntime(t, s, source, at.Add(10*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	inhibitionRuntime(t, s, target.AlertRule, at.Add(11*time.Second), []model.AlertInstance{inhibitionInstance("muted", "target", "a", "Firing"), inhibitionInstance("visible", "target", "b", "Firing")}, true)
	processGroup(t, e, at.Add(12*time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(13*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(14*time.Second), true)
	resumed := <-received
	require.Len(t, resumed.Alerts, 2)
}

func TestInhibitionGroupCurrentTargetLabelsAndInitialWait(t *testing.T) {
	ctx := context.Background()
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	e := groupEngine(t, s, webhook.URL, "3s", "1s", "")
	p, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	p.Receiver = ""
	p.Routes = []model.NotificationRoute{{Receiver: "webhook", Matchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "target"}}}}
	_, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target, err := s.AlertRule(ctx, "service")
	require.NoError(t, err)
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	inhibitionRuntime(t, s, target.AlertRule, at, []model.AlertInstance{inhibitionInstance("t", "target", "b", "Firing")}, true)
	inhibitionRuntime(t, s, source, at.Add(time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	processGroup(t, e, at.Add(time.Second), false)
	processGroup(t, e, at.Add(2*time.Second), false)
	delivery, _, err := s.ClaimNotification(ctx, at.Add(3*time.Second).UnixMilli())
	require.NoError(t, err)
	// Same identity and same group, changed label, no transition/history event.
	inhibitionRuntime(t, s, target.AlertRule, at.Add(4*time.Second), []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, false)
	valid, err := s.ValidateNotificationClaim(ctx, delivery, at.Add(4*time.Second).UnixMilli())
	require.NoError(t, err)
	assert.False(t, valid, "claim must use current target labels instead of the frozen payload's old labels")
	processGroup(t, e, at.Add(5*time.Second), false)
	require.Empty(t, received)
	inhibitionRuntime(t, s, source, at.Add(6*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(7*time.Second), true)
	body := <-received
	require.Len(t, body.Alerts, 1)
	assert.Equal(t, "a", body.Alerts[0].Labels["cluster"])
	processGroup(t, e, at.Add(8*time.Second), false)
}

func TestInhibitionGroupedMuteTimingCombination(t *testing.T) {
	ctx := context.Background()
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	e := groupEngine(t, s, webhook.URL, "0s", "1s", "")
	_, err := s.SaveMuteTiming(ctx, model.MuteTiming{Name: "minute-window", TimeIntervals: []json.RawMessage{json.RawMessage(`{"times":[{"start_time":"12:00","end_time":"12:02"}],"location":"UTC"}`)}})
	require.NoError(t, err)
	p, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	p.Receiver = ""
	p.Routes = []model.NotificationRoute{{Receiver: "webhook", Matchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "target"}}, MuteTimeIntervals: []string{"minute-window"}}}
	_, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target, err := s.AlertRule(ctx, "service")
	require.NoError(t, err)
	at := time.Date(time.Now().Year()+1, time.January, 1, 12, 0, 0, 0, time.UTC)
	inhibitionRuntime(t, s, source, at, []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	inhibitionRuntime(t, s, target.AlertRule, at.Add(time.Millisecond), []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
	processGroup(t, e, at.Add(time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(30*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(time.Minute), false)
	processGroup(t, e, at.Add(2*time.Minute), true)
	<-received
	processGroup(t, e, at.Add(3*time.Minute), false)
}

func TestInhibitionResumeAnchorSurvivesHistoryPruningAndUsesCurrentData(t *testing.T) {
	ctx := context.Background()
	webhook, received := inhibitionReceiver(t)
	defer webhook.Close()
	s := notificationStore(t)
	addNotificationPoint(t, s, webhook.URL)
	targetOnlyPolicy(t, s)
	inhibitionConfig(t, s)
	source := inhibitionAlert(t, s, "source")
	target := inhibitionAlert(t, s, "target")
	at := time.Now().Add(time.Hour)
	e := &Engine{Store: s}
	inhibitionRuntime(t, s, target, at, []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
	processGroup(t, e, at, true)
	<-received
	history, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	anchor := history[0].ID
	_, err = s.DB.Exec(`WITH RECURSIVE seq(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM seq WHERE x<10020) INSERT INTO alert_notification_outbox(event_id,contact_point_uid,rule_uid,instance_key,status,state,next_attempt_at,created_at,payload) SELECT 0,'webhook','completed','completed','test','sent',0,0,'{}' FROM seq`)
	require.NoError(t, err)
	test, err := s.EnqueueTestNotification(ctx, "webhook")
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE alert_notification_outbox SET state='canceled' WHERE id=?`, test.ID)
	require.NoError(t, err)
	var kept int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_notification_outbox WHERE id=?`, anchor).Scan(&kept))
	assert.Equal(t, 1, kept, "history retention must keep the current period's resume anchor")
	value := 42.0
	current := inhibitionInstance("t", "target", "a", "Firing")
	current.Value = &value
	current.Annotations = map[string]string{"summary": "Current data"}
	inhibitionRuntime(t, s, target, at.Add(time.Second), []model.AlertInstance{current}, false)
	inhibitionRuntime(t, s, source, at.Add(2*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Firing")}, true)
	processGroup(t, e, at.Add(3*time.Second), false)
	inhibitionRuntime(t, s, source, at.Add(4*time.Second), []model.AlertInstance{inhibitionInstance("s", "source", "a", "Normal")}, true)
	processGroup(t, e, at.Add(5*time.Second), true)
	body := <-received
	assert.Equal(t, "Current data", body["message"])
	alert := body["alerts"].([]any)[0].(map[string]any)
	assert.Equal(t, 42.0, alert["values"].(map[string]any)["A"])
	assert.Equal(t, time.UnixMilli(at.UnixMilli()).UTC().Format(time.RFC3339Nano), alert["startsAt"])
	processGroup(t, e, at.Add(6*time.Second), false)
}

func TestNotificationOrganizationOwnerUngroupedGroupedAndTemplates(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(map[bool]string{false: "ungrouped", true: "grouped"}[grouped], func(t *testing.T) {
			ctx := context.Background()
			webhook, received := inhibitionReceiver(t)
			defer webhook.Close()
			s, err := store.OpenForOrganization(filepath.Join(t.TempDir(), "org2.db"), 2)
			require.NoError(t, err)
			defer s.DB.Close()
			point := addNotificationPoint(t, s, webhook.URL)
			point.TitleTemplate = `{{range .Alerts}}{{.OrgID}}{{end}}`
			_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
			require.NoError(t, err)
			if grouped {
				p, err := s.NotificationPolicy(ctx)
				require.NoError(t, err)
				by := []string{"cluster"}
				p.NotificationTiming = model.NotificationTiming{GroupBy: &by, GroupWait: "0s", GroupInterval: "1s", RepeatInterval: "0s"}
				_, err = s.SaveNotificationPolicy(ctx, p)
				require.NoError(t, err)
			}
			rule := inhibitionAlert(t, s, "target")
			at := time.Now().Add(time.Hour)
			inhibitionRuntime(t, s, rule, at, []model.AlertInstance{inhibitionInstance("t", "target", "a", "Firing")}, true)
			e := &Engine{Store: s}
			processGroup(t, e, at, true)
			body := <-received
			assert.Equal(t, 2.0, body["orgId"])
			assert.Equal(t, "2", body["title"])
			_, err = s.EnqueueTestNotification(ctx, point.UID)
			require.NoError(t, err)
			processGroup(t, e, at.Add(time.Second), true)
			body = <-received
			assert.Equal(t, 2.0, body["orgId"])
			assert.Equal(t, "2", body["title"])
		})
	}
}
