package plugins

import (
	"context"
	"errors"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/config"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (m *Manager) appInstance(ctx context.Context, id string) (*backend.AppInstanceSettings, error) {
	settings, err := m.Store.AppSettings(ctx, id)
	if err != nil {
		return nil, err
	}
	secrets, err := m.Store.AppSecrets(ctx, id)
	if err != nil {
		return nil, err
	}
	return &backend.AppInstanceSettings{JSONData: settings.JSONData, DecryptedSecureJSONData: secrets, Updated: time.UnixMilli(settings.UpdatedAt)}, nil
}
func (m *Manager) AppContext(ctx context.Context, id string) (backend.PluginContext, error) {
	p, err := m.Store.PluginEffective(ctx, id)
	if err != nil {
		return backend.PluginContext{}, err
	}
	if p.Type != "app" || !p.Enabled {
		return backend.PluginContext{}, errors.New("app plugin is not enabled")
	}
	settings, err := m.appInstance(ctx, id)
	if err != nil {
		return backend.PluginContext{}, err
	}
	return backend.PluginContext{OrgID: 1, Namespace: "default", PluginID: id, PluginVersion: p.Version, User: &backend.User{Login: "metricspanel", Name: "MetricsPanel233", Role: "Admin"}, GrafanaConfig: config.NewGrafanaCfg(map[string]string{"GF_VERSION": RuntimeVersion, "GF_APP_URL": m.RootURL}), AppInstanceSettings: settings}, nil
}
func (m *Manager) ConfigureApp(ctx context.Context, id string, input model.AppSettingsInput) (model.AppSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if input.Version == nil {
		previous, err := m.Store.AppSettings(ctx, id)
		if err != nil {
			return previous, err
		}
		input.Version = &previous.Version
	}
	app, err := m.Store.SaveAppSettings(ctx, id, input)
	if err == nil && !app.Enabled {
		p, _ := m.Store.Plugin(ctx, id)
		if p.ID == p.PackageID {
			m.stopPackage(p.PackageID)
		} else if process := m.processes[id]; process != nil {
			process.client.Kill()
			delete(m.processes, id)
		}
	}
	return app, err
}
