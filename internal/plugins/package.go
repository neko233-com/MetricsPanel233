package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const RuntimeVersion = "13.2.3"
const MaxArchiveBytes = 64 << 20
const MaxPackageBytes = 256 << 20

//go:embed grafana-keys.json
var officialKeys []byte

type metadata struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Backend    bool   `json:"backend"`
	Executable string `json:"executable"`
	Info       struct {
		Version string `json:"version"`
	} `json:"info"`
	Dependencies struct {
		GrafanaVersion    string `json:"grafanaVersion"`
		GrafanaDependency string `json:"grafanaDependency"`
		Plugins           []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	} `json:"dependencies"`
}
type signatureManifest struct {
	ManifestVersion string            `json:"manifestVersion"`
	Plugin          string            `json:"plugin"`
	Version         string            `json:"version"`
	SignatureType   string            `json:"signatureType"`
	RootURLs        []string          `json:"rootUrls"`
	Files           map[string]string `json:"files"`
}
type archivePackage struct {
	Root    string
	Files   map[string][]byte
	Plugins []model.Plugin
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func safeName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(base) {
			return false
		}
	}
	return true
}
func trustedKeyring() (openpgp.EntityList, error) {
	var keys struct {
		Items []struct {
			Public string `json:"public"`
		} `json:"items"`
	}
	if err := json.Unmarshal(officialKeys, &keys); err != nil {
		return nil, err
	}
	ring := openpgp.EntityList{}
	for _, key := range keys.Items {
		entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.Public))
		if err != nil {
			return nil, err
		}
		ring = append(ring, entities...)
	}
	if len(ring) == 0 {
		return nil, errors.New("no Grafana signature keys")
	}
	return ring, nil
}
func verifySignature(files map[string][]byte, meta metadata, ring openpgp.EntityList, rootURL string) (string, error) {
	signed, exists := files["MANIFEST.txt"]
	if !exists {
		return "unsigned", nil
	}
	block, rest := clearsign.Decode(signed)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("invalid signed MANIFEST.txt")
	}
	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil); err != nil {
		return "", fmt.Errorf("plugin signature verification failed: %w", err)
	}
	var manifest signatureManifest
	if err := json.Unmarshal(block.Plaintext, &manifest); err != nil {
		return "", err
	}
	if manifest.ManifestVersion != "2.0.0" || manifest.Plugin != meta.ID || manifest.Version != meta.Info.Version {
		return "", errors.New("signed plugin ID/version differs from plugin.json")
	}
	if manifest.SignatureType != "community" && manifest.SignatureType != "commercial" && manifest.SignatureType != "grafana" && manifest.SignatureType != "private" {
		return "", errors.New("unsupported signature type")
	}
	if manifest.SignatureType == "private" {
		matched := false
		for _, allowed := range manifest.RootURLs {
			if strings.TrimRight(allowed, "/") == strings.TrimRight(rootURL, "/") {
				matched = true
			}
		}
		if !matched {
			return "", errors.New("private plugin signature does not allow this server root URL")
		}
	}
	for name, data := range files {
		if name == "MANIFEST.txt" {
			continue
		}
		if manifest.Files[name] != digest(data) {
			return "", fmt.Errorf("signed file checksum mismatch or unlisted file: %s", name)
		}
	}
	for name := range manifest.Files {
		if _, exists := files[name]; !exists {
			return "", fmt.Errorf("signed file missing: %s", name)
		}
	}
	return manifest.SignatureType, nil
}
func inspectArchive(raw []byte, expected string, allowedUnsigned map[string]bool, ring openpgp.EntityList, rootURL string) (archivePackage, error) {
	var out archivePackage
	if len(raw) == 0 || len(raw) > MaxArchiveBytes {
		return out, errors.New("plugin zip size must be 1–64 MiB")
	}
	hash := digest(raw)
	if expected != "" && !strings.EqualFold(hash, expected) {
		return out, errors.New("archive SHA256 differs from expected checksum")
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return out, err
	}
	if len(z.File) > 5000 {
		return out, errors.New("plugin archive exceeds 5000 entries")
	}
	full := map[string][]byte{}
	seen := map[string]bool{}
	total := uint64(0)
	root := ""
	rootDepth := 100000
	for _, entry := range z.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !safeName(name) {
			return out, fmt.Errorf("unsafe archive path: %q", entry.Name)
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return out, fmt.Errorf("duplicate archive path: %s", name)
		}
		seen[fold] = true
		if entry.Mode().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() {
			return out, errors.New("plugin archive cannot contain links or special files")
		}
		if entry.UncompressedSize64 > 64<<20 {
			return out, errors.New("plugin file exceeds 64 MiB")
		}
		total += entry.UncompressedSize64
		if total > MaxPackageBytes {
			return out, errors.New("expanded plugin archive exceeds 256 MiB")
		}
		reader, err := entry.Open()
		if err != nil {
			return out, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, int64(entry.UncompressedSize64)+1))
		reader.Close()
		if err != nil {
			return out, err
		}
		if uint64(len(data)) != entry.UncompressedSize64 {
			return out, errors.New("archive entry size mismatch")
		}
		full[name] = data
		if path.Base(name) == "plugin.json" {
			depth := strings.Count(name, "/")
			if depth < rootDepth {
				root = path.Dir(name)
				rootDepth = depth
			}
		}
	}
	if rootDepth == 100000 {
		return out, errors.New("plugin.json missing from archive")
	}
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	out.Root = root
	out.Files = map[string][]byte{}
	for name, data := range full {
		if !strings.HasPrefix(name, prefix) {
			return out, errors.New("archive contains files outside the plugin package")
		}
		relative := strings.TrimPrefix(name, prefix)
		if relative == ".metricspanel-owned" {
			return out, errors.New("reserved package filename")
		}
		out.Files[relative] = data
	}
	var parent metadata
	if err := json.Unmarshal(out.Files["plugin.json"], &parent); err != nil {
		return out, err
	}
	signature, err := verifySignature(out.Files, parent, ring, rootURL)
	if err != nil {
		return out, err
	}
	if signature == "unsigned" && !allowedUnsigned[parent.ID] {
		return out, errors.New("unsigned plugin requires an explicit --allow-unsigned-plugin ID at server startup")
	}
	fileHashes := map[string]string{}
	for name, data := range out.Files {
		fileHashes[name] = digest(data)
	}
	ids := map[string]bool{}
	for name, data := range out.Files {
		if path.Base(name) != "plugin.json" {
			continue
		}
		if len(data) > 512<<10 {
			return out, errors.New("plugin.json exceeds 512 KiB")
		}
		var meta metadata
		if err := json.Unmarshal(data, &meta); err != nil {
			return out, err
		}
		if meta.Dependencies.GrafanaVersion == "" {
			meta.Dependencies.GrafanaVersion = meta.Dependencies.GrafanaDependency
		}
		if !model.PluginID.MatchString(meta.ID) || meta.Name == "" || len(meta.Name) > 256 || ids[meta.ID] {
			return out, errors.New("plugin IDs/names must be valid and unique")
		}
		ids[meta.ID] = true
		if meta.Type != "panel" && meta.Type != "datasource" && meta.Type != "app" {
			return out, fmt.Errorf("unsupported plugin type %q", meta.Type)
		}
		if meta.Backend && !regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`).MatchString(meta.Executable) {
			return out, errors.New("backend executable must be a filename prefix")
		}
		if _, err := semver.NewVersion(meta.Info.Version); err != nil {
			return out, fmt.Errorf("invalid plugin version: %w", err)
		}
		if meta.Dependencies.GrafanaVersion != "" {
			constraint, err := semver.NewConstraint(meta.Dependencies.GrafanaVersion)
			if err != nil {
				return out, err
			}
			if !constraint.Check(semver.MustParse(RuntimeVersion)) {
				return out, fmt.Errorf("plugin %s requires Grafana %s; runtime contract is %s", meta.ID, meta.Dependencies.GrafanaVersion, RuntimeVersion)
			}
		}
		assetPath := path.Dir(name)
		module := "module.js"
		if assetPath != "." {
			module = assetPath + "/module.js"
		}
		if _, exists := out.Files[module]; !exists {
			return out, fmt.Errorf("module.js missing for plugin %s", meta.ID)
		}
		out.Plugins = append(out.Plugins, model.Plugin{ID: meta.ID, Name: meta.Name, Type: meta.Type, Version: meta.Info.Version, Backend: meta.Backend, Executable: meta.Executable, Enabled: true, SHA256: hash, Signature: signature, InstalledAt: time.Now().UnixMilli(), Metadata: data, PackageID: parent.ID, AssetPath: assetPath, Files: fileHashes})
	}
	if len(out.Plugins) > 64 {
		return out, errors.New("at most 64 plugins in one package")
	}
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].ID < out.Plugins[j].ID })
	return out, nil
}
func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
func removeOwned(root, target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if !within(root, absolute) {
		return errors.New("refusing package cleanup outside plugin root")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	if !within(root, resolved) {
		return errors.New("refusing cleanup of a package resolved outside plugin root")
	}
	marker, err := os.ReadFile(filepath.Join(absolute, ".metricspanel-owned"))
	if err != nil || string(marker) != "MetricsPanel233 package\n" {
		return errors.New("refusing cleanup of an unowned package directory")
	}
	return os.RemoveAll(absolute)
}
func writePackage(root string, p archivePackage) (string, error) {
	stage, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(stage, ".metricspanel-owned"), []byte("MetricsPanel233 package\n"), 0600); err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = removeOwned(root, stage)
		}
	}()
	for name, data := range p.Files {
		target := filepath.Join(stage, filepath.FromSlash(name))
		if !within(stage, target) {
			return "", errors.New("package file escaped staging directory")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return "", err
		}
		mode := os.FileMode(0640)
		if strings.HasSuffix(name, ".exe") || strings.Contains(name, "_linux_") || strings.Contains(name, "_darwin_") {
			mode = 0750
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	cleanup = false
	return stage, nil
}
func (m *Manager) Install(ctx context.Context, raw []byte, expected string) (model.Plugin, error) {
	return m.install(ctx, raw, expected, "", "")
}
func (m *Manager) install(ctx context.Context, raw []byte, expected, requestedID, requestedVersion string) (model.Plugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ring, err := trustedKeyring()
	if err != nil {
		return model.Plugin{}, err
	}
	p, err := inspectArchive(raw, expected, m.AllowUnsigned, ring, m.RootURL)
	if err != nil {
		return model.Plugin{}, err
	}
	for _, plugin := range p.Plugins {
		if existing, err := m.Store.Plugin(ctx, plugin.ID); err == nil && existing.PackageID != plugin.PackageID {
			return model.Plugin{}, errors.New("plugin ID belongs to another installed package")
		}
	}
	rootID := p.Plugins[0].PackageID
	var parent model.Plugin
	for _, plugin := range p.Plugins {
		if plugin.ID == rootID {
			parent = plugin
		}
	}
	if requestedID != "" && (parent.ID != requestedID || parent.Version != requestedVersion) {
		return parent, errors.New("catalog package ID/version differs from request")
	}
	if old, err := m.Store.Plugin(ctx, rootID); err == nil && old.SHA256 == parent.SHA256 {
		if err = m.verifyFiles(old); err != nil {
			return old, err
		}
		return old, nil
	}
	if err := m.ensureRoot(); err != nil {
		return parent, err
	}
	stage, err := writePackage(m.Root, p)
	if err != nil {
		return parent, err
	}
	defer func() { _ = removeOwned(m.Root, stage) }()
	destination := filepath.Join(m.Root, rootID)
	backup := ""
	if _, err := os.Stat(destination); err == nil {
		if _, err := m.Store.Plugin(ctx, rootID); err != nil {
			return parent, errors.New("destination exists without a registered plugin")
		}
		m.stopPackage(rootID)
		backup = destination + ".backup-" + fmt.Sprint(time.Now().UnixNano())
		if err = os.Rename(destination, backup); err != nil {
			return parent, err
		}
	}
	if err = os.Rename(stage, destination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return parent, err
	}
	if err = m.Store.SavePluginPackage(ctx, p.Plugins); err != nil {
		_ = os.Rename(destination, stage)
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return parent, err
	}
	if backup != "" {
		if err = removeOwned(m.Root, backup); err != nil {
			return parent, fmt.Errorf("installed plugin, old package cleanup failed: %w", err)
		}
	}
	return parent, nil
}
