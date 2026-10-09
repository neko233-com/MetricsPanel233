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

func TestDataSourceSecretsVersionsDefaultAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer func() { s.DB.Close() }()
	input := model.DataSourceInput{DataSource: model.DataSource{UID: "external-prom", Name: "External", Type: "prometheus", IsDefault: true}, SecureJSONData: map[string]string{"apiKey": "sensitive-secret-233"}}
	ds, err := s.SaveDataSource(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ds.ID)
	assert.Equal(t, 1, ds.Version)
	assert.True(t, ds.SecureJSONFields["apiKey"])
	var payload string
	var encrypted []byte
	require.NoError(t, s.DB.QueryRow(`SELECT payload,secrets FROM datasources WHERE uid=?`, ds.UID).Scan(&payload, &encrypted))
	assert.NotContains(t, payload, "sensitive-secret-233")
	assert.NotContains(t, string(encrypted), "sensitive-secret-233")
	_, err = s.SaveDataSource(ctx, input)
	require.ErrorIs(t, err, ErrDataSourceConflict)
	input.DataSource = ds
	input.SecureJSONData = nil
	ds, err = s.SaveDataSource(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, 2, ds.Version)
	secrets, err := s.DataSourceSecrets(ctx, ds.UID)
	require.NoError(t, err)
	assert.Equal(t, "sensitive-secret-233", secrets["apiKey"])
	_, err = s.openSecrets("other-uid", encrypted)
	require.Error(t, err, "ciphertext must be bound to datasource UID")
	encrypted[len(encrypted)-1] ^= 1
	_, err = s.openSecrets(ds.UID, encrypted)
	require.Error(t, err, "tampered ciphertext accepted")
	other, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "second-prom", Name: "Second", Type: "prometheus", IsDefault: true}})
	require.NoError(t, err)
	assert.Equal(t, int64(3), other.ID)
	ds, err = s.DataSource(ctx, ds.UID)
	require.NoError(t, err)
	assert.False(t, ds.IsDefault)
	assert.Equal(t, 3, ds.Version)
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	secrets, err = s.DataSourceSecrets(ctx, ds.UID)
	require.NoError(t, err)
	assert.Equal(t, "sensitive-secret-233", secrets["apiKey"])

	// Clearing a field is explicit; omitting secureJSONData preserves it.
	ds, err = s.DataSource(ctx, input.UID)
	require.NoError(t, err)
	ds.SecureJSONFields["apiKey"] = false
	ds, err = s.SaveDataSource(ctx, model.DataSourceInput{DataSource: ds})
	require.NoError(t, err)
	assert.Empty(t, ds.SecureJSONFields)
	secrets, err = s.DataSourceSecrets(ctx, ds.UID)
	require.NoError(t, err)
	assert.Empty(t, secrets)
	key, err := os.ReadFile(filepath.Join(filepath.Dir(path), "secrets.key"))
	require.NoError(t, err)
	assert.Len(t, key, 32)
	serialized, err := json.Marshal(ds)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), "sensitive-secret-233")
	require.NoError(t, s.DB.Close())
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), "secrets.key")))
	_, err = Open(path)
	require.ErrorContains(t, err, "restore the original key")
}
