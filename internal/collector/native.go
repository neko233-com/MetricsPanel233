package collector

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const maxBody = 4 << 20

func metricKey(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}
func targetLabels(t model.Target, extra map[string]string) map[string]string {
	labels := map[string]string{"job": t.Name, "instance": t.URL}
	for k, v := range extra {
		labels[k] = v
	}
	for k, v := range t.Labels {
		labels[k] = v
	}
	return labels
}
func nativeSample(t model.Target, name string, value float64, labels map[string]string) model.Sample {
	return model.Sample{Name: name, Value: value, Labels: targetLabels(t, labels)}
}

func (c *Collector) collect(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	switch t.Kind {
	case "mysql":
		return collectMySQL(ctx, t, secrets)
	case "postgresql":
		return collectPostgres(ctx, t, secrets)
	case "redis":
		return collectRedis(ctx, t, secrets)
	case "clickhouse":
		return c.collectClickHouse(ctx, t, secrets)
	case "elasticsearch", "hadoop", "hdfs", "hive", "kafka", "spark", "flink":
		return c.collectJSON(ctx, t, secrets)
	default:
		body, header, err := c.readHTTP(ctx, t, secrets, http.MethodGet, t.URL, "")
		if err != nil {
			return nil, err
		}
		return ParseExposition(strings.NewReader(string(body)), header.Get("Content-Type"), t)
	}
}
func (c *Collector) readHTTP(ctx context.Context, t model.Target, secrets map[string]string, method, address, body string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0; q=0.9, text/plain; version=0.0.4; q=0.8, application/json; q=0.7")
	req.Header.Set("User-Agent", "MetricsPanel233/0.1")
	if t.Username != "" || secrets["password"] != "" {
		req.SetBasicAuth(t.Username, secrets["password"])
	}
	if token := secrets["bearer_token"]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("collector returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxBody {
		return nil, nil, errors.New("collector response exceeds 4 MiB")
	}
	return data, resp.Header, nil
}
func collectMySQL(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	u, _ := url.Parse(t.URL)
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = u.Host
	if u.Port() == "" {
		cfg.Addr = net.JoinHostPort(u.Hostname(), "3306")
	}
	cfg.User = t.Username
	cfg.Passwd = secrets["password"]
	cfg.DBName = t.Database
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	// require encrypts without verifying, like PostgreSQL sslmode=require.
	if t.TLSMode == "require" {
		cfg.TLSConfig = "skip-verify"
	} else if t.TLSMode == "verify-full" {
		cfg.TLSConfig = "true"
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("invalid MySQL connection settings")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Sample{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if number, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
			out = append(out, nativeSample(t, "mysql_global_status_"+metricKey(key), number, nil))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out = append(out, nativeSample(t, "mysql_up", 1, nil))
	return out, nil
}
func collectPostgres(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	u, _ := url.Parse(t.URL)
	u.User = url.UserPassword(t.Username, secrets["password"])
	u.Path = "/" + t.Database
	q := u.Query()
	mode := t.TLSMode
	if mode == "" {
		mode = "disable"
	}
	q.Set("sslmode", mode)
	q.Set("connect_timeout", "10")
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection settings")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `SELECT datname,numbackends,xact_commit,xact_rollback,blks_read,blks_hit,tup_returned,tup_fetched,tup_inserted,tup_updated,tup_deleted,conflicts,temp_files,temp_bytes,deadlocks,blk_read_time,blk_write_time FROM pg_stat_database WHERE datname IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	out := []model.Sample{}
	for rows.Next() {
		var name string
		values := make([]sql.NullFloat64, len(columns)-1)
		args := []any{&name}
		for i := range values {
			args = append(args, &values[i])
		}
		if err := rows.Scan(args...); err != nil {
			return nil, err
		}
		for i, value := range values {
			if value.Valid {
				out = append(out, nativeSample(t, "pg_stat_database_"+columns[i+1], value.Float64, map[string]string{"datname": name}))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return append(out, nativeSample(t, "pg_up", 1, nil)), nil
}
func collectRedis(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	u, _ := url.Parse(t.URL)
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "6379")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if u.Scheme == "rediss" {
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	reader := bufio.NewReader(io.LimitReader(conn, maxBody+1024))
	command := func(parts ...string) (string, error) {
		var request strings.Builder
		fmt.Fprintf(&request, "*%d\r\n", len(parts))
		for _, part := range parts {
			fmt.Fprintf(&request, "$%d\r\n%s\r\n", len(part), part)
		}
		if _, err := io.WriteString(conn, request.String()); err != nil {
			return "", err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
			return "", errors.New("invalid Redis response")
		}
		switch line[0] {
		case '+':
			return strings.TrimSuffix(line[1:], "\r\n"), nil
		case '-':
			return "", errors.New("Redis command rejected; check ACL permissions")
		case '$':
			size, err := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n"))
			if err != nil || size < 0 || size > maxBody {
				return "", errors.New("invalid Redis bulk response size")
			}
			data := make([]byte, size+2)
			if _, err = io.ReadFull(reader, data); err != nil {
				return "", err
			}
			if string(data[size:]) != "\r\n" {
				return "", errors.New("invalid Redis bulk terminator")
			}
			return string(data[:size]), nil
		default:
			return "", errors.New("unexpected Redis response")
		}
	}
	if t.Username != "" {
		if _, err = command("AUTH", t.Username, secrets["password"]); err != nil {
			return nil, errors.New("Redis AUTH failed")
		}
	} else if secrets["password"] != "" {
		if _, err = command("AUTH", secrets["password"]); err != nil {
			return nil, errors.New("Redis AUTH failed")
		}
	}
	info, err := command("INFO", "ALL")
	if err != nil {
		return nil, err
	}
	return parseRedisInfo(info, t)
}
func parseRedisInfo(info string, t model.Target) ([]model.Sample, error) {
	out := []model.Sample{}
	aliases := map[string]string{"used_memory": "memory_used_bytes", "used_memory_rss": "memory_used_rss_bytes", "used_memory_peak": "memory_used_peak_bytes", "maxmemory": "memory_max_bytes", "total_commands_processed": "commands_processed_total", "total_connections_received": "connections_received_total", "keyspace_hits": "keyspace_hits_total", "keyspace_misses": "keyspace_misses_total", "evicted_keys": "evicted_keys_total", "expired_keys": "expired_keys_total"}
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), ":")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		if number, err := strconv.ParseFloat(value, 64); err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
			name := key
			if alias, ok := aliases[key]; ok {
				name = alias
			}
			out = append(out, nativeSample(t, "redis_"+metricKey(name), number, nil))
			continue
		}
		labels := map[string]string{}
		prefix := ""
		if strings.HasPrefix(key, "db") {
			labels["db"] = key
			prefix = "redis_db_"
		} else if strings.HasPrefix(key, "cmdstat_") {
			labels["cmd"] = strings.TrimPrefix(key, "cmdstat_")
			prefix = "redis_command_"
		} else if strings.HasPrefix(key, "errorstat_") {
			labels["error"] = strings.TrimPrefix(key, "errorstat_")
			prefix = "redis_error_"
		}
		if prefix != "" {
			for _, part := range strings.Split(value, ",") {
				name, raw, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				number, err := strconv.ParseFloat(raw, 64)
				if err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
					out = append(out, nativeSample(t, prefix+metricKey(name), number, labels))
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Redis INFO returned no numeric metrics")
	}
	return append(out, nativeSample(t, "redis_up", 1, nil)), nil
}
func decodeJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("collector response is not valid JSON")
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return nil, errors.New("collector response contains trailing JSON")
	}
	return value, nil
}
func flattenMetrics(t model.Target, value any, prefix string, labels map[string]string, out *[]model.Sample, depth int) error {
	if depth > 32 {
		return errors.New("metric JSON exceeds 32 levels")
	}
	if len(*out) > 10000 {
		return errors.New("collector exceeds 10000 samples")
	}
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == "timestamp" || key == "status" || key == "name" || key == "modelerType" {
				continue
			}
			if err := flattenMetrics(t, value[key], prefix+"_"+key, labels, out, depth+1); err != nil {
				return err
			}
		}
	case json.Number:
		number, err := value.Float64()
		if err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
			// Retain the exact attribute path so sanitized names cannot alias.
			extra := map[string]string{"attribute": prefix}
			for key, value := range labels {
				extra[key] = value
			}
			*out = append(*out, nativeSample(t, metricKey(t.Kind+"_"+strings.TrimPrefix(prefix, "_")), number, extra))
		}
	}
	return nil
}
func (c *Collector) collectJSON(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	body, _, err := c.readHTTP(ctx, t, secrets, http.MethodGet, t.URL, "")
	if err != nil {
		return nil, err
	}
	value, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	out := []model.Sample{}
	if t.Kind == "flink" {
		entries, ok := value.([]any)
		if !ok {
			return nil, errors.New("Flink metrics must be an array")
		}
		ids := []string{}
		for _, entry := range entries {
			item, ok := entry.(map[string]any)
			if !ok {
				return nil, errors.New("invalid Flink metric")
			}
			id, _ := item["id"].(string)
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 1000 {
			return nil, errors.New("Flink metric discovery exceeds 1000 IDs")
		}
		// Discover first, then request bounded batches from this exact scope.
		for start := 0; start < len(ids); start += 100 {
			u, _ := url.Parse(t.URL)
			query := u.Query()
			query.Set("get", strings.Join(ids[start:min(start+100, len(ids))], ","))
			u.RawQuery = query.Encode()
			body, _, err := c.readHTTP(ctx, t, secrets, http.MethodGet, u.String(), "")
			if err != nil {
				return nil, err
			}
			data, err := decodeJSON(body)
			if err != nil {
				return nil, err
			}
			rows, ok := data.([]any)
			if !ok {
				return nil, errors.New("invalid Flink metric values")
			}
			for _, row := range rows {
				item, ok := row.(map[string]any)
				if !ok {
					continue
				}
				id, _ := item["id"].(string)
				raw, _ := item["value"].(string)
				number, err := strconv.ParseFloat(raw, 64)
				if id != "" && err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
					out = append(out, nativeSample(t, "flink_"+metricKey(id), number, map[string]string{"metric": id}))
				}
			}
		}
	} else {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("metric JSON must be an object")
		}
		switch t.Kind {
		case "hadoop", "hdfs":
			beans, ok := object["beans"].([]any)
			if !ok {
				return nil, errors.New("JMX response requires beans")
			}
			for _, raw := range beans {
				bean, ok := raw.(map[string]any)
				if !ok {
					return nil, errors.New("invalid JMX bean")
				}
				name, _ := bean["name"].(string)
				if name == "" {
					return nil, errors.New("JMX bean requires a name")
				}
				if err := flattenMetrics(t, bean, "", map[string]string{"mbean": name}, &out, 0); err != nil {
					return nil, err
				}
			}
		case "hive", "kafka":
			status, ok := object["status"].(json.Number)
			if !ok || status.String() != "200" {
				return nil, errors.New("Jolokia read failed")
			}
			values, ok := object["value"].(map[string]any)
			if !ok {
				return nil, errors.New("Jolokia read requires wildcard MBean values")
			}
			for bean, attributes := range values {
				if err := flattenMetrics(t, attributes, "", map[string]string{"mbean": bean}, &out, 0); err != nil {
					return nil, err
				}
			}
		case "elasticsearch":
			nodes, ok := object["nodes"].(map[string]any)
			if !ok {
				return nil, errors.New("Elasticsearch response requires nodes")
			}
			for id, node := range nodes {
				if err := flattenMetrics(t, node, "", map[string]string{"node": id}, &out, 0); err != nil {
					return nil, err
				}
			}
		default:
			if err := flattenMetrics(t, object, "", nil, &out, 0); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("collector returned no numeric metrics")
	}
	if len(out) > 10000 {
		return nil, errors.New("collector exceeds 10000 samples")
	}
	return out, nil
}
func (c *Collector) collectClickHouse(ctx context.Context, t model.Target, secrets map[string]string) ([]model.Sample, error) {
	query := `SELECT 'current' AS kind, metric, value FROM system.metrics UNION ALL SELECT 'async',metric,value FROM system.asynchronous_metrics UNION ALL SELECT 'events',event,value FROM system.events FORMAT JSON`
	body, _, err := c.readHTTP(ctx, t, secrets, http.MethodPost, t.URL, query)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			Kind   string      `json:"kind"`
			Metric string      `json:"metric"`
			Value  json.Number `json:"value"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("invalid ClickHouse metric JSON")
	}
	out := []model.Sample{}
	for _, row := range response.Data {
		number, err := row.Value.Float64()
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			continue
		}
		out = append(out, nativeSample(t, "clickhouse_"+metricKey(row.Kind)+"_"+metricKey(row.Metric), number, map[string]string{"metric": row.Metric}))
	}
	if len(out) == 0 {
		return nil, errors.New("ClickHouse returned no metrics")
	}
	return out, nil
}
