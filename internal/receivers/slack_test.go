package receivers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func slackTransport(t *testing.T, endpoint string, webhook bool, edit func(*SlackSettings)) *Slack {
	t.Helper()
	settings := SlackSettings{EndpointURL: endpoint, Recipient: "C233", Timeout: time.Second}
	if edit != nil {
		edit(&settings)
	}
	secrets := SlackSecrets{Token: "xoxb-private-secret"}
	if webhook {
		secrets = SlackSecrets{WebhookURL: endpoint}
	}
	sender, err := NewSlack(settings, secrets)
	require.NoError(t, err)
	t.Cleanup(sender.Close)
	return sender
}
func slackPrepared(t *testing.T, sender *Slack) Prepared {
	t.Helper()
	prepared, err := sender.Prepare(SlackContent{Recipient: "C233", Title: "Firing", Text: "frozen alert", Footer: "MetricsPanel233", Color: "danger"}, Metadata{DeliveryID: "slack-delivery-233", CreatedAt: time.Unix(233, 0)})
	require.NoError(t, err)
	return prepared
}
func TestSlackWebhookImmutableWireAndResume(t *testing.T) {
	var mu sync.Mutex
	var requests [][]byte
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, body)
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	sender := slackTransport(t, server.URL+"/hooks/private-key", true, nil)
	content := SlackContent{Recipient: "#alerts", Title: "服务异常", Text: "Original text", Username: "Monitor", IconEmoji: ":warning:", Color: "danger", Footer: "Footer", TitleLink: "https://example.test/alerting", ImageURL: "https://example.test/image.png", MentionChannel: "here", MentionUsers: []string{"U1", "U2"}, MentionGroups: []string{"S1"}}
	prepared, err := sender.Prepare(content, Metadata{DeliveryID: "frozen-slack", CreatedAt: time.Unix(233, 0)})
	require.NoError(t, err)
	content.MentionUsers[0] = "MUTATED"
	content.Title = "MUTATED"
	operations := prepared.Operations()
	operations[0].Payload[0] = 'X'
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	var restored Prepared
	require.NoError(t, json.Unmarshal(raw, &restored))
	first := sender.Send(context.Background(), restored, nil)
	require.Len(t, first, 1)
	require.True(t, first[0].Sent)
	second := sender.Send(context.Background(), restored, Progress{first[0].OperationKey: first[0].Receipt})
	require.True(t, second[0].Skipped)
	mu.Lock()
	captured := requests
	headers := auth
	mu.Unlock()
	require.Len(t, captured, 1)
	require.Empty(t, headers[0])
	require.Equal(t, prepared.Operations()[0].Payload, captured[0])
	var body map[string]any
	require.NoError(t, json.Unmarshal(captured[0], &body))
	require.Equal(t, "#alerts", body["channel"])
	require.Equal(t, "Monitor", body["username"])
	require.Equal(t, ":warning:", body["icon_emoji"])
	attachment := body["attachments"].([]any)[0].(map[string]any)
	require.Equal(t, "服务异常", attachment["title"])
	require.Equal(t, "Original text", attachment["text"])
	require.Equal(t, "服务异常", attachment["fallback"])
	require.Equal(t, float64(233), attachment["ts"])
	require.Equal(t, "<!here|here> <!subteam^S1> <@U1><@U2>", attachment["pretext"])
	require.Equal(t, []any{"pretext"}, attachment["mrkdwn_in"])
	require.Equal(t, "https://example.test/image.png", attachment["image_url"])
	out, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(out), server.URL)
	require.NotContains(t, string(out), "private-key")
}
func TestSlackBotAuthAndApplicationAcknowledgement(t *testing.T) {
	var mu sync.Mutex
	var authorization, operationID, contentType, path string
	var data []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization = r.Header.Get("Authorization")
		operationID = r.Header.Get("X-MetricsPanel-Operation-ID")
		contentType = r.Header.Get("Content-Type")
		path = r.URL.Path
		data, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"channel":"C233","ts":"1234567890.233"}`))
	}))
	defer server.Close()
	sender := slackTransport(t, server.URL+"/api/chat.postMessage", false, nil)
	prepared := slackPrepared(t, sender)
	out := sender.Send(context.Background(), prepared, nil)
	require.True(t, out[0].Sent)
	require.Equal(t, "C233:1234567890.233", out[0].Receipt.ID)
	mu.Lock()
	require.Equal(t, "Bearer xoxb-private-secret", authorization)
	require.Equal(t, "/api/chat.postMessage", path)
	require.Equal(t, "application/json; charset=utf-8", contentType)
	require.Equal(t, prepared.Operations()[0].Key, operationID)
	require.Equal(t, prepared.Operations()[0].Payload, data)
	mu.Unlock()
}
func TestSlackHTTPAndApplicationFailuresRetryAfterAndNoLeaks(t *testing.T) {
	cases := []struct {
		name              string
		status            int
		body, after, code string
		retry             bool
		duration          time.Duration
	}{
		{"http429", 429, "provider-private xoxb-private-secret", "7", "slack_rate_limited", true, 7 * time.Second},
		{"http503", 503, "provider-private", "2", "slack_http_rejected", true, 2 * time.Second},
		{"http408", 408, "provider-private", "", "slack_http_rejected", true, 0},
		{"http425", 425, "provider-private", "", "slack_http_rejected", true, 0},
		{"http400", 400, "xoxb-private-secret", "", "slack_http_rejected", false, 0},
		{"http401", 401, "xoxb-private-secret", "", "slack_http_rejected", false, 0},
		{"http403", 403, "xoxb-private-secret", "", "slack_http_rejected", false, 0},
		{"http404", 404, "xoxb-private-secret", "", "slack_http_rejected", false, 0},
		{"app-auth", 200, `{"ok":false,"error":"invalid_auth"}`, "", "slack_auth_rejected", false, 0},
		{"app-channel", 200, `{"ok":false,"error":"channel_not_found"}`, "", "slack_api_rejected", false, 0},
		{"app-rate", 200, `{"ok":false,"error":"ratelimited"}`, "11", "slack_rate_limited", true, 11 * time.Second},
		{"app-unavailable", 200, `{"ok":false,"error":"service_unavailable"}`, "", "slack_unavailable", true, 0},
		{"app-unknown", 200, `{"ok":false,"error":"xoxb-private-secret https://secret.example"}`, "", "slack_api_rejected", false, 0},
		{"malformed", 200, "not JSON xoxb-private-secret", "", "slack_invalid_response", true, 0},
		{"missing-ok", 200, `{"channel":"C233"}`, "", "slack_invalid_response", true, 0},
		{"response-bound", 200, strings.Repeat("x", maxSlackResponse+1), "", "slack_response_limit", true, 0},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", item.after)
				w.WriteHeader(item.status)
				w.Write([]byte(item.body))
			}))
			defer server.Close()
			sender := slackTransport(t, server.URL, false, nil)
			out := sender.Send(context.Background(), slackPrepared(t, sender), nil)
			require.False(t, out[0].Sent)
			require.Equal(t, item.code, out[0].Code)
			require.Equal(t, item.retry, out[0].Retryable)
			require.Equal(t, item.duration, out[0].RetryAfter)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			for _, secret := range []string{"xoxb-private-secret", "secret.example", server.URL, "provider-private"} {
				require.NotContains(t, string(raw), secret)
			}
		})
	}
	at := time.Unix(233, 0)
	require.Equal(t, 10*time.Second, parseRetryAfter(at.Add(10*time.Second).UTC().Format(http.TimeFormat), at))
	for _, bad := range []string{"-1", "9223372036854775807", "1.5", "secret-url"} {
		require.Zero(t, parseRetryAfter(bad, at))
	}
}
func TestSlackRedirectsNeverForwardCredentialsOrPayload(t *testing.T) {
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.Write([]byte(`{"ok":true}`)) }))
	defer target.Close()
	for _, status := range []int{301, 302, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/private", status) }))
			defer server.Close()
			sender := slackTransport(t, server.URL, false, nil)
			out := sender.Send(context.Background(), slackPrepared(t, sender), nil)
			require.Equal(t, "redirect_rejected", out[0].Code)
			require.False(t, out[0].Retryable)
			require.Zero(t, leaked.Load())
		})
	}
}
func TestSlackTLSMutualTLSAndCancel(t *testing.T) {
	cert, ca, key := fixtureCertificate(t)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(ca)))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"channel":"C233","ts":"233.001"}`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	sender := slackTransport(t, server.URL, false, func(s *SlackSettings) { s.TLS = TLSSettings{CAPEM: ca, CertPEM: ca, KeyPEM: key} })
	require.True(t, sender.Send(context.Background(), slackPrepared(t, sender), nil)[0].Sent)
	bad := slackTransport(t, server.URL, false, nil)
	out := bad.Send(context.Background(), slackPrepared(t, bad), nil)
	require.Equal(t, "tls_verification", out[0].Code)
	require.False(t, out[0].Retryable)
	for _, explicitCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[explicitCancel], func(t *testing.T) {
			blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
			defer blocking.Close()
			sender := slackTransport(t, blocking.URL, false, func(s *SlackSettings) { s.Timeout = 60 * time.Millisecond })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if explicitCancel {
				timer := time.AfterFunc(20*time.Millisecond, cancel)
				defer timer.Stop()
			}
			start := time.Now()
			out := sender.Send(ctx, slackPrepared(t, sender), nil)
			require.Less(t, time.Since(start), 500*time.Millisecond)
			require.True(t, out[0].Retryable)
			if explicitCancel {
				require.Equal(t, "cancelled", out[0].Code)
			} else {
				require.Equal(t, "timeout", out[0].Code)
			}
		})
	}
	t.Run("cancel-streaming-body", func(t *testing.T) {
		stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Write([]byte(`{"ok":`))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer stream.Close()
		sender := slackTransport(t, stream.URL, false, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(30*time.Millisecond, cancel)
		defer timer.Stop()
		start := time.Now()
		out := sender.Send(ctx, slackPrepared(t, sender), nil)
		require.Less(t, time.Since(start), 500*time.Millisecond)
		require.Equal(t, "cancelled", out[0].Code)
		require.True(t, out[0].Retryable)
	})
}
func TestSlackAcceptedMissingReceiptNeverResendsAndInvalidPrepare(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"ok":true,"channel":"xoxb-private-secret","ts":"secret"}`))
	}))
	defer server.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	sender := slackTransport(t, server.URL, false, func(s *SlackSettings) { s.TLS.CAPEM = ca })
	prepared := slackPrepared(t, sender)
	out := sender.Send(context.Background(), prepared, nil)
	require.True(t, out[0].Sent)
	require.Equal(t, prepared.Operations()[0].Key, out[0].Receipt.ID)
	require.True(t, sender.Send(context.Background(), prepared, Progress{out[0].OperationKey: out[0].Receipt})[0].Skipped)
	require.EqualValues(t, 1, calls.Load())
	for _, configuration := range []struct {
		settings SlackSettings
		secrets  SlackSecrets
	}{{SlackSettings{}, SlackSecrets{}}, {SlackSettings{EndpointURL: "file:///private"}, SlackSecrets{Token: "private"}}, {SlackSettings{EndpointURL: "https://user:private@example.test"}, SlackSecrets{Token: "private"}}, {SlackSettings{EndpointURL: server.URL}, SlackSecrets{Token: "private\r\nheader"}}} {
		_, err := NewSlack(configuration.settings, configuration.secrets)
		require.ErrorIs(t, err, ErrInvalidConfiguration)
	}
	for _, content := range []SlackContent{{}, {Recipient: "C233", Text: "valid", MentionChannel: "everyone"}, {Recipient: "C233", Text: "valid", MentionUsers: []string{"injected>"}}, {Recipient: "C233", Text: "valid", Footer: strings.Repeat("x", 301)}, {Recipient: "C233", Text: "valid", Title: strings.Repeat("界", 1025)}} {
		_, err := sender.Prepare(content, Metadata{DeliveryID: "id", CreatedAt: time.Now()})
		require.ErrorIs(t, err, ErrInvalidPrepared)
	}
	var mentions StringList
	require.NoError(t, json.Unmarshal([]byte(`"U1, U2"`), &mentions))
	require.Equal(t, StringList{"U1", "U2"}, mentions)
	require.NoError(t, json.Unmarshal([]byte(`["U3"]`), &mentions))
	require.Equal(t, StringList{"U3"}, mentions)
}

func TestSlackCheckpointSucceedsOnceOrSafelyReportsPersistenceFailure(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte(`{"ok":true,"channel":"C233","ts":"233.001"}`))
	}))
	defer server.Close()
	sender := slackTransport(t, server.URL, false, nil)
	prepared := slackPrepared(t, sender)
	progress := Progress{}
	checkpoints := 0
	out := sender.SendJournaled(context.Background(), prepared, nil, func(_ context.Context, receipts []Outcome) error {
		checkpoints++
		require.Len(t, receipts, 1)
		require.True(t, receipts[0].Sent)
		progress[receipts[0].OperationKey] = receipts[0].Receipt
		receipts[0].Sent = false
		return nil
	})
	require.True(t, out[0].Sent)
	serialized, err := json.Marshal(progress)
	require.NoError(t, err)
	var reloaded Progress
	require.NoError(t, json.Unmarshal(serialized, &reloaded))
	restarted := slackTransport(t, server.URL, false, nil)
	out = restarted.SendJournaled(context.Background(), prepared, reloaded, func(context.Context, []Outcome) error { checkpoints++; return nil })
	require.True(t, out[0].Skipped)
	require.Equal(t, 1, checkpoints)
	require.EqualValues(t, 1, requests.Load())
	out = sender.SendJournaled(context.Background(), prepared, nil, func(context.Context, []Outcome) error { return errors.New("xoxb-private-secret " + server.URL) })
	require.True(t, out[0].Sent)
	require.Equal(t, "checkpoint_failed", out[0].Code)
	require.False(t, out[0].Retryable)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "xoxb-private-secret")
	require.NotContains(t, string(raw), server.URL)
}
