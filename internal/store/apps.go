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
	return s.pluginEffective(ctx, id, map[string]bool{})
}

func (s *Store) pluginEffective(ctx context.Context, id string, seen map[string]bool) (model.Plugin, error) {
	if seen[id] || len(seen) >= 8 {
		return model.Plugin{}, errors.New("invalid plugin package ownership chain")
	}
	seen[id] = true
	p, err := s.Plugin(ctx, id)
	if err != nil {
		return p, err
	}
	if p.Type == "app" {
		app, appErr := scanApp(s.DB.QueryRowContext(ctx, `SELECT payload FROM app_settings WHERE id=?`, id))
		if appErr != nil && !errors.Is(appErr, sql.ErrNoRows) {
			return p, appErr
		}
		if appErr == nil {
			p.Enabled = p.Enabled && app.Enabled
		}
	}
	if p.PackageID != p.ID {
		parent, err := s.pluginEffective(ctx, p.PackageID, seen)
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
func defaultApp(p model.Plugin, orgID int64) model.AppSettings {
	return model.AppSettings{ID: p.ID, OrgID: orgID, Enabled: p.Enabled, JSONData: json.RawMessage(`{}`), SecureJSONFields: map[string]bool{}, UpdatedAt: p.InstalledAt}
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
		return defaultApp(p, s.OrganizationID()), nil
	}
	app.OrgID = s.OrganizationID()
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
	p, err := s.Plugin(ctx, id)
	if err != nil {
		return model.AppSettings{}, err
	}
	if p.Type != "app" {
		return model.AppSettings{}, errors.New("plugin is not an app")
	}
	parentEnabled := true
	if p.PackageID != p.ID {
		parent, err := s.PluginEffective(ctx, p.PackageID)
		if err != nil {
			return model.AppSettings{}, err
		}
		parentEnabled = parent.Enabled
		if input.Enabled != nil && *input.Enabled && !parentEnabled {
			return model.AppSettings{}, errors.New("owning package is disabled")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.AppSettings{}, err
	}
	defer tx.Rollback()
	app, err := scanApp(tx.QueryRowContext(ctx, `SELECT payload FROM app_settings WHERE id=?`, id))
	exists := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		app = defaultApp(p, s.OrganizationID())
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
		app.Enabled = *input.Enabled
	}
	app.OrgID = s.OrganizationID()
	app.Enabled = app.Enabled && p.Enabled && parentEnabled
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
	return app, tx.Commit()
}
