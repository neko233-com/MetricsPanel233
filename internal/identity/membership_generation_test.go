package identity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMembershipGenerationSurvivesCleanupAndRestart(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "identity.db")
	catalog, err := Open(catalogPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	_, err = catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "generation-password-233"})
	require.NoError(t, err)
	user, err := catalog.CreateUser(ctx, UserInput{Login: "reader", Password: "generation-password-233", OrgID: 1, Role: RoleViewer})
	require.NoError(t, err)
	first, err := catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Generation)
	changed, err := catalog.ChangeOrganizationUserRole(ctx, 1, user.ID, first.Version, RoleEditor)
	require.NoError(t, err)
	require.Equal(t, first.Generation, changed.Generation, "ordinary role changes must preserve explicit grants")
	removed, err := catalog.RemoveOrganizationUser(ctx, 1, user.ID, changed.Version)
	require.NoError(t, err)
	require.Equal(t, first.Generation, removed.Generation)
	_, err = catalog.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.ErrorIs(t, err, ErrMembershipCleanup)
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version))
	require.NoError(t, catalog.Close())
	catalog, err = Open(catalogPath)
	require.NoError(t, err)
	second, err := catalog.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Generation)
	require.Greater(t, second.Version, removed.Version, "content CAS revisions must never repeat after rejoin")
	_, err = catalog.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.ErrorIs(t, err, ErrAccountExists)
	removed, err = catalog.RemoveOrganizationUser(ctx, 1, user.ID, second.Version)
	require.NoError(t, err)
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version))
	third, err := catalog.AddOrganizationUser(ctx, 1, user.ID, RoleNone)
	require.NoError(t, err)
	require.Equal(t, int64(3), third.Generation, "a rejected duplicate addition must not consume another generation")
}

func TestMembershipGenerationSchemaFourMigrationAndOverflow(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "identity.db")
	catalog, err := Open(catalogPath)
	require.NoError(t, err)
	_, err = catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "generation-password-233"})
	require.NoError(t, err)
	user, err := catalog.CreateUser(ctx, UserInput{Login: "reader", Password: "generation-password-233", OrgID: 1, Role: RoleViewer})
	require.NoError(t, err)
	before, err := catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	_, err = catalog.db.Exec(`DROP TABLE membership_generations;
ALTER TABLE organization_memberships DROP COLUMN generation;
UPDATE identity_schema SET version=4 WHERE id=1;`)
	require.NoError(t, err)
	require.NoError(t, catalog.Close())
	catalog, err = Open(catalogPath)
	require.NoError(t, err)
	defer catalog.Close()
	migrated, err := catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), migrated.Generation)
	require.Equal(t, before.Version, migrated.Version)
	require.Equal(t, before.Role, migrated.Role)
	require.Equal(t, before.CreatedAt, migrated.CreatedAt)
	removed, err := catalog.RemoveOrganizationUser(ctx, 1, user.ID, migrated.Version)
	require.NoError(t, err)
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version))
	_, err = catalog.db.Exec(`UPDATE membership_generations SET generation=9223372036854775807 WHERE org_id=1 AND user_id=?`, user.ID)
	require.NoError(t, err)
	_, err = catalog.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.ErrorIs(t, err, ErrVersionConflict, "generation overflow must not coerce the identity into a floating-point value or reuse it")
	var count int
	require.NoError(t, catalog.db.QueryRow(`SELECT count(*) FROM organization_memberships WHERE org_id=1 AND user_id=?`, user.ID).Scan(&count))
	require.Zero(t, count)
}
