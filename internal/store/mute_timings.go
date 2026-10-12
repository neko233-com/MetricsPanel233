package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) initMuteTimings() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS alert_mute_timings(name TEXT PRIMARY KEY,version INTEGER NOT NULL,config TEXT NOT NULL);`)
	return err
}

func scanMuteTiming(row interface{ Scan(...any) error }) (model.MuteTiming, error) {
	var value model.MuteTiming
	var raw string
	err := row.Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}

func (s *Store) MuteTimings(ctx context.Context) ([]model.MuteTiming, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT config FROM alert_mute_timings ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MuteTiming{}
	for rows.Next() {
		value, err := scanMuteTiming(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) MuteTiming(ctx context.Context, name string) (model.MuteTiming, error) {
	return scanMuteTiming(s.DB.QueryRowContext(ctx, `SELECT config FROM alert_mute_timings WHERE name=?`, name))
}

func (s *Store) SaveMuteTiming(ctx context.Context, value model.MuteTiming) (model.MuteTiming, error) {
	if _, err := value.Intervals(); err != nil {
		return value, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return value, err
	}
	defer tx.Rollback()
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM alert_mute_timings WHERE name=?`, value.Name).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return value, err
	}
	if version != value.Version {
		return value, ErrNotificationConflict
	}
	if version == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM alert_mute_timings`).Scan(&count); err != nil {
			return value, err
		}
		if count >= 100 {
			return value, errors.New("at most 100 mute timings")
		}
	}
	value.Version = version + 1
	value.UpdatedAt = time.Now().UnixMilli()
	raw, _ := json.Marshal(value)
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_mute_timings(name,version,config) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET version=excluded.version,config=excluded.config`, value.Name, value.Version, string(raw)); err != nil {
		return value, err
	}
	if err := wakeNotificationGroupsTx(ctx, tx, value.UpdatedAt); err != nil {
		return value, err
	}
	return value, tx.Commit()
}

func (s *Store) DeleteMuteTiming(ctx context.Context, name string) error {
	return s.DeleteMuteTimingVersion(ctx, name, nil)
}

func (s *Store) DeleteMuteTimingVersion(ctx context.Context, name string, version *int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version != nil {
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM alert_mute_timings WHERE name=?`, name).Scan(&current); err != nil {
			return err
		}
		if current != *version {
			return ErrNotificationConflict
		}
	}
	p, err := notificationPolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	var referenced func([]model.NotificationRoute) bool
	referenced = func(routes []model.NotificationRoute) bool {
		for _, route := range routes {
			for _, names := range [][]string{route.MuteTimeIntervals, route.ActiveTimeIntervals} {
				for _, v := range names {
					if v == name {
						return true
					}
				}
			}
			if referenced(route.Routes) {
				return true
			}
		}
		return false
	}
	if referenced(p.Routes) {
		return errors.New("mute timing is referenced by the notification policy")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM alert_mute_timings WHERE name=?`, name)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func muteTimingsTx(ctx context.Context, tx *sql.Tx) (map[string]model.MuteTiming, error) {
	rows, err := tx.QueryContext(ctx, `SELECT config FROM alert_mute_timings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.MuteTiming{}
	for rows.Next() {
		value, err := scanMuteTiming(rows)
		if err != nil {
			return nil, err
		}
		out[value.Name] = value
	}
	return out, rows.Err()
}

func routeTimingMuted(opts model.NotificationRouteOptions, timings map[string]model.MuteTiming, at time.Time) (bool, error) {
	match := func(names []string) (bool, error) {
		for _, name := range names {
			timing, ok := timings[name]
			if !ok {
				return false, errors.New("notification time interval no longer exists")
			}
			matched, err := timing.Matches(at)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}
	muted, err := match(opts.MuteTimeIntervals)
	if err != nil || muted {
		return muted, err
	}
	if len(opts.ActiveTimeIntervals) > 0 {
		active, err := match(opts.ActiveTimeIntervals)
		return !active, err
	}
	return false, nil
}
