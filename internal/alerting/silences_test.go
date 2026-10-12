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

func addSilence(t *testing.T, s *store.Store, at, end time.Time) model.Silence {
	t.Helper()
	value, err := s.SaveSilence(context.Background(), model.Silence{Matchers: []model.SilenceMatcher{{Name: "alertname", Value: "Service"}}, StartsAt: at.Add(-time.Second), EndsAt: end, CreatedBy: "operator", Comment: "Scheduled maintenance"}, at)
	require.NoError(t, err)
	return value
}

func silenceEngine(t *testing.T, s *store.Store) *Engine {
	t.Helper()
	_, err := s.SaveAlertRule(context.Background(), model.AlertRule{UID: "service", Title: "Service", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	return &Engine{Store: s, active: map[string]bool{}, slots: make(chan struct{}, 4), Query: func(context.Context, string, time.Time) ([]Value, error) { return []Value{{Value: 1}}, nil }}
}

func TestSilenceDurableExpiryOverlapsAndStableDelivery(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	ids := []string{}
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get("X-Metricspanel-Notification-Id"))
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "silences.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	addNotificationPoint(t, s, webhook.URL)
	e := silenceEngine(t, s)
	at := time.Now()
	addSilence(t, s, at, at.Add(time.Second))
	second := addSilence(t, s, at, at.Add(3*time.Second))
	_, err = e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	stableID := rows[0].ID
	assert.Equal(t, "suppressed", rows[0].State)
	assert.Zero(t, rows[0].Attempts)
	processed, err := e.ProcessNotification(ctx, at.Add(2*time.Second))
	require.NoError(t, err)
	assert.False(t, processed, "overlapping active silence prevents early delivery")
	second.EndsAt = at.Add(5 * time.Second)
	_, err = s.SaveSilence(ctx, second, at.Add(2*time.Second))
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	processed, err = e.ProcessNotification(ctx, at.Add(4*time.Second))
	require.NoError(t, err)
	assert.False(t, processed, "updated silence and pending delivery survive restart")
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := e.ProcessNotification(ctx, at.Add(6*time.Second))
			assert.NoError(t, err)
		}()
	}
	workers.Wait()
	rows, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, stableID, rows[0].ID)
	assert.Equal(t, "sent", rows[0].State)
	assert.Equal(t, 1, rows[0].Attempts)
	view, err := s.AlertRule(ctx, "service")
	require.NoError(t, err)
	assert.Equal(t, "Firing", view.Runtime.Instances[0].State)
	history, err := s.AlertHistory(ctx, "service", 100)
	require.NoError(t, err)
	require.Len(t, history, 1, "notifications do not fabricate state transitions")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, ids, 1, "concurrent workers share one restart-safe claim")
}

func TestSilenceRecoveryNeverReplaysMutedFiringOrResolved(t *testing.T) {
	for _, beforeSilence := range []bool{false, true} {
		t.Run(map[bool]string{false: "firing-during-silence", true: "queued-before-silence"}[beforeSilence], func(t *testing.T) {
			s := notificationStore(t)
			ctx := context.Background()
			received := make(chan string, 10)
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- r.Header.Get("X-Metricspanel-Notification-Id")
				w.WriteHeader(204)
			}))
			defer webhook.Close()
			addNotificationPoint(t, s, webhook.URL)
			e := silenceEngine(t, s)
			at := time.Now()
			if !beforeSilence {
				addSilence(t, s, at, at.Add(time.Minute))
			}
			_, err := e.Evaluate(ctx, "service", at)
			require.NoError(t, err)
			if beforeSilence {
				addSilence(t, s, at, at.Add(time.Minute))
			}
			e.Query = func(context.Context, string, time.Time) ([]Value, error) { return []Value{{Value: 0}}, nil }
			_, err = e.Evaluate(ctx, "service", at.Add(time.Second))
			require.NoError(t, err)
			processed, err := e.ProcessNotification(ctx, at.Add(2*time.Minute))
			require.NoError(t, err)
			assert.False(t, processed)
			assert.Empty(t, received)
			rows, err := s.Notifications(ctx, "", 100)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			for _, row := range rows {
				assert.Equal(t, "canceled", row.State)
				assert.Zero(t, row.Attempts)
			}
			view, err := s.AlertRule(ctx, "service")
			require.NoError(t, err)
			assert.Equal(t, "Normal", view.Runtime.Instances[0].State)
			history, err := s.AlertHistory(ctx, "service", 100)
			require.NoError(t, err)
			assert.Len(t, history, 2)
		})
	}
}

func TestSilenceAfterFirstDeliveryResumesExactlyOnce(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	statuses := make(chan string, 10)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status string `json:"status"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		statuses <- body.Status
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	addNotificationPoint(t, s, webhook.URL)
	e := silenceEngine(t, s)
	at := time.Now()
	_, err := e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	_, err = e.ProcessNotification(ctx, at)
	require.NoError(t, err)
	assert.Equal(t, "firing", <-statuses)
	silence := addSilence(t, s, at, at.Add(time.Minute))
	for range 3 {
		processed, err := e.ProcessNotification(ctx, at.Add(time.Second))
		require.NoError(t, err)
		assert.False(t, processed)
	}
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2, "a silence retains one resume record for the active period")
	assert.Equal(t, "suppressed", rows[0].State)
	assert.Empty(t, statuses)
	require.NoError(t, s.ExpireSilence(ctx, silence.ID, at.Add(2*time.Second)))
	processed, err := e.ProcessNotification(ctx, at.Add(3*time.Second))
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, "firing", <-statuses)
	processed, err = e.ProcessNotification(ctx, at.Add(4*time.Second))
	require.NoError(t, err)
	assert.False(t, processed)
	assert.Empty(t, statuses)
}

func TestSilenceRuleDeletionCancelsResume(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	addNotificationPoint(t, s, "http://127.0.0.1:1/webhook")
	e := silenceEngine(t, s)
	at := time.Now()
	addSilence(t, s, at, at.Add(time.Minute))
	_, err := e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	require.NoError(t, e.DeleteRule(ctx, "service"))
	_, err = e.ProcessNotification(ctx, at.Add(2*time.Minute))
	require.NoError(t, err)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	for _, row := range rows {
		assert.Equal(t, "canceled", row.State)
	}
}
