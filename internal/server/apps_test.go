package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplicationSettingsAuthVersionsAndMaskedMetadata(t *testing.T) {
	const token = "application-test-233233"
	s, h := setup(t, token)
	ctx := context.Background()
	require.NoError(t, s.SavePluginPackage(ctx, []model.Plugin{
		{ID: "test-app", PackageID: "test-app", Type: "app", Enabled: true, Metadata: json.RawMessage(`{"id":"test-app","name":"Test app","type":"app"}`)},
		{ID: "child-app", PackageID: "test-app", Type: "app", Enabled: true, Metadata: json.RawMessage(`{"id":"child-app","name":"Child app","type":"app"}`)},
	}))
	path := "/api/v1/plugins/test-app/app-settings"
	input := `{"version":0,"pinned":true,"jsonData":{"label":"Operations"},"secureJsonData":{"apiKey":"masked-app-secret"}}`
	assert.Equal(t, 401, call(h, "GET", path, "", "", "").Code)
	assert.Equal(t, 401, call(h, "PUT", path, input, "", "").Code)
	assert.Equal(t, 403, call(h, "PUT", path, input, token, "https://foreign.example").Code)
	assert.Equal(t, 400, call(h, "PUT", path, `{"pinned":true}`, token, "").Code)
	w := call(h, "PUT", path, input, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "masked-app-secret")
	assert.Contains(t, w.Body.String(), `"apiKey":true`)
	assert.Equal(t, 409, call(h, "PUT", path, input, token, "").Code)
	for _, read := range []string{path, "/api/plugins/test-app/settings", "/api/plugins"} {
		w = call(h, "GET", read, "", token, "")
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), "masked-app-secret")
		assert.Contains(t, w.Body.String(), `"apiKey":true`)
	}
	// Grafana's legacy API permits omitted versions, while the agent API requires one.
	w = call(h, "POST", "/api/plugins/test-app/settings", `{"enabled":false}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	current, err := s.AppSettings(ctx, "test-app")
	require.NoError(t, err)
	assert.Equal(t, 2, current.Version)
	assert.True(t, current.Pinned)
	assert.JSONEq(t, `{"label":"Operations"}`, string(current.JSONData))
	assert.False(t, current.Enabled)
	w = call(h, "PUT", "/api/v1/plugins/child-app/app-settings", `{"version":0,"enabled":true}`, token, "")
	assert.Equal(t, 400, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "owning package is disabled")
	w = call(h, "PUT", "/api/v1/plugins/child-app/app-settings", `{"version":0,"jsonData":{"label":"Disabled child"}}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"enabled":false`)
	_, err = s.SaveAppSettings(ctx, "test-app", model.AppSettingsInput{SecureJSONFields: map[string]bool{"apiKey": false}})
	require.NoError(t, err)
	secrets, err := s.AppSecrets(ctx, "test-app")
	require.NoError(t, err)
	assert.Empty(t, secrets)
}
