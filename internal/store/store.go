package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	Path    string
	Backend MetricBackend
}

// MetricBackend separates durable metrics from the small SQLite control plane.
type MetricBackend interface {
	Ping(context.Context) error
	Ingest(context.Context, []model.Sample) error
	Metrics(context.Context) ([]Metric, error)
	Stats(context.Context) (Stats, error)
	Query(context.Context, model.Query) (model.QueryResult, error)
	Prune(context.Context, int64) error
	LoadSeries(context.Context, int64, int64, []Matcher) ([]RawSeries, error)
	SelectSeries(context.Context, int64, int64, []Matcher) ([]RawSeries, error)
	SavePatterns(context.Context, []model.Pattern) error
	Patterns(context.Context, int) ([]model.Pattern, error)
	Pattern(context.Context, string) (model.Pattern, error)
	DeletePattern(context.Context, string) error
	SearchPatterns(context.Context, model.PatternSearch) ([]model.PatternHit, error)
}

func (s *Store) Kind() string {
	if s.Backend != nil {
		return "clickhouse"
	}
	return "sqlite"
}
func (s *Store) Health(ctx context.Context) error {
	if err := s.DB.PingContext(ctx); err != nil {
		return err
	}
	if s.Backend != nil {
		return s.Backend.Ping(ctx)
	}
	return nil
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Path: path}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS series(id INTEGER PRIMARY KEY, name TEXT NOT NULL, labels TEXT NOT NULL, UNIQUE(name,labels));
CREATE TABLE IF NOT EXISTS samples(series_id INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE, timestamp INTEGER NOT NULL, value REAL NOT NULL, PRIMARY KEY(series_id,timestamp)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS samples_time ON samples(timestamp);
CREATE TABLE IF NOT EXISTS targets(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, url TEXT NOT NULL, interval_seconds INTEGER NOT NULL, labels TEXT NOT NULL, enabled INTEGER NOT NULL, last_scrape INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', samples INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS dashboards(id TEXT PRIMARY KEY, name TEXT NOT NULL, panels TEXT NOT NULL, updated_at INTEGER NOT NULL, extras TEXT NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS patterns(id TEXT PRIMARY KEY,metric TEXT NOT NULL,labels TEXT NOT NULL,normalization TEXT NOT NULL,aggregation TEXT NOT NULL,created_at INTEGER NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS patterns_metric ON patterns(normalization,aggregation,metric,created_at);
CREATE TABLE IF NOT EXISTS alert_rules(uid TEXT PRIMARY KEY,version INTEGER NOT NULL,config TEXT NOT NULL,runtime TEXT NOT NULL,last_evaluation INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS alert_schedule(uid TEXT PRIMARY KEY REFERENCES alert_rules(uid) ON DELETE CASCADE,interval_seconds INTEGER NOT NULL,paused INTEGER NOT NULL);
INSERT OR IGNORE INTO alert_schedule(uid,interval_seconds,paused) SELECT uid,json_extract(config,'$.interval_seconds'),json_extract(config,'$.paused') FROM alert_rules;
CREATE TABLE IF NOT EXISTS alert_events(id INTEGER PRIMARY KEY AUTOINCREMENT,uid TEXT NOT NULL,timestamp INTEGER NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS alert_events_uid ON alert_events(uid,id);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Additive migration for databases created before template support.
	rows, err := db.Query(`PRAGMA table_info(dashboards)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	hasExtras := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if name == "extras" {
			hasExtras = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		db.Close()
		return nil, err
	}
	rows.Close()
	if !hasExtras {
		if _, err = db.Exec(`ALTER TABLE dashboards ADD COLUMN extras TEXT NOT NULL DEFAULT '{}'`); err != nil {
			db.Close()
			return nil, err
		}
	}
	d := model.Dashboard{ID: "system", Name: "System overview", Panels: []model.Panel{
		{ID: "memory", Title: "Memory usage", Metric: "metricspanel_memory_bytes", Aggregation: "last", Unit: "bytes"},
		{ID: "goroutines", Title: "Goroutines", Metric: "metricspanel_goroutines", Aggregation: "last", Unit: "count"},
		{ID: "requests", Title: "HTTP requests", Metric: "metricspanel_http_requests_total", Aggregation: "rate", Unit: "ops"},
	}}
	panels, _ := json.Marshal(d.Panels)
	_, err = db.Exec(`INSERT OR IGNORE INTO dashboards(id,name,panels,updated_at) VALUES(?,?,?,?)`, d.ID, d.Name, string(panels), time.Now().UnixMilli())
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Ingest(ctx context.Context, samples []model.Sample) error {
	if s.Backend != nil {
		return s.Backend.Ingest(ctx, samples)
	}
	if len(samples) == 0 || len(samples) > 10000 {
		return fmt.Errorf("batch must contain 1–10000 samples")
	}
	now := time.Now().UnixMilli()
	for i := range samples {
		if err := samples[i].Validate(); err != nil {
			return err
		}
		if samples[i].Timestamp == 0 {
			samples[i].Timestamp = now
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insertSeries, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO series(name,labels) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer insertSeries.Close()
	findSeries, err := tx.PrepareContext(ctx, `SELECT id FROM series WHERE name=? AND labels=?`)
	if err != nil {
		return err
	}
	defer findSeries.Close()
	insertSample, err := tx.PrepareContext(ctx, `INSERT INTO samples(series_id,timestamp,value) VALUES(?,?,?) ON CONFLICT(series_id,timestamp) DO UPDATE SET value=excluded.value`)
	if err != nil {
		return err
	}
	defer insertSample.Close()
	cache := map[string]int64{}
	for _, sample := range samples {
		labels := model.LabelsJSON(sample.Labels)
		key := sample.Name + "\x00" + labels
		id, ok := cache[key]
		if !ok {
			if _, err = insertSeries.ExecContext(ctx, sample.Name, labels); err != nil {
				return err
			}
			if err = findSeries.QueryRowContext(ctx, sample.Name, labels).Scan(&id); err != nil {
				return err
			}
			cache[key] = id
		}
		if _, err = insertSample.ExecContext(ctx, id, sample.Timestamp, sample.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Metric struct {
	Name        string `json:"name"`
	SeriesCount int    `json:"series_count"`
}

func (s *Store) Metrics(ctx context.Context) ([]Metric, error) {
	if s.Backend != nil {
		return s.Backend.Metrics(ctx)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT name,COUNT(*) FROM series GROUP BY name ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.Name, &m.SeriesCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type Stats struct {
	Series           int     `json:"series"`
	Samples          int     `json:"samples"`
	IngestRate       float64 `json:"ingest_rate"`
	StorageBytes     int64   `json:"storage_bytes"`
	RetentionDays    int     `json:"retention_days"`
	CollectorsOnline int     `json:"collectors_online"`
	CollectorsTotal  int     `json:"collectors_total"`
	StartedAt        int64   `json:"started_at"`
	LastSample       int64   `json:"last_sample"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	if s.Backend != nil {
		return s.Backend.Stats(ctx)
	}
	var v Stats
	var recent int
	err := s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM series),COUNT(*),COALESCE(MAX(timestamp),0),COALESCE(SUM(CASE WHEN timestamp>=? THEN 1 ELSE 0 END),0) FROM samples`, time.Now().Add(-time.Minute).UnixMilli()).Scan(&v.Series, &v.Samples, &v.LastSample, &recent)
	v.IngestRate = float64(recent) / 60
	for _, p := range []string{s.Path, s.Path + "-wal", s.Path + "-shm"} {
		if f, err := os.Stat(p); err == nil {
			v.StorageBytes += f.Size()
		}
	}
	return v, err
}

func (s *Store) Prune(ctx context.Context, before int64) error {
	if s.Backend != nil {
		return s.Backend.Prune(ctx, before)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM samples WHERE timestamp<?`, before); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM series WHERE NOT EXISTS(SELECT 1 FROM samples WHERE samples.series_id=series.id)`); err != nil {
		return err
	}
	return tx.Commit()
}
