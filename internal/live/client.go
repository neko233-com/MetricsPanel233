package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/centrifugal/protocol"
	"github.com/coder/websocket"
)

// WatchOptions is a single-channel Centrifuge JSON subscription. Credentials
// travel in the upgrade header, never in a URL or published event.
type WatchOptions struct {
	Endpoint string
	Token    string
	Channel  string
	Metadata json.RawMessage
	Header   http.Header
	Limit    int
}
type Event struct {
	Type      string          `json:"type"`
	Channel   string          `json:"channel"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Watch delivers the initial data frame and publications, and always releases
// the connection on a limit, cancellation, callback failure or server error.
// It intentionally surfaces disconnects to agents instead of silently dropping
// data while retrying. Callers can decide whether to start a fresh subscription.
func Watch(ctx context.Context, options WatchOptions, receive func(Event) error) error {
	if _, _, _, err := ParseChannel(options.Channel); err != nil {
		return err
	}
	metadata, err := canonicalMetadata(options.Metadata)
	if err != nil {
		return err
	}
	if options.Limit < 0 {
		return errors.New("stream limit must be nonnegative")
	}
	address, err := url.Parse(options.Endpoint)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return errors.New("server must be an HTTP(S) origin without credentials or a query")
	}
	switch address.Scheme {
	case "http":
		address.Scheme = "ws"
	case "https":
		address.Scheme = "wss"
	default:
		return errors.New("server must use HTTP or HTTPS")
	}
	address.Path = strings.TrimRight(address.Path, "/") + "/api/live/ws"
	header := options.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	if options.Token != "" {
		header.Set("Authorization", "Bearer "+options.Token)
	}
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, response, err := websocket.Dial(connectCtx, address.String(), &websocket.DialOptions{HTTPHeader: header, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	cancel()
	if err != nil {
		if response != nil {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			return fmt.Errorf("Live upgrade failed (HTTP %d)", response.StatusCode)
		}
		return fmt.Errorf("Live connection failed: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	encoder := protocol.NewJSONCommandEncoder()
	write := func(command *protocol.Command) error {
		raw, err := encoder.Encode(command)
		if err != nil {
			return err
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(writeCtx, websocket.MessageText, raw)
	}
	if err = write(&protocol.Command{Id: 1, Connect: &protocol.ConnectRequest{Name: "metricspanel-cli"}}); err != nil {
		return err
	}
	// Only handshakes have a first-response timeout. Quiet streams can remain
	// subscribed indefinitely and are cancelled by the caller's context.
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 20*time.Second)
	defer handshakeCancel()
	readCtx := handshakeCtx
	received := 0
	for {
		kind, raw, err := conn.Read(readCtx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Live stream disconnected: %w", err)
		}
		if kind != websocket.MessageText {
			return errors.New("Live client requires JSON text frames")
		}
		decoder := protocol.NewJSONReplyDecoder(raw)
		for {
			reply, err := decoder.Decode()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("invalid Live protocol packet: %w", err)
			}
			if reply.Error != nil {
				return fmt.Errorf("Live protocol error %d: %s", reply.Error.Code, reply.Error.Message)
			}
			if reply.Connect != nil {
				if err = write(&protocol.Command{Id: 2, Subscribe: &protocol.SubscribeRequest{Channel: options.Channel, Data: metadata}}); err != nil {
					return err
				}
			}
			var value []byte
			eventType := "publication"
			if reply.Subscribe != nil {
				readCtx = ctx
				handshakeCancel()
				value = reply.Subscribe.Data
				eventType = "initial"
			}
			if reply.Push != nil {
				if reply.Push.Unsubscribe != nil {
					return fmt.Errorf("Live subscription ended (%d): %s", reply.Push.Unsubscribe.Code, reply.Push.Unsubscribe.Reason)
				}
				if reply.Push.Disconnect != nil {
					return fmt.Errorf("Live connection ended (%d): %s", reply.Push.Disconnect.Code, reply.Push.Disconnect.Reason)
				}
				if reply.Push.Pub != nil && reply.Push.Channel == options.Channel {
					value = reply.Push.Pub.Data
				}
			}
			if reply.Id == 0 && reply.Push == nil {
				if err = write(&protocol.Command{}); err != nil {
					return err
				}
			}
			if len(value) > 0 {
				if !json.Valid(value) {
					return errors.New("Live publication is not JSON")
				}
				if err = receive(Event{Type: eventType, Channel: options.Channel, Timestamp: time.Now().UnixMilli(), Data: value}); err != nil {
					return err
				}
				received++
				if options.Limit > 0 && received >= options.Limit {
					return nil
				}
			}
		}
	}
}
