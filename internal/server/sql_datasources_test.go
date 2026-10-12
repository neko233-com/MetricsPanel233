package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLSourceConfigurationBoundsAndCredentialPlacement(t *testing.T) {
	for _, item := range []struct{ kind, url, port, mode string }{{"mysql", "localhost:3306", "3306", "disable"}, {"mysql", "mariadb://[::1]/business", "3306", "disable"}, {"postgres", "postgresql://db.internal:5433/business", "5433", "verify-full"}, {postgresSourceID, "postgresql://db.internal:5433/business", "5433", "verify-full"}} {
		ds := model.DataSource{Type: item.kind, URL: item.url, User: "reader", Database: "business", JSONData: json.RawMessage(`{}`)}
		config, err := sqlSourceConfiguration(ds)
		require.NoError(t, err)
		assert.Equal(t, item.port, config.port)
		assert.Equal(t, item.mode, config.sslmode)
		assert.Equal(t, 10000, config.maxRows)
		assert.Equal(t, 2, config.maxOpen)
	}
	for _, item := range []struct{ url, settings string }{{"mysql://reader:super-secret@localhost/business", `{}`}, {"mysql://localhost:3306/business?password=secret", `{}`}, {"mysql://localhost:0", `{}`}, {"http://localhost:3306", `{}`}, {"db:3306", `{"maxRows":10001}`}, {"db:3306", `{"queryTimeout":0}`}, {"db:3306", `{"maxOpenConns":1,"maxIdleConns":2}`}, {"db:3306", `{"sslmode":"disable","tlsAuth":true}`}, {"db:3306", `{"sslmode":"insecure"}`}} {
		_, err := sqlSourceConfiguration(model.DataSource{Type: "mysql", URL: item.url, User: "reader", Database: "db", JSONData: json.RawMessage(item.settings)})
		require.Error(t, err, item.url)
		assert.NotContains(t, err.Error(), "super-secret")
	}
	config, err := sqlSourceConfiguration(model.DataSource{Type: "postgres", URL: "db:5432", User: "reader", JSONData: json.RawMessage(`{"database":"business","sslmode":"require","maxIdleConns":0}`)})
	require.NoError(t, err)
	assert.Equal(t, "business", config.database)
	assert.Zero(t, config.maxIdle)
	_, err = sqlTLSConfig(config, map[string]string{"tlsCACert": "secret-malformed-certificate"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-malformed-certificate")
	assert.Equal(t, "failed [redacted]", safeSQLError(errors.New("failed secret-233"), map[string]string{"password": "secret-233"}).Error())
}

func TestSQLSourceMacrosMatchDialectsAndDoNotTouchLiterals(t *testing.T) {
	query := backend.DataQuery{Interval: 15 * time.Second, TimeRange: backend.TimeRange{From: time.Unix(1700000000, 0), To: time.Unix(1700000600, 0)}}
	for _, kind := range []string{"mysql", "postgres"} {
		m := sqlMacroContext{kind: kind, query: query}
		statement, err := m.expand(`SELECT $__time(COALESCE(created, updated)), $__timeGroupAlias(created,'$__interval',0), $__unixEpochFrom(), $__unixEpochTo(), $__unixEpochNanoFrom(), $__interval_ms, $__from WHERE $__timeFilter(created) AND $__unixEpochFilter(epoch) AND $__unixEpochNanoFilter(nano)`)
		require.NoError(t, err)
		assert.NotContains(t, statement, "$__")
		assert.Contains(t, statement, "1700000000")
		assert.Contains(t, statement, "1700000600")
		assert.Contains(t, statement, "1700000000000000000")
		assert.Contains(t, statement, `AS "time"`)
		require.NotNil(t, m.fill)
		assert.Equal(t, data.FillModeValue, m.fill.Mode)
		assert.Equal(t, 15*time.Second, m.fillInterval)
		if kind == "mysql" {
			assert.Contains(t, statement, "UNIX_TIMESTAMP(COALESCE(created, updated)) AS time_sec")
			assert.Contains(t, statement, "BETWEEN FROM_UNIXTIME(1700000000)")
		} else {
			assert.Contains(t, statement, `COALESCE(created, updated) AS "time"`)
			assert.Contains(t, statement, "BETWEEN '2023-11-14T22:13:20Z'")
		}
		literal := `SELECT '$__time(x)', "literal $__time(x)" /* $__missing() */ -- $__missing()`
		unchanged, err := m.expand(literal)
		require.NoError(t, err)
		assert.Equal(t, literal, unchanged)
	}
	m := sqlMacroContext{kind: "postgres", query: query, timescale: true}
	result, err := m.expand(`SELECT $__timeGroupAlias(created,'250ms',previous), $$ $__missing() $$, $tag$ $__time(x) $tag$, $1`)
	require.NoError(t, err)
	assert.Contains(t, result, `time_bucket('250 milliseconds',created) AS "time"`)
	assert.Contains(t, result, `$$ $__missing() $$`)
	assert.Contains(t, result, `$tag$ $__time(x) $tag$`)
	assert.Contains(t, result, "$1")
	assert.Equal(t, data.FillModePrevious, m.fill.Mode)
	result, err = m.expand(`SELECT $__timeGroup(created,'1s'), value FROM points`)
	require.NoError(t, err)
	assert.Contains(t, result, `AS "time", value`)
	result, err = m.expand(`SELECT 'C:\' AS path, $__timeFrom(), $__time(COALESCE(created, /* (,) */ updated)) /* outer /* $__missing() */ nested */`)
	require.NoError(t, err)
	assert.Contains(t, result, `'C:\' AS path`)
	assert.Contains(t, result, `COALESCE(created, /* (,) */ updated)`)
	for _, statement := range []string{`SELECT $__missing(x)`, `SELECT $__timeFilter()`, `SELECT $__timeGroup(t,'0s')`, `SELECT $__timeGroup(t,'1s',NaN)`, `SELECT $__timeGroup(t,'1s') /* missing`, `SELECT '$__time(x)`} {
		_, err := m.expand(statement)
		require.Error(t, err, statement)
	}
}

func TestSQLRowsFrameNullableTypesAndLimits(t *testing.T) {
	storage, err := store.Open(filepath.Join(t.TempDir(), "rows.db"))
	require.NoError(t, err)
	defer storage.DB.Close()
	_, err = storage.DB.Exec(`CREATE TABLE typed_sql_fixture(i BIGINT,n DECIMAL,b BOOLEAN,ts TIMESTAMP,label TEXT,payload BLOB)`)
	require.NoError(t, err)
	_, err = storage.DB.Exec(`INSERT INTO typed_sql_fixture VALUES(?,?,?,?,?,?),(NULL,NULL,NULL,NULL,NULL,NULL)`, 7, "23.30", true, "2026-10-12T10:30:00Z", "orders", []byte{0, 1, 2})
	require.NoError(t, err)
	rows, err := storage.DB.Query(`SELECT * FROM typed_sql_fixture`)
	require.NoError(t, err)
	defer rows.Close()
	frame, err := sqlRowsFrame(context.Background(), rows, "A", 100)
	require.NoError(t, err)
	require.Equal(t, 2, frame.Rows())
	require.Len(t, frame.Fields, 6)
	assert.Equal(t, data.FieldTypeNullableInt64, frame.Fields[0].Type())
	assert.Equal(t, data.FieldTypeNullableFloat64, frame.Fields[1].Type())
	assert.Equal(t, data.FieldTypeNullableBool, frame.Fields[2].Type())
	assert.Equal(t, data.FieldTypeNullableTime, frame.Fields[3].Type())
	value, err := frame.Fields[1].NullableFloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, 23.3, *value)
	text, ok := frame.Fields[5].ConcreteAt(0)
	require.True(t, ok)
	assert.Equal(t, "AAEC", text)
	for _, field := range frame.Fields {
		_, ok := field.ConcreteAt(1)
		assert.False(t, ok)
	}
	rows, err = storage.DB.Query(`SELECT * FROM typed_sql_fixture`)
	require.NoError(t, err)
	_, err = sqlRowsFrame(context.Background(), rows, "A", 1)
	rows.Close()
	require.ErrorContains(t, err, "configured limit")
	rows, err = storage.DB.Query(`SELECT ? AS huge`, strings.Repeat("a", 1<<20+1))
	require.NoError(t, err)
	_, err = sqlRowsFrame(context.Background(), rows, "A", 10)
	rows.Close()
	require.ErrorContains(t, err, "1 MiB")
}

func TestSQLTimeSeriesDimensionsLegacyMetricEmptyAndFill(t *testing.T) {
	frame := data.NewFrame("A", data.NewField("time", nil, []*int64{ptrSQL(int64(0)), ptrSQL(int64(0)), ptrSQL(int64(2)), ptrSQL(int64(2))}), data.NewField("value", nil, []*float64{ptrSQL(2.0), ptrSQL(3.0), ptrSQL(4.0), nil}), data.NewField("metric", nil, []*string{ptrSQL("alpha"), ptrSQL("beta"), ptrSQL("alpha"), ptrSQL("beta")}))
	frame.RefID = "A"
	converted, err := sqlTimeSeries(context.Background(), frame, &data.FillMissing{Mode: data.FillModeValue, Value: 0}, time.Second, 100)
	require.NoError(t, err)
	require.Equal(t, 3, converted.Rows())
	require.Len(t, converted.Fields, 3)
	assert.Equal(t, "alpha", converted.Fields[1].Name)
	assert.Empty(t, converted.Fields[1].Labels)
	value, err := converted.Fields[1].NullableFloatAt(1)
	require.NoError(t, err)
	assert.Equal(t, 0.0, *value)
	empty := data.NewFrame("Empty", data.NewField("time", nil, []*time.Time{}), data.NewField("value", nil, []*float64{}), data.NewField("host", nil, []*string{}))
	converted, err = sqlTimeSeries(context.Background(), empty, &data.FillMissing{Mode: data.FillModeValue, Value: 0}, time.Second, 100)
	require.NoError(t, err)
	assert.Zero(t, converted.Rows())
	assert.Len(t, converted.Fields, 2)
	bad := data.NewFrame("Bad", data.NewField("time", nil, []*int64{ptrSQL(int64(2)), ptrSQL(int64(1))}), data.NewField("value", nil, []*float64{ptrSQL(1.0), ptrSQL(2.0)}))
	_, err = sqlTimeSeries(context.Background(), bad, nil, 0, 100)
	require.ErrorContains(t, err, "sorted")
	bounded := data.NewFrame("Bounded", data.NewField("time", nil, []time.Time{time.Unix(0, 0), time.Unix(100, 0)}), data.NewField("value", nil, []*float64{ptrSQL(1.0), ptrSQL(2.0)}))
	_, err = sqlTimeSeries(context.Background(), bounded, &data.FillMissing{Mode: data.FillModeNull}, time.Second, 2)
	require.ErrorContains(t, err, "limit")
	clock := make([]time.Time, 500)
	values := make([]float64, 500)
	dimensions := make([]string, 500)
	for i := range clock {
		clock[i] = time.Unix(int64(i), 0)
		values[i] = 1
		dimensions[i] = fmt.Sprintf("host-%d", i)
	}
	sparse := data.NewFrame("Sparse", data.NewField("time", nil, clock), data.NewField("value", nil, values), data.NewField("host", nil, dimensions))
	_, err = sqlTimeSeries(context.Background(), sparse, nil, 0, 10000)
	require.ErrorContains(t, err, "200000 cells")
}

func ptrSQL[T any](value T) *T { return &value }

func TestSQLSourceDiscoveryCRUDSecretsAndPoolInvalidation(t *testing.T) {
	storage, err := store.Open(filepath.Join(t.TempDir(), "sql-source.db"))
	require.NoError(t, err)
	defer storage.DB.Close()
	app := New(storage, "sql-source-token", 30)
	defer app.Plugins.Close()
	defer app.Live.Close()
	defer app.CloseSQLSources()
	handler := app.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer sql-source-token")
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	discovery := call("GET", "/api/plugins?core=1&type=datasource", "")
	require.Equal(t, 200, discovery.Code)
	assert.Contains(t, discovery.Body.String(), `"id":"mysql"`)
	assert.Contains(t, discovery.Body.String(), `"id":"grafana-postgresql-datasource"`)
	assert.Contains(t, discovery.Body.String(), `"aliasIDs":["postgres"]`)
	metadata := call("GET", "/api/plugins/mysql/settings", "")
	require.Equal(t, 200, metadata.Code)
	assert.Contains(t, metadata.Body.String(), `"builtIn":true`)
	assert.Contains(t, metadata.Body.String(), `"annotations":true`)
	for _, kind := range []string{"postgres", postgresSourceID} {
		metadata := call("GET", "/api/plugins/"+kind+"/settings", "")
		require.Equal(t, 200, metadata.Code)
		assert.Contains(t, metadata.Body.String(), `"id":"grafana-postgresql-datasource"`)
		uid := "builtin-" + kind
		created := call("POST", "/api/datasources", fmt.Sprintf(`{"uid":%q,"name":%q,"type":%q,"url":"127.0.0.1:1","user":"reader","database":"business","jsonData":{"sslmode":"disable","queryTimeout":1}}`, uid, kind, kind))
		require.Equal(t, 200, created.Code, created.Body.String())
		assert.Contains(t, created.Body.String(), `"type":"grafana-postgresql-datasource"`)
		// Imported dashboard targets may still use the old type; UID resolution
		// must route both through the builtin SQL backend rather than plugins.
		queried := call("POST", "/api/ds/query", fmt.Sprintf(`{"from":"1700000000000","to":"1700000004000","queries":[{"refId":"Alias","rawSql":"SELECT 1 AS value","format":"table","datasource":{"uid":%q,"type":"postgres"}}]}`, uid))
		require.Equal(t, 200, queried.Code, queried.Body.String())
		assert.Contains(t, queried.Body.String(), "dial tcp")
		assert.NotContains(t, queried.Body.String(), "plugin is not installed")
		for _, groupKind := range []string{"postgres", postgresSourceID} {
			path := "/apis/" + groupKind + ".datasource.grafana.app/v0alpha1/namespaces/default/connections/" + uid + "/query"
			queried := call("POST", path, fmt.Sprintf(`{"from":"1700000000000","to":"1700000004000","queries":[{"refId":"Alias","rawSql":"SELECT 1 AS value","format":"table","datasource":{"uid":%q,"type":"postgres"}}]}`, uid))
			require.Equal(t, 200, queried.Code, queried.Body.String())
			assert.Contains(t, queried.Body.String(), "dial tcp")
		}
		require.Equal(t, 502, call("GET", "/api/datasources/uid/"+uid+"/health", "").Code)
		require.Equal(t, 200, call("DELETE", "/api/datasources/uid/"+uid, "").Code)
	}
	created := call("POST", "/api/datasources", `{"uid":"builtin-mysql","name":"MySQL read","type":"mysql","url":"127.0.0.1:1","user":"reader","database":"business","jsonData":{"queryTimeout":1},"secureJsonData":{"password":"sql-private-password"}}`)
	require.Equal(t, 200, created.Code, created.Body.String())
	assert.NotContains(t, created.Body.String(), "sql-private-password")
	health := call("GET", "/api/datasources/uid/builtin-mysql/health", "")
	assert.Equal(t, 502, health.Code, health.Body.String())
	assert.NotContains(t, health.Body.String(), "sql-private-password")
	require.Len(t, app.sqlSources.entries, 1)
	old := app.sqlSources.entries["builtin-mysql"].db
	updated := call("PUT", "/api/datasources/uid/builtin-mysql", `{"name":"MySQL updated","type":"mysql","url":"127.0.0.1:1","user":"reader","database":"business","jsonData":{"queryTimeout":1},"secureJsonFields":{"password":true}}`)
	require.Equal(t, 200, updated.Code, updated.Body.String())
	assert.Empty(t, app.sqlSources.entries)
	require.ErrorContains(t, old.Ping(), "closed")
	secrets, err := storage.DataSourceSecrets(context.Background(), "builtin-mysql")
	require.NoError(t, err)
	assert.Equal(t, "sql-private-password", secrets["password"])
	var sealed []byte
	require.NoError(t, storage.DB.QueryRow(`SELECT secrets FROM datasources WHERE uid='builtin-mysql'`).Scan(&sealed))
	assert.NotContains(t, string(sealed), "sql-private-password")
	require.Equal(t, 200, call("DELETE", "/api/datasources/uid/builtin-mysql", "").Code)
}
