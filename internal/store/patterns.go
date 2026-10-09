package store

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func (s *Store) SavePatterns(ctx context.Context, patterns []model.Pattern) error {
	if len(patterns) == 0 || len(patterns) > 200 {
		return errors.New("pattern batch must contain 1–200 windows")
	}
	for i := range patterns {
		if err := patterns[i].Validate(); err != nil {
			return err
		}
		patterns[i].SetID()
		if patterns[i].CreatedAt == 0 {
			patterns[i].CreatedAt = time.Now().UnixMilli()
		}
	}
	if s.Backend != nil {
		return s.Backend.SavePatterns(ctx, patterns)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range patterns {
		payload, _ := json.Marshal(p)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO patterns(id,metric,labels,normalization,aggregation,created_at,payload) VALUES(?,?,?,?,?,?,?)`, p.ID, p.Metric, model.LabelsJSON(p.Labels), p.Normalization, p.Aggregation, p.CreatedAt, string(payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Patterns(ctx context.Context, limit int) ([]model.Pattern, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("limit must be 1–1000")
	}
	if s.Backend != nil {
		return s.Backend.Patterns(ctx, limit)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT payload FROM patterns ORDER BY created_at DESC,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Pattern{}
	for rows.Next() {
		var payload string
		var p model.Pattern
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &p); err != nil {
			return nil, err
		}
		out = append(out, p.Summary())
	}
	return out, rows.Err()
}
func (s *Store) Pattern(ctx context.Context, id string) (model.Pattern, error) {
	if s.Backend != nil {
		return s.Backend.Pattern(ctx, id)
	}
	var payload string
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM patterns WHERE id=?`, id).Scan(&payload)
	if err != nil {
		return model.Pattern{}, err
	}
	var p model.Pattern
	err = json.Unmarshal([]byte(payload), &p)
	return p, err
}
func (s *Store) DeletePattern(ctx context.Context, id string) error {
	if s.Backend != nil {
		return s.Backend.DeletePattern(ctx, id)
	}
	r, err := s.DB.ExecContext(ctx, `DELETE FROM patterns WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}
func ValidatePatternSearch(q model.PatternSearch) error {
	if err := q.Reference.Validate(); err != nil {
		return err
	}
	if q.Limit < 1 || q.Limit > 100 {
		return errors.New("limit must be 1–100")
	}
	return model.ValidateLabels(q.Labels)
}
func (s *Store) SearchPatterns(ctx context.Context, q model.PatternSearch) ([]model.PatternHit, error) {
	if err := ValidatePatternSearch(q); err != nil {
		return nil, err
	}
	if s.Backend != nil {
		return s.Backend.SearchPatterns(ctx, q)
	}
	query := `SELECT payload FROM patterns WHERE normalization=? AND aggregation=?`
	args := []any{q.Reference.Normalization, q.Reference.Aggregation}
	if q.Metric != "" {
		query += ` AND metric=?`
		args = append(args, q.Metric)
	}
	for k, v := range q.Labels {
		query += ` AND EXISTS(SELECT 1 FROM json_each(patterns.labels) WHERE key=? AND value=?)`
		args = append(args, k, v)
	}
	if !q.IncludeSelf {
		query += ` AND id!=?`
		args = append(args, q.Reference.ID)
	}
	rows, err := s.DB.QueryContext(ctx, query+` LIMIT 50001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	top := &patternHeap{}
	heap.Init(top)
	count := 0
	for rows.Next() {
		count++
		if count > 50000 {
			return nil, errors.New("SQLite vector scan exceeds 50000 patterns; filter metric/labels or use ClickHouse")
		}
		var payload string
		var p model.Pattern
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &p); err != nil {
			return nil, err
		}
		distance := 0.0
		for i, v := range p.Values {
			delta := float64(v) - float64(q.Reference.Values[i])
			distance += delta * delta
		}
		hit := model.PatternHit{Pattern: p.Summary(), Distance: math.Sqrt(distance)}
		if top.Len() < q.Limit {
			heap.Push(top, hit)
		} else if closer(hit, (*top)[0]) {
			(*top)[0] = hit
			heap.Fix(top, 0)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []model.PatternHit(*top)
	sort.Slice(out, func(i, j int) bool { return closer(out[i], out[j]) })
	return out, nil
}
func closer(a, b model.PatternHit) bool {
	if a.Distance == b.Distance {
		return a.Pattern.ID < b.Pattern.ID
	}
	return a.Distance < b.Distance
}

type patternHeap []model.PatternHit

func (h patternHeap) Len() int           { return len(h) }
func (h patternHeap) Less(i, j int) bool { return closer(h[j], h[i]) }
func (h patternHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *patternHeap) Push(value any)    { *h = append(*h, value.(model.PatternHit)) }
func (h *patternHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
