package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/require"
)

func organizationStore(t *testing.T, orgID int64) *Store {
	t.Helper()
	s, err := OpenForOrganization(filepath.Join(t.TempDir(), "control.db"), orgID)
	require.NoError(t, err)
	t.Cleanup(func() { s.DB.Close() })
	return s
}

func TestOrganizationStoreImmutableOwnerAndLegacyPath(t *testing.T) {
	ctx := context.Background()
	s := organizationStore(t, 1)
	d, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "legacy", Name: "Legacy", Type: "mysql"}, SecureJSONData: map[string]string{"password": "original"}})
	require.NoError(t, err)
	keyPath := filepath.Join(filepath.Dir(s.Path), "secrets.key")
	key, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	// Remove only the new marker to model an existing unpartitioned database.
	_, err = s.DB.Exec(`DROP TABLE store_identity`)
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	_, err = OpenForOrganization(s.Path, 2)
	require.ErrorContains(t, err, "legacy database")
	legacy, err := Open(s.Path)
	require.NoError(t, err)
	defer legacy.DB.Close()
	require.Equal(t, int64(1), legacy.OrganizationID())
	require.Equal(t, s.Path, legacy.Path)
	loaded, err := legacy.DataSource(ctx, d.UID)
	require.NoError(t, err)
	require.Equal(t, d, loaded)
	retained, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.Equal(t, key, retained)
	_, err = OpenForOrganization(s.Path, 2)
	require.ErrorContains(t, err, "owner does not match")
	other := organizationStore(t, 2)
	_, err = Open(other.Path)
	require.ErrorContains(t, err, "owner does not match")
	_, err = other.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "foreign", Name: "Foreign", Type: "mysql", OrgID: 1}})
	require.ErrorContains(t, err, "physical store")
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = Open(catalog.Path())
	require.ErrorContains(t, err, "identity catalog")
}

func TestOrganizationSecretsBindOrganizationKindAndIDWithSharedKey(t *testing.T) {
	primary, other := organizationStore(t, 1), organizationStore(t, 2)
	// Equal keys deliberately prove the organization boundary is authenticated
	// by AAD, rather than incidentally relying on two randomly generated keys.
	other.secretKey = bytes.Clone(primary.secretKey)
	sealed, err := primary.seal("same", map[string]string{"password": "org-one"})
	require.NoError(t, err)
	_, err = other.openSecrets("same", sealed)
	require.Error(t, err)
	_, err = primary.openSecrets("app:same", sealed)
	require.Error(t, err)
	_, err = primary.openSecrets("different", sealed)
	require.Error(t, err)
	valid, err := primary.openSecrets("same", sealed)
	require.NoError(t, err)
	require.Equal(t, "org-one", valid["password"])
	_, err = other.SaveDataSource(context.Background(), model.DataSourceInput{DataSource: model.DataSource{UID: "same", Name: "Same", Type: "mysql"}, SecureJSONData: map[string]string{"password": "org-two"}})
	require.NoError(t, err)
	_, err = other.DB.Exec(`UPDATE datasources SET secrets=? WHERE uid='same'`, sealed)
	require.NoError(t, err)
	_, err = other.DataSourceSecrets(context.Background(), "same")
	require.Error(t, err, "cross-organization ciphertext copied into the actual datasource row must fail")
	// A resource whose literal UID resembles the old kind prefix remains distinct.
	app, err := primary.seal("app:same", map[string]string{"password": "app"})
	require.NoError(t, err)
	_, err = primary.openSecrets("same", app)
	require.Error(t, err)
}

func legacySeal(t *testing.T, s *Store, reference string, secrets map[string]string) []byte {
	t.Helper()
	block, err := aes.NewCipher(s.secretKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, gcm.NonceSize())
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	data, err := json.Marshal(secrets)
	require.NoError(t, err)
	return gcm.Seal(nonce, nonce, data, []byte(reference))
}

func TestOrganizationLegacySecretMigrationIsAtomicAndRetainsKey(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[tamper], func(t *testing.T) {
			s := organizationStore(t, 1)
			ctx := context.Background()
			_, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "source", Name: "Source", Type: "mysql"}})
			require.NoError(t, err)
			require.NoError(t, s.SavePlugin(ctx, model.Plugin{ID: "app", PackageID: "app", Type: "app", Enabled: true}))
			_, err = s.SaveAppSettings(ctx, "app", model.AppSettingsInput{})
			require.NoError(t, err)
			_, err = s.DB.Exec(`INSERT INTO targets(id,name,url,interval_seconds,labels,enabled) VALUES(233,'fixture','redis://127.0.0.1:6379',30,'{}',0);
INSERT INTO target_connections(id,kind,username,database_name,tls_mode,secrets) VALUES(233,'redis','','','',X'00');`)
			require.NoError(t, err)
			_, err = s.SaveContactPoint(ctx, model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "receiver", Name: "Receiver", URL: "http://127.0.0.1:17777"}})
			require.NoError(t, err)
			rows := []struct {
				table, column, id, reference string
				sealed                       []byte
			}{
				{"datasources", "uid", "source", "source", nil},
				{"app_settings", "id", "app", "app:app", nil},
				{"target_connections", "id", "233", "target:233", nil},
				{"alert_contact_points", "uid", "receiver", "contact-point:receiver", nil},
			}
			for i := range rows {
				row := &rows[i]
				row.sealed = legacySeal(t, s, row.reference, map[string]string{"password": row.id})
				if tamper && i == len(rows)-1 {
					row.sealed[len(row.sealed)-1] ^= 1
				}
				_, err = s.DB.Exec(`UPDATE `+row.table+` SET secrets=? WHERE `+row.column+`=?`, row.sealed, row.id)
				require.NoError(t, err)
			}
			keyPath := filepath.Join(filepath.Dir(s.Path), "secrets.key")
			key, err := os.ReadFile(keyPath)
			require.NoError(t, err)
			require.NoError(t, s.DB.Close())
			reopened, err := Open(s.Path)
			if tamper {
				require.ErrorContains(t, err, "cannot decrypt alert_contact_points")
				db, err := sql.Open("sqlite", s.Path)
				require.NoError(t, err)
				defer db.Close()
				for _, row := range rows {
					var sealed []byte
					require.NoError(t, db.QueryRow(`SELECT secrets FROM `+row.table+` WHERE `+row.column+`=?`, row.id).Scan(&sealed))
					require.Equal(t, row.sealed, sealed, "failed migration must roll back every ciphertext")
				}
			} else {
				require.NoError(t, err)
				defer reopened.DB.Close()
				for _, row := range rows {
					var sealed []byte
					require.NoError(t, reopened.DB.QueryRow(`SELECT secrets FROM `+row.table+` WHERE `+row.column+`=?`, row.id).Scan(&sealed))
					require.True(t, bytes.HasPrefix(sealed, secretEnvelope))
					data, err := reopened.openSecrets(row.reference, sealed)
					require.NoError(t, err)
					require.Equal(t, row.id, data["password"])
				}
			}
			retained, err := os.ReadFile(keyPath)
			require.NoError(t, err)
			require.Equal(t, key, retained)
		})
	}
}

func TestOrganizationSharedPackagesKeepAppAndDatasourceSettingsLocal(t *testing.T) {
	ctx := context.Background()
	primary, other := organizationStore(t, 1), organizationStore(t, 2)
	app := model.Plugin{ID: "same-app", PackageID: "same-app", Type: "app", Enabled: true}
	child := model.Plugin{ID: "child-source", PackageID: app.ID, Type: "datasource", Enabled: true}
	require.NoError(t, primary.SavePluginPackage(ctx, []model.Plugin{app, child}))
	require.NoError(t, other.BindPluginRegistry(primary))
	require.NoError(t, other.BindPluginRegistry(primary))
	for _, s := range []*Store{primary, other} {
		_, err := s.SaveAppSettings(ctx, app.ID, model.AppSettingsInput{JSONData: []byte(`{"local":true}`), SecureJSONData: map[string]string{"token": s.Path}})
		require.NoError(t, err)
		d, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "same-source", Name: "Same", Type: child.ID}, SecureJSONData: map[string]string{"password": s.Path}})
		require.NoError(t, err)
		require.Equal(t, s.OrganizationID(), d.OrgID)
	}
	disabled := false
	_, err := other.SaveAppSettings(ctx, app.ID, model.AppSettingsInput{Enabled: &disabled})
	require.NoError(t, err)
	p, err := primary.PluginEffective(ctx, child.ID)
	require.NoError(t, err)
	require.True(t, p.Enabled)
	p, err = other.PluginEffective(ctx, child.ID)
	require.NoError(t, err)
	require.False(t, p.Enabled)
	for _, s := range []*Store{primary, other} {
		secrets, err := s.DataSourceSecrets(ctx, "same-source")
		require.NoError(t, err)
		require.Equal(t, s.Path, secrets["password"])
		appSecrets, err := s.AppSecrets(ctx, app.ID)
		require.NoError(t, err)
		require.Equal(t, s.Path, appSecrets["token"])
		settings, err := s.AppSettings(ctx, app.ID)
		require.NoError(t, err)
		require.Equal(t, s.OrganizationID(), settings.OrgID)
	}
	require.ErrorContains(t, other.SavePlugin(ctx, app), "instance operation")
	require.ErrorContains(t, other.DeletePlugin(ctx, child.ID), "instance operation")
	require.NoError(t, other.DB.Close())
	reopened, err := OpenForOrganization(other.Path, 2)
	require.NoError(t, err)
	defer reopened.DB.Close()
	require.NoError(t, reopened.BindPluginRegistry(primary))
	settings, err := reopened.AppSettings(ctx, app.ID)
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	secret, err := reopened.AppSecrets(ctx, app.ID)
	require.NoError(t, err)
	require.Equal(t, other.Path, secret["token"])
}
