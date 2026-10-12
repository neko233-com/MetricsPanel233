package model

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type ContactPoint struct {
	UID                   string          `json:"uid"`
	Name                  string          `json:"name"`
	Type                  string          `json:"type"`
	URL                   string          `json:"url"`
	Disabled              bool            `json:"disabled"`
	DisableResolveMessage bool            `json:"disable_resolve_message"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	Version               int             `json:"version"`
	UpdatedAt             int64           `json:"updated_at"`
	SecureFields          map[string]bool `json:"secure_fields"`
	TitleTemplate         string          `json:"title_template,omitempty"`
	MessageTemplate       string          `json:"message_template,omitempty"`
}

// Secrets are write-only. Omitted values are retained on an update; an explicit
// empty value or clear_secrets removes the named credential.
type ContactPointInput struct {
	ContactPoint
	BearerToken  *string           `json:"bearer_token,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	ClearSecrets []string          `json:"clear_secrets,omitempty"`
}

var notificationHeaderName = regexp.MustCompile(`^[!#$%&'*+.^_\x60|~0-9A-Za-z-]+$`)

func ValidNotificationHeader(name, value string) bool {
	if !notificationHeaderName.MatchString(name) || len(name) > 128 || len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Content-Type", "X-Metricspanel-Notification-Id":
		return false
	}
	return true
}

func (c *ContactPoint) Defaults() {
	if c.Type == "" {
		c.Type = "webhook"
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 10
	}
}

func (c ContactPoint) Validate() error {
	if !alertUID.MatchString(c.UID) || strings.TrimSpace(c.Name) == "" || len(c.Name) > 256 {
		return errors.New("contact point requires a valid UID and a name of 1–256 bytes")
	}
	if c.Type != "webhook" {
		return errors.New("only webhook contact points are supported")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(c.URL) > 4096 {
		return errors.New("webhook URL must be an absolute HTTP(S) URL without user credentials or a fragment")
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 30 {
		return errors.New("webhook timeout must be 1–30 seconds")
	}
	if len(c.TitleTemplate) > 65536 || len(c.MessageTemplate) > 65536 {
		return errors.New("contact point template expressions must be at most 65536 bytes")
	}
	return nil
}

type NotificationMatcher struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

func (m NotificationMatcher) Validate() error {
	if len(m.Name) == 0 || len(m.Name) > 128 || len(m.Value) > 4096 {
		return errors.New("notification matcher name/value is too long or empty")
	}
	switch m.Operator {
	case "=", "!=":
	case "=~", "!~":
		if _, err := regexp.Compile("^(?:" + m.Value + ")$"); err != nil {
			return errors.New("invalid notification matcher regular expression")
		}
	default:
		return errors.New("notification matcher operator must be =, !=, =~ or !~")
	}
	return nil
}

func (m NotificationMatcher) Matches(labels map[string]string) bool {
	value := labels[m.Name]
	switch m.Operator {
	case "=":
		return value == m.Value
	case "!=":
		return value != m.Value
	case "=~", "!~":
		r, err := regexp.Compile("^(?:" + m.Value + ")$")
		matched := err == nil && r.MatchString(value)
		return matched != (m.Operator == "!~")
	}
	return false
}

type NotificationRoute struct {
	NotificationTiming
	Receiver            string                `json:"receiver"`
	Matchers            []NotificationMatcher `json:"matchers"`
	Continue            bool                  `json:"continue"`
	Routes              []NotificationRoute   `json:"routes,omitempty"`
	MuteTimeIntervals   []string              `json:"mute_time_intervals,omitempty"`
	ActiveTimeIntervals []string              `json:"active_time_intervals,omitempty"`
}

type NotificationPolicy struct {
	NotificationTiming
	Version   int                 `json:"version"`
	Receiver  string              `json:"receiver"`
	Routes    []NotificationRoute `json:"routes"`
	UpdatedAt int64               `json:"updated_at"`
}

func (p NotificationPolicy) Validate() error {
	if p.Receiver != "" && !alertUID.MatchString(p.Receiver) {
		return errors.New("invalid default receiver UID")
	}
	if err := p.NotificationTiming.Validate(); err != nil {
		return err
	}
	return validateNotificationRoutes(p.Routes, p.Receiver, 0, new(int))
}

func (p NotificationPolicy) Receivers(labels map[string]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, route := range p.MatchingRoutes(labels) {
		if route.Receiver != "" && !seen[route.Receiver] {
			out = append(out, route.Receiver)
			seen[route.Receiver] = true
		}
	}
	return out
}

type NotificationDelivery struct {
	ID              int64  `json:"id"`
	EventID         int64  `json:"event_id"`
	ContactPointUID string `json:"contact_point_uid"`
	RuleUID         string `json:"rule_uid"`
	InstanceKey     string `json:"instance_key"`
	Status          string `json:"status"`
	State           string `json:"state"`
	Attempts        int    `json:"attempts"`
	MaxAttempts     int    `json:"max_attempts"`
	NextAttemptAt   int64  `json:"next_attempt_at"`
	LastAttemptAt   int64  `json:"last_attempt_at"`
	CreatedAt       int64  `json:"created_at"`
	DeliveredAt     int64  `json:"delivered_at"`
	LastError       string `json:"last_error"`
	HTTPStatus      int    `json:"http_status"`
}
