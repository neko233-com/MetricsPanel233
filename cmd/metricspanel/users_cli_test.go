package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/neko233-com/MetricsPanel233/internal/localauth"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsersCLIUsesBasicAuthAndNeverEchoesPasswords(t *testing.T) {
	root := t.TempDir()
	catalog, err := identity.Open(filepath.Join(root, "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	_, err = catalog.BootstrapAdmin(context.Background(), identity.UserInput{
		Login: "admin", Name: "Administrator", Email: "admin@example.com", Password: "correct-horse-233", OrgID: 1,
	})
	require.NoError(t, err)
	authenticator, err := instance.NewHTTPAuthenticator(catalog, instance.HTTPAuthenticationConfig{CookieName: identity.SessionCookieName})
	require.NoError(t, err)
	auth, err := localauth.New(localauth.Config{Catalog: catalog, Authenticator: authenticator, CookieName: identity.SessionCookieName})
	require.NoError(t, err)
	db, err := store.Open(filepath.Join(root, "control.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.DB.Close()) })
	host := httptest.NewServer(auth.Wrap(server.New(db, "", 30).Handler()))
	t.Cleanup(host.Close)

	credentials := []string{"--server", host.URL, "--api-username", "admin", "--api-password", "correct-horse-233"}
	userFile := filepath.Join(root, "user.json")
	require.NoError(t, os.WriteFile(userFile, []byte(`{"login":"viewer","name":"Viewer","email":"viewer@example.com","password":"viewer-password-233","role":"Viewer"}`), 0600))
	createdRaw, err := capture(t, append([]string{"users", "create", "--file", userFile}, credentials...)...)
	require.NoError(t, err)
	assert.NotContains(t, string(createdRaw), "viewer-password-233")
	var created struct {
		ID                int64 `json:"id"`
		Version           int64 `json:"version"`
		MembershipVersion int64 `json:"membershipVersion"`
	}
	require.NoError(t, json.Unmarshal(createdRaw, &created))
	require.Positive(t, created.ID)

	list, err := capture(t, append([]string{"users", "list"}, credentials...)...)
	require.NoError(t, err)
	assert.NotContains(t, string(list), "viewer-password-233")
	var listed []struct {
		Login string `json:"login"`
	}
	require.NoError(t, json.Unmarshal(list, &listed))
	assert.Equal(t, "viewer", listed[1].Login)

	me, err := capture(t, append([]string{"users", "me"}, credentials...)...)
	require.NoError(t, err)
	var profile struct {
		Login string `json:"login"`
	}
	require.NoError(t, json.Unmarshal(me, &profile))
	assert.Equal(t, "admin", profile.Login)

	updateFile := filepath.Join(root, "update.json")
	update := map[string]any{"version": created.Version, "membershipVersion": created.MembershipVersion, "role": "Editor"}
	payload, err := json.Marshal(update)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(updateFile, payload, 0600))
	updated, err := capture(t, append([]string{"users", "update", "--id", "2", "--file", updateFile}, credentials...)...)
	require.NoError(t, err)
	var changed struct {
		Role string `json:"role"`
	}
	require.NoError(t, json.Unmarshal(updated, &changed))
	assert.Equal(t, "Editor", changed.Role)
}
