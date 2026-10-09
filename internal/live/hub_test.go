package live

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChannelAndMetadataValidation(t *testing.T) {
	for _, invalid := range []string{"", "ds/a", "ds//p", "unknown/a/p", "ds/a/../file", "plugin/id/path\x00"} {
		_, _, _, err := ParseChannel(invalid)
		require.Error(t, err, invalid)
	}
	scope, id, path, err := ParseChannel("ds/example/nested/path")
	require.NoError(t, err)
	assert.Equal(t, "ds", scope)
	assert.Equal(t, "example", id)
	assert.Equal(t, "nested/path", path)
	first, err := canonicalMetadata([]byte(`{"b":{"z":1,"a":2},"a":1}`))
	require.NoError(t, err)
	second, err := canonicalMetadata([]byte(`{"a":1,"b":{"a":2,"z":1}}`))
	require.NoError(t, err)
	assert.Equal(t, first, second)
	_, err = canonicalMetadata([]byte(`{} {}`))
	require.Error(t, err)
	assert.True(t, json.Valid(first))
}
