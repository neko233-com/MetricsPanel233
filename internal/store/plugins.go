package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func loadSecretKey(path string) ([]byte, error) {
	keyPath := filepath.Join(filepath.Dir(path), "secrets.key")
	key, err := os.ReadFile(keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("secrets.key must contain exactly 32 bytes")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("another process is initializing secrets.key; retry opening this database")
	}
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(key); err != nil {
		f.Close()
		os.Remove(keyPath)
		return nil, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(keyPath)
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}
func (s *Store) seal(uid string, secrets map[string]string) ([]byte, error) {
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	data, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, []byte(uid)), nil
}
func (s *Store) openSecrets(uid string, sealed []byte) (map[string]string, error) {
	block, err := aes.NewCipher(s.secretKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("invalid encrypted datasource secrets")
	}
	data, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(uid))
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	err = json.Unmarshal(data, &secrets)
	return secrets, err
}
func (s *Store) Plugins(ctx context.Context) ([]model.Plugin, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT payload FROM plugins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Plugin{}
	for rows.Next() {
		var payload string
		var p model.Plugin
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Plugin(ctx context.Context, id string) (model.Plugin, error) {
	var p model.Plugin
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload FROM plugins WHERE id=?`, id).Scan(&payload); err != nil {
		return p, err
	}
	err := json.Unmarshal([]byte(payload), &p)
	return p, err
}
func (s *Store) SavePlugin(ctx context.Context, p model.Plugin) error {
	if !model.PluginID.MatchString(p.ID) {
		return errors.New("invalid plugin ID")
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO plugins(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, p.ID, string(payload))
	return err
}
func (s *Store) SavePluginPackage(ctx context.Context, plugins []model.Plugin) error {
	if len(plugins) == 0 {
		return errors.New("empty plugin package")
	}
	root := plugins[0].PackageID
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM plugins`)
	if err != nil {
		return err
	}
	old := []model.Plugin{}
	for rows.Next() {
		var payload string
		var p model.Plugin
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			rows.Close()
			return err
		}
		if p.PackageID == root {
			old = append(old, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	incoming := map[string]bool{}
	for _, p := range plugins {
		incoming[p.ID] = true
	}
	for _, p := range old {
		if incoming[p.ID] {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM datasources WHERE type=?`, p.ID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return errors.New("package update would remove a plugin used by a datasource")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE id=?`, p.ID); err != nil {
			return err
		}
	}
	for _, p := range plugins {
		if !model.PluginID.MatchString(p.ID) || p.PackageID != root {
			return errors.New("invalid plugin package")
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugins(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, p.ID, string(payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) DeletePluginPackage(ctx context.Context, id string) error {
	p, err := s.Plugin(ctx, id)
	if err != nil {
		return err
	}
	if p.PackageID != id {
		return errors.New("uninstall the root package rather than a bundled child plugin")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM plugins`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var payload string
		var p model.Plugin
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			rows.Close()
			return err
		}
		if p.PackageID == id {
			ids = append(ids, p.ID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, uid := range ids {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM datasources WHERE type=?`, uid).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return errors.New("remove this package's datasources before uninstalling")
		}
	}
	for _, uid := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE id=?`, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM datasources WHERE type=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("delete this plugin's data sources before uninstalling")
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM plugins WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanDataSource(row interface{ Scan(...any) error }) (model.DataSource, error) {
	var d model.DataSource
	var payload string
	var id int64
	if err := row.Scan(&id, &payload); err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return d, err
	}
	d.ID = id
	return d, nil
}
func (s *Store) DataSources(ctx context.Context) ([]model.DataSource, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,payload FROM datasources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DataSource{}
	for rows.Next() {
		d, err := scanDataSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) DataSource(ctx context.Context, uid string) (model.DataSource, error) {
	return scanDataSource(s.DB.QueryRowContext(ctx, `SELECT id,payload FROM datasources WHERE uid=?`, uid))
}
func (s *Store) DataSourceSecrets(ctx context.Context, uid string) (map[string]string, error) {
	var sealed []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT secrets FROM datasources WHERE uid=?`, uid).Scan(&sealed); err != nil {
		return nil, err
	}
	return s.openSecrets(uid, sealed)
}

var ErrDataSourceConflict = errors.New("datasource version changed; read the current version before updating")

func (s *Store) SaveDataSource(ctx context.Context, input model.DataSourceInput) (model.DataSource, error) {
	input.Defaults()
	if err := input.Validate(); err != nil {
		return model.DataSource{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.DataSource{}, err
	}
	defer tx.Rollback()
	if input.Type != "prometheus" {
		var payload string
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM plugins WHERE id=?`, input.Type).Scan(&payload); err != nil {
			return model.DataSource{}, errors.New("datasource plugin is not installed")
		}
		var p model.Plugin
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return model.DataSource{}, err
		}
		if p.Type != "datasource" || !p.Enabled {
			return model.DataSource{}, errors.New("datasource plugin is not enabled")
		}
	}
	previous, err := scanDataSource(tx.QueryRowContext(ctx, `SELECT id,payload FROM datasources WHERE uid=?`, input.UID))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.DataSource{}, err
	}
	var sameName int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM datasources WHERE json_extract(payload,'$.name')=? AND uid<>?`, input.Name, input.UID).Scan(&sameName); err != nil {
		return model.DataSource{}, err
	}
	if sameName > 0 {
		return model.DataSource{}, errors.New("datasource name is already in use")
	}
	if exists && (input.Version == 0 || input.Version != previous.Version) || !exists && input.Version != 0 {
		return model.DataSource{}, ErrDataSourceConflict
	}
	secrets := map[string]string{}
	if exists {
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT secrets FROM datasources WHERE uid=?`, input.UID).Scan(&sealed); err != nil {
			return model.DataSource{}, err
		}
		secrets, err = s.openSecrets(input.UID, sealed)
		if err != nil {
			return model.DataSource{}, err
		}
	}
	for key, keep := range input.SecureJSONFields {
		if !keep {
			delete(secrets, key)
		}
	}
	for key, value := range input.SecureJSONData {
		if value == "" {
			delete(secrets, key)
		} else {
			secrets[key] = value
		}
	}
	input.SecureJSONFields = map[string]bool{}
	if len(secrets) > 32 {
		return model.DataSource{}, errors.New("at most 32 stored secret fields")
	}
	for key := range secrets {
		input.SecureJSONFields[key] = true
	}
	sealed, err := s.seal(input.UID, secrets)
	if err != nil {
		return model.DataSource{}, err
	}
	d := input.DataSource
	d.Version++
	d.UpdatedAt = time.Now().UnixMilli()
	d.ID = previous.ID
	if !exists {
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),1)+1 FROM sqlite_sequence WHERE name='datasources'`).Scan(&d.ID); err != nil {
			return d, err
		}
	}
	if d.IsDefault {
		rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM datasources`)
		if err != nil {
			return d, err
		}
		others := []model.DataSource{}
		for rows.Next() {
			other, err := scanDataSource(rows)
			if err != nil {
				rows.Close()
				return d, err
			}
			others = append(others, other)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return d, err
		}
		for _, other := range others {
			if other.IsDefault && other.UID != d.UID {
				other.IsDefault = false
				other.Version++
				other.UpdatedAt = d.UpdatedAt
				data, _ := json.Marshal(other)
				if _, err = tx.ExecContext(ctx, `UPDATE datasources SET payload=? WHERE uid=?`, string(data), other.UID); err != nil {
					return d, err
				}
			}
		}
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO datasources(id,uid,type,payload,secrets) VALUES(?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET type=excluded.type,payload=excluded.payload,secrets=excluded.secrets`, d.ID, d.UID, d.Type, string(payload), sealed)
	if err != nil {
		return d, err
	}
	if !exists {
		d.ID, err = result.LastInsertId()
		if err != nil {
			return d, err
		}
	}
	if err = tx.Commit(); err != nil {
		return d, err
	}
	return d, nil
}
func (s *Store) DeleteDataSource(ctx context.Context, uid string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM datasources WHERE uid=?`, uid)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) DataSourceByID(ctx context.Context, id int64) (model.DataSource, error) {
	if id < 1 {
		return model.DataSource{}, fmt.Errorf("invalid datasource ID")
	}
	return scanDataSource(s.DB.QueryRowContext(ctx, `SELECT id,payload FROM datasources WHERE id=?`, id))
}
