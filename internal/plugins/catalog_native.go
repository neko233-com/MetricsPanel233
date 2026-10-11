package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// NativeList retains the Grafana.com contract, including fields not consumed by
// MetricsPanel's own UI. In particular the numeric id and slug are distinct.
func (c *CatalogClient) NativeList(ctx context.Context, includeDeprecated bool) (json.RawMessage, error) {
	flag := "false"
	if includeDeprecated {
		flag = "true"
	}
	return c.nativeRead(ctx, "/plugins?includeDeprecated="+flag, func(raw []byte) error {
		var response struct {
			Items []catalogRemote `json:"items"`
		}
		if err := decodeCatalog(raw, &response); err != nil {
			return err
		}
		if response.Items == nil || len(response.Items) > 2000 {
			return errors.New("Grafana catalog must contain at most 2000 items")
		}
		for _, item := range response.Items {
			if _, err := normalizeCatalogPlugin(item); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *CatalogClient) NativeDetail(ctx context.Context, id string) (json.RawMessage, error) {
	if err := validateCatalogID(id); err != nil {
		return nil, err
	}
	return c.nativeRead(ctx, "/plugins/"+id, func(raw []byte) error {
		var item catalogRemote
		if err := decodeCatalog(raw, &item); err != nil {
			return err
		}
		if item.Slug != id {
			return errors.New("Grafana catalog returned a different plugin ID")
		}
		_, err := normalizeCatalogPlugin(item)
		return err
	})
}

func (c *CatalogClient) NativeVersions(ctx context.Context, id string) (json.RawMessage, error) {
	if err := validateCatalogID(id); err != nil {
		return nil, err
	}
	return c.nativeRead(ctx, "/plugins/"+id+"/versions", func(raw []byte) error {
		var response struct {
			Items []catalogRemoteVersion `json:"items"`
		}
		if err := decodeCatalog(raw, &response); err != nil {
			return err
		}
		if response.Items == nil || len(response.Items) > 1000 {
			return errors.New("Grafana catalog must contain at most 1000 versions")
		}
		for _, item := range response.Items {
			if item.PluginSlug != "" && item.PluginSlug != id {
				return errors.New("Grafana catalog returned versions for a different plugin")
			}
			if err := validateNativeVersion(item.Version); err != nil {
				return errors.New("Grafana catalog returned an invalid version")
			}
		}
		return nil
	})
}

func (c *CatalogClient) NativeVersion(ctx context.Context, id, version string) (json.RawMessage, error) {
	if err := validateCatalogID(id); err != nil {
		return nil, err
	}
	if err := validateNativeVersion(version); err != nil {
		return nil, err
	}
	return c.nativeRead(ctx, "/plugins/"+id+"/versions/"+version, func(raw []byte) error {
		var item catalogRemoteVersion
		if err := decodeCatalog(raw, &item); err != nil {
			return err
		}
		if item.Version != version || (item.PluginSlug != "" && item.PluginSlug != id) {
			return errors.New("Grafana catalog returned a different plugin version")
		}
		return nil
	})
}

func validateNativeVersion(version string) error {
	if len(version) > 100 {
		return fmt.Errorf("%w: exact semantic version required", ErrCatalogInput)
	}
	if _, err := semver.StrictNewVersion(version); err != nil {
		return fmt.Errorf("%w: exact semantic version required", ErrCatalogInput)
	}
	return nil
}

func (c *CatalogClient) LatestVersion(ctx context.Context, id string) (string, error) {
	raw, err := c.NativeDetail(ctx, id)
	if err != nil {
		return "", err
	}
	var item catalogRemote
	if err := decodeCatalog(raw, &item); err != nil {
		return "", err
	}
	if err := validateNativeVersion(item.Version); err != nil {
		return "", errors.New("Grafana catalog has no installable latest semantic version")
	}
	return item.Version, nil
}

func (c *CatalogClient) nativeRead(ctx context.Context, endpoint string, validate func([]byte) error) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.read(ctx, endpoint, func(raw []byte) error {
		if err := validate(raw); err != nil {
			return err
		}
		result = append(json.RawMessage(nil), raw...)
		return nil
	})
	return result, err
}

// CachedLatestVersion does not make installed-plugin discovery depend on network
// availability. It uses only recent, successfully read official metadata.
func (c *CatalogClient) CachedLatestVersion(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range []string{"/plugins/" + id, "/plugins?includeDeprecated=true", "/plugins?includeDeprecated=false"} {
		entry, ok := c.cache[key]
		if !ok || !c.now().Before(entry.Expires) {
			continue
		}
		if strings.Contains(key, "?") {
			var list struct {
				Items []catalogRemote `json:"items"`
			}
			if json.Unmarshal(entry.Raw, &list) == nil {
				for _, plugin := range list.Items {
					if plugin.Slug == id {
						return plugin.Version
					}
				}
			}
		} else {
			var plugin catalogRemote
			if json.Unmarshal(entry.Raw, &plugin) == nil && plugin.Slug == id {
				return plugin.Version
			}
		}
	}
	return ""
}

type NativePluginError struct {
	ErrorCode  string `json:"errorCode"`
	PluginID   string `json:"pluginId"`
	PluginType string `json:"pluginType,omitempty"`
}

type NativeSignature struct {
	Status string
	Type   string
	Org    string
}

type pluginFailure struct {
	packageID string
	sha256    string
	code      string
}

// recordFailure is called while m.mu is held by a real backend startup attempt.
// The archive digest prevents a replaced package inheriting an earlier failure.
func (m *Manager) recordFailure(p model.Plugin, code string) {
	if m.failures == nil {
		m.failures = map[string]pluginFailure{}
	}
	m.failures[p.ID] = pluginFailure{packageID: p.PackageID, sha256: p.SHA256, code: code}
}

// InspectionErrors reports observed installed-package and process state. It
// retains actual startup failures for the current package until recovery or
// uninstall; it does not claim to retain an audit history after server restart.
func (m *Manager) InspectionErrors(ctx context.Context) ([]NativePluginError, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	installed, err := m.Store.Plugins(ctx)
	if err != nil {
		return nil, err
	}
	result := []NativePluginError{}
	for _, p := range installed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		code := ""
		if err := m.verifyFiles(p); err != nil {
			code = "signatureModified"
			if p.Signature == "unsigned" && !m.AllowUnsigned[p.PackageID] {
				code = "signatureMissing"
			}
		} else {
			var metadata struct {
				AngularDetected bool `json:"angularDetected"`
				Angular         struct {
					Detected bool `json:"detected"`
				} `json:"angular"`
			}
			if json.Unmarshal(p.Metadata, &metadata) == nil && (metadata.AngularDetected || metadata.Angular.Detected) {
				code = "angular"
			} else if p.Backend {
				if host := m.processes[p.ID]; host != nil && host.client.Exited() {
					code = "failedBackendStart"
				} else {
					executable := p.Executable + "_" + runtime.GOOS + "_" + runtime.GOARCH
					if runtime.GOOS == "windows" {
						executable += ".exe"
					}
					if info, err := os.Stat(filepath.Join(m.directory(p), executable)); err != nil || !info.Mode().IsRegular() {
						code = "failedBackendStart"
					}
				}
				if failure, exists := m.failures[p.ID]; code == "" && exists && failure.packageID == p.PackageID && failure.sha256 == p.SHA256 && failure.code == "failedBackendStart" {
					code = failure.code
				}
			}
		}
		if code != "" {
			result = append(result, NativePluginError{ErrorCode: code, PluginID: p.ID, PluginType: p.Type})
		}
	}
	return result, nil
}

func (m *Manager) SignatureInfo(ctx context.Context, id string) (NativeSignature, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, err := m.Store.Plugin(ctx, id)
	if err != nil {
		return NativeSignature{}, err
	}
	result := NativeSignature{Status: "valid", Type: p.Signature}
	if err := m.verifyFiles(p); err != nil {
		result.Status = "modified"
		if p.Signature == "unsigned" && !m.AllowUnsigned[p.PackageID] {
			result.Status, result.Type = "missing", ""
		}
		return result, nil
	}
	if p.Signature == "unsigned" {
		result.Status, result.Type = "missing", ""
		return result, nil
	}
	rootPackage := p
	rootPackage.AssetPath = "."
	manifest, err := m.verifiedAsset(rootPackage, "MANIFEST.txt")
	if err == nil {
		if block, _ := clearsign.Decode(manifest); block != nil {
			var signature struct {
				SignedByOrg string `json:"signedByOrg"`
			}
			if json.Unmarshal(block.Bytes, &signature) == nil {
				result.Org = signature.SignedByOrg
			}
		}
	}
	return result, nil
}

func (m *Manager) Markdown(ctx context.Context, id, name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.ToUpper(name), ".MD")
	if name != "README" && name != "CHANGELOG" {
		return nil, fmt.Errorf("%w: Markdown document must be README or CHANGELOG", ErrCatalogInput)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, err := m.Store.Plugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.verifyFiles(p); err != nil {
		return nil, err
	}
	for _, candidate := range []string{name + ".md", strings.ToLower(name) + ".md", "README.md", "readme.md"} {
		body, err := m.verifiedAsset(p, candidate)
		if err == nil && len(body) > 0 {
			return body, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, sql.ErrNoRows
}

// verifiedAsset also serves disabled plugins' documentation, without making
// disabled executable assets public. The caller holds m.mu while inspecting it.
func (m *Manager) verifiedAsset(p model.Plugin, name string) ([]byte, error) {
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
	body, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if digest(body) != expected {
		return nil, errors.New("plugin asset was modified after installation")
	}
	return body, nil
}
