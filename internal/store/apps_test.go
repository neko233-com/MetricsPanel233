package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppSecretsVersionsParentLifecycleAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer func() { s.DB.Close() }()
	parent := model.Plugin{ID: "test-app", PackageID: "test-app", Type: "app", Enabled: true, InstalledAt: 233}
	child := model.Plugin{ID: "child-datasource", PackageID: "test-app", Type: "datasource", Enabled: true}
	require.NoError(t, s.SavePluginPackage(ctx, []model.Plugin{parent, child}))
	defaults, err := s.AppSettings(ctx, parent.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(defaults.JSONData))
	assert.Zero(t, defaults.Version)
	version := 0
	pinned := true
	input := model.AppSettingsInput{Pinned: &pinned, Version: &version, JSONData: json.RawMessage(`{"label":"Operations"}`), SecureJSONData: map[string]string{"apiKey": "app-secret-233"}}
	saved, err := s.SaveAppSettings(ctx, parent.ID, input)
	require.NoError(t, err)
	assert.Equal(t, 1, saved.Version)
	assert.True(t, saved.Pinned)
	_, err = s.SaveAppSettings(ctx, parent.ID, input)
	require.ErrorIs(t, err, ErrAppConflict)
	var payload string
	var sealed []byte
	require.NoError(t, s.DB.QueryRow(`SELECT payload,secrets FROM app_settings WHERE id=?`, parent.ID).Scan(&payload, &sealed))
	assert.NotContains(t, payload, "app-secret-233")
	assert.NotContains(t, string(sealed), "app-secret-233")
	_, err = s.openSecrets(parent.ID, sealed)
	require.Error(t, err, "app secrets must not be interchangeable with datasource ciphertext")
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	secrets, err := s.AppSecrets(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, "app-secret-233", secrets["apiKey"])
	version = saved.Version
	disabled := false
	saved, err = s.SaveAppSettings(ctx, parent.ID, model.AppSettingsInput{Version: &version, Enabled: &disabled})
	require.NoError(t, err)
	assert.JSONEq(t, `{"label":"Operations"}`, string(saved.JSONData))
	assert.True(t, saved.SecureJSONFields["apiKey"])
	effective, err := s.PluginEffective(ctx, child.ID)
	require.NoError(t, err)
	assert.False(t, effective.Enabled)
	original, err := s.Plugin(ctx, child.ID)
	require.NoError(t, err)
	assert.True(t, original.Enabled, "parent toggle must preserve child preference")
	_, err = s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "disabled-child", Name: "Disabled child", Type: child.ID}})
	require.ErrorContains(t, err, "owning app package")
	version = saved.Version
	enabled := true
	saved, err = s.SaveAppSettings(ctx, parent.ID, model.AppSettingsInput{Version: &version, Enabled: &enabled})
	require.NoError(t, err)
	effective, err = s.PluginEffective(ctx, child.ID)
	require.NoError(t, err)
	assert.True(t, effective.Enabled)
	version = saved.Version
	saved, err = s.SaveAppSettings(ctx, parent.ID, model.AppSettingsInput{Version: &version, SecureJSONFields: map[string]bool{"apiKey": false}})
	require.NoError(t, err)
	assert.Empty(t, saved.SecureJSONFields)
	require.NoError(t, s.DB.Close())
	keyPath := filepath.Join(filepath.Dir(path), "secrets.key")
	key, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(keyPath))
	_, err = Open(path)
	require.ErrorContains(t, err, "restore the original key")
	require.NoError(t, os.WriteFile(keyPath, key, 0600))
	s, err = Open(path)
	require.NoError(t, err)
	require.NoError(t, s.DeletePluginPackage(ctx, parent.ID))
	var rows int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM app_settings`).Scan(&rows))
	assert.Zero(t, rows)
}
