package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// BindPluginRegistry shares instance-installed metadata while app settings and
// datasource credentials remain in this organization's database. Call it before
// starting workers or handling requests.
func (s *Store) BindPluginRegistry(primary *Store) error {
	if s.OrganizationID() == 1 || primary == nil || primary.OrganizationID() != 1 || primary.pluginRegistry != nil || s == primary {
		return errors.New("an organization store needs the primary instance plugin registry")
	}
	if s.pluginRegistry == primary {
		return nil
	}
	if s.pluginRegistry != nil {
		return errors.New("an organization plugin registry cannot be rebound")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE app_settings_org(id TEXT PRIMARY KEY,payload TEXT NOT NULL,secrets BLOB NOT NULL);
INSERT INTO app_settings_org SELECT id,payload,secrets FROM app_settings;
DROP TABLE app_settings;
ALTER TABLE app_settings_org RENAME TO app_settings;`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.pluginRegistry = primary
	return nil
}

// migrateOrganizationSecrets changes only the ciphertext envelope and AAD. The
// organization 1 key, resource identifiers, payloads and public secret flags stay
// unchanged. A failed decrypt rolls back the entire migration.
func (s *Store) migrateOrganizationSecrets(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, source := range []struct{ table, column, prefix string }{
		{"datasources", "uid", ""},
		{"app_settings", "id", "app:"},
		{"target_connections", "id", "target:"},
		{"alert_contact_points", "uid", "contact-point:"},
	} {
		rows, err := tx.QueryContext(ctx, `SELECT CAST(`+source.column+` AS TEXT),secrets FROM `+source.table)
		if err != nil {
			return err
		}
		type entry struct {
			id     string
			sealed []byte
		}
		var entries []entry
		for rows.Next() {
			var item entry
			if err := rows.Scan(&item.id, &item.sealed); err != nil {
				rows.Close()
				return err
			}
			entries = append(entries, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range entries {
			secrets, err := s.openSecrets(source.prefix+item.id, item.sealed)
			if err != nil {
				return fmt.Errorf("cannot decrypt %s credentials during organization migration", source.table)
			}
			if bytes.HasPrefix(item.sealed, secretEnvelope) {
				continue
			}
			sealed, err := s.seal(source.prefix+item.id, secrets)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+source.table+` SET secrets=? WHERE `+source.column+`=?`, sealed, item.id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
