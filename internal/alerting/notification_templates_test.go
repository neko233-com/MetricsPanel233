package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationTemplateSnapshotBeforeRenderRetryRestartAndSecretIsolation(t *testing.T) {
	ctx := context.Background()
	received := make(chan map[string]any, 8)
	var count atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer private-token-233", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		received <- payload
		if count.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer webhook.Close()
	path := filepath.Join(t.TempDir(), "template-snapshot.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	point := addNotificationPoint(t, s, webhook.URL)
	source := `{{define "ops.title"}}OLD {{.Receiver}} {{len .Alerts.Firing}}{{end}}{{define "ops.message"}}{{range .Alerts}}{{.Labels.alertname}}={{index .Values "A"}}/{{.RuleUID}}{{end}}{{end}}`
	template, err := s.SaveNotificationTemplate(ctx, model.NotificationTemplate{Name: "ops", Template: source})
	require.NoError(t, err)
	point.TitleTemplate = `{{template "ops.title" .}}`
	point.MessageTemplate = `{{template "ops.message" .}}`
	point, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	e := silenceEngine(t, s)
	e.TemplateURL = &url.URL{Scheme: "http", Host: "original.example"}
	at := time.Now().Add(time.Hour)
	_, err = e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	snapshot, exists, err := s.NotificationRenderSnapshot(ctx, rows[0].ID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Empty(t, snapshot.Payload)
	assert.Equal(t, 1, snapshot.Bundle.Templates[0].Version)
	snapshotJSON, err := json.Marshal(snapshot.Bundle)
	require.NoError(t, err)
	assert.NotContains(t, string(snapshotJSON), "private-token-233")
	assert.NotContains(t, string(snapshotJSON), "private-key-233")
	assert.NotContains(t, string(snapshotJSON), webhook.URL, "render snapshot excludes contact URL and credentials")
	template.Template = strings.ReplaceAll(source, "OLD", "NEW")
	_, err = s.SaveNotificationTemplate(ctx, template)
	require.NoError(t, err)
	point.TitleTemplate = "NEW CONTACT TITLE"
	point, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	processGroup(t, e, at, true)
	first := <-received
	assert.Equal(t, "OLD Operations 1", first["title"])
	assert.Equal(t, "Service=1/service", first["message"])
	assert.Equal(t, "http://original.example", first["externalURL"])
	point.TitleTemplate, point.MessageTemplate = "", ""
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	require.NoError(t, s.DeleteNotificationTemplate(ctx, "ops", nil))
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e.Store = s
	e.TemplateURL = &url.URL{Scheme: "http", Host: "changed.example"}
	processGroup(t, e, at.Add(5*time.Second), true)
	second := <-received
	assert.Equal(t, first, second, "same notification ID retains its rendered body, template versions and URLs after restart")
	rows, err = s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, "sent", rows[0].State)
	assert.Equal(t, 2, rows[0].Attempts)
}

func TestNotificationTemplateDefaultWebhookPayloadIsUnchanged(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	received := make(chan map[string]any, 2)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		received <- payload
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	addNotificationPoint(t, s, webhook.URL)
	_, err := s.SaveNotificationTemplate(ctx, model.NotificationTemplate{Name: "unselected", Template: "This must not replace the default webhook"})
	require.NoError(t, err)
	e := silenceEngine(t, s)
	at := time.Now().Add(time.Hour)
	_, err = e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	rows, err := s.Notifications(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var raw string
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM alert_notification_outbox WHERE id=?`, rows[0].ID).Scan(&raw))
	var expected map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &expected))
	expected["notificationId"] = strconv.FormatInt(rows[0].ID, 10)
	_, exists, err := s.NotificationRenderSnapshot(ctx, rows[0].ID)
	require.NoError(t, err)
	assert.False(t, exists)
	processGroup(t, e, at, true)
	assert.Equal(t, expected, <-received)
}

func TestNotificationTemplateFailureAndLimitNeverSendFallbackOrSecrets(t *testing.T) {
	for _, expression := range []string{`{{.BearerToken}}`, `{{range 1000000000000}}{{end}}`, `{{printf "%99999999999s" "x"}}`} {
		t.Run(expression, func(t *testing.T) {
			s := notificationStore(t)
			ctx := context.Background()
			var received atomic.Int32
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(204) }))
			defer webhook.Close()
			point := addNotificationPoint(t, s, webhook.URL)
			point.TitleTemplate = expression
			_, err := s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
			require.NoError(t, err)
			e := silenceEngine(t, s)
			at := time.Now().Add(time.Hour)
			_, err = e.Evaluate(ctx, "service", at)
			require.NoError(t, err)
			started := time.Now()
			processGroup(t, e, at, true)
			assert.Less(t, time.Since(started), time.Second)
			assert.Zero(t, received.Load())
			rows, err := s.Notifications(ctx, "", 100)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, "failed", rows[0].State)
			assert.Equal(t, 1, rows[0].Attempts)
			assert.Zero(t, rows[0].HTTPStatus)
			assert.Equal(t, "Notification template rendering failed for the saved version", rows[0].LastError)
			raw, _ := json.Marshal(rows)
			assert.NotContains(t, string(raw), "private-token-233")
			assert.NotContains(t, string(raw), "private-key-233")
			_, err = s.RetryNotification(ctx, rows[0].ID)
			require.ErrorContains(t, err, "saved template version failed to render")
			processGroup(t, e, at.Add(time.Minute), false)
		})
	}
}

func TestNotificationTemplateGroupedDataHasEveryRuleUID(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	received := make(chan map[string]any, 2)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&v))
		received <- v
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	e := groupEngine(t, s, webhook.URL, "0s", "1s", "0s")
	point, err := s.ContactPoint(ctx, "webhook")
	require.NoError(t, err)
	point.TitleTemplate = `{{.GroupLabels.team}}/{{len .Alerts.Firing}}`
	point.MessageTemplate = `{{range .Alerts}}{{.RuleUID}} {{end}}`
	_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	_, err = s.SaveAlertRule(ctx, model.AlertRule{UID: "other", Title: "Other", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	e.Query = groupValues(map[string]float64{"a": 1})
	at := time.Now().Add(time.Hour)
	_, err = e.Evaluate(ctx, "service", at)
	require.NoError(t, err)
	_, err = e.Evaluate(ctx, "other", at)
	require.NoError(t, err)
	processGroup(t, e, at, true)
	body := <-received
	assert.Equal(t, "ops/2", body["title"])
	assert.Equal(t, "other service ", body["message"])
}
