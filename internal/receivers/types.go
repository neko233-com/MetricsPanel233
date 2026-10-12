package receivers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

const (
	MaxOperations   = 32
	MaxPayloadBytes = 1 << 20
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 30 * time.Second
)

var (
	ErrInvalidConfiguration = errors.New("invalid receiver configuration")
	ErrInvalidPrepared      = errors.New("invalid prepared receiver payload")
	operationKey            = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
)

type Metadata struct {
	DeliveryID string
	CreatedAt  time.Time
}

// Operation payloads may contain personal data or protocol credentials. A
// future persistent queue must encrypt this snapshot, rather than publish it.
type Operation struct {
	Key     string `json:"key"`
	Target  string `json:"target"`
	Payload []byte `json:"payload"`
}

// Prepared owns its byte slices; callers cannot change a prepared message by
// editing the input or a returned operation. JSON restores the same bytes.
type Prepared struct {
	protocol   string
	operations []Operation
}

func NewPrepared(protocol string, operations []Operation) (Prepared, error) {
	if (protocol != "smtp" && protocol != "slack") || len(operations) == 0 || len(operations) > MaxOperations {
		return Prepared{}, ErrInvalidPrepared
	}
	seen := map[string]bool{}
	result := Prepared{protocol: protocol, operations: make([]Operation, len(operations))}
	total := 0
	for i, op := range operations {
		total += len(op.Payload)
		if !operationKey.MatchString(op.Key) || seen[op.Key] || len(op.Target) > 512 || len(op.Payload) == 0 || total > MaxPayloadBytes {
			return Prepared{}, ErrInvalidPrepared
		}
		seen[op.Key] = true
		op.Payload = bytes.Clone(op.Payload)
		result.operations[i] = op
	}
	return result, nil
}

func (p Prepared) Protocol() string { return p.protocol }
func (p Prepared) Operations() []Operation {
	result := make([]Operation, len(p.operations))
	for i, op := range p.operations {
		op.Payload = bytes.Clone(op.Payload)
		result[i] = op
	}
	return result
}

type preparedJSON struct {
	Version    int         `json:"version"`
	Protocol   string      `json:"protocol"`
	Operations []Operation `json:"operations"`
}

func (p Prepared) MarshalJSON() ([]byte, error) {
	if _, err := NewPrepared(p.protocol, p.operations); err != nil {
		return nil, err
	}
	return json.Marshal(preparedJSON{1, p.protocol, p.operations})
}
func (p *Prepared) UnmarshalJSON(data []byte) error {
	// Base64 payload overhead is bounded before allocation by the decoder.
	if len(data) > 2*MaxPayloadBytes+MaxOperations*2048 {
		return ErrInvalidPrepared
	}
	var input preparedJSON
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || input.Version != 1 {
		return ErrInvalidPrepared
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrInvalidPrepared
	}
	value, err := NewPrepared(input.Protocol, input.Operations)
	if err != nil {
		return err
	}
	*p = value
	return nil
}

// Receipt is a non-secret provider acknowledgement. It proves remote acceptance,
// not end-user delivery. SMTP uses the stable Message-ID; Slack uses channel:ts.
type Receipt struct {
	ID string `json:"id"`
}
type Progress map[string]Receipt

// Outcome never includes raw provider errors, response bodies, URLs or secrets.
// RetryAfter is a lower bound; scheduling, attempt caps and CAS belong to callers.
type Outcome struct {
	OperationKey string        `json:"operation_key"`
	Sent         bool          `json:"sent"`
	Skipped      bool          `json:"skipped"`
	Retryable    bool          `json:"retryable"`
	Code         string        `json:"code"`
	StatusCode   int           `json:"status_code,omitempty"`
	RetryAfter   time.Duration `json:"retry_after,omitempty"`
	Receipt      Receipt       `json:"receipt,omitempty"`
}
type Transport interface {
	Send(context.Context, Prepared, Progress) []Outcome
}

// Checkpoint synchronously persists remote acceptance before another operation
// starts. It must respect ctx. A failed checkpoint stops later network work;
// Sent still describes remote acceptance, not local persistence. Remote ACK and
// a database commit cannot be atomic, so a process crash can still duplicate.
type Checkpoint func(context.Context, []Outcome) error
type JournaledTransport interface {
	Transport
	SendJournaled(context.Context, Prepared, Progress, Checkpoint) []Outcome
}

func checkpointSuccessful(ctx context.Context, checkpoint Checkpoint, outcomes []Outcome) bool {
	return checkpoint == nil || checkpoint(ctx, append([]Outcome{}, outcomes...)) == nil
}

func outcomeFailure(key, code string, retryable bool) Outcome {
	return Outcome{OperationKey: key, Code: code, Retryable: retryable}
}
func outcomeSent(key string, receipt Receipt, skipped bool) Outcome {
	return Outcome{OperationKey: key, Sent: true, Skipped: skipped, Code: "sent", Receipt: receipt}
}
func validateTimeout(timeout time.Duration) (time.Duration, error) {
	if timeout == 0 {
		return DefaultTimeout, nil
	}
	if timeout < 0 || timeout > MaxTimeout {
		return 0, ErrInvalidConfiguration
	}
	return timeout, nil
}
func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// PEM/key values are write-only material; profile persistence must seal them.
type TLSSettings struct {
	ServerName string `json:"server_name,omitempty"`
	CAPEM      string `json:"-"`
	CertPEM    string `json:"-"`
	KeyPEM     string `json:"-"`
	SkipVerify bool   `json:"skip_verify,omitempty"`
}

func (TLSSettings) String() string { return "receiver TLS settings" }
func tlsConfiguration(settings TLSSettings) (*tls.Config, error) {
	if len(settings.CAPEM)+len(settings.CertPEM)+len(settings.KeyPEM) > MaxPayloadBytes {
		return nil, ErrInvalidConfiguration
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: settings.ServerName, InsecureSkipVerify: settings.SkipVerify}
	if settings.CAPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(settings.CAPEM)) {
			return nil, ErrInvalidConfiguration
		}
		config.RootCAs = pool
	}
	if (settings.CertPEM == "") != (settings.KeyPEM == "") {
		return nil, ErrInvalidConfiguration
	}
	if settings.CertPEM != "" {
		cert, err := tls.X509KeyPair([]byte(settings.CertPEM), []byte(settings.KeyPEM))
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		config.Certificates = []tls.Certificate{cert}
	}
	return config, nil
}
