package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// ClickHouse uses a durable, partitioned ReplacingMergeTree with compressed,
// columnar data and server-side vectorized aggregation. SQLite retains only
// dashboards/collector configuration when this backend is selected.
type ClickHouse struct {
	URL, Database, User, Password string
	RetentionDays                 int
	Client                        *http.Client
}

func (c *ClickHouse) Ping(ctx context.Context) error {
	_, err := c.execute(ctx, "SELECT 1", nil, nil, false)
	return err
}

func OpenClickHouse(ctx context.Context, endpoint, database, user, password string, retention int) (*ClickHouse, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid ClickHouse HTTP URL")
	}
	if !regexpIdentifier(database) {
		return nil, errors.New("ClickHouse database must contain only letters, digits and underscores")
	}
	c := &ClickHouse{URL: strings.TrimRight(endpoint, "/"), Database: database, User: user, Password: password, RetentionDays: retention, Client: &http.Client{Timeout: 30 * time.Second}}
	if _, err = c.execute(ctx, "CREATE DATABASE IF NOT EXISTS "+database, nil, nil, false); err != nil {
		return nil, err
	}
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.samples (
name LowCardinality(String), labels_json String CODEC(ZSTD(3)), labels Map(String,String),
timestamp Int64 CODEC(Delta,ZSTD(3)), value Float64 CODEC(Gorilla,ZSTD(3)), version UInt64
) ENGINE=ReplacingMergeTree(version) PARTITION BY toDate(fromUnixTimestamp64Milli(timestamp))
ORDER BY (name,labels_json,timestamp) TTL fromUnixTimestamp64Milli(timestamp) + INTERVAL %d DAY
SETTINGS index_granularity=8192, fsync_after_insert=1, fsync_part_directory=1`, database, retention)
	if _, err = c.execute(ctx, ddl, nil, nil, false); err != nil {
		return nil, err
	}
	return c, nil
}
func regexpIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func (c *ClickHouse) execute(ctx context.Context, query string, params map[string]string, body []byte, insert bool) ([]byte, error) {
	u, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("max_execution_time", "20")
	q.Set("max_memory_usage", "536870912")
	q.Set("output_format_json_quote_64bit_integers", "0")
	for k, v := range params {
		q.Set("param_"+k, v)
	}
	if insert {
		q.Set("async_insert", "1")
		q.Set("wait_for_async_insert", "1")
		q.Set("async_insert_busy_timeout_ms", "200")
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", c.User)
	req.Header.Set("X-ClickHouse-Key", c.Password)
	response, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024*1024 {
		return nil, errors.New("ClickHouse result exceeds 64 MiB")
	}
	if response.StatusCode != 200 {
		msg := string(data)
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		return nil, fmt.Errorf("ClickHouse HTTP %d: %s", response.StatusCode, msg)
	}
	return data, nil
}
func (c *ClickHouse) Ingest(ctx context.Context, samples []model.Sample) error {
	if len(samples) == 0 || len(samples) > 10000 {
		return errors.New("batch must contain 1–10000 samples")
	}
	for _, s := range samples {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	now := time.Now()
	for i, s := range samples {
		if s.Timestamp == 0 {
			s.Timestamp = now.UnixMilli()
		}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		if err := enc.Encode(map[string]any{"name": s.Name, "labels_json": model.LabelsJSON(s.Labels), "labels": s.Labels, "timestamp": s.Timestamp, "value": s.Value, "version": uint64(now.UnixNano()) + uint64(i)}); err != nil {
			return err
		}
	}
	_, err := c.execute(ctx, "INSERT INTO "+c.Database+".samples FORMAT JSONEachRow", nil, body.Bytes(), true)
	return err
}
func decodeRows[T any](data []byte) ([]T, error) {
	out := []T{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var v T
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (c *ClickHouse) Metrics(ctx context.Context) ([]Metric, error) {
	data, err := c.execute(ctx, "SELECT name,toUInt32(uniqExact(labels_json)) AS series_count FROM "+c.Database+".samples GROUP BY name ORDER BY name FORMAT JSONEachRow", nil, nil, false)
	if err != nil {
		return nil, err
	}
	return decodeRows[Metric](data)
}
func (c *ClickHouse) Stats(ctx context.Context) (Stats, error) {
	query := `SELECT toInt64(uniqExact(tuple(name,labels_json))) AS series,toInt64(count()) AS samples,countIf(timestamp>={recent:Int64})/60.0 AS ingest_rate,toInt64(max(timestamp)) AS last_sample FROM ` + c.Database + `.samples FINAL FORMAT JSONEachRow`
	data, err := c.execute(ctx, query, map[string]string{"recent": strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)}, nil, false)
	if err != nil {
		return Stats{}, err
	}
	values, err := decodeRows[Stats](data)
	if err != nil {
		return Stats{}, err
	}
	v := Stats{}
	if len(values) > 0 {
		v = values[0]
	}
	data, err = c.execute(ctx, `SELECT toInt64(sum(bytes_on_disk)) AS storage_bytes FROM system.parts WHERE active AND database={db:String} AND table='samples' FORMAT JSONEachRow`, map[string]string{"db": c.Database}, nil, false)
	if err != nil {
		return v, err
	}
	parts, err := decodeRows[Stats](data)
	if len(parts) > 0 {
		v.StorageBytes = parts[0].StorageBytes
	}
	return v, err
}
func (c *ClickHouse) Prune(context.Context, int64) error { return nil } // MergeTree TTL removes expired parts asynchronously.
func (c *ClickHouse) conditions(start, end int64, matchers []Matcher) (string, map[string]string) {
	where := `timestamp>={start:Int64} AND timestamp<={end:Int64}`
	params := map[string]string{"start": strconv.FormatInt(start, 10), "end": strconv.FormatInt(end, 10)}
	for i, m := range matchers {
		key := fmt.Sprintf("m%d", i)
		value := fmt.Sprintf("v%d", i)
		field := "name"
		if m.Name != "__name__" {
			field = "labels[{" + key + ":String}]"
			params[key] = m.Name
		}
		params[value] = m.Value
		switch m.Type {
		case "=", "!=":
			where += " AND " + field + m.Type + "{" + value + ":String}"
		case "=~", "!~":
			prefix := ""
			if m.Type == "!~" {
				prefix = "NOT "
			}
			where += " AND " + prefix + "match(" + field + ",{" + value + ":String})"
			params[value] = "^(?:" + m.Value + ")$"
		}
	}
	return where, params
}
func (c *ClickHouse) LoadSeries(ctx context.Context, start, end int64, matchers []Matcher) ([]RawSeries, error) {
	where, params := c.conditions(start, end, matchers)
	data, err := c.execute(ctx, `SELECT name,labels_json,toInt64(timestamp) AS timestamp,value FROM `+c.Database+`.samples FINAL WHERE `+where+` ORDER BY name,labels_json,timestamp LIMIT 1000001 FORMAT JSONEachRow`, params, nil, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRows[struct {
		Name      string  `json:"name"`
		Labels    string  `json:"labels_json"`
		Timestamp int64   `json:"timestamp"`
		Value     float64 `json:"value"`
	}](data)
	if err != nil {
		return nil, err
	}
	if len(rows) > 1000000 {
		return nil, errors.New("PromQL selection exceeds 1000000 samples")
	}
	out := []RawSeries{}
	last := ""
	for _, r := range rows {
		key := r.Name + "\x00" + r.Labels
		if key != last {
			last = key
			if len(out) >= 10000 {
				return nil, errors.New("PromQL selection exceeds 10000 series")
			}
			var labels map[string]string
			if err = json.Unmarshal([]byte(r.Labels), &labels); err != nil {
				return nil, err
			}
			out = append(out, RawSeries{Name: r.Name, Labels: labels, Points: []model.Point{}})
		}
		out[len(out)-1].Points = append(out[len(out)-1].Points, model.Point{Timestamp: r.Timestamp, Value: r.Value})
	}
	return out, nil
}
func (c *ClickHouse) SelectSeries(ctx context.Context, start, end int64, matchers []Matcher) ([]RawSeries, error) {
	where, params := c.conditions(start, end, matchers)
	data, err := c.execute(ctx, `SELECT DISTINCT name,labels_json FROM `+c.Database+`.samples WHERE `+where+` ORDER BY name,labels_json LIMIT 10001 FORMAT JSONEachRow`, params, nil, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodeRows[struct {
		Name   string `json:"name"`
		Labels string `json:"labels_json"`
	}](data)
	if err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, errors.New("discovery exceeds 10000 series; narrow selectors")
	}
	out := make([]RawSeries, 0, len(rows))
	for _, row := range rows {
		var labels map[string]string
		if err = json.Unmarshal([]byte(row.Labels), &labels); err != nil {
			return nil, err
		}
		out = append(out, RawSeries{Name: row.Name, Labels: labels})
	}
	return out, nil
}
func (c *ClickHouse) Query(ctx context.Context, q model.Query) (model.QueryResult, error) {
	out := model.QueryResult{Metric: q.Metric, Aggregation: q.Aggregation, Start: q.Start, End: q.End, Step: q.Step, Series: []model.Series{}}
	if q.Start < 0 || q.End <= q.Start || q.End-q.Start > 31*24*3600000 || q.Step < 1000 || (q.End-q.Start)/q.Step > 2000 || q.Metric == "" || !model.ValidAggregation(q.Aggregation) {
		return out, errors.New("invalid query; range <=31 days, step >=1s, <=2000 buckets required")
	}
	if err := model.ValidateLabels(q.Labels); err != nil {
		return out, err
	}
	matchers := []Matcher{{Name: "__name__", Value: q.Metric, Type: "="}}
	keys := []string{}
	for k := range q.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		matchers = append(matchers, Matcher{Name: k, Value: q.Labels[k], Type: "="})
	}
	from := q.Start
	if q.Aggregation == "rate" {
		from -= q.Step
	}
	where, params := c.conditions(from, q.End, matchers)
	params["bucket_start"] = strconv.FormatInt(q.Start, 10)
	params["step"] = strconv.FormatInt(q.Step, 10)
	bucket := `{bucket_start:Int64}+intDiv(timestamp-{bucket_start:Int64},{step:Int64})*{step:Int64}`
	source := c.Database + `.samples FINAL WHERE ` + where
	inner := `SELECT labels_json,` + bucket + ` AS bucket,argMax(value,timestamp) AS value FROM ` + source + ` GROUP BY labels_json,bucket`
	if q.Aggregation == "rate" {
		window := `PARTITION BY labels_json ORDER BY timestamp ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW`
		inner = `SELECT labels_json,` + bucket + ` AS bucket,argMax(if(value>=prev_value,value-prev_value,value)/((timestamp-prev_timestamp)/1000.0),timestamp) AS value FROM (SELECT labels_json,timestamp,value,lagInFrame(timestamp,1,0) OVER (` + window + `) AS prev_timestamp,lagInFrame(value,1,0) OVER (` + window + `) AS prev_value FROM ` + source + `) WHERE timestamp>={bucket_start:Int64} AND prev_timestamp>0 AND timestamp>prev_timestamp GROUP BY labels_json,bucket`
	}
	query := inner
	if q.Aggregation != "last" && q.Aggregation != "rate" {
		query = `SELECT '{}' AS labels_json,bucket,` + q.Aggregation + `(value) AS value FROM (` + inner + `) GROUP BY bucket`
	}
	data, err := c.execute(ctx, query+` ORDER BY labels_json,bucket LIMIT 400201 FORMAT JSONEachRow`, params, nil, false)
	if err != nil {
		return out, err
	}
	rows, err := decodeRows[struct {
		Labels string  `json:"labels_json"`
		Bucket int64   `json:"bucket"`
		Value  float64 `json:"value"`
	}](data)
	if err != nil {
		return out, err
	}
	last := ""
	for _, r := range rows {
		if r.Labels != last {
			last = r.Labels
			if len(out.Series) >= 200 {
				return out, errors.New("query exceeds 200 series; add label filters")
			}
			var labels map[string]string
			if err = json.Unmarshal([]byte(r.Labels), &labels); err != nil {
				return out, err
			}
			out.Series = append(out.Series, model.Series{Labels: labels, Points: []model.Point{}})
		}
		out.Series[len(out.Series)-1].Points = append(out.Series[len(out.Series)-1].Points, model.Point{Timestamp: r.Bucket, Value: r.Value})
	}
	return out, nil
}
