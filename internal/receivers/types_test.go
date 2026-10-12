package receivers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreparedImmutableVersionedRoundTrip(t *testing.T) {
	input := []Operation{{Key: "recipient:one", Target: "one@example.test", Payload: []byte("frozen")}}
	prepared, err := NewPrepared("smtp", input)
	require.NoError(t, err)
	input[0].Payload[0] = 'X'
	input[0].Target = "changed"
	copy := prepared.Operations()
	copy[0].Payload[0] = 'Y'
	copy[0].Key = "changed"
	require.Equal(t, "frozen", string(prepared.Operations()[0].Payload))
	raw, err := json.Marshal(prepared)
	require.NoError(t, err)
	var restored Prepared
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, prepared.Operations(), restored.Operations())
	for _, bad := range []string{`{"version":2,"protocol":"smtp","operations":[]}`, `{"version":1,"protocol":"smtp","operations":[],"unknown":true}`, string(raw) + ` {}`, `null`} {
		require.Error(t, json.Unmarshal([]byte(bad), &restored))
		require.Equal(t, prepared.Operations(), restored.Operations(), "a failed restore leaves existing bytes intact")
	}
	_, err = NewPrepared("smtp", []Operation{{Key: "same", Payload: []byte("a")}, {Key: "same", Payload: []byte("b")}})
	require.ErrorIs(t, err, ErrInvalidPrepared)
	_, err = NewPrepared("smtp", []Operation{{Key: "one", Payload: []byte(strings.Repeat("x", MaxPayloadBytes+1))}})
	require.ErrorIs(t, err, ErrInvalidPrepared)
	_, err = NewPrepared("unsupported", input)
	require.ErrorIs(t, err, ErrInvalidPrepared)
}
func TestReceiverSecretJSONAndBounds(t *testing.T) {
	for _, value := range []any{SMTPSecrets{Password: "private-password"}, SlackSecrets{Token: "private-token", WebhookURL: "https://private-url"}, TLSSettings{CAPEM: "private-ca", CertPEM: "private-cert", KeyPEM: "private-key"}} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private")
	}
	for _, bad := range []time.Duration{-time.Second, MaxTimeout + time.Nanosecond} {
		_, err := validateTimeout(bad)
		require.ErrorIs(t, err, ErrInvalidConfiguration)
	}
	got, err := validateTimeout(0)
	require.NoError(t, err)
	require.Equal(t, DefaultTimeout, got)
	_, err = tlsConfiguration(TLSSettings{KeyPEM: "private"})
	require.ErrorIs(t, err, ErrInvalidConfiguration)
}
