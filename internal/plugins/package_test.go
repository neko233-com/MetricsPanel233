package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clockHash = "b2dc870d732f39edf4a37fd20ad0e0fa9efa3730efff46b2a5b23213e3b0507f"

func TestOfficialSignedClockPackageInstallIdempotencyRestartAndTampering(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/grafana-clock-panel-3.2.4.zip")
	require.NoError(t, err)
	assert.Equal(t, clockHash, digest(raw))
	ring, err := trustedKeyring()
	require.NoError(t, err)
	p, err := inspectArchive(raw, clockHash, nil, ring, "")
	require.NoError(t, err)
	require.Len(t, p.Plugins, 1)
	assert.Equal(t, "grafana-clock-panel", p.Plugins[0].ID)
	assert.Equal(t, "grafana", p.Plugins[0].Signature)
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "control.db"))
	require.NoError(t, err)
	m := New(s, "", nil)
	installed, err := m.Install(ctx, raw, clockHash)
	require.NoError(t, err)
	assert.Equal(t, "3.2.4", installed.Version)
	again, err := m.Install(ctx, raw, clockHash)
	require.NoError(t, err)
	assert.Equal(t, installed.InstalledAt, again.InstalledAt)
	items, err := os.ReadDir(m.Root)
	require.NoError(t, err)
	require.Len(t, items, 1, "repeat installation left temporary directories")
	assets, err := m.Asset(ctx, installed.ID, "module.js")
	require.NoError(t, err)
	assert.Contains(t, string(assets), "define(")
	require.NoError(t, s.DB.Close())
	s, err = store.Open(filepath.Join(dir, "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	m = New(s, "", nil)
	_, err = m.Asset(ctx, installed.ID, "module.js")
	require.NoError(t, err)
	_, err = m.Asset(ctx, installed.ID, "../control.db")
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(m.Root, installed.ID, "module.js"), []byte("modified"), 0600))
	_, err = m.Asset(ctx, installed.ID, "module.js")
	require.ErrorContains(t, err, "modified")
}
func unsignedArchive(t *testing.T, extra string) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	files := map[string]string{"test-panel/plugin.json": `{"id":"test-panel","name":"Test","type":"panel","info":{"version":"1.0.0"},"dependencies":{"grafanaVersion":">=12"}}`, "test-panel/module.js": "System.register([],function(){return{execute:function(){}}})"}
	if extra != "" {
		files[extra] = "unsafe"
	}
	for name, data := range files {
		f, err := z.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(data))
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())
	return out.Bytes()
}
func TestUnsignedPolicyZipSlipAndChecksums(t *testing.T) {
	ring, err := trustedKeyring()
	require.NoError(t, err)
	raw := unsignedArchive(t, "")
	_, err = inspectArchive(raw, "", nil, ring, "")
	require.ErrorContains(t, err, "unsigned")
	_, err = inspectArchive(raw, "bad", map[string]bool{"test-panel": true}, ring, "")
	require.ErrorContains(t, err, "SHA256")
	_, err = inspectArchive(raw, "", map[string]bool{"test-panel": true}, ring, "")
	require.NoError(t, err)
	for _, unsafe := range []string{"../escaped.txt", "test-panel/../escaped.txt", "test-panel/CON.txt", "test-panel/a:b", "test-panel/module.JS"} {
		_, err = inspectArchive(unsignedArchive(t, unsafe), "", map[string]bool{"test-panel": true}, ring, "")
		require.Error(t, err, unsafe)
	}
}
