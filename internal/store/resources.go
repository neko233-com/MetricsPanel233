package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) Targets(ctx context.Context) ([]model.Target, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,url,interval_seconds,labels,enabled,last_scrape,last_error,samples,duration_ms FROM targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Target{}
	for rows.Next() {
		var t model.Target
		var labels string
		if err = rows.Scan(&t.ID, &t.Name, &t.URL, &t.IntervalSeconds, &labels, &t.Enabled, &t.LastScrape, &t.LastError, &t.Samples, &t.DurationMS); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(labels), &t.Labels); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) SaveTarget(ctx context.Context, t model.Target) (model.Target, error) {
	if err := t.Validate(); err != nil {
		return t, err
	}
	if t.ID == 0 {
		r, err := s.DB.ExecContext(ctx, `INSERT INTO targets(name,url,interval_seconds,labels,enabled) VALUES(?,?,?,?,?)`, t.Name, t.URL, t.IntervalSeconds, model.LabelsJSON(t.Labels), t.Enabled)
		if err != nil {
			return t, err
		}
		t.ID, err = r.LastInsertId()
		return t, err
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE targets SET name=?,url=?,interval_seconds=?,labels=?,enabled=?,last_scrape=0,last_error='' WHERE id=?`, t.Name, t.URL, t.IntervalSeconds, model.LabelsJSON(t.Labels), t.Enabled, t.ID)
	if err != nil {
		return t, err
	}
	n, err := r.RowsAffected()
	if n == 0 {
		return t, sql.ErrNoRows
	}
	return t, err
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
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,panels,updated_at,extras FROM dashboards ORDER BY CASE WHEN id='system' THEN 0 ELSE 1 END,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Dashboard{}
	for rows.Next() {
		var d model.Dashboard
		var panels, extras string
		if err = rows.Scan(&d.ID, &d.Name, &panels, &d.UpdatedAt, &extras); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(panels), &d.Panels); err != nil {
			return nil, err
		}
		var extra struct {
			Variables   []model.Variable `json:"variables"`
			Grafana     json.RawMessage  `json:"grafana"`
			Annotations json.RawMessage  `json:"annotations"`
		}
		if err = json.Unmarshal([]byte(extras), &extra); err != nil {
			return nil, err
		}
		d.Variables = extra.Variables
		d.Grafana = extra.Grafana
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
	if err := d.Validate(); err != nil {
		return d, err
	}
	d.UpdatedAt = time.Now().UnixMilli()
	panels, err := json.Marshal(d.Panels)
	if err != nil {
		return d, err
	}
	extra := map[string]any{"variables": d.Variables, "grafana": d.Grafana}
	if len(d.Annotations) > 0 {
		extra["annotations"] = d.Annotations
	}
	extras, err := json.Marshal(extra)
	if err != nil {
		return d, err
	}
	if revision != nil {
		err = s.DB.QueryRowContext(ctx, `UPDATE dashboards SET name=?,panels=?,updated_at=MAX(?,updated_at+1),extras=? WHERE id=? AND updated_at=? RETURNING updated_at`, d.Name, string(panels), d.UpdatedAt, string(extras), d.ID, *revision).Scan(&d.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return d, ErrDashboardConflict
		}
	} else {
		err = s.DB.QueryRowContext(ctx, `INSERT INTO dashboards(id,name,panels,updated_at,extras) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,panels=excluded.panels,updated_at=MAX(excluded.updated_at,dashboards.updated_at+1),extras=excluded.extras RETURNING updated_at`, d.ID, d.Name, string(panels), d.UpdatedAt, string(extras)).Scan(&d.UpdatedAt)
	}
	return d, err
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
