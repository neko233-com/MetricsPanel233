package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

type dashboardHistoryMetadataKey struct{}

// WithDashboardHistoryMetadata attaches save metadata to every existing atomic
// save path, including library imports and native resource mutations.
func WithDashboardHistoryMetadata(ctx context.Context, metadata model.DashboardHistoryMetadata) context.Context {
	return context.WithValue(ctx, dashboardHistoryMetadataKey{}, metadata)
}

func dashboardHistoryMetadata(ctx context.Context) (model.DashboardHistoryMetadata, error) {
	metadata, _ := ctx.Value(dashboardHistoryMetadataKey{}).(model.DashboardHistoryMetadata)
	if utf8.RuneCountInString(metadata.Message) > 500 || len(metadata.CreatedBy) > 256 || metadata.RestoredFrom < 0 {
		return metadata, errors.New("invalid dashboard history metadata")
	}
	if metadata.CreatedBy == "" {
		metadata.CreatedBy = "admin"
	}
	return metadata, nil
}

func insertDashboardVersion(ctx context.Context, tx *sql.Tx, d model.Dashboard, parentVersion int64, metadata model.DashboardHistoryMetadata) error {
	if metadata.RestoredFrom > 0 {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM dashboard_versions WHERE dashboard_id=? AND version=?`, d.ID, metadata.RestoredFrom).Scan(&found); err != nil {
			return fmt.Errorf("restored dashboard version: %w", err)
		}
	}
	if parentVersion == 0 && metadata.Message == "" {
		metadata.Message = "Initial save"
	}
	if metadata.RestoredFrom > 0 && metadata.Message == "" {
		metadata.Message = fmt.Sprintf("Restored from version %d", metadata.RestoredFrom)
	}
	snapshot, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_versions(dashboard_id,grafana_id,uid,version,parent_version,restored_from,created,created_by,message,snapshot) VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, d.GrafanaID, d.UID(), d.GrafanaVersion, parentVersion, metadata.RestoredFrom, d.UpdatedAt, metadata.CreatedBy, metadata.Message, string(snapshot))
	return err
}

// backfillDashboardHistory captures only the currently available version. It
// never invents past versions or rewrites the existing dashboard source row.
func (s *Store) backfillDashboardHistory(ctx context.Context) error {
	dashboards, err := s.Dashboards(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, dashboard := range dashboards {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dashboard_versions WHERE dashboard_id=?)`, dashboard.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			metadata := model.DashboardHistoryMetadata{CreatedBy: "admin", Message: "Current version imported during upgrade"}
			if err := insertDashboardVersion(ctx, tx, dashboard, 0, metadata); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type dashboardHistoryCursor struct {
	Dashboard string `json:"dashboard"`
	Maximum   int64  `json:"maximum"`
	Before    int64  `json:"before"`
}

const dashboardVersionColumns = `id,grafana_id,uid,parent_version,restored_from,version,created,created_by,message`

// DashboardVersions lists metadata newest first. start is an offset, matching
// Grafana 13's implementation. Continuation is bound to this dashboard and the
// original maximum version, so a save between pages does not shift the stream.
func (s *Store) DashboardVersions(ctx context.Context, id string, limit, start int, continueToken string) (model.DashboardVersionList, error) {
	result := model.DashboardVersionList{Versions: []model.DashboardVersion{}}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 1000 || start < 0 {
		return result, errors.New("dashboard history limit must be at most 1000 and start nonnegative")
	}
	cursor := dashboardHistoryCursor{Dashboard: id}
	if continueToken != "" {
		if len(continueToken) > 1024 {
			return result, errors.New("invalid dashboard history continuation token")
		}
		raw, err := base64.RawURLEncoding.DecodeString(continueToken)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Dashboard != id || cursor.Maximum <= 0 || cursor.Before <= 0 || cursor.Before > cursor.Maximum {
			return result, errors.New("invalid dashboard history continuation token")
		}
		start = 0 // continuation already selects the next slice of the anchored stream.
	}
	var exists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dashboards WHERE id=?)`, id).Scan(&exists); err != nil {
		return result, err
	}
	if !exists {
		return result, sql.ErrNoRows
	}
	if cursor.Maximum == 0 {
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM dashboard_versions WHERE dashboard_id=?`, id).Scan(&cursor.Maximum); err != nil {
			return result, err
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+dashboardVersionColumns+` FROM dashboard_versions WHERE dashboard_id=? AND version<=? AND (?=0 OR version<?) ORDER BY version DESC LIMIT ? OFFSET ?`, id, cursor.Maximum, cursor.Before, cursor.Before, limit+1, start)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		version, err := scanDashboardVersion(rows, nil)
		if err != nil {
			return result, err
		}
		result.Versions = append(result.Versions, version)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Versions) > limit {
		result.Versions = result.Versions[:limit]
		cursor.Before = result.Versions[limit-1].Version
		raw, err := json.Marshal(cursor)
		if err != nil {
			return result, err
		}
		result.ContinueToken = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}

// DashboardVersion returns an immutable full snapshot; version is the local
// dashboard version number, not the history table's global numeric ID.
func (s *Store) DashboardVersion(ctx context.Context, id string, version int64) (model.DashboardVersion, error) {
	if version <= 0 {
		return model.DashboardVersion{}, errors.New("dashboard version must be positive")
	}
	var snapshot string
	result, err := scanDashboardVersion(s.DB.QueryRowContext(ctx, `SELECT `+dashboardVersionColumns+`,snapshot FROM dashboard_versions WHERE dashboard_id=? AND version=?`, id, version), &snapshot)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(snapshot), &result.Snapshot); err != nil {
		return result, fmt.Errorf("invalid dashboard history snapshot: %w", err)
	}
	return result, nil
}

func scanDashboardVersion(row rowScanner, snapshot *string) (model.DashboardVersion, error) {
	var result model.DashboardVersion
	var created int64
	fields := []any{&result.ID, &result.DashboardID, &result.DashboardUID, &result.ParentVersion, &result.RestoredFrom, &result.Version, &created, &result.CreatedBy, &result.Message}
	if snapshot != nil {
		fields = append(fields, snapshot)
	}
	err := row.Scan(fields...)
	result.Created = time.UnixMilli(created).UTC()
	return result, err
}
