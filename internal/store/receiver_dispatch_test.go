package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func queuePeriods(p receivers.Prepared, period model.ReceiverPeriod) map[string][]model.ReceiverPeriod {
	out := map[string][]model.ReceiverPeriod{}
	for _, op := range p.Operations() {
		out[op.Key] = []model.ReceiverPeriod{period}
	}
	return out
}
func queueSlack(t *testing.T, s *Store, r model.Receiver, gen int64, status string, at time.Time, period model.ReceiverPeriod) model.ReceiverDispatchInput {
	t.Helper()
	in := model.ReceiverDispatchInput{ReceiverUID: r.UID, ReceiverVersion: r.Version, GroupKey: "critical-group", Generation: gen, Status: status, CreatedAt: at.UnixMilli(), DataSnapshot: json.RawMessage(`{"labels":{"token":"data-private-marker"},"value":9007199254740993}`), TemplateSnapshot: json.RawMessage(`{"source":"template-private-marker"}`)}
	for _, i := range r.Integrations {
		p, err := s.PrepareReceiverSlack(context.Background(), i.UID, i.Revision, receivers.SlackContent{Recipient: "C123", Text: "body-private-marker"}, receivers.Metadata{DeliveryID: status + "-" + i.UID, CreatedAt: at})
		require.NoError(t, err)
		in.Deliveries = append(in.Deliveries, model.ReceiverPreparedDelivery{IntegrationUID: i.UID, Revision: i.Revision, Prepared: p, Periods: queuePeriods(p, period)})
	}
	return in
}
func queueSend(t *testing.T, s *Store, c *ClaimedReceiverDelivery, at time.Time) []receivers.Outcome {
	t.Helper()
	defer c.Close()
	ctx := context.Background()
	out := c.Transport().SendJournaled(ctx, c.Prepared(), c.Progress(), func(ctx context.Context, out []receivers.Outcome) error {
		return s.CheckpointReceiverDelivery(ctx, c, out, at.Add(time.Millisecond))
	})
	require.NoError(t, s.FinishReceiverDelivery(ctx, c, out, at.Add(2*time.Millisecond)))
	return out
}

func TestReceiverDispatchIndependentChannelsFrozenSnapshotsAndRestart(t *testing.T) {
	ctx := context.Background()
	s := receiverStore(t)
	var hitsA, hitsB, hitsNew atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), "body-private-marker")
		w.Write([]byte("ok"))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hitsB.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer b.Close()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hitsNew.Add(1); w.Write([]byte("ok")) }))
	defer replacement.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("channels", "Channels", a.URL, b.URL))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance-private-marker", StartedAt: at.Add(-time.Minute).UnixMilli()}
	input := queueSlack(t, s, r, 1, "firing", at, period)
	dispatch, err := s.EnqueueReceiverDispatch(ctx, input)
	require.NoError(t, err)
	again := input
	again.CreatedAt++
	same, err := s.EnqueueReceiverDispatch(ctx, again)
	require.NoError(t, err)
	assert.Equal(t, dispatch.ID, same.ID)
	changed := input
	changed.DataSnapshot = json.RawMessage(`{"changed":true}`)
	_, err = s.EnqueueReceiverDispatch(ctx, changed)
	require.ErrorIs(t, err, ErrReceiverGeneration)
	edit := receiverInput(r)
	edit.Integrations[1].SecureSettings = map[string]string{"url": replacement.URL}
	updated, err := s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Integrations[1].Revision)
	c, err := s.ClaimReceiverDelivery(ctx, at.Add(time.Second), time.Minute)
	require.NoError(t, err)
	out := queueSend(t, s, c, at.Add(time.Second))
	require.True(t, out[0].Sent)
	c, err = s.ClaimReceiverDelivery(ctx, at.Add(time.Second), time.Minute)
	require.NoError(t, err)
	out = queueSend(t, s, c, at.Add(time.Second))
	require.False(t, out[0].Sent)
	require.True(t, out[0].Retryable)
	assert.Equal(t, int32(1), hitsB.Load())
	assert.Zero(t, hitsNew.Load())
	metadata, err := json.Marshal(c)
	require.NoError(t, err)
	for _, private := range []string{b.URL, "body-private-marker", "data-private-marker", "template-private-marker", "instance-private-marker"} {
		assert.NotContains(t, string(metadata), private)
	}
	for _, table := range []string{"alert_receiver_dispatches", "alert_receiver_deliveries"} {
		rows, err := s.DB.Query("SELECT sealed FROM " + table)
		require.NoError(t, err)
		for rows.Next() {
			var sealed []byte
			require.NoError(t, rows.Scan(&sealed))
			for _, private := range []string{"body-private-marker", "data-private-marker", "template-private-marker", "instance-private-marker"} {
				assert.NotContains(t, string(sealed), private)
			}
		}
		require.NoError(t, rows.Close())
	}
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	c, err = s.ClaimReceiverDelivery(ctx, at.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	require.Equal(t, r.Integrations[1].UID, c.IntegrationUID)
	require.Equal(t, int64(1), c.Revision)
	out = queueSend(t, s, c, at.Add(3*time.Second))
	require.True(t, out[0].Sent)
	assert.Equal(t, int32(1), hitsA.Load())
	assert.Equal(t, int32(2), hitsB.Load())
	assert.Zero(t, hitsNew.Load(), "old pending delivery uses its encrypted historical config")
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_firing_receipts`).Scan(&count))
	assert.Equal(t, 2, count)
	// A new destination has no firing ACK; historical ACKed channel revisions do.
	resolvedCurrent := queueSlack(t, s, updated, 2, "resolved", at.Add(4*time.Second), period)
	_, err = s.EnqueueReceiverDispatch(ctx, resolvedCurrent)
	require.ErrorIs(t, err, ErrReceiverUnacknowledged)
	oldChannels := r
	oldChannels.Version = updated.Version
	resolved := queueSlack(t, s, oldChannels, 2, "resolved", at.Add(4*time.Second), period)
	_, err = s.EnqueueReceiverDispatch(ctx, resolved)
	require.NoError(t, err)
	for range 2 {
		c, err = s.ClaimReceiverDelivery(ctx, at.Add(5*time.Second), time.Minute)
		require.NoError(t, err)
		queueSend(t, s, c, at.Add(5*time.Second))
	}
	assert.Equal(t, int32(2), hitsA.Load())
	assert.Equal(t, int32(3), hitsB.Load())
	assert.Zero(t, hitsNew.Load())
	_, err = s.ClaimReceiverDelivery(ctx, at.Add(6*time.Second), time.Minute)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestReceiverDispatchResolvedMembershipAndOrdering(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("periods", "Periods", server.URL))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "resolved", at, period))
	require.ErrorIs(t, err, ErrReceiverUnacknowledged)
	firing, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "firing", at, period))
	require.NoError(t, err)
	_, err = s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.ErrorIs(t, err, sql.ErrNoRows, "same integration/group cannot overtake its firing lease")
	queueSend(t, s, firing, at)
	second, err := s.ClaimReceiverDelivery(ctx, at.Add(time.Second), time.Minute)
	require.NoError(t, err)
	queueSend(t, s, second, at.Add(time.Second))
	mixed := queueSlack(t, s, r, 3, "resolved", at, period)
	for key, periods := range mixed.Deliveries[0].Periods {
		mixed.Deliveries[0].Periods[key] = append(periods, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "never-fired", StartedAt: at.UnixMilli()})
	}
	_, err = s.EnqueueReceiverDispatch(ctx, mixed)
	require.ErrorIs(t, err, ErrReceiverUnacknowledged, "mixed groups must be filtered and re-rendered, never silently send unACKed members")
	period.StartedAt++
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 3, "resolved", at, period))
	require.ErrorIs(t, err, ErrReceiverUnacknowledged, "a prior alert period's ACK must not leak into the next period")
}

func TestReceiverDeliveryClaimCASConcurrentExpiryAndCheckpointRestart(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("leases", "Leases", server.URL))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	claims := make(chan *ClaimedReceiverDelivery, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
			if err == nil {
				claims <- c
			} else {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(failures)
	require.Len(t, claims, 1)
	for err := range failures {
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	first := <-claims
	defer first.Close()
	later := at.Add(time.Minute + time.Millisecond)
	second, err := s.ClaimReceiverDelivery(ctx, later, time.Minute)
	require.NoError(t, err)
	defer second.Close()
	assert.NotEqual(t, first.token, second.token)
	require.ErrorIs(t, s.ValidateReceiverClaim(ctx, first, later), ErrReceiverLease)
	require.ErrorIs(t, s.FinishReceiverDelivery(ctx, first, nil, later), ErrReceiverLease)
	out := second.Transport().SendJournaled(ctx, second.Prepared(), second.Progress(), func(ctx context.Context, out []receivers.Outcome) error {
		return s.CheckpointReceiverDelivery(ctx, second, out, later.Add(time.Millisecond))
	})
	require.True(t, out[0].Sent)
	// Simulate crash after persisted remote ACK, before Finish clears the lease.
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	_, err = s.ClaimReceiverDelivery(ctx, later.Add(time.Minute+time.Second), time.Minute)
	require.ErrorIs(t, err, sql.ErrNoRows)
	assert.Equal(t, int32(1), hits.Load())
	values, err := s.ReceiverDeliveries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Equal(t, "sent", values[0].State)
	ops, err := s.ReceiverOperations(ctx, values[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "sent", ops[0].State)
	assert.Equal(t, int64(2), ops[0].Attempts)
}

func TestReceiverDeliveryRetryAfterTerminalAndAttemptBound(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("retry", "Retry", "https://example.test/hook"))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	for attempt := 1; attempt <= 8; attempt++ {
		c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
		require.NoError(t, err)
		key := c.Prepared().Operations()[0].Key
		out := receivers.Outcome{OperationKey: key, Retryable: true, Code: "slack_rate_limited", StatusCode: 429, RetryAfter: 40 * time.Second}
		require.NoError(t, s.FinishReceiverDelivery(ctx, c, []receivers.Outcome{out}, at.Add(time.Second)))
		c.Close()
		ops, err := s.ReceiverOperations(ctx, c.ID)
		require.NoError(t, err)
		if attempt < 8 {
			want := 40 * time.Second
			if exponential := time.Second * time.Duration(1<<(attempt-1)); exponential > want {
				want = exponential
			}
			assert.Equal(t, at.Add(time.Second+want).UnixMilli(), ops[0].NextAttemptAt)
			_, err = s.ClaimReceiverDelivery(ctx, at.Add(time.Second+want-time.Millisecond), time.Minute)
			require.ErrorIs(t, err, sql.ErrNoRows)
			at = at.Add(time.Second + want)
		} else {
			assert.Equal(t, "failed", ops[0].State)
			assert.Equal(t, "attempts_exhausted", ops[0].Code)
		}
	}
	_, err = s.ClaimReceiverDelivery(ctx, at.Add(time.Hour), time.Minute)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "firing", at, period))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	key := c.Prepared().Operations()[0].Key
	require.NoError(t, s.FinishReceiverDelivery(ctx, c, []receivers.Outcome{{OperationKey: key, Code: "slack_auth_rejected", StatusCode: 401}}, at.Add(time.Millisecond)))
	ops, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", ops[0].State)
	assert.Equal(t, int64(1), ops[0].Attempts)
}

type receiverSMTPFixture struct {
	listener       net.Listener
	mu             sync.Mutex
	connections    map[net.Conn]bool
	wg             sync.WaitGroup
	accepted       map[string]int
	attempts       map[string]int
	retryRecipient string
}

func newReceiverSMTPFixture(t *testing.T, retryRecipient string) *receiverSMTPFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &receiverSMTPFixture{listener: listener, connections: map[net.Conn]bool{}, accepted: map[string]int{}, attempts: map[string]int{}, retryRecipient: retryRecipient}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections[conn] = true
			f.wg.Add(1)
			f.mu.Unlock()
			go func() {
				defer f.wg.Done()
				defer func() { conn.Close(); f.mu.Lock(); delete(f.connections, conn); f.mu.Unlock() }()
				f.serve(conn)
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
func (f *receiverSMTPFixture) serve(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	text := textproto.NewConn(conn)
	defer text.Close()
	text.PrintfLine("220 local.test ESMTP")
	recipients := []string{}
	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch command {
		case "EHLO", "HELO":
			text.PrintfLine("250 local.test")
		case "MAIL":
			recipients = nil
			text.PrintfLine("250 accepted")
		case "RCPT":
			start, end := strings.Index(line, "<"), strings.Index(line, ">")
			if start < 0 || end < start {
				text.PrintfLine("550 malformed")
				continue
			}
			recipient := line[start+1 : end]
			f.mu.Lock()
			f.attempts[recipient]++
			retry := recipient == f.retryRecipient && f.attempts[recipient] == 1
			f.mu.Unlock()
			if retry {
				text.PrintfLine("450 temporary")
			} else {
				recipients = append(recipients, recipient)
				text.PrintfLine("250 accepted")
			}
		case "DATA":
			text.PrintfLine("354 continue")
			if _, err = text.ReadDotBytes(); err != nil {
				return
			}
			f.mu.Lock()
			for _, r := range recipients {
				f.accepted[r]++
			}
			f.mu.Unlock()
			text.PrintfLine("250 accepted")
		case "QUIT":
			text.PrintfLine("221 closing")
			return
		case "RSET":
			recipients = nil
			text.PrintfLine("250 reset")
		default:
			text.PrintfLine("502 unsupported")
		}
	}
}
func (f *receiverSMTPFixture) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.accepted {
		out[k] = v
	}
	return out
}
func receiverEmail(t *testing.T, s *Store, f *receiverSMTPFixture) (model.Receiver, model.SMTPProfile) {
	t.Helper()
	ctx := context.Background()
	p, err := s.SaveSMTPProfile(ctx, model.SMTPProfileInput{SMTPProfile: model.SMTPProfile{UID: "local-mail", Name: "Local SMTP", Settings: marshalReceiver(map[string]any{"host": f.listener.Addr().String(), "from_address": "sender@example.test", "auth_mode": "none", "starttls_policy": "NoStartTLS"})}})
	require.NoError(t, err)
	r, err := s.SaveReceiver(ctx, model.ReceiverInput{UID: "email-receiver", Name: "Local email", Integrations: []model.ReceiverIntegrationInput{{ReceiverIntegration: model.ReceiverIntegration{UID: "local-email", Type: "email", SMTPProfileUID: p.UID, Settings: json.RawMessage(`{"addresses":"first@example.test;second@example.test"}`)}}}})
	require.NoError(t, err)
	return r, p
}
func queueEmail(t *testing.T, s *Store, r model.Receiver, at time.Time) model.ReceiverDispatchInput {
	t.Helper()
	i := r.Integrations[0]
	p, err := s.PrepareReceiverEmail(context.Background(), i.UID, i.Revision, receivers.EmailContent{Subject: "Persisted mail", TextBody: "email-private-body"}, receivers.Metadata{DeliveryID: "email-fixture", CreatedAt: at})
	require.NoError(t, err)
	return model.ReceiverDispatchInput{ReceiverUID: r.UID, ReceiverVersion: r.Version, GroupKey: "mail", Generation: 1, Status: "firing", CreatedAt: at.UnixMilli(), Deliveries: []model.ReceiverPreparedDelivery{{IntegrationUID: i.UID, Revision: i.Revision, Prepared: p, Periods: queuePeriods(p, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()})}}}
}

func TestReceiverSMTPRecipientACKRetryAndRestart(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	f := newReceiverSMTPFixture(t, "second@example.test")
	r, _ := receiverEmail(t, s, f)
	at := time.Now()
	_, err := s.EnqueueReceiverDispatch(ctx, queueEmail(t, s, r, at))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	out := queueSend(t, s, c, at)
	require.Len(t, out, 2)
	assert.Equal(t, map[string]int{"first@example.test": 1}, f.counts())
	ops, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	states := map[string]int{}
	for _, op := range ops {
		states[op.State]++
	}
	assert.Equal(t, map[string]int{"sent": 1, "pending": 1}, states)
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	c, err = s.ClaimReceiverDelivery(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.Len(t, c.Prepared().Operations(), 1)
	assert.Len(t, c.Progress(), 1)
	queueSend(t, s, c, at.Add(2*time.Second))
	assert.Equal(t, map[string]int{"first@example.test": 1, "second@example.test": 1}, f.counts())
}

func TestReceiverCheckpointFailureStopsSMTPAndPreservesKnownRemoteACK(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	f := newReceiverSMTPFixture(t, "")
	r, _ := receiverEmail(t, s, f)
	at := time.Now()
	_, err := s.EnqueueReceiverDispatch(ctx, queueEmail(t, s, r, at))
	require.NoError(t, err)
	_, err = s.DB.Exec(`CREATE TRIGGER receiver_checkpoint_abort BEFORE UPDATE OF state ON alert_receiver_operations WHEN NEW.state='sent' BEGIN SELECT RAISE(ABORT,'checkpoint unavailable'); END`)
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	out := c.Transport().SendJournaled(ctx, c.Prepared(), c.Progress(), func(ctx context.Context, out []receivers.Outcome) error {
		return s.CheckpointReceiverDelivery(ctx, c, out, at.Add(time.Millisecond))
	})
	require.Len(t, out, 2)
	assert.True(t, out[0].Sent)
	assert.Equal(t, "checkpoint_failed", out[0].Code)
	assert.Equal(t, "checkpoint_blocked", out[1].Code)
	assert.Equal(t, map[string]int{"first@example.test": 1}, f.counts())
	require.Error(t, s.FinishReceiverDelivery(ctx, c, out, at.Add(2*time.Millisecond)))
	_, err = s.DB.Exec(`DROP TRIGGER receiver_checkpoint_abort`)
	require.NoError(t, err)
	require.NoError(t, s.FinishReceiverDelivery(ctx, c, out, at.Add(3*time.Millisecond)))
	c.Close()
	ops, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	sent := 0
	for _, op := range ops {
		if op.State == "sent" {
			sent++
		}
	}
	assert.Equal(t, 1, sent, "remote success is retained after checkpoint failure, never recorded as retryable failure")
	c, err = s.ClaimReceiverDelivery(ctx, at.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	queueSend(t, s, c, at.Add(2*time.Second))
	assert.Equal(t, map[string]int{"first@example.test": 1, "second@example.test": 1}, f.counts())
}

func TestReceiverOnlyNewSecretsMissingKeyPreOpenGuard(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	_, err := s.SaveSMTPProfile(ctx, model.SMTPProfileInput{SMTPProfile: model.SMTPProfile{UID: "only-new", Name: "Only new", Settings: json.RawMessage(`{"host":"127.0.0.1:2525","from_address":"sender@example.test","auth_user":"user"}`)}, SecureSettings: map[string]string{"password": "new-schema-only-secret"}})
	require.NoError(t, err)
	for _, table := range []string{"datasources", "app_settings", "target_connections", "alert_contact_points"} {
		_, err = s.DB.Exec("DELETE FROM " + table)
		require.NoError(t, err)
	}
	path := s.Path
	require.NoError(t, s.DB.Close())
	key := filepath.Join(filepath.Dir(path), "secrets.key")
	require.NoError(t, os.Remove(key))
	opened, err := Open(path)
	if opened != nil {
		opened.DB.Close()
	}
	require.ErrorContains(t, err, "restore the original key")
	_, err = os.Stat(key)
	require.True(t, os.IsNotExist(err), "Open must not create a replacement key before rejecting ciphertext")
}

func TestReceiverExplicitDisableDeleteAndResolvePreferenceFenceHistoricalLeases(t *testing.T) {
	for _, action := range []string{"disabled", "delete", "remove", "disable_resolve"} {
		t.Run(action, func(t *testing.T) {
			s := receiverStore(t)
			ctx := context.Background()
			var oldHits, newHits atomic.Int32
			original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldHits.Add(1); w.Write([]byte("ok")) }))
			defer original.Close()
			replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newHits.Add(1); w.Write([]byte("ok")) }))
			defer replacement.Close()
			r, err := s.SaveReceiver(ctx, receiverSlackInput("fence", "Fence", original.URL))
			require.NoError(t, err)
			at := time.Now()
			period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
			_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
			require.NoError(t, err)
			c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
			require.NoError(t, err)
			queueSend(t, s, c, at)
			// Address edits keep the old channel eligible to receive its resolve, and
			// the encrypted revision never sends that resolve to the new channel.
			edit := receiverInput(r)
			edit.Integrations[0].SecureSettings = map[string]string{"url": replacement.URL}
			current, err := s.SaveReceiver(ctx, edit)
			require.NoError(t, err)
			historical := r
			historical.Version = current.Version
			resolveInput := queueSlack(t, s, historical, 2, "resolved", at.Add(time.Second), period)
			_, err = s.EnqueueReceiverDispatch(ctx, resolveInput)
			require.NoError(t, err)
			leased, err := s.ClaimReceiverDelivery(ctx, at.Add(time.Second), time.Minute)
			require.NoError(t, err)
			defer leased.Close()
			require.NoError(t, s.ValidateReceiverClaim(ctx, leased, at.Add(time.Second+time.Millisecond)))
			switch action {
			case "delete":
				require.NoError(t, s.DeleteReceiver(ctx, current.UID, current.Version))
			case "remove":
				edit = receiverInput(current)
				edit.Integrations = receiverSlackInput("extra", "Extra", replacement.URL).Integrations
				_, err = s.SaveReceiver(ctx, edit)
				require.NoError(t, err)
			default:
				edit = receiverInput(current)
				if action == "disabled" {
					edit.Integrations[0].Disabled = true
				} else {
					edit.Integrations[0].DisableResolveMessage = true
				}
				_, err = s.SaveReceiver(ctx, edit)
				require.NoError(t, err)
			}
			require.Error(t, s.ValidateReceiverClaim(ctx, leased, at.Add(time.Second+2*time.Millisecond)), "configuration changes invalidate work before network send")
			require.Error(t, s.CheckpointReceiverDelivery(ctx, leased, []receivers.Outcome{{OperationKey: leased.Prepared().Operations()[0].Key, Sent: true, Code: "sent", Receipt: receivers.Receipt{ID: "ack"}}}, at.Add(time.Second+2*time.Millisecond)))
			_, err = s.ClaimReceiverDelivery(ctx, at.Add(2*time.Minute), time.Minute)
			require.ErrorIs(t, err, sql.ErrNoRows)
			assert.Equal(t, int32(1), oldHits.Load())
			assert.Zero(t, newHits.Load())
			ops, err := s.ReceiverOperations(ctx, leased.ID)
			require.NoError(t, err)
			assert.Equal(t, "canceled", ops[0].State)
			deliveries, err := s.ReceiverDeliveries(ctx, 10)
			require.NoError(t, err)
			assert.Equal(t, "canceled", deliveries[0].State)
		})
	}
}

func TestReceiverExplicitDisableCancelsPendingFiringWithoutDeletingACKHistory(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("disable", "Disable", "https://example.test/hook"))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	edit := receiverInput(r)
	edit.Integrations[0].Disabled = true
	r, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	require.ErrorIs(t, s.ValidateReceiverClaim(ctx, c, at.Add(time.Second)), ErrReceiverLease)
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "firing", at, period))
	require.ErrorIs(t, err, model.ErrInvalidReceiver)
	edit = receiverInput(r)
	edit.Integrations[0].Disabled = false
	r, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	_, err = s.ClaimReceiverDelivery(ctx, at.Add(2*time.Minute), time.Minute)
	require.ErrorIs(t, err, sql.ErrNoRows, "enabling cannot resurrect stale canceled events")
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "firing", at.Add(2*time.Minute), period))
	require.NoError(t, err)
}

func TestReceiverSafeDispatchRealHTTPAndConfigurationFence(t *testing.T) {
	ctx := context.Background()
	s := receiverStore(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("send", "Send", server.URL))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	c.Status = "resolved"
	require.ErrorIs(t, s.ValidateReceiverClaim(ctx, c, time.Now()), ErrReceiverLease, "public metadata cannot redirect ACK receipts into a different status")
	c.Status = "firing"
	out, err := s.SendClaimedReceiverDelivery(ctx, c)
	require.NoError(t, err)
	require.True(t, out[0].Sent)
	assert.Equal(t, int32(1), hits.Load())
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 2, "firing", time.Now(), period))
	require.NoError(t, err)
	c, err = s.ClaimReceiverDelivery(ctx, time.Now(), time.Minute)
	require.NoError(t, err)
	defer c.Close()
	edit := receiverInput(r)
	edit.Integrations[0].Disabled = true
	_, err = s.SaveReceiver(ctx, edit)
	require.NoError(t, err)
	_, err = s.SendClaimedReceiverDelivery(ctx, c)
	require.ErrorIs(t, err, ErrReceiverLease)
	assert.Equal(t, int32(1), hits.Load(), "execution helper checks the current configuration before HTTP")
}

func TestReceiverIndependentStoreConnectionsCannotDoubleClaim(t *testing.T) {
	ctx := context.Background()
	s := receiverStore(t)
	r, err := s.SaveReceiver(ctx, receiverSlackInput("processes", "Processes", "https://example.test/hook"))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	stores := []*Store{s}
	for range 7 {
		other, err := Open(s.Path)
		require.NoError(t, err)
		defer other.DB.Close()
		require.NoError(t, other.InitializeReceiverSchema(ctx))
		stores = append(stores, other)
	}
	barrier := make(chan struct{})
	claims := make(chan *ClaimedReceiverDelivery, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for _, db := range stores {
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			<-barrier
			c, err := db.ClaimReceiverDelivery(ctx, at, time.Minute)
			if err == nil {
				claims <- c
			} else {
				failures <- err
			}
		}(db)
	}
	close(barrier)
	wg.Wait()
	close(claims)
	close(failures)
	require.Len(t, claims, 1)
	c := <-claims
	defer c.Close()
	for err := range failures {
		assert.True(t, errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "SQLITE_BUSY"), "SQLite transaction promotion may ask a competing worker to retry, but never returns a duplicate claim: %v", err)
	}
	for _, db := range stores {
		_, err := db.ClaimReceiverDelivery(ctx, at.Add(time.Second), time.Minute)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
}

func TestReceiverPreparedBundleFreezePreservesNumbersAndRejectsTampering(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("bundle", "Bundle", "https://example.test/hook"))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	input := queueSlack(t, s, r, 1, "firing", at, period)
	dispatch, err := s.EnqueueReceiverDispatch(ctx, input)
	require.NoError(t, err)
	var sealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT sealed FROM alert_receiver_dispatches WHERE id=?`, dispatch.ID).Scan(&sealed))
	var snapshot receiverDispatchSnapshot
	require.NoError(t, s.openReceiver("dispatch", fmt.Sprint(dispatch.ID), 1, sealed, &snapshot))
	assert.Contains(t, string(snapshot.Data), "9007199254740993")
	assert.Contains(t, string(snapshot.Templates), "template-private-marker")
	// Mutating the caller's structures after enqueue cannot alter frozen bytes.
	input.DataSnapshot[0] = '['
	ops := input.Deliveries[0].Prepared.Operations()
	ops[0].Payload[0] = '['
	for key := range input.Deliveries[0].Periods {
		input.Deliveries[0].Periods[key][0].StartedAt++
	}
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	assert.Contains(t, string(c.Prepared().Operations()[0].Payload), "body-private-marker")
	assert.Equal(t, period, c.periods[c.Prepared().Operations()[0].Key][0])
	var deliverySealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT sealed FROM alert_receiver_deliveries WHERE id=?`, c.ID).Scan(&deliverySealed))
	deliverySealed[len(deliverySealed)-1] ^= 1
	_, err = s.DB.Exec(`UPDATE alert_receiver_deliveries SET sealed=? WHERE id=?`, deliverySealed, c.ID)
	require.NoError(t, err)
	require.ErrorIs(t, s.InitializeReceiverSchema(ctx), ErrReceiverSnapshot)
}

func TestReceiverExpiredMixedAttemptCapsDoNotStrandOtherOperations(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	f := newReceiverSMTPFixture(t, "")
	r, _ := receiverEmail(t, s, f)
	at := time.Now()
	_, err := s.EnqueueReceiverDispatch(ctx, queueEmail(t, s, r, at))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	key := c.Prepared().Operations()[0].Key
	_, err = s.DB.Exec(`UPDATE alert_receiver_operations SET attempts=max_attempts WHERE delivery_id=? AND op_key=?`, c.ID, key)
	require.NoError(t, err)
	c, err = s.ClaimReceiverDelivery(ctx, at.Add(time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	require.Len(t, c.Prepared().Operations(), 1)
	queueSend(t, s, c, at.Add(time.Minute+time.Second))
	operations, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	states := map[string]int{}
	for _, op := range operations {
		states[op.State]++
	}
	assert.Equal(t, map[string]int{"sent": 1, "failed": 1}, states)
}

func TestReceiverSafeDispatchCancellationPersistsRetryWithBoundedFinishContext(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("cancel", "Cancel", server.URL))
	require.NoError(t, err)
	at := time.Now()
	period := model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, period))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	networkCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := s.SendClaimedReceiverDelivery(networkCtx, c)
		if err == nil && (len(out) != 1 || out[0].Code != "cancelled" || !out[0].Retryable) {
			err = errors.New("unexpected canceled transport outcome")
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("local HTTP transport did not enter")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled dispatch did not finish")
	}
	operations, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", operations[0].State)
	assert.Equal(t, "cancelled", operations[0].Code)
	deliveries, err := s.ReceiverDeliveries(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, "pending", deliveries[0].State)
	assert.Zero(t, deliveries[0].LeaseUntil)
}

func TestReceiverSafeSendSameLeaseReloadsDurableACKAfterFinishFailure(t *testing.T) {
	for _, protocol := range []string{"slack", "smtp"} {
		t.Run(protocol, func(t *testing.T) {
			s := receiverStore(t)
			ctx := context.Background()
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
			defer server.Close()
			var fixture *receiverSMTPFixture
			var r model.Receiver
			var input model.ReceiverDispatchInput
			var err error
			at := time.Now()
			if protocol == "slack" {
				r, err = s.SaveReceiver(ctx, receiverSlackInput("durable-retry", "Durable retry", server.URL))
				require.NoError(t, err)
				input = queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()})
			} else {
				fixture = newReceiverSMTPFixture(t, "")
				r, _ = receiverEmail(t, s, fixture)
				input = queueEmail(t, s, r, at)
			}
			_, err = s.EnqueueReceiverDispatch(ctx, input)
			require.NoError(t, err)
			c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
			require.NoError(t, err)
			defer c.Close()
			_, err = s.DB.Exec(`CREATE TRIGGER receiver_finish_abort BEFORE UPDATE ON alert_receiver_deliveries WHEN NEW.state='sent' BEGIN SELECT RAISE(ABORT,'aggregate unavailable'); END`)
			require.NoError(t, err)
			out, err := s.SendClaimedReceiverDelivery(ctx, c)
			require.ErrorContains(t, err, "aggregate unavailable")
			for _, op := range out {
				require.True(t, op.Sent)
				assert.False(t, op.Skipped)
			}
			operations, err := s.ReceiverOperations(ctx, c.ID)
			require.NoError(t, err)
			for _, op := range operations {
				require.Equal(t, "sent", op.State)
			}
			assert.Empty(t, c.Progress(), "the original handle intentionally remains an immutable claim-time snapshot")
			_, err = s.DB.Exec(`DROP TRIGGER receiver_finish_abort`)
			require.NoError(t, err)
			// A copied stale handle and an independent Store cannot bypass durable ACKs.
			copied := *c
			other, err := Open(s.Path)
			require.NoError(t, err)
			defer other.DB.Close()
			require.NoError(t, other.InitializeReceiverSchema(ctx))
			out, err = other.SendClaimedReceiverDelivery(ctx, &copied)
			require.NoError(t, err)
			for _, op := range out {
				assert.True(t, op.Sent)
				assert.True(t, op.Skipped)
			}
			if protocol == "slack" {
				assert.Equal(t, int32(1), hits.Load())
			} else {
				assert.Equal(t, map[string]int{"first@example.test": 1, "second@example.test": 1}, fixture.counts())
			}
			_, err = s.SendClaimedReceiverDelivery(ctx, c)
			require.ErrorIs(t, err, ErrReceiverLease, "completed lease cannot be executed again")
		})
	}
}

func TestReceiverSafeSendSerializesCopiedHandlesAcrossStoreConnections(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRemote := func() { releaseOnce.Do(func() { close(release) }) }
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if hits.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			w.Write([]byte("ok"))
		case <-r.Context().Done():
		}
	}))
	defer func() { releaseRemote(); server.Close() }()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("serial-send", "Serial send", server.URL))
	require.NoError(t, err)
	at := time.Now()
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	other, err := Open(s.Path)
	require.NoError(t, err)
	defer other.DB.Close()
	require.NoError(t, other.InitializeReceiverSchema(ctx))
	done := make(chan error, 1)
	go func() { _, err := s.SendClaimedReceiverDelivery(ctx, c); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("local HTTP execution did not start")
	}
	outcomes := make(chan error, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 != 0 {
				db = other
			}
			copy := *c
			_, err := db.SendClaimedReceiverDelivery(ctx, &copy)
			outcomes <- err
		}(i)
	}
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		require.ErrorIs(t, err, ErrReceiverSending)
	}
	assert.Equal(t, int32(1), hits.Load())
	releaseRemote()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("serialized HTTP execution did not complete")
	}
	var locks int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_send_leases WHERE send_token<>''`).Scan(&locks))
	assert.Zero(t, locks)
}

func TestReceiverSafeSendReplacesOrphanLatchAfterLeaseReclaimAndRestart(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	fixture := newReceiverSMTPFixture(t, "second@example.test")
	r, _ := receiverEmail(t, s, fixture)
	at := time.Now()
	_, err := s.EnqueueReceiverDispatch(ctx, queueEmail(t, s, r, at))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	work, tag, err := s.beginReceiverSend(ctx, c, at)
	require.NoError(t, err)
	assert.NotEmpty(t, tag)
	out := work.Transport().SendJournaled(ctx, work.Prepared(), work.Progress(), func(ctx context.Context, out []receivers.Outcome) error {
		return s.CheckpointReceiverDelivery(ctx, work, out, at.Add(time.Millisecond))
	})
	require.Len(t, out, 2)
	assert.True(t, out[0].Sent)
	assert.False(t, out[1].Sent)
	// Simulate process death before Finish and latch release. The new lease has a
	// new token; it may override the stale latch while retaining the first ACK.
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	require.NoError(t, s.InitializeReceiverSchema(ctx))
	reclaimed, err := s.ClaimReceiverDelivery(ctx, at.Add(time.Minute+time.Second), time.Minute)
	require.NoError(t, err)
	defer reclaimed.Close()
	assert.NotEqual(t, c.token, reclaimed.token)
	out, err = s.SendClaimedReceiverDelivery(ctx, reclaimed)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.True(t, out[0].Sent)
	assert.Equal(t, map[string]int{"first@example.test": 1, "second@example.test": 1}, fixture.counts())
}

func TestReceiverSafeSendBindsDeadlineToDurableLease(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("deadline", "Deadline", server.URL))
	require.NoError(t, err)
	at := time.Now()
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, 35*time.Second)
	require.NoError(t, err)
	defer c.Close()
	copied := *c
	copied.LeaseUntil += time.Minute.Milliseconds()
	require.ErrorIs(t, s.ValidateReceiverClaim(ctx, &copied, time.Now()), ErrReceiverLease, "public metadata cannot extend the durable lease")
	_, err = s.SendClaimedReceiverDelivery(ctx, &copied)
	require.ErrorIs(t, err, ErrReceiverLease)
	assert.Zero(t, hits.Load())
	require.NoError(t, s.ValidateReceiverClaim(ctx, c, time.Now()))
	out, err := s.SendClaimedReceiverDelivery(ctx, c)
	require.NoError(t, err)
	require.True(t, out[0].Sent)
	assert.Equal(t, int32(1), hits.Load())
}

func TestReceiverSafeSendSameLeaseRetriesCannotBypassOperationAttemptCap(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("retry-cap", "Retry cap", server.URL))
	require.NoError(t, err)
	at := time.Now()
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	_, err = s.DB.Exec(`CREATE TRIGGER receiver_partial_finish_abort BEFORE UPDATE ON alert_receiver_deliveries WHEN NEW.state IN ('pending','failed') BEGIN SELECT RAISE(ABORT,'aggregate unavailable'); END`)
	require.NoError(t, err)
	for range 8 {
		out, err := s.SendClaimedReceiverDelivery(ctx, c)
		require.ErrorContains(t, err, "aggregate unavailable")
		require.Len(t, out, 1)
		assert.False(t, out[0].Sent)
		assert.True(t, out[0].Retryable)
	}
	_, err = s.DB.Exec(`DROP TRIGGER receiver_partial_finish_abort`)
	require.NoError(t, err)
	out, err := s.SendClaimedReceiverDelivery(ctx, c)
	require.NoError(t, err)
	assert.Empty(t, out, "attempts exhausted recovers aggregate without another HTTP request")
	assert.Equal(t, int32(8), hits.Load())
	operations, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", operations[0].State)
	assert.Equal(t, int64(8), operations[0].Attempts)
	assert.Equal(t, "attempts_exhausted", operations[0].Code)
}

func TestReceiverSafeSendRetriesSQLiteContentionWithoutConsumingAttempts(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("sqlite-retry", "SQLite retry", server.URL))
	require.NoError(t, err)
	at := time.Now()
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	other, err := Open(s.Path)
	require.NoError(t, err)
	defer other.DB.Close()
	writer, err := other.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer writer.Rollback()
	_, err = writer.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET attempts=attempts WHERE id=?`, c.ID)
	require.NoError(t, err)
	// A zero driver busy timeout exposes actual typed SQLITE_BUSY to the retry
	// loop instead of waiting inside SQLite. Every failed transaction rolls back.
	_, err = s.DB.Exec(`PRAGMA busy_timeout=0`)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := s.SendClaimedReceiverDelivery(ctx, c); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("contention returned before the writer released: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	assert.Zero(t, hits.Load())
	require.NoError(t, writer.Commit())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("SQLite retry did not progress after commit")
	}
	assert.Equal(t, int32(1), hits.Load())
	operations, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), operations[0].Attempts)
	var executions int
	require.NoError(t, s.DB.QueryRow(`SELECT execution_count FROM alert_receiver_send_leases WHERE delivery_id=?`, c.ID).Scan(&executions))
	assert.Equal(t, 1, executions)
}

func TestReceiverSafeSendSQLiteContentionHonorsCallerCancellation(t *testing.T) {
	s := receiverStore(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("ok")) }))
	defer server.Close()
	r, err := s.SaveReceiver(ctx, receiverSlackInput("sqlite-cancel", "SQLite cancel", server.URL))
	require.NoError(t, err)
	at := time.Now()
	_, err = s.EnqueueReceiverDispatch(ctx, queueSlack(t, s, r, 1, "firing", at, model.ReceiverPeriod{RuleUID: "rule", InstanceKey: "instance", StartedAt: at.UnixMilli()}))
	require.NoError(t, err)
	c, err := s.ClaimReceiverDelivery(ctx, at, time.Minute)
	require.NoError(t, err)
	defer c.Close()
	other, err := Open(s.Path)
	require.NoError(t, err)
	defer other.DB.Close()
	writer, err := other.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer writer.Rollback()
	_, err = writer.ExecContext(ctx, `UPDATE alert_receiver_deliveries SET attempts=attempts WHERE id=?`, c.ID)
	require.NoError(t, err)
	sendCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	done := make(chan error, 1)
	go func() { _, err := s.SendClaimedReceiverDelivery(sendCtx, c); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not bound SQLite retry")
	}
	assert.Less(t, time.Since(started), time.Second)
	assert.Zero(t, hits.Load())
	require.NoError(t, writer.Rollback())
	var locks int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_receiver_send_leases WHERE delivery_id=?`, c.ID).Scan(&locks))
	assert.Zero(t, locks)
	operations, err := s.ReceiverOperations(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), operations[0].Attempts)
	// Cancellation leaves the original claim usable without an orphan execution.
	_, err = s.SendClaimedReceiverDelivery(ctx, c)
	require.NoError(t, err)
	assert.Equal(t, int32(1), hits.Load())
}
