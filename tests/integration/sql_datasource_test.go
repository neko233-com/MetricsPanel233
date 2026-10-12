//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A local browser verification can reuse this one live fixture. CI does not
// set the hook, and the hold is bounded when the browser never connects.
func (e environment) waitSQLBrowserQA() {
	path := os.Getenv("METRICSPANEL_SQL_QA_READY_FILE")
	if path == "" {
		return
	}
	ready, err := json.Marshal(map[string]string{"url": e.address("sqlite"), "clickhouse_url": e.address("clickhouse-app"), "token": token, "created_at": time.Now().UTC().Format(time.RFC3339Nano)})
	require.NoError(e.t, err)
	require.NoError(e.t, os.WriteFile(path, ready, 0600))
	e.t.Logf("single-container SQL browser fixture ready; waiting for %s.done (maximum 3 minutes)", path)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path + ".done"); err == nil {
			e.t.Log("SQL browser verification released the fixture")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Log("SQL browser fixture hold expired; continuing normal cleanup")
}

func (e environment) verifySQLDataSources(address string, create bool) {
	e.t.Helper()
	if create {
		out, err := e.composeInput(`CREATE TABLE IF NOT EXISTS business.metricspanel_sql_fixture(id INT PRIMARY KEY,value DECIMAL(10,2));
INSERT INTO business.metricspanel_sql_fixture VALUES(1,23.30) ON DUPLICATE KEY UPDATE value=23.30;
GRANT UPDATE ON business.metricspanel_sql_fixture TO 'exporter'@'127.0.0.1';
`, "exec", "-T", "suite", "mariadb", "--socket=/run/mysqld/mysqld.sock", "--batch")
		require.NoError(e.t, err, out)
		out, err = e.composeInput(`CREATE TABLE IF NOT EXISTS public.metricspanel_sql_fixture(id INT PRIMARY KEY,value NUMERIC(10,2));
INSERT INTO public.metricspanel_sql_fixture VALUES(1,23.30) ON CONFLICT(id) DO UPDATE SET value=23.30;
GRANT SELECT,UPDATE ON public.metricspanel_sql_fixture TO metrics;
`, "exec", "-T", "suite", "su-exec", "postgres", "psql", "-v", "ON_ERROR_STOP=1", "-d", "postgres")
		require.NoError(e.t, err, out)
	}
	for _, source := range []struct{ kind, host, user, database, password string }{
		{"mysql", "127.0.0.1:3306", "exporter", "business", "integration-exporter-password"},
		{"postgres", "127.0.0.1:5432", "metrics", "postgres", "integration-postgres-password"},
	} {
		uid := "docker-builtin-" + source.kind
		if create {
			raw := e.must(address, "POST", "/api/datasources", model.DataSourceInput{DataSource: model.DataSource{UID: uid, Name: "Builtin " + source.kind, Type: source.kind, URL: source.host, User: source.user, Database: source.database, JSONData: json.RawMessage(`{"sslmode":"disable","queryTimeout":1,"maxRows":100,"maxOpenConns":2,"maxIdleConns":1}`)}, SecureJSONData: map[string]string{"password": source.password}})
			assert.NotContains(e.t, string(raw), source.password)
		}
		var persisted model.DataSource
		raw := e.must(address, "GET", "/api/datasources/uid/"+uid, nil)
		require.NoError(e.t, json.Unmarshal(raw, &persisted))
		canonicalType := source.kind
		if source.kind == "postgres" {
			canonicalType = "grafana-postgresql-datasource"
		}
		assert.Equal(e.t, canonicalType, persisted.Type)
		require.True(e.t, persisted.SecureJSONFields["password"])
		assert.NotContains(e.t, string(raw), source.password)
		e.must(address, "GET", "/api/datasources/uid/"+uid+"/health", nil)
		query := func(statement, format string) backend.DataResponse {
			payload := map[string]any{"from": "1700000000000", "to": "1700000004000", "queries": []any{map[string]any{"refId": "SQL", "rawSql": statement, "format": format, "intervalMs": 1000, "datasource": map[string]string{"uid": uid, "type": source.kind}}}}
			var result backend.QueryDataResponse
			raw := e.must(address, "POST", "/api/ds/query", payload)
			assert.NotContains(e.t, string(raw), source.password)
			require.NoError(e.t, json.Unmarshal(raw, &result))
			return result.Responses["SQL"]
		}
		statement := `SELECT 7 AS integer_value,CAST(23.30 AS DECIMAL(10,2)) AS decimal_value,CAST(NULL AS SIGNED) AS absent_value,'orders' AS label,CAST('2023-11-14 22:13:20' AS DATETIME) AS created`
		if source.kind == "postgres" {
			statement = `SELECT 7::bigint AS integer_value,23.30::numeric AS decimal_value,NULL::double precision AS absent_value,'orders'::text AS label,TIMESTAMPTZ '2023-11-14 22:13:20+00' AS created`
		}
		table := query(statement, "table")
		require.NoError(e.t, table.Error)
		require.Len(e.t, table.Frames, 1)
		require.Equal(e.t, 1, table.Frames[0].Rows())
		assert.Equal(e.t, "SQL", table.Frames[0].RefID)
		value, err := table.Frames[0].Fields[1].FloatAt(0)
		require.NoError(e.t, err)
		assert.Equal(e.t, 23.3, value)
		_, present := table.Frames[0].Fields[2].ConcreteAt(0)
		assert.False(e.t, present)
		assert.True(e.t, table.Frames[0].Fields[4].Type() == data.FieldTypeTime || table.Frames[0].Fields[4].Type() == data.FieldTypeNullableTime)
		points := `(SELECT FROM_UNIXTIME(1700000000) AS created,1.5 AS value,'alpha' AS host UNION ALL SELECT FROM_UNIXTIME(1700000000),10.0,'beta' UNION ALL SELECT FROM_UNIXTIME(1700000002),3.5,'alpha' UNION ALL SELECT FROM_UNIXTIME(1700000002),30.0,'beta') AS points`
		if source.kind == "postgres" {
			points = `(VALUES(to_timestamp(1700000000),1.5::double precision,'alpha'::text),(to_timestamp(1700000000),10.0,'beta'),(to_timestamp(1700000002),3.5,'alpha'),(to_timestamp(1700000002),30.0,'beta')) AS points(created,value,host)`
		}
		statement = `SELECT $__timeGroupAlias(created,'1s',0),SUM(value) AS value,host FROM ` + points + ` WHERE $__timeFilter(created) GROUP BY time,host ORDER BY time,host`
		series := query(statement, "time_series")
		require.NoError(e.t, series.Error)
		require.Len(e.t, series.Frames, 1)
		require.Equal(e.t, 3, series.Frames[0].Rows())
		require.Len(e.t, series.Frames[0].Fields, 3)
		assert.Equal(e.t, "SQL", series.Frames[0].RefID)
		assert.Equal(e.t, data.FrameTypeTimeSeriesWide, series.Frames[0].Meta.Type)
		for _, field := range series.Frames[0].Fields[1:] {
			assert.Contains(e.t, []string{"alpha", "beta"}, field.Labels["host"])
			value, err := field.FloatAt(1)
			require.NoError(e.t, err)
			assert.Zero(e.t, value)
		}
		legacy := query(`SELECT 1700000000 AS time,233.0 AS value,'orders' AS metric`, "time_series")
		require.NoError(e.t, legacy.Error)
		assert.Equal(e.t, "orders", legacy.Frames[0].Fields[1].Name)
		assert.Empty(e.t, legacy.Frames[0].Fields[1].Labels)
		empty := query(`SELECT 1700000000 AS time,233.0 AS value,'orders' AS metric WHERE 1=0`, "time_series")
		require.NoError(e.t, empty.Error)
		assert.Zero(e.t, empty.Frames[0].Rows())
		write := query(`UPDATE metricspanel_sql_fixture SET value=999 WHERE id=1`, "table")
		require.Error(e.t, write.Error, "query transaction must enforce read-only even with UPDATE grant")
		unchanged := query(`SELECT value FROM metricspanel_sql_fixture WHERE id=1`, "table")
		require.NoError(e.t, unchanged.Error)
		value, err = unchanged.Frames[0].Fields[0].FloatAt(0)
		require.NoError(e.t, err)
		assert.Equal(e.t, 23.3, value)
		invalid := query(`SELECT $__timeFilter()`, "table")
		require.ErrorContains(e.t, invalid.Error, "column")
		start := time.Now()
		slow := `SELECT SLEEP(3) AS delay`
		if source.kind == "postgres" {
			slow = `SELECT pg_sleep(3) AS delay`
		}
		timed := query(slow, "table")
		require.Error(e.t, timed.Error)
		assert.Less(e.t, time.Since(start), 2500*time.Millisecond, "SQL query timeout must cancel the database operation")
		e.must(address, "GET", "/api/datasources/uid/"+uid+"/health", nil)
		// The native datasource API also carries the same query as JSON chunks.
		payload := map[string]any{"from": "1700000000000", "to": "1700000004000", "queries": []any{map[string]any{"refId": "Chunk", "rawSql": `SELECT 233 AS value`, "format": "table", "datasource": map[string]string{"uid": uid, "type": source.kind}}}}
		body, err := json.Marshal(payload)
		require.NoError(e.t, err)
		request, err := http.NewRequest("POST", fmt.Sprintf("%s/apis/%s.datasource.grafana.app/v0alpha1/namespaces/default/connections/%s/query", address, source.kind, uid), bytes.NewReader(body))
		require.NoError(e.t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/jsonl")
		response, err := e.client.Do(request)
		require.NoError(e.t, err)
		chunks, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(e.t, err)
		require.Equal(e.t, 200, response.StatusCode, string(chunks))
		assert.Contains(e.t, response.Header.Get("Content-Type"), "text/jsonl")
		assert.Contains(e.t, string(chunks), `"refId":"Chunk"`)
		assert.Contains(e.t, string(chunks), "233")
		assert.NotContains(e.t, string(chunks), `"error"`)
		// SQL frames participate in the existing server-side expression graph.
		var graph backend.QueryDataResponse
		raw = e.must(address, "POST", "/api/ds/query", map[string]any{"from": "1700000000000", "to": "1700000004000", "queries": []any{
			map[string]any{"refId": "A", "rawSql": `SELECT 1700000000 AS time,233.0 AS value`, "format": "time_series", "datasource": map[string]string{"uid": uid}},
			map[string]any{"refId": "B", "type": "reduce", "expression": "A", "reducer": "last", "datasource": map[string]string{"uid": "__expr__"}},
			map[string]any{"refId": "C", "type": "math", "expression": "$B * 2", "datasource": map[string]string{"uid": "__expr__"}},
		}})
		require.NoError(e.t, json.Unmarshal(raw, &graph))
		require.NoError(e.t, graph.Responses["C"].Error)
		value, err = graph.Responses["C"].Frames[0].Fields[0].FloatAt(0)
		require.NoError(e.t, err)
		assert.Equal(e.t, 466.0, value)
		if create {
			persisted.JSONData = json.RawMessage(`{"sslmode":"disable","queryTimeout":1,"maxRows":2,"maxOpenConns":2,"maxIdleConns":1}`)
			e.must(address, "PUT", "/api/datasources/uid/"+uid, model.DataSourceInput{DataSource: persisted})
			limited := query(`SELECT 1 AS value UNION ALL SELECT 2 UNION ALL SELECT 3`, "table")
			require.ErrorContains(e.t, limited.Error, "limit")
			require.NoError(e.t, json.Unmarshal(e.must(address, "GET", "/api/datasources/uid/"+uid, nil), &persisted))
			persisted.JSONData = json.RawMessage(`{"sslmode":"disable","queryTimeout":1,"maxRows":100,"maxOpenConns":2,"maxIdleConns":1}`)
			e.must(address, "PUT", "/api/datasources/uid/"+uid, model.DataSourceInput{DataSource: persisted})
			e.must(address, "GET", "/api/datasources/uid/"+uid+"/health", nil)
		}
		e.t.Logf("real builtin %s SQL datasource: table/null/time/decimal, macros/dimensions/fill, readonly/cancellation, JSONL, expressions and encrypted credential persistence passed", source.kind)
	}
	var installed []model.Plugin
	require.NoError(e.t, json.Unmarshal(e.must(address, "GET", "/api/v1/plugins", nil), &installed))
	for _, plugin := range installed {
		assert.False(e.t, strings.EqualFold(plugin.ID, "mysql") || strings.EqualFold(plugin.ID, "postgres") || strings.EqualFold(plugin.ID, "grafana-postgresql-datasource"), "builtin SQL must not require installed plugin packages")
	}
}
