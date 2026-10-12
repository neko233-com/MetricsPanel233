package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type receivedGroup struct {
	Status       string            `json:"status"`
	GroupLabels  map[string]string `json:"groupLabels"`
	CommonLabels map[string]string `json:"commonLabels"`
	Alerts       []struct {
		Status   string             `json:"status"`
		StartsAt string             `json:"startsAt"`
		Labels   map[string]string  `json:"labels"`
		Values   map[string]float64 `json:"values"`
	} `json:"alerts"`
	ID string `json:"notificationId"`
}

func groupEngine(t *testing.T, s *store.Store, url string, wait, interval, repeat string) *Engine {
	t.Helper()
	addNotificationPoint(t, s, url)
	p, err := s.NotificationPolicy(context.Background())
	require.NoError(t, err)
	labels := []string{"team"}
	p.NotificationTiming = model.NotificationTiming{GroupBy: &labels, GroupWait: wait, GroupInterval: interval, RepeatInterval: repeat}
	_, err = s.SaveNotificationPolicy(context.Background(), p)
	require.NoError(t, err)
	_, err = s.SaveAlertRule(context.Background(), model.AlertRule{UID: "service", Title: "Service", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	return &Engine{Store: s, active: map[string]bool{}, slots: make(chan struct{}, 4)}
}
func groupValues(values map[string]float64) func(context.Context, string, time.Time) ([]Value, error) {
	return func(context.Context, string, time.Time) ([]Value, error) {
		out := []Value{}
		for name, value := range values {
			out = append(out, Value{Value: value, Labels: map[string]string{"team": "ops", "instance": name}})
		}
		return out, nil
	}
}
func groupReceiver(t *testing.T) (*httptest.Server, chan receivedGroup) {
	t.Helper()
	received := make(chan receivedGroup, 32)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v receivedGroup
		require.NoError(t, json.NewDecoder(r.Body).Decode(&v))
		received <- v
		w.WriteHeader(204)
	})), received
}
func processGroup(t *testing.T, e *Engine, at time.Time, want bool) {
	t.Helper()
	got, err := e.ProcessNotification(context.Background(), at)
	require.NoError(t, err)
	require.Equal(t, want, got)
}
func evaluateGroup(t *testing.T, e *Engine, at time.Time, values map[string]float64) {
	t.Helper()
	e.Query = groupValues(values)
	_, err := e.Evaluate(context.Background(), "service", at)
	require.NoError(t, err)
}

func TestNotificationGroupWaitRestartMembersRecoveryAndRepeat(t *testing.T) {
	ctx := context.Background()
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "groups.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	e := groupEngine(t, s, webhook.URL, "3s", "2s", "3s")
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	processGroup(t, e, at.Add(2*time.Second), false)
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 9, "b": 2})
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := e.ProcessNotification(ctx, at.Add(3*time.Second))
			assert.NoError(t, err)
		}()
	}
	workers.Wait()
	require.Len(t, received, 1)
	first := <-received
	require.Len(t, first.Alerts, 2)
	assert.Equal(t, map[string]string{"team": "ops"}, first.GroupLabels)
	assert.NotContains(t, first.CommonLabels, "instance")
	for _, v := range first.Alerts {
		if v.Labels["instance"] == "a" {
			assert.Equal(t, 9.0, v.Values["A"], "flush uses latest evaluation")
		}
	}
	processGroup(t, e, at.Add(6*time.Second), false)
	processGroup(t, e, at.Add(7*time.Second), true)
	repeat := <-received
	assert.NotEqual(t, first.ID, repeat.ID)
	require.Len(t, repeat.Alerts, 2)
	evaluateGroup(t, e, at.Add(8*time.Second), map[string]float64{"a": 0, "b": 2, "c": 3})
	processGroup(t, e, at.Add(8*time.Second), false)
	processGroup(t, e, at.Add(9*time.Second), true)
	updated := <-received
	require.Len(t, updated.Alerts, 3)
	countResolved := 0
	for _, a := range updated.Alerts {
		if a.Status == "resolved" {
			countResolved++
		}
	}
	assert.Equal(t, 1, countResolved)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	processGroup(t, e, at.Add(12*time.Second), false)
	processGroup(t, e, at.Add(13*time.Second), true)
	last := <-received
	require.Len(t, last.Alerts, 2, "resolved member removed after successful send")
	history, err := s.AlertHistory(ctx, "service", 100)
	require.NoError(t, err)
	require.Len(t, history, 4, "group/repeat does not fabricate history")
	groups, err := s.NotificationGroups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "4s", groups[0].EffectiveRepeatInterval)
}

func TestNotificationGroupRecoveryBeforeWaitAndPartialSilenceResume(t *testing.T) {
	s := notificationStore(t)
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "2s", "1s", "0s")
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 0})
	processGroup(t, e, at.Add(2*time.Second), false)
	assert.Empty(t, received)
	evaluateGroup(t, e, at.Add(3*time.Second), map[string]float64{"a": 1, "b": 2})
	processGroup(t, e, at.Add(3*time.Second), false)
	processGroup(t, e, at.Add(5*time.Second), true)
	first := <-received
	require.Len(t, first.Alerts, 2)
	silence, err := s.SaveSilence(context.Background(), model.Silence{Matchers: []model.SilenceMatcher{{Name: "instance", Value: "a"}}, StartsAt: at.Add(6 * time.Second), EndsAt: at.Add(10 * time.Second), CreatedBy: "operator", Comment: "a maintenance"}, at.Add(6*time.Second))
	require.NoError(t, err)
	processGroup(t, e, at.Add(6*time.Second), true)
	partial := <-received
	require.Len(t, partial.Alerts, 1)
	assert.Equal(t, "b", partial.Alerts[0].Labels["instance"])
	processGroup(t, e, at.Add(7*time.Second), false)
	processGroup(t, e, at.Add(9*time.Second), false)
	assert.Empty(t, received, "partial silencing does not repeat visible members on each poll")
	processGroup(t, e, at.Add(10*time.Second), true)
	resumed := <-received
	require.Len(t, resumed.Alerts, 2)
	processGroup(t, e, at.Add(11*time.Second), false)
	silence.ID = ""
	silence.StartsAt = at.Add(12 * time.Second)
	silence.EndsAt = at.Add(16 * time.Second)
	_, err = s.SaveSilence(context.Background(), silence, at.Add(12*time.Second))
	require.NoError(t, err)
	processGroup(t, e, at.Add(12*time.Second), true)
	<-received
	evaluateGroup(t, e, at.Add(13*time.Second), map[string]float64{"a": 0, "b": 2})
	processGroup(t, e, at.Add(16*time.Second), false)
	assert.Empty(t, received, "muted resolution never replays stale firing or recovery")
}

func TestNotificationGroupFailedSnapshotRetryAndPolicyChange(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	requests := make(chan receivedGroup, 10)
	var code atomic.Int32
	code.Store(400)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v receivedGroup
		require.NoError(t, json.NewDecoder(r.Body).Decode(&v))
		requests <- v
		w.WriteHeader(int(code.Load()))
	}))
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
	at := time.Now().Add(time.Hour)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	processGroup(t, e, at, true)
	first := <-requests
	processGroup(t, e, at.Add(time.Second), false)
	history, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "failed", history[0].State)
	code.Store(204)
	_, err = s.RetryNotification(ctx, history[0].ID)
	require.NoError(t, err)
	processGroup(t, e, at.Add(2*time.Second), true)
	second := <-requests
	assert.Equal(t, first.ID, second.ID)
	processGroup(t, e, at.Add(3*time.Second), false)
	p, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	p.Receiver = ""
	p.Routes = nil
	_, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	processGroup(t, e, at.Add(4*time.Second), false)
	assert.Empty(t, requests)
}

func TestNotificationGroupTimeIntervalsDSTOverlapRestartAndRecovery(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume-after-overlapping-intervals", true: "resolve-while-muted"}[resolve], func(t *testing.T) {
			ctx := context.Background()
			webhook, received := groupReceiver(t)
			defer webhook.Close()
			path := filepath.Join(t.TempDir(), "timings.db")
			s, err := store.Open(path)
			require.NoError(t, err)
			e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
			_, err = s.SaveMuteTiming(ctx, model.MuteTiming{Name: "new-york-fold", TimeIntervals: []json.RawMessage{json.RawMessage(`{"times":[{"start_time":"01:30","end_time":"01:32"}],"location":"America/New_York"}`)}})
			require.NoError(t, err)
			_, err = s.SaveMuteTiming(ctx, model.MuteTiming{Name: "utc-overlap", TimeIntervals: []json.RawMessage{json.RawMessage(`{"times":[{"start_time":"06:31","end_time":"06:34"}],"location":"UTC"}`)}})
			require.NoError(t, err)
			p, err := s.NotificationPolicy(ctx)
			require.NoError(t, err)
			p.Routes = []model.NotificationRoute{{Matchers: []model.NotificationMatcher{{Name: "team", Operator: "=", Value: "ops"}}, MuteTimeIntervals: []string{"new-york-fold", "utc-overlap"}}}
			_, err = s.SaveNotificationPolicy(ctx, p)
			require.NoError(t, err)
			at := time.Date(time.Now().Year()+2, time.November, 1, 5, 30, 15, 0, time.UTC)
			at = at.AddDate(0, 0, (7-int(at.Weekday()))%7)
			evaluateGroup(t, e, at, map[string]float64{"a": 1})
			processGroup(t, e, at, false)
			assert.Empty(t, received)
			if resolve {
				evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 0})
			}
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			defer s.DB.Close()
			e.Store = s
			processGroup(t, e, at.Add(105*time.Second), !resolve)
			if resolve {
				assert.Empty(t, received)
				processGroup(t, e, at.Add(64*time.Minute), false)
				return
			}
			first := <-received
			require.Len(t, first.Alerts, 1)
			secondFold := at.Add(time.Hour)
			processGroup(t, e, secondFold, false)
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			defer s.DB.Close()
			e.Store = s
			processGroup(t, e, secondFold.Add(105*time.Second), false)
			processGroup(t, e, secondFold.Add(225*time.Second), true)
			resumed := <-received
			require.Len(t, resumed.Alerts, 1)
			assert.NotEqual(t, first.ID, resumed.ID)
			processGroup(t, e, secondFold.Add(285*time.Second), false)
			assert.Empty(t, received)
			history, err := s.AlertHistory(ctx, "service", 100)
			require.NoError(t, err)
			require.Len(t, history, 1, "recurring timing resumes do not invent transitions")
		})
	}
}

func TestNotificationGroupInflightNewMemberCoalescesWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	requests := make(chan receivedGroup, 8)
	release := make(chan struct{})
	var count atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body receivedGroup
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- body
		if count.Add(1) == 1 {
			<-release
		}
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "0s", "2s", "0s")
	at := time.Now().Add(time.Hour)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	done := make(chan error, 1)
	go func() { _, err := e.ProcessNotification(ctx, at); done <- err }()
	first := <-requests
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 1, "b": 2})
	processGroup(t, e, at.Add(time.Second), false)
	close(release)
	require.NoError(t, <-done)
	processGroup(t, e, at.Add(2*time.Second), true)
	updated := <-requests
	require.Len(t, updated.Alerts, 2)
	assert.NotEqual(t, first.ID, updated.ID)
	processGroup(t, e, at.Add(3*time.Second), false)
	assert.Empty(t, requests)
}

func TestNotificationGroupHTTPRetryRestartPreservesSnapshotAndNewMembers(t *testing.T) {
	ctx := context.Background()
	requests := make(chan receivedGroup, 8)
	var count atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body receivedGroup
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- body
		if count.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "retry.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	e := groupEngine(t, s, webhook.URL, "0s", "2s", "0s")
	at := time.Now().Add(time.Hour)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	processGroup(t, e, at, true)
	first := <-requests
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 9, "b": 2})
	processGroup(t, e, at.Add(3*time.Second), false)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	processGroup(t, e, at.Add(5*time.Second), true)
	retry := <-requests
	assert.Equal(t, first, retry, "same ID retries the same frozen payload after restart")
	processGroup(t, e, at.Add(6*time.Second), false)
	processGroup(t, e, at.Add(7*time.Second), true)
	updated := <-requests
	require.Len(t, updated.Alerts, 2)
	assert.NotEqual(t, first.ID, updated.ID)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, 2, rows[1].Attempts)
}

func TestNotificationGroupObsoleteRouteCleanupAndActiveInterval(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
	_, err := s.SaveMuteTiming(ctx, model.MuteTiming{Name: "always", TimeIntervals: []json.RawMessage{json.RawMessage(`{"location":"UTC"}`)}})
	require.NoError(t, err)
	p, err := s.NotificationPolicy(ctx)
	require.NoError(t, err)
	p.Routes = []model.NotificationRoute{{Receiver: "webhook", MuteTimeIntervals: []string{"always"}}}
	p, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	at := time.Now().Add(time.Hour)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	processGroup(t, e, at, false)
	p.Routes = nil
	p, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	require.NoError(t, s.DeleteMuteTiming(ctx, "always"))
	processGroup(t, e, at.Add(time.Second), true)
	<-received
	processGroup(t, e, at.Add(2*time.Second), false)
	// Restrict the same active alert to a future calendar window. A policy change
	// and interval expiry resume it without fabricating a new state transition.
	window := at.UTC().Truncate(time.Minute).Add(2 * time.Minute)
	end := window.Add(time.Minute)
	endTime := end.Format("15:04")
	if endTime == "00:00" {
		endTime = "24:00"
	}
	raw, _ := json.Marshal(map[string]any{"times": []map[string]string{{"start_time": window.Format("15:04"), "end_time": endTime}}, "location": "UTC"})
	_, err = s.SaveMuteTiming(ctx, model.MuteTiming{Name: "active-window", TimeIntervals: []json.RawMessage{raw}})
	require.NoError(t, err)
	p.Routes = []model.NotificationRoute{{ActiveTimeIntervals: []string{"active-window"}}}
	_, err = s.SaveNotificationPolicy(ctx, p)
	require.NoError(t, err)
	processGroup(t, e, at.Add(3*time.Second), false)
	processGroup(t, e, window, true)
	<-received
	processGroup(t, e, window.Add(time.Second), false)
	assert.Empty(t, received)
	history, err := s.AlertHistory(ctx, "service", 100)
	require.NoError(t, err)
	assert.Len(t, history, 1)
}

func TestNotificationGroupRetryCannotReplayAnEarlierAlertPeriod(t *testing.T) {
	for _, failedResolved := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-firing", true: "old-resolved"}[failedResolved], func(t *testing.T) {
			ctx := context.Background()
			s := notificationStore(t)
			received := make(chan receivedGroup, 8)
			var code atomic.Int32
			code.Store(400)
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var v receivedGroup
				require.NoError(t, json.NewDecoder(r.Body).Decode(&v))
				received <- v
				w.WriteHeader(int(code.Load()))
			}))
			defer webhook.Close()
			e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
			at := time.Now().Add(time.Hour)
			if failedResolved {
				code.Store(204)
			}
			evaluateGroup(t, e, at, map[string]float64{"a": 1})
			processGroup(t, e, at, true)
			first := <-received
			evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"a": 0})
			if failedResolved {
				code.Store(400)
				processGroup(t, e, at.Add(time.Second), true)
				resolved := <-received
				assert.Equal(t, "resolved", resolved.Status)
			}
			rows, err := s.Notifications(ctx, "", 100)
			require.NoError(t, err)
			failed := rows[0]
			require.Equal(t, "failed", failed.State)
			evaluateGroup(t, e, at.Add(2*time.Second), map[string]float64{"a": 9})
			code.Store(204)
			_, err = s.RetryNotification(ctx, failed.ID)
			require.NoError(t, err)
			processGroup(t, e, at.Add(3*time.Second), false)
			processGroup(t, e, at.Add(3*time.Second), true)
			fresh := <-received
			assert.Equal(t, "firing", fresh.Status)
			assert.NotEqual(t, first.Alerts[0].StartsAt, fresh.Alerts[0].StartsAt)
			assert.Equal(t, 9.0, fresh.Alerts[0].Values["A"])
			assert.Empty(t, received, "manual retry discards obsolete firing/resolved snapshot")
		})
	}
}

func TestNotificationGroupInterruptedFinalLeaseCanBeRetried(t *testing.T) {
	ctx := context.Background()
	s := notificationStore(t)
	webhook, received := groupReceiver(t)
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
	at := time.Now().Add(time.Hour)
	evaluateGroup(t, e, at, map[string]float64{"a": 1})
	claimed, _, err := s.ClaimNotification(ctx, at.UnixMilli())
	require.NoError(t, err)
	_, err = s.DB.ExecContext(ctx, `UPDATE alert_notification_outbox SET attempts=max_attempts,next_attempt_at=? WHERE id=?`, at.Add(time.Second).UnixMilli(), claimed.ID)
	require.NoError(t, err)
	processGroup(t, e, at.Add(2*time.Second), false)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Equal(t, "failed", rows[0].State)
	_, err = s.RetryNotification(ctx, claimed.ID)
	require.NoError(t, err)
	processGroup(t, e, at.Add(3*time.Second), true)
	body := <-received
	assert.Equal(t, strconv.FormatInt(claimed.ID, 10), body.ID)
}

func TestNotificationGroupPendingSilenceRestartOverlapAndResolution(t *testing.T) {
	for _, resolve := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "resolved"}[resolve], func(t *testing.T) {
			ctx := context.Background()
			webhook, received := groupReceiver(t)
			defer webhook.Close()
			path := filepath.Join(t.TempDir(), "pending-silences.db")
			s, err := store.Open(path)
			require.NoError(t, err)
			e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
			at := time.Now().Add(time.Hour)
			evaluateGroup(t, e, at, map[string]float64{"a": 1})
			processGroup(t, e, at, true)
			first := <-received
			for _, period := range []struct{ start, end int }{{5, 9}, {7, 12}} {
				_, err = s.SaveSilence(ctx, model.Silence{Matchers: []model.SilenceMatcher{{Name: "team", Value: "ops"}}, StartsAt: at.Add(time.Duration(period.start) * time.Second), EndsAt: at.Add(time.Duration(period.end) * time.Second), CreatedBy: "operator", Comment: "future maintenance"}, at.Add(3*time.Second))
				require.NoError(t, err)
			}
			processGroup(t, e, at.Add(3*time.Second), false)
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			defer s.DB.Close()
			e.Store = s
			processGroup(t, e, at.Add(5*time.Second), false)
			processGroup(t, e, at.Add(9*time.Second), false)
			if resolve {
				evaluateGroup(t, e, at.Add(10*time.Second), map[string]float64{"a": 0})
			}
			processGroup(t, e, at.Add(12*time.Second), !resolve)
			if resolve {
				assert.Empty(t, received)
			} else {
				resumed := <-received
				assert.NotEqual(t, first.ID, resumed.ID)
				processGroup(t, e, at.Add(13*time.Second), false)
				assert.Empty(t, received)
			}
		})
	}
}
