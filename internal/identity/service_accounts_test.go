package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func serviceValue[T any](value T) *T { return &value }

func openServiceAccountTestStore(t *testing.T) (*Store, Organization) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	org, err := store.BeginOrganization(context.Background(), "Second organization")
	require.NoError(t, err)
	org, err = store.ActivateOrganization(context.Background(), org.ID, org.Version)
	require.NoError(t, err)
	return store, org
}

func TestServiceAccountDurableCredentialAndSafeDTO(t *testing.T) {
	ctx := context.Background()
	location := filepath.Join(t.TempDir(), "identity.db")
	store, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	account, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: " Agent "})
	require.NoError(t, err)
	require.Equal(t, "Agent", account.Name)
	require.Equal(t, RoleViewer, account.Role)
	token, key, err := store.CreateServiceAccountToken(ctx, 1, account.ID, " CLI ", 0)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, serviceCredentialPrefix))
	require.Equal(t, "CLI", token.Name)
	require.Zero(t, token.ExpiresAt)
	require.Zero(t, token.LastUsed)
	var digest []byte
	require.NoError(t, store.db.QueryRow(`SELECT token_hash FROM service_account_tokens WHERE id=?`, token.ID).Scan(&digest))
	expected := sha256.Sum256([]byte(key))
	require.Equal(t, expected[:], digest)
	require.Len(t, digest, 32)
	for _, dto := range []any{account, token} {
		encoded, err := json.Marshal(dto)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), key)
		require.NotContains(t, string(encoded), "token_hash")
		require.NotContains(t, string(encoded), `"key"`)
	}
	before := time.Now().UnixMilli()
	principal, err := store.AuthenticateServiceToken(ctx, key, 0)
	require.NoError(t, err)
	require.Equal(t, TypeServiceAccount, principal.Type)
	require.Equal(t, account.ID, principal.ID)
	require.Equal(t, account.Login, principal.Login)
	require.EqualValues(t, 1, principal.OrgID)
	require.False(t, principal.IsServerAdmin)
	require.NoError(t, principal.Validate())
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	restored, err := store.ServiceAccount(ctx, 1, account.ID)
	require.NoError(t, err)
	require.Equal(t, account, restored)
	tokens, err := store.ServiceAccountTokens(ctx, 1, account.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.GreaterOrEqual(t, tokens[0].LastUsed, before)
	_, err = store.AuthenticateServiceToken(ctx, key, 1)
	require.NoError(t, err)
	for _, invalid := range []string{"", key + "=", "grafana_sa_" + key[len(serviceCredentialPrefix):], key[:len(key)-1] + "B", serviceCredentialPrefix + strings.Repeat("A", 43)} {
		_, err = store.AuthenticateServiceToken(ctx, invalid, 0)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
	require.NoError(t, store.RevokeServiceAccountToken(ctx, 1, account.ID, token.ID))
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	// Check the durable database and WAL as well as the DTO serialization.
	for _, file := range []string{location, location + "-wal"} {
		bytes, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		require.NoError(t, err)
		require.NotContains(t, string(bytes), key)
	}
}

func TestServiceAccountOrganizationIsolationAndCurrentState(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	first, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "agent", Role: RoleAdmin})
	require.NoError(t, err)
	second, err := store.CreateServiceAccount(ctx, org.ID, ServiceAccountInput{Name: "agent", Role: RoleEditor})
	require.NoError(t, err)
	token, key, err := store.CreateServiceAccountToken(ctx, org.ID, second.ID, "daily", 3600)
	require.NoError(t, err)
	_, _, err = store.CreateServiceAccountToken(ctx, 1, first.ID, "daily", 0)
	require.NoError(t, err, "the same token name is valid in a different organization")
	_, err = store.ServiceAccount(ctx, 1, second.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.ServiceAccountTokens(ctx, 1, second.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.ErrorIs(t, store.RevokeServiceAccountToken(ctx, 1, second.ID, token.ID), sql.ErrNoRows)
	require.ErrorIs(t, store.RevokeServiceAccountToken(ctx, org.ID, first.ID, token.ID), sql.ErrNoRows)
	_, err = store.AuthenticateServiceToken(ctx, key, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = store.AuthenticateServiceToken(ctx, key, -1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	tokens, err := store.ServiceAccountTokens(ctx, org.ID, second.ID)
	require.NoError(t, err)
	require.Zero(t, tokens[0].LastUsed, "failed authentication must not mark a token as used")
	principal, err := store.AuthenticateServiceToken(ctx, key, org.ID)
	require.NoError(t, err)
	require.Equal(t, RoleEditor, principal.Role)
	require.False(t, principal.IsServerAdmin)
	second, err = store.UpdateServiceAccount(ctx, org.ID, second.ID, second.Version, ServiceAccountUpdate{Role: serviceValue(RoleNone), Name: serviceValue("read only agent")})
	require.NoError(t, err)
	principal, err = store.AuthenticateServiceToken(ctx, key, org.ID)
	require.NoError(t, err)
	require.Equal(t, RoleNone, principal.Role)
	require.Equal(t, "read only agent", principal.Name)
	require.Equal(t, second.Login, principal.Login)
	second, err = store.UpdateServiceAccount(ctx, org.ID, second.ID, second.Version, ServiceAccountUpdate{Disabled: serviceValue(true)})
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, org.ID)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, _, err = store.CreateServiceAccountToken(ctx, org.ID, second.ID, "while disabled", 0)
	require.ErrorIs(t, err, ErrForbidden)
	second, err = store.UpdateServiceAccount(ctx, org.ID, second.ID, second.Version, ServiceAccountUpdate{Disabled: serviceValue(false)})
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, 0)
	require.NoError(t, err)
	org, err = store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, 0)
	require.ErrorIs(t, err, ErrUnauthenticated)
	org, err = store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationActive)
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, org.ID)
	require.NoError(t, err)
	require.NoError(t, store.RevokeServiceAccountToken(ctx, org.ID, second.ID, token.ID))
	require.NoError(t, store.RevokeServiceAccountToken(ctx, org.ID, second.ID, token.ID), "revocation is idempotent")
	_, err = store.AuthenticateServiceToken(ctx, key, 0)
	require.ErrorIs(t, err, ErrUnauthenticated)
	require.NoError(t, store.DeleteServiceAccount(ctx, org.ID, second.ID, second.Version))
	var count int
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM service_account_tokens WHERE account_id=?`, second.ID).Scan(&count))
	require.Zero(t, count, "deletion must atomically cascade credentials")
	replacement, err := store.CreateServiceAccount(ctx, org.ID, ServiceAccountInput{Name: second.Name, Role: RoleAdmin})
	require.NoError(t, err)
	require.Greater(t, replacement.ID, second.ID)
	_, err = store.AuthenticateServiceToken(ctx, key, 0)
	require.ErrorIs(t, err, ErrUnauthenticated, "recreating the display name cannot restore a deleted credential")
}

func TestServiceAccountCASNamesExpiryAndSearch(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	first, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Alpha"})
	require.NoError(t, err)
	second, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Beta%"})
	require.NoError(t, err)
	_, err = store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: " ALPHA "})
	require.ErrorIs(t, err, ErrServiceAccountExists)
	_, err = store.CreateServiceAccount(ctx, org.ID, ServiceAccountInput{Name: "Alpha"})
	require.NoError(t, err)
	_, err = store.UpdateServiceAccount(ctx, 1, first.ID, 0, ServiceAccountUpdate{Name: serviceValue("stale")})
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = store.UpdateServiceAccount(ctx, 1, first.ID, first.Version, ServiceAccountUpdate{Name: serviceValue(second.Name)})
	require.ErrorIs(t, err, ErrServiceAccountExists)
	unchanged, err := store.ServiceAccount(ctx, 1, first.ID)
	require.NoError(t, err)
	require.Equal(t, first, unchanged)
	first, err = store.UpdateServiceAccount(ctx, 1, first.ID, first.Version, ServiceAccountUpdate{Role: serviceValue(RoleEditor)})
	require.NoError(t, err)
	require.EqualValues(t, 2, first.Version)
	require.Greater(t, first.UpdatedAt, first.CreatedAt)
	require.ErrorIs(t, store.DeleteServiceAccount(ctx, 1, first.ID, first.Version-1), ErrVersionConflict)
	_, err = store.UpdateServiceAccount(ctx, org.ID, first.ID, first.Version, ServiceAccountUpdate{Name: serviceValue("foreign")})
	require.ErrorIs(t, err, sql.ErrNoRows)
	for _, invalid := range []ServiceAccountInput{{Name: ""}, {Name: "line\nbreak"}, {Name: strings.Repeat("界", 86)}, {Name: "valid", Role: "SuperAdmin"}} {
		_, err = store.CreateServiceAccount(ctx, 1, invalid)
		require.Error(t, err)
	}
	_, _, err = store.CreateServiceAccountToken(ctx, 1, first.ID, "negative", -1)
	require.ErrorIs(t, err, ErrServiceTokenExpiration)
	_, _, err = store.CreateServiceAccountToken(ctx, 1, first.ID, "overflow", math.MaxInt64)
	require.ErrorIs(t, err, ErrServiceTokenExpiration)
	token, key, err := store.CreateServiceAccountToken(ctx, 1, first.ID, "One", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1000, token.ExpiresAt-token.CreatedAt)
	_, noKey, err := store.CreateServiceAccountToken(ctx, 1, second.ID, "one", 0)
	require.ErrorIs(t, err, ErrServiceTokenExists)
	require.Empty(t, noKey)
	_, err = store.db.Exec(`UPDATE service_account_tokens SET expires_at=? WHERE id=?`, time.Now().UnixMilli()-1, token.ID)
	require.NoError(t, err)
	_, err = store.AuthenticateServiceToken(ctx, key, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	search, err := store.SearchServiceAccounts(ctx, 1, ServiceAccountSearch{Page: 2, PerPage: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, search.TotalCount)
	require.Equal(t, []ServiceAccount{second}, search.ServiceAccounts)
	search, err = store.SearchServiceAccounts(ctx, 1, ServiceAccountSearch{Query: "%"})
	require.NoError(t, err)
	require.Equal(t, []ServiceAccount{second}, search.ServiceAccounts, "search treats SQL wildcard characters literally")
	search, err = store.SearchServiceAccounts(ctx, 1, ServiceAccountSearch{Query: "ALP", ExpiredTokens: true, Disabled: serviceValue(false)})
	require.NoError(t, err)
	require.Equal(t, []ServiceAccount{first}, search.ServiceAccounts)
	_, err = store.SearchServiceAccounts(ctx, 1, ServiceAccountSearch{Page: math.MaxInt, PerPage: 1000})
	require.Error(t, err)
	_, err = store.SearchServiceAccounts(ctx, 1, ServiceAccountSearch{Page: -1})
	require.Error(t, err)
	_, err = store.ServiceAccountTokens(ctx, 1, 987654321)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestServiceAccountConcurrentCASAndTokenLimit(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	account, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Concurrent"})
	require.NoError(t, err)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.UpdateServiceAccount(ctx, 1, account.ID, account.Version, ServiceAccountUpdate{Role: serviceValue(RoleEditor)})
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	writes := 0
	for err := range errorsFound {
		if err == nil {
			writes++
		} else {
			require.ErrorIs(t, err, ErrVersionConflict)
		}
	}
	require.Equal(t, 1, writes)
	for i := 0; i < maxServiceAccountTokens-1; i++ {
		_, _, err := store.CreateServiceAccountToken(ctx, 1, account.ID, "Token "+strconv.Itoa(i), 0)
		require.NoError(t, err)
	}
	errorsFound = make(chan error, 8)
	for i := range 8 {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, _, err := store.CreateServiceAccountToken(ctx, 1, account.ID, "Concurrent token "+strconv.Itoa(i), 0)
			errorsFound <- err
		}(i)
	}
	wait.Wait()
	close(errorsFound)
	writes = 0
	for err := range errorsFound {
		if err == nil {
			writes++
		} else {
			require.ErrorIs(t, err, ErrServiceTokenLimit)
		}
	}
	require.Equal(t, 1, writes)
	tokens, err := store.ServiceAccountTokens(ctx, 1, account.ID)
	require.NoError(t, err)
	require.Len(t, tokens, maxServiceAccountTokens)
}
