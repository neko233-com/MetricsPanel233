package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const patternColumns = `id,metric,labels,window_start AS start,window_end AS end,aggregation,normalization,mean,stddev,coverage,created_at`

func (c *ClickHouse) createPatterns(ctx context.Context) error {
	ddl := `CREATE TABLE IF NOT EXISTS ` + c.Database + `.patterns (
id String,metric LowCardinality(String),labels_json String CODEC(ZSTD(3)),labels Map(String,String),
window_start Int64,window_end Int64,aggregation LowCardinality(String),normalization LowCardinality(String),
embedding Array(Float32) CODEC(NONE),mean Float64,stddev Float64,coverage Float64,created_at Int64,version UInt64,
CONSTRAINT dimensions CHECK length(embedding)=64,
INDEX patterns_hnsw embedding TYPE vector_similarity('hnsw','L2Distance',64)
) ENGINE=ReplacingMergeTree(version) ORDER BY id SETTINGS fsync_after_insert=1,fsync_part_directory=1`
	_, err := c.execute(ctx, ddl, nil, nil, false)
	return err
}
func (c *ClickHouse) SavePatterns(ctx context.Context, patterns []model.Pattern) error {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	version := uint64(time.Now().UnixNano())
	for i, p := range patterns {
		row := map[string]any{"id": p.ID, "metric": p.Metric, "labels_json": model.LabelsJSON(p.Labels), "labels": p.Labels, "window_start": p.Start, "window_end": p.End, "aggregation": p.Aggregation, "normalization": p.Normalization, "embedding": p.Values, "mean": p.Mean, "stddev": p.StdDev, "coverage": p.Coverage, "created_at": p.CreatedAt, "version": version + uint64(i)}
		if p.Labels == nil {
			row["labels"] = map[string]string{}
		}
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	_, err := c.execute(ctx, `INSERT INTO `+c.Database+`.patterns FORMAT JSONEachRow`, nil, body.Bytes(), true)
	return err
}
func (c *ClickHouse) Patterns(ctx context.Context, limit int) ([]model.Pattern, error) {
	data, err := c.execute(ctx, `SELECT `+patternColumns+` FROM `+c.Database+`.patterns FINAL ORDER BY created_at DESC,id LIMIT {limit:UInt32} FORMAT JSONEachRow`, map[string]string{"limit": strconv.Itoa(limit)}, nil, false)
	if err != nil {
		return nil, err
	}
	return decodeRows[model.Pattern](data)
}
func (c *ClickHouse) Pattern(ctx context.Context, id string) (model.Pattern, error) {
	data, err := c.execute(ctx, `SELECT `+patternColumns+`,embedding AS values FROM `+c.Database+`.patterns FINAL WHERE id={id:String} LIMIT 1 FORMAT JSONEachRow`, map[string]string{"id": id}, nil, false)
	if err != nil {
		return model.Pattern{}, err
	}
	rows, err := decodeRows[model.Pattern](data)
	if err != nil {
		return model.Pattern{}, err
	}
	if len(rows) == 0 {
		return model.Pattern{}, sql.ErrNoRows
	}
	return rows[0], nil
}
func (c *ClickHouse) DeletePattern(ctx context.Context, id string) error {
	if _, err := c.Pattern(ctx, id); err != nil {
		return err
	}
	_, err := c.execute(ctx, `DELETE FROM `+c.Database+`.patterns WHERE id={id:String} SETTINGS mutations_sync=2`, map[string]string{"id": id}, nil, false)
	return err
}
func (c *ClickHouse) patternSearchSQL(q model.PatternSearch) (string, map[string]string) {
	vector, _ := json.Marshal(q.Reference.Values)
	params := map[string]string{"reference": string(vector), "normalization": q.Reference.Normalization, "aggregation": q.Reference.Aggregation, "limit": strconv.Itoa(q.Limit)}
	where := `normalization={normalization:String} AND aggregation={aggregation:String}`
	if q.Metric != "" {
		where += ` AND metric={metric:String}`
		params["metric"] = q.Metric
	}
	index := 0
	for k, v := range q.Labels {
		key := fmt.Sprintf("key%d", index)
		value := fmt.Sprintf("value%d", index)
		where += ` AND labels[{` + key + `:String}]={` + value + `:String}`
		params[key] = k
		params[value] = v
		index++
	}
	if !q.IncludeSelf {
		where += ` AND id!={id:String}`
		params["id"] = q.Reference.ID
	}
	settings := `vector_search_with_rescoring=1,vector_search_index_fetch_multiplier=4`
	if q.Exact {
		settings = `use_skip_indexes=0`
	}
	query := `SELECT ` + patternColumns + `,L2Distance(embedding,{reference:Array(Float32)}) AS distance FROM ` + c.Database + `.patterns FINAL WHERE ` + where + ` ORDER BY distance ASC LIMIT {limit:UInt32} SETTINGS ` + settings + ` FORMAT JSONEachRow`
	return query, params
}
func (c *ClickHouse) SearchPatterns(ctx context.Context, q model.PatternSearch) ([]model.PatternHit, error) {
	query, params := c.patternSearchSQL(q)
	data, err := c.execute(ctx, query, params, nil, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRows[struct {
		model.Pattern
		Distance float64 `json:"distance"`
	}](data)
	if err != nil {
		return nil, err
	}
	out := make([]model.PatternHit, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.PatternHit{Pattern: r.Pattern, Distance: r.Distance})
	}
	return out, nil
}

// PatternIndexPlan is used by the real-database integration verifier to prove
// HNSW is actually selected by the query planner rather than just present in DDL.
func (c *ClickHouse) PatternIndexPlan(ctx context.Context, q model.PatternSearch) (string, error) {
	query, params := c.patternSearchSQL(q)
	data, err := c.execute(ctx, "EXPLAIN indexes=1 "+query, params, nil, false)
	return string(data), err
}
