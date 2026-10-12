package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrganizationClickHouseDatabaseIdentifierLength(t *testing.T) {
	base := strings.Repeat("a", 100)
	derived, err := ClickHouseDatabase(base, 9223372036854775807)
	require.NoError(t, err)
	require.LessOrEqual(t, len(derived), 128)
	validated, err := ClickHouseDatabase(derived, 1)
	require.NoError(t, err)
	require.Equal(t, derived, validated)
	_, err = ClickHouseDatabase(strings.Repeat("a", 101), 2)
	require.Error(t, err)
	_, err = ClickHouseDatabase(strings.Repeat("a", 129), 1)
	require.Error(t, err)
}

func TestOrganizationDirectoryRejectsRedirectedPaths(t *testing.T) {
	root, err := canonicalRoot(t.TempDir())
	require.NoError(t, err)
	outside := t.TempDir()
	redirected := filepath.Join(root, "2")
	if err := os.Symlink(outside, redirected); err != nil {
		t.Skipf("symbolic links unavailable on this host: %v", err)
	}
	_, err = organizationPath(root, 2)
	require.ErrorContains(t, err, "redirected")
	path, err := organizationPath(root, 3)
	require.NoError(t, err)
	key := filepath.Join(filepath.Dir(path), "secrets.key")
	outsideKey := filepath.Join(outside, "secrets.key")
	require.NoError(t, os.WriteFile(outsideKey, make([]byte, 32), 0600))
	require.NoError(t, os.Symlink(outsideKey, key))
	_, err = organizationPath(root, 3)
	require.ErrorContains(t, err, "must not be symbolic links")
	for _, id := range []int64{-1, 0, 1} {
		_, err := organizationPath(root, id)
		require.Error(t, err)
	}
}
