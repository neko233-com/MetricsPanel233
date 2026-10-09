// Package live hosts Grafana's Centrifuge transport and multiplexes SDK streams.
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/prometheus/client_golang/prometheus"
)

const MaxChannels = 256

type Channel struct {
	Channel     string `json:"channel"`
	Subscribers int    `json:"subscribers"`
	StartedAt   int64  `json:"started_at"`
	Packets     uint64 `json:"packets"`
	Bytes       uint64 `json:"bytes"`
	Running     bool   `json:"running"`
}
type Snapshot struct {
	Connections int       `json:"connections"`
	Channels    []Channel `json:"channels"`
}
type subscription struct {
	Channel
	pc       backend.PluginContext
	path     string
	metadata []byte
	clients  map[string]bool
	ctx      context.Context
	cancel   context.CancelFunc
}
type Hub struct {
	Manager func() *plugins.Manager
	mu      sync.Mutex
	node    *centrifuge.Node
	handler http.Handler
	streams map[string]*subscription
	closed  bool
	epoch   uint64
	wg      sync.WaitGroup
}

func New(manager func() *plugins.Manager) *Hub {
	return &Hub{Manager: manager, streams: map[string]*subscription{}}
}
func ParseChannel(channel string) (scope, id, path string, err error) {
	parts := strings.SplitN(channel, "/", 3)
	if len(parts) != 3 || len(channel) > 255 || (parts[0] != "ds" && parts[0] != "plugin") || parts[1] == "" || parts[2] == "" || strings.ContainsAny(channel, "\x00\r\n\\") || strings.Contains(parts[2], "..") {
		return "", "", "", errors.New("channel must be ds/UID/path or plugin/ID/path")
	}
	return parts[0], parts[1], parts[2], nil
}
func canonicalMetadata(raw []byte) ([]byte, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("stream metadata exceeds 1 MiB")
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid stream metadata")
	}
	return json.Marshal(value)
}
func (h *Hub) start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("Live service is shut down")
	}
	if h.node != nil {
		return nil
	}
	node, err := centrifuge.New(centrifuge.Config{Version: "MetricsPanel233", Name: "metricspanel-live", ClientChannelLimit: 128, UserConnectionLimit: 128, ClientQueueMaxSize: 4 << 20, ChannelMaxLength: 255, Metrics: centrifuge.MetricsConfig{RegistererGatherer: prometheus.NewRegistry()}})
	if err != nil {
		return err
	}
	node.OnConnect(h.onConnect)
	if err = node.Run(); err != nil {
		return err
	}
	h.node = node
	h.handler = centrifuge.NewWebsocketHandler(node, centrifuge.WebsocketConfig{MessageSizeLimit: 1 << 20, WriteTimeout: 5 * time.Second, UseWriteBufferPool: true})
	return nil
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.start(); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	h.mu.Lock()
	handler := h.handler
	h.mu.Unlock()
	handler.ServeHTTP(w, r.WithContext(centrifuge.SetCredentials(r.Context(), &centrifuge.Credentials{UserID: "1"})))
}
func (h *Hub) onConnect(client *centrifuge.Client) {
	client.OnSubscribe(func(event centrifuge.SubscribeEvent, callback centrifuge.SubscribeCallback) {
		ctx, cancel := context.WithTimeout(client.Context(), 10*time.Second)
		defer cancel()
		scope, id, path, err := ParseChannel(event.Channel)
		if err != nil {
			callback(centrifuge.SubscribeReply{}, centrifuge.ErrorBadRequest)
			return
		}
		metadata, err := canonicalMetadata(event.Data)
		if err != nil {
			callback(centrifuge.SubscribeReply{}, centrifuge.ErrorBadRequest)
			return
		}
		h.mu.Lock()
		epoch := h.epoch
		h.mu.Unlock()
		pc, err := h.Manager().StreamContext(ctx, scope, id)
		if err != nil {
			callback(centrifuge.SubscribeReply{}, centrifuge.ErrorPermissionDenied)
			return
		}
		response, err := h.Manager().SubscribeStream(ctx, pc, path, metadata)
		if err != nil {
			callback(centrifuge.SubscribeReply{}, &centrifuge.Error{Code: 1100, Message: err.Error()})
			return
		}
		if response.Status != 0 {
			status := centrifuge.ErrorPermissionDenied
			if response.Status == 1 {
				status = centrifuge.ErrorUnknownChannel
			}
			callback(centrifuge.SubscribeReply{}, status)
			return
		}
		if len(response.Data) > 1<<20 || len(response.Data) > 0 && !json.Valid(response.Data) {
			callback(centrifuge.SubscribeReply{}, centrifuge.ErrorInternal)
			return
		}
		entry, err := h.acquire(client.ID(), event.Channel, pc, path, metadata, epoch)
		if err != nil {
			callback(centrifuge.SubscribeReply{}, &centrifuge.Error{Code: 1101, Message: err.Error(), Temporary: errors.Is(err, errContextChanged)})
			return
		}
		ready := make(chan struct{})
		callback(centrifuge.SubscribeReply{Options: centrifuge.SubscribeOptions{Data: response.Data, EmitPresence: true, EmitJoinLeave: event.JoinLeave, PushJoinLeave: event.JoinLeave}, SubscriptionReady: ready}, nil)
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return
		}
		h.wg.Add(1)
		h.mu.Unlock()
		go func() {
			defer h.wg.Done()
			select {
			case <-ready:
				h.run(entry)
			case <-client.Context().Done():
				h.release(client.ID(), event.Channel)
			case <-entry.ctx.Done():
			}
		}()
	})
	client.OnUnsubscribe(func(event centrifuge.UnsubscribeEvent) { h.release(client.ID(), event.Channel) })
	client.OnDisconnect(func(centrifuge.DisconnectEvent) { h.releaseClient(client.ID()) })
	client.OnPublish(func(event centrifuge.PublishEvent, callback centrifuge.PublishCallback) {
		ctx, cancel := context.WithTimeout(client.Context(), 10*time.Second)
		defer cancel()
		result, err := h.Publish(ctx, event.Channel, event.Data)
		if err != nil {
			callback(centrifuge.PublishReply{}, &centrifuge.Error{Code: 1102, Message: err.Error()})
			return
		}
		callback(centrifuge.PublishReply{Result: result}, nil)
	})
	client.OnPresence(func(event centrifuge.PresenceEvent, callback centrifuge.PresenceCallback) {
		if !client.IsSubscribed(event.Channel) {
			callback(centrifuge.PresenceReply{}, centrifuge.ErrorPermissionDenied)
			return
		}
		callback(centrifuge.PresenceReply{}, nil)
	})
}

var errContextChanged = errors.New("stream context changed; subscribe again")

func (h *Hub) acquire(clientID, channel string, pc backend.PluginContext, path string, metadata []byte, epoch uint64) (*subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("Live service is shut down")
	}
	if epoch != h.epoch {
		return nil, errContextChanged
	}
	entry := h.streams[channel]
	if entry == nil {
		if len(h.streams) >= MaxChannels {
			return nil, errors.New("Live channel limit reached")
		}
		ctx, cancel := context.WithCancel(context.Background())
		entry = &subscription{Channel: Channel{Channel: channel, StartedAt: time.Now().UnixMilli()}, pc: pc, path: path, metadata: metadata, clients: map[string]bool{}, ctx: ctx, cancel: cancel}
		h.streams[channel] = entry
	} else if !bytes.Equal(entry.metadata, metadata) {
		return nil, errors.New("channel metadata must match existing subscribers; use a different path")
	}
	entry.clients[clientID] = true
	entry.Subscribers = len(entry.clients)
	return entry, nil
}

// Invalidate prevents streams from retaining deleted or outdated credentials.
// A configuration update asks clients to resubscribe with the current context.
func (h *Hub) Invalidate(scope, id string, retry bool) {
	h.mu.Lock()
	h.epoch++
	channels := []string{}
	for channel, entry := range h.streams {
		if scope == "ds" && strings.HasPrefix(channel, "ds/"+id+"/") || scope == "plugin" && entry.pc.PluginID == id {
			delete(h.streams, channel)
			entry.cancel()
			channels = append(channels, channel)
		}
	}
	node := h.node
	h.mu.Unlock()
	code := uint32(2101)
	if retry {
		code = 2501
	}
	for _, channel := range channels {
		_ = node.Unsubscribe("1", channel, centrifuge.WithCustomUnsubscribe(centrifuge.Unsubscribe{Code: code, Reason: "stream configuration changed"}))
	}
}
func (h *Hub) release(clientID, channel string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.streams[channel]; entry != nil {
		delete(entry.clients, clientID)
		entry.Subscribers = len(entry.clients)
		if entry.Subscribers == 0 {
			delete(h.streams, channel)
			entry.cancel()
		}
	}
}
func (h *Hub) releaseClient(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel, entry := range h.streams {
		delete(entry.clients, clientID)
		entry.Subscribers = len(entry.clients)
		if entry.Subscribers == 0 {
			delete(h.streams, channel)
			entry.cancel()
		}
	}
}
func (h *Hub) run(entry *subscription) {
	h.mu.Lock()
	if h.closed || h.streams[entry.Channel.Channel] != entry || entry.Running {
		h.mu.Unlock()
		return
	}
	entry.Running = true
	h.wg.Add(1)
	node := h.node
	h.mu.Unlock()
	go func() {
		defer h.wg.Done()
		err := h.Manager().RunStream(entry.ctx, entry.pc, entry.path, entry.metadata, func(value []byte) error {
			if !json.Valid(value) {
				return errors.New("plugin stream packet must be JSON")
			}
			if _, err := node.Publish(entry.Channel.Channel, value); err != nil {
				return err
			}
			h.mu.Lock()
			entry.Packets++
			entry.Bytes += uint64(len(value))
			h.mu.Unlock()
			return nil
		})
		h.mu.Lock()
		current := h.streams[entry.Channel.Channel] == entry
		if current {
			delete(h.streams, entry.Channel.Channel)
			entry.cancel()
		}
		h.mu.Unlock()
		if current {
			reason := "plugin stream ended"
			if err != nil {
				reason = "plugin stream stopped"
			}
			_ = node.Unsubscribe("1", entry.Channel.Channel, centrifuge.WithCustomUnsubscribe(centrifuge.Unsubscribe{Code: 2101, Reason: reason}))
		}
	}()
}
func (h *Hub) Publish(ctx context.Context, channel string, value []byte) (*centrifuge.PublishResult, error) {
	if len(value) > 1<<20 || !json.Valid(value) {
		return nil, errors.New("publication must be JSON under 1 MiB")
	}
	scope, id, path, err := ParseChannel(channel)
	if err != nil {
		return nil, err
	}
	pc, err := h.Manager().StreamContext(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	result, err := h.Manager().PublishStream(ctx, pc, path, value)
	if err != nil {
		return nil, err
	}
	if result.Status != 0 {
		return nil, fmt.Errorf("plugin denied publication (status %d)", result.Status)
	}
	if len(result.Data) > 1<<20 || !json.Valid(result.Data) {
		return nil, errors.New("plugin returned invalid publication JSON")
	}
	if err := h.start(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	node := h.node
	h.mu.Unlock()
	published, err := node.Publish(channel, result.Data)
	return &published, err
}
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := Snapshot{Channels: []Channel{}}
	if h.node != nil {
		out.Connections = h.node.Hub().NumClients()
	}
	for _, entry := range h.streams {
		out.Channels = append(out.Channels, entry.Channel)
	}
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].Channel < out.Channels[j].Channel })
	return out
}
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for _, entry := range h.streams {
		entry.cancel()
	}
	h.streams = map[string]*subscription{}
	node := h.node
	h.mu.Unlock()
	if node != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = node.Shutdown(ctx)
		cancel()
	}
	h.wg.Wait()
}
