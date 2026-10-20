package receivers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const SlackAPIURL = "https://slack.com/api/chat.postMessage"
const maxSlackResponse = 64 << 10

// Template expressions belong to the upstream renderer. Prepare accepts only
// rendered SlackContent and never starts a second, unbounded template execution.
type SlackSettings struct {
	EndpointURL    string        `json:"endpointUrl,omitempty"`
	Recipient      string        `json:"recipient,omitempty"`
	Text           string        `json:"text,omitempty"`
	Title          string        `json:"title,omitempty"`
	Username       string        `json:"username,omitempty"`
	IconEmoji      string        `json:"icon_emoji,omitempty"`
	IconURL        string        `json:"icon_url,omitempty"`
	MentionChannel string        `json:"mentionChannel,omitempty"`
	MentionUsers   StringList    `json:"mentionUsers,omitempty"`
	MentionGroups  StringList    `json:"mentionGroups,omitempty"`
	Color          string        `json:"color,omitempty"`
	Footer         string        `json:"footer,omitempty"`
	Timeout        time.Duration `json:"-"`
	TLS            TLSSettings   `json:"tls,omitempty"`
}
type SlackSecrets struct {
	WebhookURL string `json:"-"`
	Token      string `json:"-"`
}

func (SlackSecrets) String() string { return "Slack secrets" }

// Grafana accepts both comma-separated strings and string arrays for mentions.
type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	if len(data) > 16384 {
		return ErrInvalidConfiguration
	}
	var text string
	if json.Unmarshal(data, &text) == nil {
		list := StringList{}
		for _, item := range strings.Split(text, ",") {
			if item = strings.TrimSpace(item); item != "" {
				list = append(list, item)
			}
		}
		if len(list) > 32 {
			return ErrInvalidConfiguration
		}
		*s = list
		return nil
	}
	var list []string
	if json.Unmarshal(data, &list) != nil || len(list) > 32 {
		return ErrInvalidConfiguration
	}
	*s = append(StringList{}, list...)
	return nil
}

type SlackContent struct {
	Recipient      string
	Text           string
	Title          string
	Username       string
	IconEmoji      string
	IconURL        string
	MentionChannel string
	MentionUsers   []string
	MentionGroups  []string
	Color          string
	Footer         string
	TitleLink      string
	ImageURL       string // URL only; bot file-upload workflows are a separate operation
}
type Slack struct {
	settings SlackSettings
	secrets  SlackSecrets
	endpoint string
	webhook  bool
	client   *http.Client
}

var _ Transport = (*Slack)(nil)
var _ JournaledTransport = (*Slack)(nil)

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" && !strings.ContainsAny(value, "\r\n\x00")
}
func NewSlack(settings SlackSettings, secrets SlackSecrets) (*Slack, error) {
	var err error
	settings.Timeout, err = validateTimeout(settings.Timeout)
	if err != nil {
		return nil, err
	}
	if settings.EndpointURL == "" {
		settings.EndpointURL = SlackAPIURL
	}
	endpoint := settings.EndpointURL
	if secrets.WebhookURL != "" {
		endpoint = secrets.WebhookURL
	}
	if !validHTTPURL(endpoint) || len(endpoint) > 8192 || !safeLine(secrets.Token, 16384) {
		return nil, ErrInvalidConfiguration
	}
	webhook := secrets.Token == ""
	if webhook && secrets.WebhookURL == "" {
		return nil, ErrInvalidConfiguration
	}
	if settings.MentionChannel != "" && settings.MentionChannel != "here" && settings.MentionChannel != "channel" {
		return nil, ErrInvalidConfiguration
	}
	if len(settings.MentionUsers) > 32 || len(settings.MentionGroups) > 32 {
		return nil, ErrInvalidConfiguration
	}
	settings.MentionUsers = append(StringList{}, settings.MentionUsers...)
	settings.MentionGroups = append(StringList{}, settings.MentionGroups...)
	config, err := tlsConfiguration(settings.TLS)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: settings.Timeout}).DialContext, TLSClientConfig: config, TLSHandshakeTimeout: settings.Timeout, ResponseHeaderTimeout: settings.Timeout, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: settings.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Slack{settings: settings, secrets: secrets, endpoint: endpoint, webhook: webhook, client: client}, nil
}

// Close releases idle connections; it does not cancel a concurrent Send.
func (s *Slack) Close() { s.client.CloseIdleConnections() }

type slackAttachment struct {
	Title      string   `json:"title,omitempty"`
	TitleLink  string   `json:"title_link,omitempty"`
	Text       string   `json:"text"`
	ImageURL   string   `json:"image_url,omitempty"`
	Fallback   string   `json:"fallback"`
	Footer     string   `json:"footer"`
	FooterIcon string   `json:"footer_icon"`
	Color      string   `json:"color,omitempty"`
	Timestamp  int64    `json:"ts,omitempty"`
	Pretext    string   `json:"pretext,omitempty"`
	Markdown   []string `json:"mrkdwn_in,omitempty"`
}
type slackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	IconEmoji   string            `json:"icon_emoji,omitempty"`
	IconURL     string            `json:"icon_url,omitempty"`
	Attachments []slackAttachment `json:"attachments"`
}

var slackMentionID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

func (s *Slack) Prepare(content SlackContent, meta Metadata) (Prepared, error) {
	if len(content.Title) > 4096 || len(content.Footer) > 1200 || len(content.Text) > 160000 {
		return Prepared{}, ErrInvalidPrepared
	}
	if !validateMetadata(meta) || utf8.RuneCountInString(content.Title) > 1024 || utf8.RuneCountInString(content.Footer) > 300 || utf8.RuneCountInString(content.Text) > 40000 || (content.Title == "" && content.Text == "") || len(content.MentionUsers) > 32 || len(content.MentionGroups) > 32 {
		return Prepared{}, ErrInvalidPrepared
	}
	if !safeLine(content.Recipient, 512) || (!s.webhook && content.Recipient == "") {
		return Prepared{}, ErrInvalidPrepared
	}
	for _, value := range []string{content.IconURL, content.TitleLink, content.ImageURL} {
		if value != "" && !validHTTPURL(value) {
			return Prepared{}, ErrInvalidPrepared
		}
	}
	if content.MentionChannel != "" && content.MentionChannel != "here" && content.MentionChannel != "channel" {
		return Prepared{}, ErrInvalidPrepared
	}
	for _, list := range [][]string{content.MentionUsers, content.MentionGroups} {
		for _, id := range list {
			if !slackMentionID.MatchString(id) {
				return Prepared{}, ErrInvalidPrepared
			}
		}
	}
	mentions := []string{}
	if content.MentionChannel != "" {
		mentions = append(mentions, "<!"+content.MentionChannel+"|"+content.MentionChannel+">")
	}
	if len(content.MentionGroups) > 0 {
		var group strings.Builder
		for _, id := range content.MentionGroups {
			group.WriteString("<!subteam^" + id + ">")
		}
		mentions = append(mentions, group.String())
	}
	if len(content.MentionUsers) > 0 {
		var users strings.Builder
		for _, id := range content.MentionUsers {
			users.WriteString("<@" + id + ">")
		}
		mentions = append(mentions, users.String())
	}
	attachment := slackAttachment{Title: content.Title, TitleLink: content.TitleLink, Text: content.Text, ImageURL: content.ImageURL, Fallback: content.Title, Footer: content.Footer, FooterIcon: "https://grafana.com/static/assets/img/fav32.png", Color: content.Color, Timestamp: meta.CreatedAt.Unix(), Pretext: strings.Join(mentions, " ")}
	if len(mentions) > 0 {
		attachment.Markdown = []string{"pretext"}
	}
	message := slackMessage{Channel: content.Recipient, Username: content.Username, IconEmoji: content.IconEmoji, IconURL: content.IconURL, Attachments: []slackAttachment{attachment}}
	data, err := json.Marshal(message)
	if err != nil {
		return Prepared{}, ErrInvalidPrepared
	}
	sum := sha256.Sum256([]byte(meta.DeliveryID))
	key := "message:" + hex.EncodeToString(sum[:])
	return NewPrepared("slack", []Operation{{Key: key, Target: content.Recipient, Payload: data}})
}
func (s *Slack) Send(parent context.Context, prepared Prepared, progress Progress) []Outcome {
	return s.SendJournaled(parent, prepared, progress, nil)
}
func (s *Slack) SendJournaled(parent context.Context, prepared Prepared, progress Progress, checkpoint Checkpoint) []Outcome {
	ops := prepared.Operations()
	if prepared.Protocol() != "slack" || len(ops) != 1 {
		return []Outcome{outcomeFailure("", "invalid_prepared", false)}
	}
	op := ops[0]
	var message slackMessage
	d := json.NewDecoder(bytes.NewReader(op.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&message) != nil || d.Decode(new(any)) != io.EOF || len(message.Attachments) != 1 || message.Channel != op.Target || (!s.webhook && message.Channel == "") {
		return []Outcome{outcomeFailure(op.Key, "invalid_prepared", false)}
	}
	if receipt, ok := progress[op.Key]; ok {
		return []Outcome{outcomeSent(op.Key, receipt, true)}
	}
	ctx, cancel := boundedContext(parent, s.settings.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(op.Payload))
	if err != nil {
		return []Outcome{outcomeFailure(op.Key, "invalid_configuration", false)}
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "MetricsPanel233")
	request.Header.Set("X-MetricsPanel-Operation-ID", op.Key)
	if !s.webhook {
		request.Header.Set("Authorization", "Bearer "+s.secrets.Token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return []Outcome{slackHTTPFailure(ctx, op.Key, err)}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "slack_http_rejected"
		retry := response.StatusCode >= 500 || response.StatusCode == 408 || response.StatusCode == 425 || response.StatusCode == 429
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			code = "redirect_rejected"
		}
		if response.StatusCode == 429 {
			code = "slack_rate_limited"
		}
		out := outcomeFailure(op.Key, code, retry)
		out.StatusCode = response.StatusCode
		if retry {
			out.RetryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		}
		return []Outcome{out}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSlackResponse+1))
	if err != nil {
		return []Outcome{slackHTTPFailure(ctx, op.Key, err)}
	}
	if len(data) > maxSlackResponse {
		return []Outcome{outcomeFailure(op.Key, "slack_response_limit", true)}
	}
	if s.webhook && strings.TrimSpace(string(data)) == "ok" {
		return slackCheckpoint(ctx, checkpoint, outcomeSent(op.Key, Receipt{ID: op.Key}, false))
	}
	var reply struct {
		OK      *bool  `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.OK == nil {
		return []Outcome{outcomeFailure(op.Key, "slack_invalid_response", true)}
	}
	if !*reply.OK {
		out := slackApplicationFailure(op.Key, reply.Error)
		out.StatusCode = response.StatusCode
		if out.Retryable {
			out.RetryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		}
		return []Outcome{out}
	}
	receipt := Receipt{ID: op.Key}
	// Only non-secret, narrowly shaped provider identifiers reach public status.
	if slackChannelID.MatchString(reply.Channel) && slackTimestamp.MatchString(reply.TS) {
		receipt.ID = reply.Channel + ":" + reply.TS
	}
	return slackCheckpoint(ctx, checkpoint, outcomeSent(op.Key, receipt, false))
}
func slackCheckpoint(ctx context.Context, checkpoint Checkpoint, outcome Outcome) []Outcome {
	out := []Outcome{outcome}
	if !checkpointSuccessful(ctx, checkpoint, out) {
		out[0].Code = "checkpoint_failed"
	}
	return out
}

var slackTimestamp = regexp.MustCompile(`^[0-9]{1,20}(?:\.[0-9]{1,20})?$`)
var slackChannelID = regexp.MustCompile(`^[CGD][A-Z0-9]{1,63}$`)

func slackApplicationFailure(key, value string) Outcome {
	switch value {
	case "ratelimited", "rate_limited":
		return outcomeFailure(key, "slack_rate_limited", true)
	case "internal_error", "fatal_error", "service_unavailable", "request_timeout", "timeout", "team_added_to_org", "org_login_required":
		return outcomeFailure(key, "slack_unavailable", true)
	case "invalid_auth", "not_authed", "token_expired", "token_revoked", "account_inactive", "missing_scope", "no_permission":
		return outcomeFailure(key, "slack_auth_rejected", false)
	default:
		return outcomeFailure(key, "slack_api_rejected", false)
	}
}
func slackHTTPFailure(ctx context.Context, key string, err error) Outcome {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return outcomeFailure(key, "timeout", true)
		}
		return outcomeFailure(key, "cancelled", true)
	}
	// http.Client.Timeout cancels the transport without cancelling the caller
	// context. Classify that case as a timeout, not a connection failure.
	var timeoutError interface{ Timeout() bool }
	if errors.As(err, &timeoutError) && timeoutError.Timeout() {
		return outcomeFailure(key, "timeout", true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return outcomeFailure(key, "timeout", true)
	}
	// Reuse certificate classification without exposing URL-bearing url.Error.
	out := smtpFailure(ctx, key, "slack_connection", err)
	return out
}
func parseRetryAfter(value string, at time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(at) {
		return deadline.Sub(at)
	}
	return 0
}
