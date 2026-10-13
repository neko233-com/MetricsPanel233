package alerting

import (
	"context"
	"encoding/json"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeSMTP struct {
	listener       net.Listener
	mu             sync.Mutex
	wg             sync.WaitGroup
	connections    map[net.Conn]bool
	attempts       map[string]int
	messages       map[string][]string
	firstRejection int
}

func nativeSMTPServer(t *testing.T, rejection int) *nativeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &nativeSMTP{listener: listener, connections: map[net.Conn]bool{}, attempts: map[string]int{}, messages: map[string][]string{}, firstRejection: rejection}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections[c] = true
			f.wg.Add(1)
			f.mu.Unlock()
			go func() {
				defer f.wg.Done()
				defer func() { c.Close(); f.mu.Lock(); delete(f.connections, c); f.mu.Unlock() }()
				f.serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		f.mu.Lock()
		for c := range f.connections {
			c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

func (f *nativeSMTP) serve(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	p := textproto.NewConn(c)
	defer p.Close()
	_ = p.PrintfLine("220 local.test ESMTP")
	targets := []string{}
	for {
		line, err := p.ReadLine()
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch command {
		case "EHLO", "HELO":
			_ = p.PrintfLine("250 local.test")
		case "MAIL":
			targets = nil
			_ = p.PrintfLine("250 accepted")
		case "RCPT":
			start, end := strings.Index(line, "<"), strings.Index(line, ">")
			if start < 0 || end <= start {
				_ = p.PrintfLine("550 malformed")
				continue
			}
			target := line[start+1 : end]
			f.mu.Lock()
			f.attempts[target]++
			reject := target == "b@example.test" && f.attempts[target] == 1
			f.mu.Unlock()
			if reject {
				_ = p.PrintfLine("%d rejected", f.firstRejection)
			} else {
				targets = append(targets, target)
				_ = p.PrintfLine("250 accepted")
			}
		case "DATA":
			_ = p.PrintfLine("354 continue")
			raw, err := p.ReadDotBytes()
			if err != nil {
				return
			}
			f.mu.Lock()
			for _, target := range targets {
				f.messages[target] = append(f.messages[target], string(raw))
			}
			f.mu.Unlock()
			_ = p.PrintfLine("250 queued")
		case "RSET":
			targets = nil
			_ = p.PrintfLine("250 reset")
		case "QUIT":
			_ = p.PrintfLine("221 bye")
			return
		default:
			_ = p.PrintfLine("250 accepted")
		}
	}
}

func nativeReceiverEngine(t *testing.T, smtp *nativeSMTP) (*Engine, model.Receiver, model.NotificationTemplate) {
	t.Helper()
	s := notificationStore(t)
	e := groupEngine(t, s, "http://127.0.0.1:1/legacy", "0s", "1s", "0s")
	ctx := context.Background()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	profile, err := s.SaveSMTPProfile(ctx, model.SMTPProfileInput{SMTPProfile: model.SMTPProfile{UID: "smtp", Name: "SMTP", Settings: mustReceiverJSON(map[string]any{"host": smtp.listener.Addr().String(), "from_address": "panel@example.test", "starttls_policy": receivers.NoStartTLS})}})
	require.NoError(t, err)
	template, err := s.SaveNotificationTemplate(ctx, model.NotificationTemplate{Name: "mixed", Template: `{{define "mixed"}}firing={{range .Alerts.Firing}}{{.Labels.instance}}|{{end}} resolved={{range .Alerts.Resolved}}{{.Labels.instance}}|{{end}}{{end}}`})
	require.NoError(t, err)
	r, err := s.SaveReceiver(ctx, model.ReceiverInput{UID: "webhook", Name: "Operations", Version: 1, Integrations: []model.ReceiverIntegrationInput{{ReceiverIntegration: model.ReceiverIntegration{UID: "email", Type: "email", SMTPProfileUID: profile.UID, SMTPProfileRevision: profile.Revision, Settings: mustReceiverJSON(receivers.EmailSettings{Addresses: "a@example.test;b@example.test", SingleEmail: true, Subject: "Frozen source subject", Message: `{{template "mixed" .}}`})}}}})
	require.NoError(t, err)
	// Test-only ownership fixture. Production has no cutover/activation API.
	_, err = s.DB.Exec(`UPDATE alert_receiver_ownership SET mode='native',epoch=epoch+1 WHERE id=1`)
	require.NoError(t, err)
	return e, r, template
}

func nativeBody(t *testing.T, raw string) string {
	t.Helper()
	message, err := mail.ReadMessage(strings.NewReader(raw))
	require.NoError(t, err)
	body, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	require.NoError(t, err)
	return string(body)
}

func TestNativeReceiverMixedSMTPPartitionsPartialACKRestartAndFrozenProducerBundle(t *testing.T) {
	ctx := context.Background()
	smtp := nativeSMTPServer(t, 550)
	e, r, template := nativeReceiverEngine(t, smtp)
	at := time.Now()
	evaluateGroup(t, e, at, map[string]float64{"old-a": 1, "old-b": 1})
	processGroup(t, e, at, true)
	history, err := e.Store.Notifications(ctx, "", 20)
	require.NoError(t, err)
	require.Equal(t, "failed", history[0].State)
	var notified int
	require.NoError(t, e.Store.DB.QueryRow(`SELECT sum(notified) FROM alert_notification_group_members`).Scan(&notified))
	require.Zero(t, notified, "partial native firing ACK must not depend on legacy global notified")
	path := e.Store.Path
	require.NoError(t, e.Store.DB.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	defer reopened.DB.Close()
	require.NoError(t, reopened.InitializeReceiverSchema(ctx))
	e.Store = reopened
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"old-a": 0, "old-b": 0, "live": 1})
	claim, _, err := reopened.ClaimNotification(ctx, at.Add(2*time.Second).UnixMilli())
	require.NoError(t, err)
	require.Equal(t, "firing", claim.Status)
	template.Template = `{{define "mixed"}}edited-after-producer{{end}}`
	_, err = reopened.SaveNotificationTemplate(ctx, template)
	require.NoError(t, err)
	edit := model.ReceiverInput{UID: r.UID, Name: r.Name, Version: r.Version}
	for _, integration := range r.Integrations {
		integration.Settings = mustReceiverJSON(receivers.EmailSettings{Addresses: "replacement@example.test", Subject: "Edited subject", Message: "Edited body"})
		edit.Integrations = append(edit.Integrations, model.ReceiverIntegrationInput{ReceiverIntegration: integration})
	}
	_, err = reopened.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	require.NoError(t, e.processReceiverNotification(ctx, claim, at.Add(2*time.Second)))
	smtp.mu.Lock()
	defer smtp.mu.Unlock()
	require.Len(t, smtp.messages["a@example.test"], 2, "one combined MIME per destination for each parent source")
	require.Len(t, smtp.messages["b@example.test"], 1, "unACKed resolved destination receives only current firing members")
	a := nativeBody(t, smtp.messages["a@example.test"][1])
	b := nativeBody(t, smtp.messages["b@example.test"][0])
	assert.Contains(t, a, "firing=live| resolved=")
	resolved := strings.TrimSpace(strings.SplitN(a, "resolved=", 2)[1])
	assert.ElementsMatch(t, []string{"old-a", "old-b"}, strings.Split(strings.TrimSuffix(resolved, "|"), "|"))
	assert.Contains(t, b, "firing=live| resolved=")
	assert.NotContains(t, b, "old-a")
	assert.NotContains(t, b, "old-b")
	assert.NotContains(t, a, "edited-after-producer")
	assert.NotContains(t, a, "Edited body")
	assert.Contains(t, smtp.messages["a@example.test"][1], "Frozen source subject")
	assert.NotContains(t, smtp.messages["b@example.test"][0], "a@example.test", "MIME recipient headers use the selected destination only")
	assert.Empty(t, smtp.messages["replacement@example.test"])
	var receipts int
	require.NoError(t, reopened.DB.QueryRow(`SELECT count(*) FROM alert_receiver_firing_receipts`).Scan(&receipts))
	assert.Equal(t, 4, receipts, "two old periods ACKed only by A, then live ACKed by A and B; resolved members never create firing receipts")
	history, err = reopened.Notifications(ctx, "", 20)
	require.NoError(t, err)
	assert.Equal(t, "sent", history[0].State)
}

func TestNativeReceiverSMTPRecipientRetryRestartsWithoutReplayingACK(t *testing.T) {
	ctx := context.Background()
	smtp := nativeSMTPServer(t, 450)
	e, _, _ := nativeReceiverEngine(t, smtp)
	at := time.Now()
	evaluateGroup(t, e, at, map[string]float64{"live": 1})
	processGroup(t, e, at, true)
	history, err := e.Store.Notifications(ctx, "", 20)
	require.NoError(t, err)
	require.Equal(t, "pending", history[0].State)
	var prepared int
	require.NoError(t, e.Store.DB.QueryRow(`SELECT count(*) FROM alert_receiver_source_preparations`).Scan(&prepared))
	require.Equal(t, 1, prepared)
	path := e.Store.Path
	require.NoError(t, e.Store.DB.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	defer reopened.DB.Close()
	require.NoError(t, reopened.InitializeReceiverSchema(ctx))
	e.Store = reopened
	processGroup(t, e, at.Add(2*time.Second), true)
	smtp.mu.Lock()
	defer smtp.mu.Unlock()
	require.Len(t, smtp.messages["a@example.test"], 1)
	require.Len(t, smtp.messages["b@example.test"], 1)
	assert.Equal(t, 1, smtp.attempts["a@example.test"])
	assert.Equal(t, 2, smtp.attempts["b@example.test"])
	history, err = reopened.Notifications(ctx, "", 20)
	require.NoError(t, err)
	assert.Equal(t, "sent", history[0].State)
}

func editNativeReceiverContent(t *testing.T, e *Engine, r model.Receiver, disableResolved bool) {
	t.Helper()
	input := model.ReceiverInput{UID: r.UID, Name: r.Name, Version: r.Version}
	for _, integration := range r.Integrations {
		integration.Settings = mustReceiverJSON(receivers.EmailSettings{Addresses: "a@example.test;b@example.test", SingleEmail: true, Subject: "New producer subject", Message: `{{template "mixed" .}}`})
		integration.DisableResolveMessage = disableResolved
		input.Integrations = append(input.Integrations, model.ReceiverIntegrationInput{ReceiverIntegration: integration})
	}
	_, err := e.Store.SaveReceiver(context.Background(), input)
	require.NoError(t, err)
}

func TestNativeReceiverContentOnlyRevisionUsesOneMixedMIMEWithOriginalACKProof(t *testing.T) {
	ctx := context.Background()
	smtp := nativeSMTPServer(t, 550)
	e, r, _ := nativeReceiverEngine(t, smtp)
	at := time.Now()
	evaluateGroup(t, e, at, map[string]float64{"old": 1})
	processGroup(t, e, at, true)
	editNativeReceiverContent(t, e, r, false)
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"old": 0, "live": 1})
	claim, _, err := e.Store.ClaimNotification(ctx, at.Add(2*time.Second).UnixMilli())
	require.NoError(t, err)
	source, err := e.Store.ReceiverNotificationSource(ctx, claim, at.Add(2*time.Second))
	require.NoError(t, err)
	destinations, err := e.receiverSourceDestinations(ctx, claim, source)
	require.NoError(t, err)
	parts, err := e.Store.ReceiverNotificationPartitions(ctx, source, at.Add(2*time.Second), destinations)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	for _, part := range parts {
		if part.Target == "a@example.test" {
			assert.Equal(t, int64(2), part.Integration.Revision)
			require.Len(t, part.Members, 2)
			for _, member := range part.Members {
				if member.Status == "resolved" {
					assert.Equal(t, int64(1), member.ACKRevision)
				} else {
					assert.Zero(t, member.ACKRevision)
				}
			}
		}
	}
	require.NoError(t, e.processReceiverNotification(ctx, claim, at.Add(2*time.Second)))
	smtp.mu.Lock()
	defer smtp.mu.Unlock()
	require.Len(t, smtp.messages["a@example.test"], 2)
	require.Len(t, smtp.messages["b@example.test"], 1)
	body := nativeBody(t, smtp.messages["a@example.test"][1])
	assert.Contains(t, body, "firing=live|")
	assert.Contains(t, body, "resolved=old|")
	assert.Contains(t, smtp.messages["a@example.test"][1], "New producer subject")
	assert.NotContains(t, nativeBody(t, smtp.messages["b@example.test"][0]), "old|")
}

func TestNativeReceiverDisablingResolvedAfterPreparationRegeneratesLiveFiring(t *testing.T) {
	ctx := context.Background()
	smtp := nativeSMTPServer(t, 550)
	e, r, _ := nativeReceiverEngine(t, smtp)
	at := time.Now()
	evaluateGroup(t, e, at, map[string]float64{"old": 1})
	processGroup(t, e, at, true)
	evaluateGroup(t, e, at.Add(time.Second), map[string]float64{"old": 0, "live": 1})
	claim, _, err := e.Store.ClaimNotification(ctx, at.Add(2*time.Second).UnixMilli())
	require.NoError(t, err)
	source, err := e.Store.ReceiverNotificationSource(ctx, claim, at.Add(2*time.Second))
	require.NoError(t, err)
	destinations, err := e.receiverSourceDestinations(ctx, claim, source)
	require.NoError(t, err)
	parts, err := e.Store.ReceiverNotificationPartitions(ctx, source, at.Add(2*time.Second), destinations)
	require.NoError(t, err)
	inputs := []model.ReceiverDispatchInput{}
	for index, part := range parts {
		input, err := e.prepareReceiverPartition(ctx, claim, part, index)
		require.NoError(t, err)
		inputs = append(inputs, input)
	}
	require.NoError(t, e.Store.EnqueueReceiverSourceDispatches(ctx, source, inputs, at.Add(2*time.Second)))
	editNativeReceiverContent(t, e, r, true)
	require.NoError(t, e.processReceiverNotification(ctx, claim, at.Add(2*time.Second)))
	processGroup(t, e, at.Add(3*time.Second), true)
	smtp.mu.Lock()
	defer smtp.mu.Unlock()
	require.Len(t, smtp.messages["a@example.test"], 2, "revoked mixed preparation must retain live membership even with repeat disabled")
	require.Len(t, smtp.messages["b@example.test"], 1)
	body := nativeBody(t, smtp.messages["a@example.test"][1])
	assert.Contains(t, body, "firing=live|")
	assert.NotContains(t, body, "resolved=old|")
	var unfinished int
	require.NoError(t, e.Store.DB.QueryRow(`SELECT count(*) FROM alert_receiver_source_dispatches l JOIN alert_notification_outbox n ON n.id=l.notification_id JOIN alert_receiver_deliveries d ON d.dispatch_id=l.dispatch_id WHERE n.state='canceled' AND d.state IN ('pending','partial','sending')`).Scan(&unfinished))
	assert.Zero(t, unfinished, "terminal parent drains unfinished children but retains original firing ACKs")
}

func TestReceiverSchemaDefaultKeepsLegacyParentScheduler(t *testing.T) {
	var legacyHits, nativeHits atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { legacyHits.Add(1); w.WriteHeader(204) }))
	defer legacy.Close()
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nativeHits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer native.Close()
	s := notificationStore(t)
	e := groupEngine(t, s, legacy.URL, "0s", "1s", "0s")
	ctx := context.Background()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	_, err := s.SaveReceiver(ctx, model.ReceiverInput{UID: "webhook", Name: "Operations", Version: 1, Integrations: []model.ReceiverIntegrationInput{{ReceiverIntegration: model.ReceiverIntegration{UID: "slack", Type: "slack", Settings: json.RawMessage(`{"recipient":"C123"}`)}, SecureSettings: map[string]string{"url": native.URL}}}})
	require.NoError(t, err)
	at := time.Now()
	evaluateGroup(t, e, at, map[string]float64{"live": 1})
	processGroup(t, e, at, true)
	assert.Equal(t, int32(1), legacyHits.Load())
	assert.Zero(t, nativeHits.Load())
	owner, err := s.NotificationQueueOwnership(ctx)
	require.NoError(t, err)
	assert.Equal(t, "legacy", owner.Mode)
}
