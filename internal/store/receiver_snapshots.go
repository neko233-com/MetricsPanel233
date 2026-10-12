package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/receivers"
)

var (
	ErrReceiverConflict       = errors.New("receiver version changed; reload before saving")
	ErrReceiverReferenced     = errors.New("receiver resource is referenced")
	ErrReceiverSnapshot       = errors.New("receiver snapshot could not be authenticated")
	ErrReceiverLease          = errors.New("receiver delivery lease is no longer valid")
	ErrReceiverGeneration     = errors.New("receiver dispatch generation conflicts")
	ErrReceiverUnacknowledged = errors.New("resolved delivery contains a destination without firing acknowledgement")
	ErrReceiverSuppressed     = errors.New("receiver delivery was explicitly disabled or deleted")
	ErrReceiverSending        = errors.New("receiver delivery is already executing under this lease")
)

var receiverEnvelope = []byte("MetricsPanel233\x00ReceiverGCM\x01\x00")

func (s *Store) receiverAAD(kind, uid string, revision int64) []byte {
	value, _ := json.Marshal([]any{"receiver-v1", s.OrganizationID(), kind, uid, revision})
	return value
}
func (s *Store) sealReceiver(kind, uid string, revision int64, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, model.ErrInvalidReceiver
	}
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, ErrReceiverSnapshot
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrReceiverSnapshot
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	result := append(bytes.Clone(receiverEnvelope), nonce...)
	return gcm.Seal(result, nonce, raw, s.receiverAAD(kind, uid, revision)), nil
}
func (s *Store) openReceiver(kind, uid string, revision int64, sealed []byte, value any) error {
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return ErrReceiverSnapshot
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ErrReceiverSnapshot
	}
	n := len(receiverEnvelope)
	if !bytes.HasPrefix(sealed, receiverEnvelope) || len(sealed) < n+gcm.NonceSize()+gcm.Overhead() {
		return ErrReceiverSnapshot
	}
	raw, err := gcm.Open(nil, sealed[n:n+gcm.NonceSize()], sealed[n+gcm.NonceSize():], s.receiverAAD(kind, uid, revision))
	if err != nil {
		return ErrReceiverSnapshot
	}
	if json.Unmarshal(raw, value) != nil {
		return ErrReceiverSnapshot
	}
	return nil
}
func (s *Store) receiverHash(kind string, value any) string {
	raw, _ := json.Marshal(value)
	h := hmac.New(sha256.New, s.secretKey)
	h.Write(s.receiverAAD(kind, "", 0))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func strictReceiverJSON(raw json.RawMessage, value any) error {
	if len(raw) == 0 || len(raw) > 128*1024 {
		return model.ErrInvalidReceiver
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return model.ErrInvalidReceiver
	}
	if d.Decode(new(any)) != io.EOF {
		return model.ErrInvalidReceiver
	}
	return nil
}

// These settings use the standalone transport contract. Credentials and PEM
// material are supplied separately; neither is ever copied into a public DTO.
type smtpProfileSettings struct {
	receivers.SMTPSettings
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}
type slackIntegrationSettings struct {
	receivers.SlackSettings
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}
type receiverConfigSnapshot struct {
	Integration model.ReceiverIntegration `json:"integration"`
	Secrets     map[string]string         `json:"secrets"`
	LegacyPoint *model.ContactPoint       `json:"legacy_point,omitempty"`
}
type smtpProfileSnapshot struct {
	Settings json.RawMessage   `json:"settings"`
	Secrets  map[string]string `json:"secrets"`
}

func receiverTLS(settings *receivers.TLSSettings, secrets map[string]string) {
	settings.CAPEM = secrets["tls.caCertificate"]
	settings.CertPEM = secrets["tls.clientCertificate"]
	settings.KeyPEM = secrets["tls.clientKey"]
}
func smtpTransport(snapshot smtpProfileSnapshot) (*receivers.SMTP, error) {
	var settings smtpProfileSettings
	if err := strictReceiverJSON(snapshot.Settings, &settings); err != nil {
		return nil, err
	}
	if settings.TimeoutSeconds < 0 || settings.TimeoutSeconds > 30 {
		return nil, model.ErrInvalidReceiver
	}
	settings.Timeout = time.Duration(settings.TimeoutSeconds) * time.Second
	receiverTLS(&settings.TLS, snapshot.Secrets)
	transport, err := receivers.NewSMTP(settings.SMTPSettings, receivers.SMTPSecrets{Password: snapshot.Secrets["password"]})
	if err != nil {
		return nil, model.ErrInvalidReceiver
	}
	return transport, nil
}
func slackTransport(snapshot receiverConfigSnapshot) (*receivers.Slack, error) {
	var settings slackIntegrationSettings
	if err := strictReceiverJSON(snapshot.Integration.Settings, &settings); err != nil {
		return nil, err
	}
	if settings.TimeoutSeconds < 0 || settings.TimeoutSeconds > 30 {
		return nil, model.ErrInvalidReceiver
	}
	settings.Timeout = time.Duration(settings.TimeoutSeconds) * time.Second
	receiverTLS(&settings.TLS, snapshot.Secrets)
	transport, err := receivers.NewSlack(settings.SlackSettings, receivers.SlackSecrets{Token: snapshot.Secrets["token"], WebhookURL: snapshot.Secrets["url"]})
	if err != nil {
		return nil, model.ErrInvalidReceiver
	}
	return transport, nil
}
func receiverSecretAllowed(kind, key string) bool {
	if key == "tls.caCertificate" || key == "tls.clientCertificate" || key == "tls.clientKey" {
		return true
	}
	return kind == "smtp" && key == "password" || kind == "slack" && (key == "url" || key == "token")
}
func receiverApplySecrets(kind string, old map[string]string, set map[string]string, clear []string) (map[string]string, error) {
	result := map[string]string{}
	for k, v := range old {
		result[k] = v
	}
	if len(set) > 64 || len(clear) > 64 {
		return nil, model.ErrInvalidReceiver
	}
	for _, k := range clear {
		if !receiverSecretAllowed(kind, k) {
			return nil, model.ErrInvalidReceiver
		}
		delete(result, k)
	}
	for k, v := range set {
		if !receiverSecretAllowed(kind, k) || len(v) > receivers.MaxPayloadBytes {
			return nil, model.ErrInvalidReceiver
		}
		if v == "" {
			delete(result, k)
		} else {
			result[k] = v
		}
	}
	size := 0
	for _, v := range result {
		size += len(v)
	}
	if size > receivers.MaxPayloadBytes {
		return nil, model.ErrInvalidReceiver
	}
	return result, nil
}
func receiverSecureFields(secrets map[string]string) map[string]bool {
	fields := map[string]bool{}
	for k, v := range secrets {
		if v != "" {
			fields[k] = true
		}
	}
	return fields
}
func marshalReceiver(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
