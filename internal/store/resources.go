package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) Targets(ctx context.Context) ([]model.Target, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id,name,url,interval_seconds,labels,enabled,last_scrape,last_error,samples,duration_ms,COALESCE(c.kind,''),COALESCE(c.username,''),COALESCE(c.database_name,''),COALESCE(c.tls_mode,''),c.secrets FROM targets t LEFT JOIN target_connections c ON c.id=t.id ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Target{}
	for rows.Next() {
		var t model.Target
		var labels string
		var sealed []byte
		if err = rows.Scan(&t.ID, &t.Name, &t.URL, &t.IntervalSeconds, &labels, &t.Enabled, &t.LastScrape, &t.LastError, &t.Samples, &t.DurationMS, &t.Kind, &t.Username, &t.Database, &t.TLSMode, &sealed); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(labels), &t.Labels); err != nil {
			return nil, err
		}
		if len(sealed) > 0 {
			secrets, err := s.openSecrets(fmt.Sprintf("target:%d", t.ID), sealed)
			if err != nil {
				return nil, err
			}
			t.SecureFields = map[string]bool{}
			for key, value := range secrets {
				if value != "" {
					t.SecureFields[key] = true
				}
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) SaveTarget(ctx context.Context, t model.Target) (model.Target, error) {
	if err := t.Validate(); err != nil {
		return t, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	secrets := map[string]string{}
	if t.ID != 0 {
		var sealed []byte
		err = tx.QueryRowContext(ctx, `SELECT secrets FROM target_connections WHERE id=?`, t.ID).Scan(&sealed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		if len(sealed) > 0 {
			secrets, err = s.openSecrets(fmt.Sprintf("target:%d", t.ID), sealed)
			if err != nil {
				return t, err
			}
		}
	}
	if t.ID == 0 {
		r, err := tx.ExecContext(ctx, `INSERT INTO targets(name,url,interval_seconds,labels,enabled) VALUES(?,?,?,?,?)`, t.Name, t.URL, t.IntervalSeconds, model.LabelsJSON(t.Labels), t.Enabled)
		if err != nil {
			return t, err
		}
		t.ID, err = r.LastInsertId()
		if err != nil {
			return t, err
		}
	} else {
		r, err := tx.ExecContext(ctx, `UPDATE targets SET name=?,url=?,interval_seconds=?,labels=?,enabled=?,last_scrape=0,last_error='' WHERE id=?`, t.Name, t.URL, t.IntervalSeconds, model.LabelsJSON(t.Labels), t.Enabled, t.ID)
		if err != nil {
			return t, err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return t, err
		}
		if n == 0 {
			return t, sql.ErrNoRows
		}
	}
	for key, value := range t.SecureSettings {
		if value == "" {
			delete(secrets, key)
		} else {
			secrets[key] = value
		}
	}
	sealed, err := s.seal(fmt.Sprintf("target:%d", t.ID), secrets)
	if err != nil {
		return t, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO target_connections(id,kind,username,database_name,tls_mode,secrets) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,username=excluded.username,database_name=excluded.database_name,tls_mode=excluded.tls_mode,secrets=excluded.secrets`, t.ID, t.Kind, t.Username, t.Database, t.TLSMode, sealed)
	if err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	t.SecureSettings = nil
	t.SecureFields = map[string]bool{}
	for key, value := range secrets {
		if value != "" {
			t.SecureFields[key] = true
		}
	}
	return t, nil
}

// TargetSecrets is used by the collector only. Public target responses contain flags.
func (s *Store) TargetSecrets(ctx context.Context, id int64) (map[string]string, error) {
	var sealed []byte
	err := s.DB.QueryRowContext(ctx, `SELECT secrets FROM target_connections WHERE id=?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return s.openSecrets(fmt.Sprintf("target:%d", id), sealed)
}
func (s *Store) TargetResult(ctx context.Context, id int64, count int, duration int64, scrapeErr error) error {
	message := ""
	if scrapeErr != nil {
		message = scrapeErr.Error()
		if len(message) > 1000 {
			message = message[:1000]
		}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE targets SET last_scrape=?,last_error=?,samples=?,duration_ms=? WHERE id=?`, time.Now().UnixMilli(), message, count, duration, id)
	return err
}
func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	r, err := s.DB.ExecContext(ctx, `DELETE FROM targets WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) Dashboards(ctx context.Context) ([]model.Dashboard, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id,d.name,d.panels,d.updated_at,d.extras,i.id FROM dashboards d JOIN dashboard_identity i ON i.dashboard_id=d.id ORDER BY CASE WHEN d.id='system' THEN 0 ELSE 1 END,d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Dashboard{}
	for rows.Next() {
		var d model.Dashboard
		var panels, extras string
		if err = rows.Scan(&d.ID, &d.Name, &panels, &d.UpdatedAt, &extras, &d.GrafanaID); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(panels), &d.Panels); err != nil {
			return nil, err
		}
		var extra struct {
			Variables      []model.Variable `json:"variables"`
			Grafana        json.RawMessage  `json:"grafana"`
			Annotations    json.RawMessage  `json:"annotations"`
			FolderUID      string           `json:"folder_uid"`
			PluginID       string           `json:"plugin_id"`
			PluginPath     string           `json:"plugin_path"`
			GrafanaVersion int64            `json:"grafana_version"`
		}
		if err = json.Unmarshal([]byte(extras), &extra); err != nil {
			return nil, err
		}
		d.Variables = extra.Variables
		if string(extra.Grafana) != "null" {
			d.Grafana = extra.Grafana
		}
		d.FolderUID, d.PluginID, d.PluginPath = extra.FolderUID, extra.PluginID, extra.PluginPath
		d.GrafanaVersion = extra.GrafanaVersion
		if d.GrafanaVersion <= 0 {
			d.GrafanaVersion = d.SourceVersion()
		}
		if string(extra.Annotations) != "null" {
			d.Annotations = extra.Annotations
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) SaveDashboard(ctx context.Context, d model.Dashboard) (model.Dashboard, error) {
	return s.saveDashboard(ctx, d, nil)
}

var ErrDashboardConflict = errors.New("Dashboard changed. Close and reopen this editor.")

// SaveDashboardAtRevision atomically refuses stale edits, including deleted dashboards.
func (s *Store) SaveDashboardAtRevision(ctx context.Context, d model.Dashboard, revision int64) (model.Dashboard, error) {
	return s.saveDashboard(ctx, d, &revision)
}

func (s *Store) saveDashboard(ctx context.Context, d model.Dashboard, revision *int64) (model.Dashboard, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	d, err = saveDashboardTx(ctx, tx, d, revision)
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}

func saveDashboardTx(ctx context.Context, tx *sql.Tx, d model.Dashboard, revision *int64) (model.Dashboard, error) {
	metadata, err := dashboardHistoryMetadata(ctx)
	if err != nil {
		return d, err
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	d.FolderUID = model.RootFolderUID(d.FolderUID)
	if _, err := folderByUID(ctx, tx, d.FolderUID); err != nil {
		return d, err
	}
	var previousExtras string
	var parentVersion int64
	lookupErr := tx.QueryRowContext(ctx, `SELECT extras FROM dashboards WHERE id=?`, d.ID).Scan(&previousExtras)
	if lookupErr == nil {
		var previous struct {
			Version int64           `json:"grafana_version"`
			Grafana json.RawMessage `json:"grafana"`
		}
		if err := json.Unmarshal([]byte(previousExtras), &previous); err != nil {
			return d, err
		}
		version := previous.Version
		if version <= 0 {
			version = (model.Dashboard{Grafana: previous.Grafana}).SourceVersion()
		}
		if version == math.MaxInt64 {
			return d, errors.New("dashboard version limit reached")
		}
		parentVersion = version
		d.GrafanaVersion = version + 1
	} else if errors.Is(lookupErr, sql.ErrNoRows) {
		d.GrafanaVersion = d.SourceVersion()
	} else {
		return d, lookupErr
	}
	d.UpdatedAt = time.Now().UnixMilli()
	panels, err := json.Marshal(d.Panels)
	if err != nil {
		return d, err
	}
	extra := map[string]any{"variables": d.Variables, "grafana": d.Grafana}
	extra["grafana_version"] = d.GrafanaVersion
	if d.FolderUID != "" {
		extra["folder_uid"] = d.FolderUID
	}
	if d.PluginID != "" {
		extra["plugin_id"], extra["plugin_path"] = d.PluginID, d.PluginPath
	}
	if len(d.Annotations) > 0 {
		extra["annotations"] = d.Annotations
	}
	extras, err := json.Marshal(extra)
	if err != nil {
		return d, err
	}
	if revision != nil {
		err = tx.QueryRowContext(ctx, `UPDATE dashboards SET name=?,panels=?,updated_at=MAX(?,updated_at+1),extras=? WHERE id=? AND updated_at=? RETURNING updated_at`, d.Name, string(panels), d.UpdatedAt, string(extras), d.ID, *revision).Scan(&d.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return d, ErrDashboardConflict
		}
	} else {
		err = tx.QueryRowContext(ctx, `INSERT INTO dashboards(id,name,panels,updated_at,extras) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,panels=excluded.panels,updated_at=MAX(excluded.updated_at,dashboards.updated_at+1),extras=excluded.extras RETURNING updated_at`, d.ID, d.Name, string(panels), d.UpdatedAt, string(extras)).Scan(&d.UpdatedAt)
	}
	if err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dashboard_identity(dashboard_id) SELECT ? WHERE NOT EXISTS(SELECT 1 FROM dashboard_identity WHERE dashboard_id=?)`, d.ID, d.ID); err != nil {
		return d, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM dashboard_identity WHERE dashboard_id=?`, d.ID).Scan(&d.GrafanaID); err != nil {
		return d, err
	}
	if err = syncLibraryConnections(ctx, tx, d); err != nil {
		return d, err
	}
	if err = insertDashboardVersion(ctx, tx, d, parentVersion, metadata); err != nil {
		return d, err
	}
	return d, nil
}
func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	r, err := s.DB.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return err
}
