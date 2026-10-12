package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/stretchr/testify/require"
)

func TestOrganizationCatalogDurableInitializationAndState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity.db")
	catalog, err := identity.Open(path)
	require.NoError(t, err)
	primary, err := catalog.Organization(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, identity.Active, primary.State)
	require.EqualValues(t, 1, primary.Version)
	_, err = catalog.SetOrganizationState(ctx, 1, primary.Version, identity.Disabled)
	require.ErrorIs(t, err, identity.ErrPrimaryOrganization)
	created, err := catalog.BeginOrganization(ctx, "  生产指标  ")
	require.NoError(t, err)
	require.Equal(t, "生产指标", created.Name)
	require.EqualValues(t, 2, created.ID)
	require.Equal(t, identity.Creating, created.State)
	_, err = catalog.SetOrganizationState(ctx, created.ID, created.Version, identity.Active)
	require.ErrorIs(t, err, identity.ErrInvalidOrganizationState, "initialization needs the dedicated activation step")
	require.NoError(t, catalog.Close())
	// A crash between registration and storage initialization keeps its recovery
	// marker; reopening must not silently activate the organization or reuse IDs.
	catalog, err = identity.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { catalog.Close() })
	recovered, err := catalog.Organization(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created, recovered)
	active, err := catalog.ActivateOrganization(ctx, created.ID, created.Version)
	require.NoError(t, err)
	require.Equal(t, identity.Active, active.State)
	require.EqualValues(t, 2, active.Version)
	require.Greater(t, active.UpdatedAt, active.CreatedAt)
	_, err = catalog.ActivateOrganization(ctx, created.ID, created.Version)
	require.ErrorIs(t, err, identity.ErrVersionConflict)
	disabled, err := catalog.SetOrganizationState(ctx, active.ID, active.Version, identity.Disabled)
	require.NoError(t, err)
	renamed, err := catalog.RenameOrganization(ctx, disabled.ID, disabled.Version, "业务监控")
	require.NoError(t, err)
	require.Equal(t, identity.Disabled, renamed.State)
	active, err = catalog.SetOrganizationState(ctx, renamed.ID, renamed.Version, identity.Active)
	require.NoError(t, err)
	deleting, err := catalog.SetOrganizationState(ctx, active.ID, active.Version, identity.Deleting)
	require.NoError(t, err)
	_, err = catalog.SetOrganizationState(ctx, deleting.ID, deleting.Version, identity.Active)
	require.ErrorIs(t, err, identity.ErrInvalidOrganizationState)
	_, err = catalog.RenameOrganization(ctx, deleting.ID, deleting.Version, "Resurrect")
	require.ErrorIs(t, err, identity.ErrInvalidOrganizationState)
	orgs, err := catalog.Organizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 2)
	require.Equal(t, deleting, orgs[1])
	_, err = catalog.Organization(ctx, 99)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOrganizationCatalogNamesAndConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { catalog.Close() })
	for _, name := range []string{"", " \t ", "name\x00bad", "bad\nname", string([]byte{0xff})} {
		_, err := catalog.BeginOrganization(ctx, name)
		require.Error(t, err)
	}
	created, err := catalog.BeginOrganization(ctx, "Operations")
	require.NoError(t, err)
	_, err = catalog.BeginOrganization(ctx, " operations ")
	require.ErrorIs(t, err, identity.ErrOrganizationName)
	active, err := catalog.ActivateOrganization(ctx, created.ID, created.Version)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, name := range []string{"Team A", "Team B"} {
		workers.Add(1)
		go func(name string) {
			defer workers.Done()
			<-start
			_, err := catalog.RenameOrganization(ctx, active.ID, active.Version, name)
			results <- err
		}(name)
	}
	close(start)
	workers.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, identity.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflicts)
	current, err := catalog.Organization(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, active.Version+1, current.Version)
	_, err = catalog.RenameOrganization(ctx, current.ID, current.Version, "Main Org.")
	require.ErrorIs(t, err, identity.ErrOrganizationName)
	unchanged, err := catalog.Organization(ctx, current.ID)
	require.NoError(t, err)
	require.Equal(t, current, unchanged)
}

func TestIdentityCatalogRefusesExistingBusinessDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE dashboards(id TEXT PRIMARY KEY,payload TEXT NOT NULL); INSERT INTO dashboards VALUES('existing','preserve');`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = identity.Open(path)
	require.ErrorContains(t, err, "must not reuse")
	db, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload FROM dashboards WHERE id='existing'`).Scan(&payload))
	require.Equal(t, "preserve", payload)
	var identityTables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='identity_schema'`).Scan(&identityTables))
	require.Zero(t, identityTables)
}

func TestIdentityCatalogAdditiveMigrationPreservesOrganizationRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity-v1.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE identity_schema(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL);
INSERT INTO identity_schema VALUES(1,1);
CREATE TABLE organizations(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,name_key TEXT NOT NULL UNIQUE,state TEXT NOT NULL,version INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
INSERT INTO organizations VALUES(1,'Existing workspace','existing workspace','active',7,1000,2000),(8,'Recover creation','recover creation','creating',3,1500,1900);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	catalog, err := identity.Open(path)
	require.NoError(t, err)
	defer catalog.Close()
	org, err := catalog.Organization(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, identity.Organization{ID: 1, Name: "Existing workspace", State: identity.Active, Version: 7, CreatedAt: 1000, UpdatedAt: 2000}, org)
	recovering, err := catalog.Organization(ctx, 8)
	require.NoError(t, err)
	require.Equal(t, identity.Creating, recovering.State)
	require.EqualValues(t, 3, recovering.Version)
	users, err := catalog.Users(ctx, 10, 0)
	require.NoError(t, err)
	require.Empty(t, users, "migration must not create a default account or password")
	created, err := catalog.BeginOrganization(ctx, "After migration")
	require.NoError(t, err)
	require.EqualValues(t, 9, created.ID, "registered IDs and recovery state survive additive migration")
}
