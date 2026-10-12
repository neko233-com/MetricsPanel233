package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Build literal legacy core tables rather than creating a current catalog and
// merely lowering its version number. Schema 3 has no rotation/generation; 5
// has both but neither user UID nor session descriptive metadata.
func legacyMetadataCatalog(t *testing.T, schema int) (string, string, []byte) {
	t.Helper()
	location := filepath.Join(t.TempDir(), "identity.db")
	db, err := sql.Open("sqlite", location)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE identity_schema(id INTEGER PRIMARY KEY,version INTEGER NOT NULL);
CREATE TABLE organizations(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,name_key TEXT NOT NULL UNIQUE,state TEXT NOT NULL,version INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
INSERT INTO organizations VALUES(1,'Legacy main','legacy main','active',7,1000,2000);
CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT,login TEXT NOT NULL,name TEXT NOT NULL,email TEXT NOT NULL,current_org_id INTEGER NOT NULL REFERENCES organizations(id),is_server_admin INTEGER NOT NULL,disabled INTEGER NOT NULL,version INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
INSERT INTO users VALUES(7,'legacy','Legacy human','legacy@example.test',1,1,0,17,1000,2000);
INSERT INTO users VALUES(9,'disabled','Disabled human','',1,0,1,19,1200,2200);
CREATE TABLE user_identifiers(identifier TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE);
INSERT INTO user_identifiers VALUES('legacy',7),('legacy@example.test',7),('disabled',9);
CREATE TABLE password_credentials(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,format TEXT NOT NULL,salt BLOB NOT NULL,hash BLOB NOT NULL,version INTEGER NOT NULL);`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO identity_schema VALUES(1,?)`, schema)
	require.NoError(t, err)
	generationColumn, rotationColumns := "", ""
	if schema == 5 {
		generationColumn = ",generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0)"
		rotationColumns = ",previous_token_hash BLOB,rotated_at INTEGER NOT NULL DEFAULT 0,token_seen INTEGER NOT NULL DEFAULT 1"
	}
	_, err = db.Exec(`CREATE TABLE organization_memberships(org_id INTEGER NOT NULL REFERENCES organizations(id),user_id INTEGER NOT NULL REFERENCES users(id),role TEXT NOT NULL,state TEXT NOT NULL,version INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL` + generationColumn + `,PRIMARY KEY(org_id,user_id));
INSERT INTO organization_memberships(org_id,user_id,role,state,version,created_at,updated_at) VALUES(1,7,'Editor','active',23,1000,2000),(1,9,'Viewer','active',3,1200,2200);
CREATE TABLE browser_sessions(id INTEGER PRIMARY KEY AUTOINCREMENT,token_hash BLOB NOT NULL UNIQUE,user_id INTEGER NOT NULL REFERENCES users(id),current_org_id INTEGER NOT NULL REFERENCES organizations(id),created_at INTEGER NOT NULL,last_seen INTEGER NOT NULL,expires_at INTEGER NOT NULL,idle_timeout_ms INTEGER NOT NULL,revoked_at INTEGER NOT NULL DEFAULT 0` + rotationColumns + `);`)
	require.NoError(t, err)
	if schema == 5 {
		_, err = db.Exec(`UPDATE organization_memberships SET generation=2 WHERE user_id=7;
CREATE TABLE membership_generations(org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,generation INTEGER NOT NULL,last_version INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(org_id,user_id));
INSERT INTO membership_generations VALUES(1,7,2,23),(1,9,1,3);`)
		require.NoError(t, err)
	}
	salt, hash, err := makePassword(context.Background(), "legacy-password-233")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO password_credentials VALUES(7,?,?,?,8)`, passwordFormat, salt, hash)
	require.NoError(t, err)
	key, digest, err := newOpaqueCredential(sessionCredentialPrefix)
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	_, err = db.Exec(`INSERT INTO browser_sessions(id,token_hash,user_id,current_org_id,created_at,last_seen,expires_at,idle_timeout_ms) VALUES(21,?,7,1,?,?,?,?)`, digest, now-1000, now, now+time.Hour.Milliseconds(), time.Hour.Milliseconds())
	require.NoError(t, err)
	if schema == 5 {
		_, err = db.Exec(`UPDATE browser_sessions SET previous_token_hash=token_hash,rotated_at=created_at,token_seen=0 WHERE id=21`)
		require.NoError(t, err)
	}
	return location, key, hash
}

func TestIdentityMetadataLegacySchemaMigrationAndRestart(t *testing.T) {
	for _, schema := range []int{3, 5} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			ctx := context.Background()
			location, key, originalHash := legacyMetadataCatalog(t, schema)
			store, err := Open(location)
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			user, err := store.User(ctx, 7)
			require.NoError(t, err)
			require.True(t, validUserUID(user.UID))
			require.EqualValues(t, 7, user.ID)
			require.Equal(t, "legacy", user.Login)
			require.Equal(t, "Legacy human", user.Name)
			require.Equal(t, "legacy@example.test", user.Email)
			require.True(t, user.IsServerAdmin)
			require.False(t, user.Disabled)
			require.EqualValues(t, 17, user.Version)
			require.EqualValues(t, 1000, user.CreatedAt)
			require.EqualValues(t, 2000, user.UpdatedAt)
			disabled, err := store.User(ctx, 9)
			require.NoError(t, err)
			require.True(t, disabled.Disabled)
			require.NotEqual(t, user.UID, disabled.UID)
			org, err := store.Organization(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, "Legacy main", org.Name)
			require.EqualValues(t, 7, org.Version)
			var hash []byte
			var credentialVersion int
			require.NoError(t, store.db.QueryRow(`SELECT hash,version FROM password_credentials WHERE user_id=7`).Scan(&hash, &credentialVersion))
			require.Equal(t, originalHash, hash)
			require.Equal(t, 8, credentialVersion)
			membership, err := store.Membership(ctx, 1, 7)
			require.NoError(t, err)
			require.EqualValues(t, 23, membership.Version)
			if schema == 5 {
				require.EqualValues(t, 2, membership.Generation)
			}
			sessions, err := store.Sessions(ctx, 7)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.EqualValues(t, 21, sessions[0].ID)
			require.Empty(t, sessions[0].ClientIP)
			require.Empty(t, sessions[0].UserAgent)
			require.Empty(t, sessions[0].AuthModule, "migration cannot invent unknown device/auth-provider history")
			require.Equal(t, schema == 3, sessions[0].TokenSeen)
			var version int
			require.NoError(t, store.db.QueryRow(`SELECT version FROM identity_schema WHERE id=1`).Scan(&version))
			require.Equal(t, 6, version)
			principal, err := store.AuthenticateSession(ctx, key, 1)
			require.NoError(t, err)
			require.EqualValues(t, 7, principal.ID)
			require.True(t, principal.IsServerAdmin)
			_, err = store.AuthenticatePassword(ctx, user.UID, "legacy-password-233", 1)
			require.ErrorIs(t, err, ErrUnauthenticated, "a lookup UID is not automatically a login credential")
			created := adminTestUser(t, store, "new-human", "", "New", 1, false)
			require.Greater(t, created.ID, disabled.ID)
			require.NotEqual(t, user.UID, created.UID)
			require.NoError(t, store.Close())
			store, err = Open(location)
			require.NoError(t, err)
			restored, err := store.UserByUID(ctx, user.UID)
			require.NoError(t, err)
			require.Equal(t, user, restored)
			serialized, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Contains(t, string(serialized), `"uid":"`+user.UID+`"`)
			require.NotContains(t, string(serialized), key)
			require.NotContains(t, string(serialized), "legacy-password-233")
			require.NotContains(t, string(serialized), `"hash"`)
		})
	}
}

func TestUserUIDUniqueImmutableAndProfileStable(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	user := adminTestUser(t, store, "alice", "", "Alice", 1, false)
	updated, err := store.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Login: serviceValue("renamed"), Name: serviceValue("Updated")})
	require.NoError(t, err)
	require.Equal(t, user.UID, updated.UID)
	for _, uid := range []string{"", "bad/uid", user.UID} {
		_, err := store.db.Exec(`INSERT INTO users(uid,login,name,email,current_org_id,is_server_admin,disabled,version,created_at,updated_at) VALUES(?,'collision','Collision','',1,0,0,1,1,1)`, uid)
		require.Error(t, err)
	}
	_, err = store.db.Exec(`UPDATE users SET uid='another_UID' WHERE id=?`, user.ID)
	require.Error(t, err)
	current, err := store.UserByUID(ctx, user.UID)
	require.NoError(t, err)
	require.Equal(t, updated, current)
	_, err = store.UserByUID(ctx, "bad/uid")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestIdentityMetadataMigrationFailureRollsBack(t *testing.T) {
	for _, failure := range []string{"duplicate", "invalid", "mid-backfill"} {
		t.Run(failure, func(t *testing.T) {
			location, _, originalHash := legacyMetadataCatalog(t, 5)
			db, err := sql.Open("sqlite", location)
			require.NoError(t, err)
			defer db.Close()
			if failure != "mid-backfill" {
				_, err = db.Exec(`ALTER TABLE users ADD COLUMN uid TEXT NOT NULL DEFAULT ''`)
				require.NoError(t, err)
			}
			switch failure {
			case "duplicate":
				_, err = db.Exec(`UPDATE users SET uid='duplicate_UID'`)
			case "invalid":
				_, err = db.Exec(`UPDATE users SET uid='invalid/UID' WHERE id=9`)
			case "mid-backfill":
				_, err = db.Exec(`CREATE TRIGGER reject_uid_backfill BEFORE UPDATE ON users WHEN OLD.id=9 BEGIN SELECT RAISE(ABORT,'injected UID backfill failure'); END`)
			}
			require.NoError(t, err)
			store, err := Open(location)
			require.Error(t, err)
			require.Nil(t, store)
			var version int
			require.NoError(t, db.QueryRow(`SELECT version FROM identity_schema`).Scan(&version))
			require.Equal(t, 5, version)
			var hash []byte
			require.NoError(t, db.QueryRow(`SELECT hash FROM password_credentials WHERE user_id=7`).Scan(&hash))
			require.Equal(t, originalHash, hash)
			var indexCount int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='users_uid'`).Scan(&indexCount))
			require.Zero(t, indexCount)
			if failure == "mid-backfill" {
				var uidColumns int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('users') WHERE name='uid'`).Scan(&uidColumns))
				require.Zero(t, uidColumns, "the UID column and earlier assignment roll back with the failing later item")
			}
			var metadataColumns int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('browser_sessions') WHERE name IN ('client_ip','user_agent','auth_module')`).Scan(&metadataColumns))
			require.Zero(t, metadataColumns)
		})
	}
}
