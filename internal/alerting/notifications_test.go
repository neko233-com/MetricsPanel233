package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func notificationStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "notifications.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.DB.Close() })
	return s
}

func addNotificationPoint(t *testing.T, s *store.Store, url string) model.ContactPoint {
	t.Helper()
	token := "private-token-233"
	p, err := s.SaveContactPoint(context.Background(), model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "webhook", Name: "Operations", URL: url}, BearerToken: &token, Headers: map[string]string{"X-Private-Key": "private-key-233"}})
	require.NoError(t, err)
	_, err = s.SaveNotificationPolicy(context.Background(), model.NotificationPolicy{Receiver: p.UID})
	require.NoError(t, err)
	return p
}

func TestNotificationFiringResolvedRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	requests := []map[string]any{}
	ids := []string{}
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer private-token-233", r.Header.Get("Authorization"))
		assert.Equal(t, "private-key-233", r.Header.Get("X-Private-Key"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, body)
		ids = append(ids, r.Header.Get("X-Metricspanel-Notification-Id"))
		if len(requests) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("private-token-233 response must never enter delivery history"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "metrics.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	addNotificationPoint(t, s, webhook.URL)
	rule, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "service", Title: "Service down", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	e := &Engine{Store: s, active: map[string]bool{}, slots: make(chan struct{}, 4), Query: func(context.Context, string, time.Time) ([]Value, error) { return []Value{{Value: 1}}, nil }}
	at := time.Now().Add(-time.Second)
	_, err = e.Evaluate(ctx, rule.UID, at)
	require.NoError(t, err)
	e.Query = func(context.Context, string, time.Time) ([]Value, error) { return []Value{{Value: 0}}, nil }
	_, err = e.Evaluate(ctx, rule.UID, at.Add(time.Second))
	require.NoError(t, err)
	history, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, "resolved", history[0].Status)
	assert.Equal(t, "firing", history[1].Status)
	processed, err := e.ProcessNotification(ctx, at.Add(time.Second))
	require.NoError(t, err)
	assert.True(t, processed)
	processed, err = e.ProcessNotification(ctx, at.Add(2*time.Second))
	require.NoError(t, err)
	assert.False(t, processed, "resolution must wait for the earlier firing retry")
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	processed, err = e.ProcessNotification(ctx, at.Add(7*time.Second))
	require.NoError(t, err)
	assert.True(t, processed)
	processed, err = e.ProcessNotification(ctx, at.Add(7*time.Second))
	require.NoError(t, err)
	assert.True(t, processed)
	history, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "sent", history[0].State)
	assert.Equal(t, "sent", history[1].State)
	assert.Equal(t, 2, history[1].Attempts)
	assert.Equal(t, 1, history[0].Attempts)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 3)
	assert.Equal(t, ids[0], ids[1], "retries have a stable delivery id")
	assert.Equal(t, strconv.FormatInt(history[1].ID, 10), ids[0])
	assert.Equal(t, "firing", requests[1]["status"])
	assert.Equal(t, "resolved", requests[2]["status"])
	first := requests[1]["alerts"].([]any)[0].(map[string]any)
	resolved := requests[2]["alerts"].([]any)[0].(map[string]any)
	assert.Equal(t, first["startsAt"], resolved["startsAt"])
	assert.NotEqual(t, "0001-01-01T00:00:00Z", resolved["endsAt"])
}

func TestNotificationLeasesBoundedAttemptsAndSecrets(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	p := addNotificationPoint(t, s, "http://127.0.0.1:1/webhook")
	var config string
	var sealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT config,secrets FROM alert_contact_points WHERE uid=?`, p.UID).Scan(&config, &sealed))
	assert.NotContains(t, config, "private-token-233")
	assert.NotContains(t, string(sealed), "private-token-233")
	updated, err := s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: p})
	require.NoError(t, err)
	secrets, err := s.ContactPointSecrets(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "private-token-233", secrets["bearer_token"])
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: p})
	assert.ErrorIs(t, err, store.ErrNotificationConflict)
	updated, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: updated, ClearSecrets: []string{"bearer_token"}})
	require.NoError(t, err)
	assert.False(t, updated.SecureFields["bearer_token"])
	n, err := s.EnqueueTestNotification(ctx, p.UID)
	require.NoError(t, err)
	claimed, _, err := s.ClaimNotification(ctx, n.CreatedAt)
	require.NoError(t, err)
	assert.Equal(t, n.ID, claimed.ID)
	_, _, err = s.ClaimNotification(ctx, n.CreatedAt+1000)
	assert.Error(t, err, "an active lease cannot be claimed twice")
	reclaimed, _, err := s.ClaimNotification(ctx, n.CreatedAt+60001)
	require.NoError(t, err)
	assert.Equal(t, n.ID, reclaimed.ID)
	assert.Equal(t, 2, reclaimed.Attempts)
	require.NoError(t, s.ReleaseNotification(ctx, reclaimed))
	require.NoError(t, s.DB.QueryRow(`SELECT attempts FROM alert_notification_outbox WHERE id=?`, n.ID).Scan(&claimed.Attempts))
	assert.Equal(t, 1, claimed.Attempts)
	_, err = s.DB.Exec(`UPDATE alert_notification_outbox SET max_attempts=2,attempts=1,next_attempt_at=? WHERE id=?`, n.CreatedAt, n.ID)
	require.NoError(t, err)
	e := &Engine{Store: s}
	processed, err := e.ProcessNotification(ctx, time.UnixMilli(n.CreatedAt+120000))
	require.NoError(t, err)
	assert.True(t, processed)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "failed", rows[0].State)
	assert.Equal(t, 2, rows[0].Attempts)
	assert.Equal(t, "Webhook request failed", rows[0].LastError)
	retried, err := s.RetryNotification(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", retried.State)
	assert.Zero(t, retried.Attempts)
	assert.ErrorIs(t, s.DeleteContactPoint(ctx, p.UID), store.ErrContactPointReferenced)
	policy, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	policy.Receiver = ""
	_, err = s.SaveNotificationPolicy(ctx, policy)
	require.NoError(t, err)
	require.NoError(t, s.DeleteContactPoint(ctx, p.UID))
	rows, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "canceled", rows[0].State)
}

func TestNotificationSecretKeyMustBeRetained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	_, err = s.SaveContactPoint(context.Background(), model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "point", Name: "point", URL: "http://localhost/webhook"}})
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), "secrets.key")))
	_, err = store.Open(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alert_contact_points exist but secrets.key is missing")
}

func TestNotificationPolicyMatchersAndRecovery(t *testing.T) {
	p := model.NotificationPolicy{Receiver: "default", Routes: []model.NotificationRoute{{Receiver: "critical", Matchers: []model.NotificationMatcher{{Name: "severity", Operator: "=", Value: "critical"}}, Continue: true}, {Receiver: "team", Matchers: []model.NotificationMatcher{{Name: "team", Operator: "=~", Value: "infra|ops"}}}}}
	require.NoError(t, p.Validate())
	assert.Equal(t, []string{"critical", "team"}, p.Receivers(map[string]string{"severity": "critical", "team": "ops"}))
	assert.Equal(t, []string{"default"}, p.Receivers(map[string]string{"team": "application"}))
	assert.False(t, model.NotificationMatcher{Name: "team", Operator: "=~", Value: "ops"}.Matches(map[string]string{"team": "devops"}), "Alertmanager regular expressions match the whole value")
	assert.True(t, model.NotificationMatcher{Name: "team", Operator: "!~", Value: "ops"}.Matches(map[string]string{"team": "devops"}))
	assert.Equal(t, 5*time.Second, notificationBackoff(1))
	assert.Equal(t, 5*time.Minute, notificationBackoff(8))
}

func TestNotificationConcurrentClaimsCancellationAndTimeout(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(204)
		case <-r.Context().Done():
		}
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); webhook.Close() }()
	p := addNotificationPoint(t, s, webhook.URL)
	n, err := s.EnqueueTestNotification(ctx, p.UID)
	require.NoError(t, err)
	e := &Engine{Store: s}
	runningCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := e.ProcessNotification(runningCtx, time.Now()); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook delivery did not start")
	}
	processed, err := e.ProcessNotification(ctx, time.Now())
	require.NoError(t, err)
	assert.False(t, processed, "the second worker must not reclaim the active request")
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled delivery did not release its durable lease")
	}
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "pending", rows[0].State)
	assert.Zero(t, rows[0].Attempts, "shutdown should not consume a delivery attempt")
	p.TimeoutSeconds = 1
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: p})
	require.NoError(t, err)
	processed, err = e.ProcessNotification(ctx, time.Now())
	require.NoError(t, err)
	assert.True(t, processed)
	rows, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "Webhook request timed out", rows[0].LastError)
	assert.Equal(t, "pending", rows[0].State)
	assert.GreaterOrEqual(t, rows[0].NextAttemptAt, rows[0].LastAttemptAt+6000, "retry delay begins after the timeout finishes")
	_, err = s.DB.Exec(`UPDATE alert_notification_outbox SET state='sending',attempts=max_attempts,next_attempt_at=? WHERE id=?`, time.Now().Add(-time.Minute).UnixMilli(), n.ID)
	require.NoError(t, err)
	processed, err = e.ProcessNotification(ctx, time.Now())
	require.NoError(t, err)
	assert.False(t, processed, "a crashed final attempt must be terminal, not exceed the configured attempt cap")
	rows, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "failed", rows[0].State)
	assert.Equal(t, rows[0].MaxAttempts, rows[0].Attempts)
}

func TestNotificationDoesNotFollowRedirectOrLeakResponse(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(302)
		_, _ = w.Write([]byte("private-key-233"))
	}))
	defer source.Close()
	p := addNotificationPoint(t, s, source.URL)
	_, err := s.EnqueueTestNotification(ctx, p.UID)
	require.NoError(t, err)
	e := &Engine{Store: s}
	_, err = e.ProcessNotification(ctx, time.Now())
	require.NoError(t, err)
	assert.False(t, redirected)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, 302, rows[0].HTTPStatus)
	assert.Equal(t, "failed", rows[0].State)
	assert.Equal(t, "Webhook returned HTTP 302", rows[0].LastError)
	assert.NotContains(t, rows[0].LastError, "private-key-233")
}

func TestNotificationHTTPStatusRetryClassification(t *testing.T) {
	for _, code := range []int{400, 401, 408, 425, 429, 500, 503} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			s := notificationStore(t)
			ctx := context.Background()
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer webhook.Close()
			p := addNotificationPoint(t, s, webhook.URL)
			_, err := s.EnqueueTestNotification(ctx, p.UID)
			require.NoError(t, err)
			e := &Engine{Store: s}
			_, err = e.ProcessNotification(ctx, time.Now())
			require.NoError(t, err)
			rows, err := s.Notifications(ctx, "", 100)
			require.NoError(t, err)
			expected := "pending"
			if code == 400 || code == 401 {
				expected = "failed"
			}
			assert.Equal(t, expected, rows[0].State)
			assert.Equal(t, 1, rows[0].Attempts)
			assert.Equal(t, code, rows[0].HTTPStatus)
		})
	}
}

func TestNotificationActivePeriodPersistsAcrossStateChanges(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	addNotificationPoint(t, s, "http://localhost/webhook")
	rule, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "period", Title: "Period", Expr: "vector(1)"})
	require.NoError(t, err)
	at := time.Now().UnixMilli()
	for i, transition := range []struct{ from, to string }{{"Normal", "Firing"}, {"Firing", "Error"}, {"Error", "Firing"}, {"Firing", "Normal"}} {
		event := model.AlertEvent{UID: rule.UID, Key: "instance", Labels: map[string]string{"alertname": rule.Title}, From: transition.from, To: transition.to, Timestamp: at + int64(i)*1000}
		runtime := model.AlertRuntime{LastEvaluation: event.Timestamp, Health: "ok", Instances: []model.AlertInstance{}}
		require.NoError(t, s.CommitAlertEvaluation(ctx, rule.UID, rule.Version, runtime, []model.AlertEvent{event}))
	}
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2, "internal transitions within an active period do not send duplicate firing notifications")
	var startsAt string
	require.NoError(t, s.DB.QueryRow(`SELECT json_extract(payload,'$.alerts[0].startsAt') FROM alert_notification_outbox WHERE status='resolved'`).Scan(&startsAt))
	assert.Equal(t, time.UnixMilli(at).UTC().Format(time.RFC3339Nano), startsAt)
	var activePeriods int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_notification_active`).Scan(&activePeriods))
	assert.Zero(t, activePeriods)
}
