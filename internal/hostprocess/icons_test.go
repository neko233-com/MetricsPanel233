package hostprocess

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIconKeysOnlyResolveRegisteredFilesAndReturnBoundedPNG(t *testing.T) {
	c := newIconCache()
	file := filepath.Join(t.TempDir(), "no-icon.bin")
	require.NoError(t, os.WriteFile(file, []byte("not a native application resource"), 0600))
	key := c.register(file)
	assert.NotEqual(t, "generic", key)
	assert.NotContains(t, key, file)
	assert.Equal(t, key, c.register(file), "unchanged file identity retains its icon key")
	data, source, err := c.get(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, "generic", source)
	assert.Less(t, len(data), maxIconBytes)
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, 32, cfg.Width)
	assert.Equal(t, 32, cfg.Height)
	_, _, err = c.get(context.Background(), file)
	assert.ErrorIs(t, err, ErrIconNotFound)
	_, _, err = c.get(context.Background(), "../../Windows/system.ini")
	assert.ErrorIs(t, err, ErrIconNotFound)
	for i := 0; i < maxIconEntries+3; i++ {
		p := filepath.Join(t.TempDir(), "icon.bin")
		require.NoError(t, os.WriteFile(p, []byte("no icon"), 0600))
		c.register(p)
	}
	assert.Len(t, c.entries, maxIconEntries+4)
	assert.Len(t, c.paths, maxIconEntries+4)
}
func TestNormalizePNGRejectsUnboundedDimensionsAndResizes(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2000, 1))))
	_, err := normalizePNG(&b)
	assert.Error(t, err)
	b.Reset()
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 96, 48))))
	data, err := normalizePNG(&b)
	require.NoError(t, err)
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, 32, cfg.Width)
	assert.Equal(t, 32, cfg.Height)
}

func TestIconMetadataSurvivesMoreThan256UniqueExecutables(t *testing.T) {
	c := newIconCache()
	dir := t.TempDir()
	keys := []string{}
	for i := 0; i < maxIconEntries+44; i++ {
		path := filepath.Join(dir, fmt.Sprintf("application-%03d.bin", i))
		require.NoError(t, os.WriteFile(path, []byte("application without native icon"), 0600))
		key := c.register(path)
		require.NotEqual(t, "generic", key)
		keys = append(keys, key)
	}
	for _, key := range keys {
		_, _, err := c.get(context.Background(), key)
		require.NoError(t, err)
	}
	require.Len(t, c.images, maxIconEntries, "PNG byte cache remains bounded")
	require.Len(t, c.entries, len(keys), "metadata for returned keys survives byte-cache eviction")
	assert.LessOrEqual(t, c.bytes, maxIconCacheBytes)
	_, _, err := c.get(context.Background(), keys[0])
	require.NoError(t, err, "the earliest snapshot icon must still resolve after 300 registrations and decodes")
}

func TestIconMetadataExpiryAndReregistrationExtendTheKeyLifetime(t *testing.T) {
	c := newIconCache()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "application.bin")
	require.NoError(t, os.WriteFile(path, []byte("no icon"), 0600))
	key := c.register(path)
	_, _, err := c.get(context.Background(), key)
	require.NoError(t, err)
	now = now.Add(SnapshotTTL - time.Second)
	require.Equal(t, key, c.register(path), "a new snapshot must renew the existing file key")
	now = now.Add(time.Second)
	_, _, err = c.get(context.Background(), key)
	require.NoError(t, err, "the original expiry must not invalidate a renewed snapshot key")
	now = now.Add(SnapshotTTL - 2*time.Second)
	_, _, err = c.get(context.Background(), key)
	require.NoError(t, err)
	now = now.Add(time.Second)
	_, _, err = c.get(context.Background(), key)
	assert.ErrorIs(t, err, ErrIconNotFound, "TTL expiry is exact, including cached PNG entries")
	newKey := c.register(path)
	require.NotEqual(t, key, newKey)
	_, _, err = c.get(context.Background(), newKey)
	require.NoError(t, err)
	_, _, err = c.get(context.Background(), key)
	assert.ErrorIs(t, err, ErrIconNotFound)
}
