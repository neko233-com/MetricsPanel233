package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorSecretsRemainEncryptedMaskedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	target, err := db.SaveTarget(ctx, model.Target{Name: "redis", Kind: "redis", URL: "redis://localhost:6379", Username: "metrics", IntervalSeconds: 15, SecureSettings: map[string]string{"password": "secret-233"}})
	require.NoError(t, err)
	data, err := json.Marshal(target)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "secret-233")
	assert.True(t, target.SecureFields["password"])
	var sealed []byte
	require.NoError(t, db.DB.QueryRow(`SELECT secrets FROM target_connections WHERE id=?`, target.ID).Scan(&sealed))
	assert.NotContains(t, string(sealed), "secret-233")
	target.Name = "cache"
	_, err = db.SaveTarget(ctx, target)
	require.NoError(t, err)
	require.NoError(t, db.DB.Close())
	db, err = store.Open(path)
	require.NoError(t, err)
	secrets, err := db.TargetSecrets(ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, "secret-233", secrets["password"])
	list, err := db.Targets(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "cache", list[0].Name)
	assert.Nil(t, list[0].SecureSettings)
	assert.True(t, list[0].SecureFields["password"])
	target.SecureSettings = map[string]string{"password": ""}
	_, err = db.SaveTarget(ctx, target)
	require.NoError(t, err)
	secrets, err = db.TargetSecrets(ctx, target.ID)
	require.NoError(t, err)
	assert.Empty(t, secrets)
	require.NoError(t, db.DB.Close())
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), "secrets.key")))
	_, err = store.Open(path)
	require.ErrorContains(t, err, "secrets.key is missing")
}
