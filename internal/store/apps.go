package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrAppConflict = errors.New("app configuration version changed; read the current version before updating")

// PluginEffective also checks a bundled plugin's owning package. Stored child
// flags survive a parent disable/enable without granting access while disabled.
func (s *Store) PluginEffective(ctx context.Context, id string) (model.Plugin, error) {
	p, err := s.Plugin(ctx, id)
	if err != nil {
		return p, err
	}
	if p.PackageID != p.ID {
		parent, err := s.Plugin(ctx, p.PackageID)
		if err != nil {
			return p, err
		}
		p.Enabled = p.Enabled && parent.Enabled
	}
	return p, nil
}

func scanApp(row interface{ Scan(...any) error }) (model.AppSettings, error) {
	var app model.AppSettings
	var payload string
	if err := row.Scan(&payload); err != nil {
		return app, err
	}
	err := json.Unmarshal([]byte(payload), &app)
	return app, err
}
func defaultApp(p model.Plugin) model.AppSettings {
	return model.AppSettings{ID: p.ID, OrgID: 1, Enabled: p.Enabled, JSONData: json.RawMessage(`{}`), SecureJSONFields: map[string]bool{}, UpdatedAt: p.InstalledAt}
}
func (s *Store) AppSettings(ctx context.Context, id string) (model.AppSettings, error) {
	p, err := s.PluginEffective(ctx, id)
	if err != nil {
		return model.AppSettings{}, err
	}
	if p.Type != "app" {
		return model.AppSettings{}, errors.New("plugin is not an app")
	}
	app, err := scanApp(s.DB.QueryRowContext(ctx, `SELECT payload FROM app_settings WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return defaultApp(p), nil
	}
	app.Enabled = p.Enabled
	return app, err
}
func (s *Store) AppSecrets(ctx context.Context, id string) (map[string]string, error) {
	var sealed []byte
	err := s.DB.QueryRowContext(ctx, `SELECT secrets FROM app_settings WHERE id=?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return s.openSecrets("app:"+id, sealed)
}
func (s *Store) SaveAppSettings(ctx context.Context, id string, input model.AppSettingsInput) (model.AppSettings, error) {
	if err := input.Validate(); err != nil {
		return model.AppSettings{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.AppSettings{}, err
	}
	defer tx.Rollback()
	var payload string
	var p model.Plugin
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM plugins WHERE id=?`, id).Scan(&payload); err != nil {
		return model.AppSettings{}, err
	}
	if err = json.Unmarshal([]byte(payload), &p); err != nil {
		return model.AppSettings{}, err
	}
	if p.Type != "app" {
		return model.AppSettings{}, errors.New("plugin is not an app")
	}
	parentEnabled := true
	if p.PackageID != p.ID {
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM plugins WHERE id=?`, p.PackageID).Scan(&payload); err != nil {
			return model.AppSettings{}, err
		}
		var parent model.Plugin
		if err = json.Unmarshal([]byte(payload), &parent); err != nil {
			return model.AppSettings{}, err
		}
		parentEnabled = parent.Enabled
		if input.Enabled != nil && *input.Enabled && !parentEnabled {
			return model.AppSettings{}, errors.New("owning package is disabled")
		}
	}
	app, err := scanApp(tx.QueryRowContext(ctx, `SELECT payload FROM app_settings WHERE id=?`, id))
	exists := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		app = defaultApp(p)
	} else if err != nil {
		return app, err
	}
	if input.Version != nil && *input.Version != app.Version {
		return app, ErrAppConflict
	}
	secrets := map[string]string{}
	if exists {
		var sealed []byte
		if err = tx.QueryRowContext(ctx, `SELECT secrets FROM app_settings WHERE id=?`, id).Scan(&sealed); err != nil {
			return app, err
		}
		secrets, err = s.openSecrets("app:"+id, sealed)
		if err != nil {
			return app, err
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
	if len(secrets) > 32 {
		return app, errors.New("at most 32 stored secret fields")
	}
	if input.Enabled != nil {
		p.Enabled = *input.Enabled
	}
	app.Enabled = p.Enabled && parentEnabled
	if input.Pinned != nil {
		app.Pinned = *input.Pinned
	}
	if len(input.JSONData) > 0 {
		app.JSONData = input.JSONData
	}
	app.SecureJSONFields = map[string]bool{}
	for key := range secrets {
		app.SecureJSONFields[key] = true
	}
	app.Version++
	app.UpdatedAt = max(time.Now().UnixMilli(), app.UpdatedAt+1)
	sealed, err := s.seal("app:"+id, secrets)
	if err != nil {
		return app, err
	}
	payloadBytes, err := json.Marshal(app)
	if err != nil {
		return app, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_settings(id,payload,secrets) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,secrets=excluded.secrets`, id, string(payloadBytes), sealed); err != nil {
		return app, err
	}
	pluginBytes, err := json.Marshal(p)
	if err != nil {
		return app, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE plugins SET payload=? WHERE id=?`, string(pluginBytes), id); err != nil {
		return app, err
	}
	return app, tx.Commit()
}
