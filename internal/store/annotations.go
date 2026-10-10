package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrAnnotationConflict = errors.New("annotation idempotency key already has a different payload")

func (s *Store) CreateAnnotation(ctx context.Context, a model.Annotation, key string) (model.Annotation, error) {
	if len(key) > 128 {
		return a, fmt.Errorf("%w: idempotency key exceeds 128 bytes", model.ErrInvalidAnnotation)
	}
	if err := a.Normalize(); err != nil {
		return a, err
	}
	a.ID, a.Created, a.Updated = 0, 0, 0
	data, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	if key != "" {
		var payload, oldHash string
		err = tx.QueryRowContext(ctx, `SELECT payload,request_hash FROM annotations WHERE idempotency_key=?`, key).Scan(&payload, &oldHash)
		if err == nil {
			if hash != oldHash {
				return a, ErrAnnotationConflict
			}
			err = json.Unmarshal([]byte(payload), &a)
			return a, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return a, err
		}
	}
	a, err = insertAnnotation(ctx, tx, a, key, hash)
	if err != nil {
		return a, err
	}
	return a, tx.Commit()
}

// Alert transitions use this insertion in their existing state/history transaction.
func insertAnnotation(ctx context.Context, tx *sql.Tx, a model.Annotation, key, hash string) (model.Annotation, error) {
	a.Created = time.Now().UnixMilli()
	a.Updated = a.Created
	var storedKey any
	if key != "" {
		storedKey = key
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO annotations(dashboard_uid,time,time_end,panel_id,user_id,payload,idempotency_key,request_hash) VALUES(?,?,?,?,?,'{}',?,?)`, a.DashboardUID, a.Time, a.TimeEnd, a.PanelID, a.UserID, storedKey, hash)
	if err != nil {
		return a, err
	}
	a.ID, err = result.LastInsertId()
	if err != nil {
		return a, err
	}
	if err = writeAnnotation(ctx, tx, a); err != nil {
		return a, err
	}
	return a, nil
}

func writeAnnotation(ctx context.Context, tx *sql.Tx, a model.Annotation) error {
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE annotations SET time=?,time_end=?,payload=? WHERE id=?`, a.Time, a.TimeEnd, string(payload), a.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM annotation_tags WHERE annotation_id=?`, a.ID); err != nil {
		return err
	}
	for _, tag := range a.Tags {
		if _, err = tx.ExecContext(ctx, `INSERT INTO annotation_tags(annotation_id,tag) VALUES(?,?)`, a.ID, tag); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) PatchAnnotation(ctx context.Context, id int64, p model.AnnotationPatch) (model.Annotation, error) {
	var a model.Annotation
	if (p.Time != nil && *p.Time < 0) || (p.TimeEnd != nil && *p.TimeEnd < 0) {
		return a, fmt.Errorf("%w: negative timestamps", model.ErrInvalidAnnotation)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	var payload string
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM annotations WHERE id=?`, id).Scan(&payload); err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(payload), &a); err != nil {
		return a, err
	}
	if a.Time == a.TimeEnd && p.Time != nil && *p.Time > 0 && p.TimeEnd == nil {
		a.TimeEnd = *p.Time
	}
	if p.Time != nil && *p.Time > 0 {
		a.Time = *p.Time
	}
	if p.TimeEnd != nil && *p.TimeEnd > 0 {
		a.TimeEnd = *p.TimeEnd
	}
	if p.Text != nil && *p.Text != "" {
		a.Text = *p.Text
	}
	if p.Tags != nil {
		a.Tags = *p.Tags
	}
	if len(p.Data) > 0 {
		a.Data = p.Data
	}
	if err = a.Normalize(); err != nil {
		return a, err
	}
	a.Updated = time.Now().UnixMilli()
	if err = writeAnnotation(ctx, tx, a); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

func (s *Store) DeletePanelAnnotations(ctx context.Context, uid string, panelID int64) error {
	if uid == "" || panelID <= 0 {
		return fmt.Errorf("%w: mass delete requires dashboard and panel", model.ErrInvalidAnnotation)
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM annotations WHERE dashboard_uid=? AND panel_id=?`, uid, panelID)
	return err
}

func (s *Store) Annotations(ctx context.Context, q model.AnnotationQuery) ([]model.Annotation, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 || q.From < 0 || q.To < 0 || (q.To > 0 && q.From > q.To) || len(q.Tags) > 32 || q.AlertID < 0 || len(q.AlertUID) > 128 || !annotationTypeValid(q.Type) {
		return nil, fmt.Errorf("%w: query limit 1–1000 and non-inverted range required", model.ErrInvalidAnnotation)
	}
	conditions := []string{"1=1"}
	args := []any{}
	for _, filter := range []struct {
		column string
		value  int64
	}{{"time_end >=", q.From}, {"time <=", q.To}, {"id =", q.ID}, {"panel_id =", q.PanelID}, {"user_id =", q.UserID}} {
		if filter.value > 0 {
			conditions = append(conditions, "a."+filter.column+" ?")
			args = append(args, filter.value)
		}
	}
	if q.DashboardUID != "" {
		conditions = append(conditions, "a.dashboard_uid = ?")
		args = append(args, q.DashboardUID)
	}
	if q.Type == "annotation" {
		conditions = append(conditions, `NOT EXISTS(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=a.id)`)
	} else if q.Type == "alert" {
		conditions = append(conditions, `EXISTS(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=a.id)`)
	}
	if q.AlertUID != "" {
		conditions = append(conditions, `EXISTS(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=a.id AND x.rule_uid=?)`)
		args = append(args, q.AlertUID)
	}
	if q.AlertID > 0 {
		conditions = append(conditions, `EXISTS(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=a.id AND x.rule_id=?)`)
		args = append(args, q.AlertID)
	}
	clauses := []string{}
	for _, tag := range q.Tags {
		if len(tag) > 256 {
			return nil, fmt.Errorf("%w: tag exceeds 256 bytes", model.ErrInvalidAnnotation)
		}
		clauses = append(clauses, `EXISTS(SELECT 1 FROM annotation_tags t WHERE t.annotation_id=a.id AND t.tag=?)`)
		args = append(args, tag)
	}
	if len(clauses) > 0 {
		join := " AND "
		if q.MatchAny {
			join = " OR "
		}
		conditions = append(conditions, "("+strings.Join(clauses, join)+")")
	}
	args = append(args, q.Limit)
	rows, err := s.DB.QueryContext(ctx, `SELECT a.payload FROM annotations a WHERE `+strings.Join(conditions, " AND ")+` ORDER BY a.time_end DESC,a.time DESC,a.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Annotation{}
	for rows.Next() {
		var payload string
		var a model.Annotation
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAnnotation(ctx context.Context, id int64) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM annotations WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func annotationTypeValid(kind string) bool {
	return kind == "" || kind == "annotation" || kind == "alert"
}

func (s *Store) AnnotationTags(ctx context.Context, query string, limit int, kind ...string) ([]map[string]any, error) {
	filter := ""
	if len(kind) > 0 {
		filter = kind[0]
	}
	if limit < 1 || limit > 1000 || len(query) > 256 || !annotationTypeValid(filter) {
		return nil, fmt.Errorf("%w: tag query limit 1–1000", model.ErrInvalidAnnotation)
	}
	condition := ""
	if filter != "" {
		exists := "EXISTS"
		if filter == "annotation" {
			exists = "NOT EXISTS"
		}
		condition = " AND " + exists + `(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=t.annotation_id)`
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT tag,COUNT(*) FROM annotation_tags t WHERE instr(tag,?)>0`+condition+` GROUP BY tag ORDER BY tag LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var tag string
		var count int64
		if err = rows.Scan(&tag, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"tag": tag, "count": count})
	}
	return out, rows.Err()
}
