package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceURLUsesTheReachableListenerAddress(t *testing.T) {
	assert.Equal(t, "http://127.0.0.1:7333", workspaceURL(defaultRootURL, "127.0.0.1:7333"))
	assert.Equal(t, "http://127.0.0.1:9000", workspaceURL(defaultRootURL, "[::]:9000"))
	assert.Equal(t, "http://127.0.0.1:9000", workspaceURL(defaultRootURL, "0.0.0.0:9000"))
	assert.Equal(t, "http://[::1]:9000", workspaceURL(defaultRootURL, "[::1]:9000"))
	assert.Equal(t, "https://metrics.example.com", workspaceURL("https://metrics.example.com/", "127.0.0.1:7333"))
}

func TestAuthDescriptionNeverPrintsTheToken(t *testing.T) {
	assert.Equal(t, "loopback only, no token", authDescription("token", "", "127.0.0.1:7333"))
	assert.Equal(t, "no token configured", authDescription("token", "", "0.0.0.0:7333"))
	assert.Equal(t, "Bearer token required", authDescription("token", "agent-secret", "127.0.0.1:7333"))
	assert.NotContains(t, authDescription("token", "agent-secret", "127.0.0.1:7333"), "agent-secret")
	assert.Equal(t, "local accounts (browser sessions)", authDescription("local", "agent-secret", "0.0.0.0:7333"))
	assert.NotContains(t, authDescription("local", "agent-secret", "0.0.0.0:7333"), "agent-secret")
}

func TestOpenBrowserRejectsNonHTTPTargets(t *testing.T) {
	for _, target := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "not a url", "https:///missing-host"} {
		require.Error(t, openBrowser(target), target)
	}
}
