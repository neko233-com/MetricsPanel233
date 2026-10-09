package plugins

import (
	"context"
	"errors"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/config"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
)

// StreamContext resolves the same datasource credentials used by QueryData.
func (m *Manager) StreamContext(ctx context.Context, scope, id string) (backend.PluginContext, error) {
	if scope == "ds" {
		ds, err := m.Store.DataSource(ctx, id)
		if err != nil {
			return backend.PluginContext{}, err
		}
		return m.PluginContext(ctx, ds)
	}
	if scope != "plugin" {
		return backend.PluginContext{}, errors.New("stream scope must be ds or plugin")
	}
	p, err := m.Store.Plugin(ctx, id)
	if err != nil {
		return backend.PluginContext{}, err
	}
	if p.Type == "app" {
		return m.AppContext(ctx, id)
	}
	return backend.PluginContext{OrgID: 1, Namespace: "default", PluginID: p.ID, PluginVersion: p.Version, User: &backend.User{Login: "metricspanel", Name: "MetricsPanel233", Role: "Admin"}, GrafanaConfig: config.NewGrafanaCfg(map[string]string{"GF_VERSION": RuntimeVersion, "GF_APP_URL": m.RootURL})}, nil
}
func validStreamPath(path string) bool {
	return path != "" && len(path) <= 160 && !strings.ContainsAny(path, "\x00\r\n\\") && !strings.Contains(path, "..")
}
func (m *Manager) SubscribeStream(ctx context.Context, pc backend.PluginContext, path string, metadata []byte) (*pluginv2.SubscribeStreamResponse, error) {
	if !validStreamPath(path) || len(metadata) > 1<<20 {
		return nil, errors.New("invalid stream path or metadata")
	}
	select {
	case m.requests <- struct{}{}:
		defer func() { <-m.requests }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	host, err := m.getProcess(ctx, pc.PluginID)
	if err != nil {
		return nil, err
	}
	return host.stream.SubscribeStream(ctx, backend.ToProto().SubscribeStreamRequest(&backend.SubscribeStreamRequest{PluginContext: pc, Path: path, Data: metadata}))
}
func (m *Manager) PublishStream(ctx context.Context, pc backend.PluginContext, path string, value []byte) (*pluginv2.PublishStreamResponse, error) {
	if !validStreamPath(path) || len(value) > 1<<20 {
		return nil, errors.New("invalid stream path or publication")
	}
	select {
	case m.requests <- struct{}{}:
		defer func() { <-m.requests }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	host, err := m.getProcess(ctx, pc.PluginID)
	if err != nil {
		return nil, err
	}
	return host.stream.PublishStream(ctx, backend.ToProto().PublishStreamRequest(&backend.PublishStreamRequest{PluginContext: pc, Path: path, Data: value}))
}
func (m *Manager) RunStream(ctx context.Context, pc backend.PluginContext, path string, metadata []byte, send func([]byte) error) error {
	if !validStreamPath(path) || len(metadata) > 1<<20 {
		return errors.New("invalid stream path or metadata")
	}
	host, err := m.getProcess(ctx, pc.PluginID)
	if err != nil {
		return err
	}
	stream, err := host.stream.RunStream(ctx, backend.ToProto().RunStreamRequest(&backend.RunStreamRequest{PluginContext: pc, Path: path, Data: metadata}))
	if err != nil {
		return err
	}
	for {
		packet, err := stream.Recv()
		if err != nil {
			return err
		}
		if len(packet.Data) > 1<<20 {
			return errors.New("stream packet exceeds 1 MiB")
		}
		if err := send(packet.Data); err != nil {
			return err
		}
	}
}
