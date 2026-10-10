package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/grpcplugin"
	"github.com/grafana/grafana-plugin-sdk-go/config"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/querycontext"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"google.golang.org/grpc"
)

type process struct {
	client      *plugin.Client
	packageID   string
	data        grpcplugin.DataClient
	diagnostics grpcplugin.DiagnosticsClient
	resource    grpcplugin.ResourceClient
	stream      grpcplugin.StreamClient
}
type Manager struct {
	Store         *store.Store
	Root          string
	RootURL       string
	AllowUnsigned map[string]bool
	mu            sync.RWMutex
	processes     map[string]*process
	requests      chan struct{}
}

func New(s *store.Store, root string, allowed []string) *Manager {
	if root == "" {
		root = filepath.Join(filepath.Dir(s.Path), "plugins")
	}
	absolute, err := filepath.Abs(root)
	if err == nil {
		root = absolute
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	m := &Manager{Store: s, Root: root, AllowUnsigned: map[string]bool{}, processes: map[string]*process{}, requests: make(chan struct{}, 8)}
	for _, id := range allowed {
		m.AllowUnsigned[id] = true
	}
	return m
}
func (m *Manager) ensureRoot() error {
	if err := os.MkdirAll(m.Root, 0750); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	m.Root = resolved
	return nil
}
func (m *Manager) directory(p model.Plugin) string {
	return filepath.Join(m.Root, p.PackageID, filepath.FromSlash(p.AssetPath))
}
func (m *Manager) verifyFiles(p model.Plugin) error {
	root := filepath.Join(m.Root, p.PackageID)
	if !model.PluginID.MatchString(p.PackageID) || !within(m.Root, root) {
		return errors.New("invalid registered plugin package")
	}
	for name, expected := range p.Files {
		if !safeName(name) {
			return errors.New("invalid registered plugin file")
		}
		file := filepath.Join(root, filepath.FromSlash(name))
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil {
			return err
		}
		if !within(root, resolved) {
			return errors.New("plugin file resolved outside package")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if digest(data) != expected {
			return fmt.Errorf("installed plugin file checksum changed: %s", name)
		}
	}
	if len(p.Files) == 0 {
		return errors.New("plugin has no verified file inventory")
	}
	if p.Signature == "unsigned" && !m.AllowUnsigned[p.PackageID] {
		return errors.New("unsigned package is not allowed by this server")
	}
	return nil
}
func (m *Manager) Asset(ctx context.Context, id, name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, err := m.Store.PluginEffective(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, errors.New("plugin is disabled")
	}
	if !safeName(name) {
		return nil, errors.New("invalid asset path")
	}
	if p.Signature == "unsigned" && !m.AllowUnsigned[p.PackageID] {
		return nil, errors.New("unsigned package is not allowed by this server")
	}
	relative := name
	if p.AssetPath != "." {
		relative = p.AssetPath + "/" + name
	}
	expected, exists := p.Files[relative]
	if !exists {
		return nil, sql.ErrNoRows
	}
	root := filepath.Join(m.Root, p.PackageID)
	file := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return nil, err
	}
	if !within(root, resolved) {
		return nil, errors.New("asset resolved outside plugin package")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if digest(data) != expected {
		return nil, errors.New("plugin asset was modified after installation")
	}
	return data, nil
}
func (m *Manager) SetEnabled(ctx context.Context, id string, enabled bool) (model.Plugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.Store.Plugin(ctx, id)
	if err != nil {
		return p, err
	}
	if enabled {
		if p.PackageID != p.ID {
			parent, err := m.Store.Plugin(ctx, p.PackageID)
			if err != nil {
				return p, err
			}
			if !parent.Enabled {
				return p, errors.New("enable the owning package before enabling this bundled plugin")
			}
		}
		if err = m.verifyFiles(p); err != nil {
			return p, err
		}
	}
	p.Enabled = enabled
	if !enabled {
		if p.ID == p.PackageID {
			m.stopPackage(p.PackageID)
		} else if process := m.processes[p.ID]; process != nil {
			process.client.Kill()
			delete(m.processes, p.ID)
		}
	}
	if p.Type == "app" {
		_, err = m.Store.SaveAppSettings(ctx, p.ID, model.AppSettingsInput{Enabled: &enabled})
	} else {
		err = m.Store.SavePlugin(ctx, p)
	}
	return p, err
}
func (m *Manager) stopPackage(id string) {
	for name, p := range m.processes {
		if p.packageID == id {
			p.client.Kill()
			delete(m.processes, name)
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.processes {
		p.client.Kill()
	}
	m.processes = map[string]*process{}
}
func (m *Manager) Uninstall(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.Store.Plugin(ctx, id)
	if err != nil {
		return err
	}
	if p.PackageID != id {
		return errors.New("uninstall the root package rather than a bundled plugin")
	}
	m.stopPackage(id)
	destination := filepath.Join(m.Root, id)
	marker, err := os.ReadFile(filepath.Join(destination, ".metricspanel-owned"))
	if err != nil || string(marker) != "MetricsPanel233 package\n" {
		return errors.New("refusing removal of an unowned package directory")
	}
	retired := destination + ".removed-" + fmt.Sprint(time.Now().UnixNano())
	if err = os.Rename(destination, retired); err != nil {
		return err
	}
	if err = m.Store.DeletePluginPackage(ctx, id); err != nil {
		if rollback := os.Rename(retired, destination); rollback != nil {
			return fmt.Errorf("%w; package restore failed: %v", err, rollback)
		}
		return err
	}
	return removeOwned(m.Root, retired)
}
func processEnvironment() []string {
	out := []string{"GF_VERSION=" + RuntimeVersion}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "LANG"} {
		if value := os.Getenv(key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}
func (m *Manager) getProcess(ctx context.Context, id string) (*process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.Store.PluginEffective(ctx, id)
	if err != nil {
		return nil, err
	}
	if !p.Enabled || !p.Backend {
		return nil, errors.New("plugin backend is not enabled")
	}
	if cached := m.processes[id]; cached != nil && !cached.client.Exited() {
		return cached, nil
	}
	if err = m.verifyFiles(p); err != nil {
		return nil, err
	}
	executable := p.Executable + "_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	binary := filepath.Join(m.directory(p), executable)
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin executable for %s/%s is missing", runtime.GOOS, runtime.GOARCH)
	}
	command := exec.Command(binary)
	configureCommand(command)
	command.Dir = m.directory(p)
	command.Env = append(processEnvironment(), "GF_PLUGIN_ID="+id)
	client := plugin.NewClient(&plugin.ClientConfig{SkipHostEnv: true, HandshakeConfig: plugin.HandshakeConfig{ProtocolVersion: grpcplugin.ProtocolVersion, MagicCookieKey: grpcplugin.MagicCookieKey, MagicCookieValue: grpcplugin.MagicCookieValue}, VersionedPlugins: map[int]plugin.PluginSet{grpcplugin.ProtocolVersion: {"data": &grpcplugin.DataGRPCPlugin{}, "diagnostics": &grpcplugin.DiagnosticsGRPCPlugin{}, "resource": &grpcplugin.ResourceGRPCPlugin{}, "stream": &grpcplugin.StreamGRPCPlugin{}}}, Cmd: command, AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, AutoMTLS: true, StartTimeout: 10 * time.Second, Logger: hclog.NewNullLogger(), GRPCDialOptions: []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20), grpc.MaxCallSendMsgSize(4<<20))}})
	connection, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		client.Kill()
		return nil, err
	}
	host := &process{client: client, packageID: p.PackageID}
	value, err := connection.Dispense("data")
	if err != nil {
		client.Kill()
		return nil, err
	}
	host.data = value.(grpcplugin.DataClient)
	value, err = connection.Dispense("diagnostics")
	if err != nil {
		client.Kill()
		return nil, err
	}
	host.diagnostics = value.(grpcplugin.DiagnosticsClient)
	value, err = connection.Dispense("resource")
	if err != nil {
		client.Kill()
		return nil, err
	}
	host.resource = value.(grpcplugin.ResourceClient)
	value, err = connection.Dispense("stream")
	if err != nil {
		client.Kill()
		return nil, err
	}
	host.stream = value.(grpcplugin.StreamClient)
	m.processes[id] = host
	return host, nil
}
func (m *Manager) PluginContext(ctx context.Context, ds model.DataSource) (backend.PluginContext, error) {
	p, err := m.Store.Plugin(ctx, ds.Type)
	if err != nil {
		return backend.PluginContext{}, err
	}
	secrets, err := m.Store.DataSourceSecrets(ctx, ds.UID)
	if err != nil {
		return backend.PluginContext{}, err
	}
	pc := backend.PluginContext{OrgID: 1, Namespace: "default", PluginID: ds.Type, PluginVersion: p.Version, User: &backend.User{Login: "metricspanel", Name: "MetricsPanel233", Role: "Admin"}, GrafanaConfig: config.NewGrafanaCfg(map[string]string{"GF_VERSION": RuntimeVersion, "GF_APP_URL": m.RootURL}), DataSourceInstanceSettings: &backend.DataSourceInstanceSettings{ID: ds.ID, UID: ds.UID, Type: ds.Type, Name: ds.Name, URL: ds.URL, User: ds.User, Database: ds.Database, BasicAuthEnabled: ds.BasicAuth, BasicAuthUser: ds.BasicAuthUser, JSONData: ds.JSONData, DecryptedSecureJSONData: secrets, Updated: time.UnixMilli(ds.UpdatedAt)}}
	if p.PackageID != p.ID {
		parent, err := m.Store.Plugin(ctx, p.PackageID)
		if err != nil {
			return pc, err
		}
		if parent.Type == "app" {
			pc.AppInstanceSettings, err = m.appInstance(ctx, parent.ID)
			if err != nil {
				return pc, err
			}
		}
	}
	return pc, nil
}
func (m *Manager) Query(ctx context.Context, ds model.DataSource, queries []backend.DataQuery) (*backend.QueryDataResponse, error) {
	select {
	case m.requests <- struct{}{}:
		defer func() { <-m.requests }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	host, err := m.getProcess(ctx, ds.Type)
	if err != nil {
		return nil, err
	}
	pc, err := m.PluginContext(ctx, ds)
	if err != nil {
		return nil, err
	}
	request := &backend.QueryDataRequest{PluginContext: pc, Queries: queries, Format: backend.DataFrameFormat_JSON, Headers: querycontext.Headers(ctx)}
	result, err := host.data.QueryData(ctx, backend.ToProto().QueryDataRequest(request))
	if err != nil {
		return nil, err
	}
	return backend.FromProto().QueryDataResponse(result)
}
func (m *Manager) Health(ctx context.Context, ds model.DataSource) (*backend.CheckHealthResult, error) {
	pc, err := m.PluginContext(ctx, ds)
	if err != nil {
		return nil, err
	}
	return m.HealthContext(ctx, pc)
}
func (m *Manager) HealthContext(ctx context.Context, pc backend.PluginContext) (*backend.CheckHealthResult, error) {
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
	result, err := host.diagnostics.CheckHealth(ctx, &pluginv2.CheckHealthRequest{PluginContext: backend.ToProto().PluginContext(pc)})
	if err != nil {
		return nil, err
	}
	return backend.FromProto().CheckHealthResponse(result), nil
}
func (m *Manager) Resource(ctx context.Context, ds model.DataSource, request *backend.CallResourceRequest) ([]*backend.CallResourceResponse, error) {
	pc, err := m.PluginContext(ctx, ds)
	if err != nil {
		return nil, err
	}
	return m.ResourceContext(ctx, pc, request)
}
func (m *Manager) ResourceContext(ctx context.Context, pc backend.PluginContext, request *backend.CallResourceRequest) ([]*backend.CallResourceResponse, error) {
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
	request.PluginContext = pc
	stream, err := host.resource.CallResource(ctx, backend.ToProto().CallResourceRequest(request))
	if err != nil {
		return nil, err
	}
	out := []*backend.CallResourceResponse{}
	size := 0
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		size += len(message.Body)
		if size > 32<<20 {
			return nil, errors.New("plugin resource response exceeds 32 MiB")
		}
		out = append(out, backend.FromProto().CallResourceResponse(message))
		if len(out) > 1000 {
			return nil, errors.New("plugin resource response exceeds 1000 chunks")
		}
	}
	return out, nil
}
func (m *Manager) CatalogInstall(ctx context.Context, id, version string) (model.Plugin, error) {
	if _, err := semver.StrictNewVersion(version); err != nil {
		return model.Plugin{}, errors.New("catalog installation requires an exact semantic version")
	}
	if !model.PluginID.MatchString(id) || version == "" || strings.ContainsAny(version, "/?#\\") || len(version) > 100 {
		return model.Plugin{}, errors.New("plugin ID and exact version required")
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("catalog download must remain HTTPS with at most 10 redirects")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, "GET", "https://grafana.com/api/plugins/"+id+"/versions/"+version+"/download?os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil)
	if err != nil {
		return model.Plugin{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return model.Plugin{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return model.Plugin{}, fmt.Errorf("Grafana catalog returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxArchiveBytes+1))
	if err != nil {
		return model.Plugin{}, err
	}
	p, err := m.install(ctx, raw, "", id, version)
	if err != nil {
		return p, err
	}
	if p.ID != id || p.Version != version {
		return p, errors.New("catalog package ID/version differs from request")
	}
	return p, nil
}
