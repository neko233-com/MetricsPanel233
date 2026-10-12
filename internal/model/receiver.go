package model

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/receivers"
)

var ErrInvalidReceiver = errors.New("invalid receiver configuration")

type Receiver struct {
	UID          string                `json:"uid"`
	Name         string                `json:"name"`
	Version      int64                 `json:"version"`
	UpdatedAt    int64                 `json:"updated_at"`
	Integrations []ReceiverIntegration `json:"integrations"`
}
type ReceiverIntegration struct {
	UID                   string          `json:"uid"`
	Type                  string          `json:"type"`
	ConfigVersion         string          `json:"config_version"`
	Revision              int64           `json:"revision"`
	ChannelEpoch          int64           `json:"channel_epoch"`
	Disabled              bool            `json:"disabled"`
	DisableResolveMessage bool            `json:"disable_resolve_message"`
	SMTPProfileUID        string          `json:"smtp_profile_uid,omitempty"`
	SMTPProfileRevision   int64           `json:"smtp_profile_revision,omitempty"`
	Settings              json.RawMessage `json:"settings"`
	SecureFields          map[string]bool `json:"secure_fields"`
}
type ReceiverIntegrationInput struct {
	ReceiverIntegration
	SecureSettings map[string]string `json:"secure_settings,omitempty"`
	ClearSecrets   []string          `json:"clear_secrets,omitempty"`
}
type ReceiverInput struct {
	UID          string                     `json:"uid"`
	Name         string                     `json:"name"`
	Version      int64                      `json:"version"`
	Integrations []ReceiverIntegrationInput `json:"integrations"`
}
type SMTPProfile struct {
	UID          string          `json:"uid"`
	Name         string          `json:"name"`
	Version      int64           `json:"version"`
	Revision     int64           `json:"revision"`
	UpdatedAt    int64           `json:"updated_at"`
	Settings     json.RawMessage `json:"settings"`
	SecureFields map[string]bool `json:"secure_fields"`
}
type SMTPProfileInput struct {
	SMTPProfile
	SecureSettings map[string]string `json:"secure_settings,omitempty"`
	ClearSecrets   []string          `json:"clear_secrets,omitempty"`
}

func ValidReceiverIdentity(uid, name string) bool {
	return alertUID.MatchString(uid) && strings.TrimSpace(name) != "" && len(name) <= 256 && !strings.ContainsRune(name, 0)
}
func (r ReceiverInput) Validate() error {
	if !ValidReceiverIdentity(r.UID, r.Name) || r.Version < 0 || len(r.Integrations) < 1 || len(r.Integrations) > receivers.MaxOperations {
		return ErrInvalidReceiver
	}
	seen := map[string]bool{}
	for _, i := range r.Integrations {
		if !alertUID.MatchString(i.UID) || seen[i.UID] || i.Revision < 0 || i.SMTPProfileRevision < 0 || len(i.Settings) > 128*1024 || !json.Valid(i.Settings) || len(i.SecureSettings) > 64 || len(i.ClearSecrets) > 64 {
			return ErrInvalidReceiver
		}
		if i.Type != "email" && i.Type != "slack" && i.Type != "webhook" {
			return ErrInvalidReceiver
		}
		seen[i.UID] = true
	}
	return nil
}

// These DTOs expose delivery metadata, never configuration, template or payload
// snapshots. Revisions and prepared operations are obtained only by a lease.
type ReceiverDispatch struct {
	ID          int64  `json:"id"`
	ReceiverUID string `json:"receiver_uid"`
	GroupKey    string `json:"group_key"`
	Generation  int64  `json:"generation"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"created_at"`
}
type ReceiverDelivery struct {
	ID             int64  `json:"id"`
	DispatchID     int64  `json:"dispatch_id"`
	ReceiverUID    string `json:"receiver_uid"`
	IntegrationUID string `json:"integration_uid"`
	Revision       int64  `json:"revision"`
	ChannelEpoch   int64  `json:"channel_epoch"`
	GroupKey       string `json:"group_key"`
	Generation     int64  `json:"generation"`
	Fingerprint    string `json:"fingerprint"`
	Status         string `json:"status"`
	State          string `json:"state"`
	Attempts       int64  `json:"attempts"`
	NextAttemptAt  int64  `json:"next_attempt_at"`
	LeaseUntil     int64  `json:"lease_until"`
	CreatedAt      int64  `json:"created_at"`
	CompletedAt    int64  `json:"completed_at"`
}
type ReceiverOperation struct {
	DeliveryID    int64             `json:"delivery_id"`
	Key           string            `json:"key"`
	State         string            `json:"state"`
	Attempts      int64             `json:"attempts"`
	MaxAttempts   int64             `json:"max_attempts"`
	NextAttemptAt int64             `json:"next_attempt_at"`
	Code          string            `json:"code"`
	StatusCode    int               `json:"status_code,omitempty"`
	Receipt       receivers.Receipt `json:"receipt"`
}
type ReceiverPeriod struct {
	RuleUID     string `json:"rule_uid"`
	InstanceKey string `json:"instance_key"`
	StartedAt   int64  `json:"started_at"`
}

func (p ReceiverPeriod) Valid() bool {
	return alertUID.MatchString(p.RuleUID) && p.InstanceKey != "" && len(p.InstanceKey) <= 1024 && p.StartedAt > 0
}

// Internal enqueue input: callers have already rendered bounded templates and
// filtered group membership. Each operation explicitly names its alert periods.
// Resolved operations require ACKs for every supplied period and destination.
type ReceiverPreparedDelivery struct {
	IntegrationUID string
	Revision       int64
	Prepared       receivers.Prepared
	Periods        map[string][]ReceiverPeriod
}
type ReceiverDispatchInput struct {
	ReceiverUID      string
	ReceiverVersion  int64
	GroupKey         string
	Generation       int64
	Status           string // firing, resolved, test
	DataSnapshot     json.RawMessage
	TemplateSnapshot json.RawMessage
	Deliveries       []ReceiverPreparedDelivery
	CreatedAt        int64
}
