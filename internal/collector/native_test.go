package collector

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNativeHTTPProtocolsAndAtomicFailures(t *testing.T) {
	fixtures := []struct {
		kind, body, metric string
		value              float64
	}{
		{"hadoop", `{"beans":[{"name":"Hadoop:service=ResourceManager,name=JvmMetrics","MemHeapUsedM":233}]}`, "hadoop_memheapusedm", 233},
		{"hdfs", `{"beans":[{"name":"Hadoop:service=NameNode,name=FSNamesystem","CapacityUsed":233}]}`, "hdfs_capacityused", 233},
		{"hive", `{"status":200,"value":{"metrics:name=connections":{"Count":233}}}`, "hive_count", 233},
		{"kafka", `{"status":200,"value":{"kafka.server:type=BrokerTopicMetrics,name=MessagesInPerSec":{"Count":233}}}`, "kafka_count", 233},
		{"spark", `{"gauges":{"driver.JVM.heap.used":{"value":233}},"counters":{},"timers":{}}`, "spark_gauges_driver_jvm_heap_used_value", 233},
		{"elasticsearch", `{"nodes":{"node-233":{"name":"data","jvm":{"mem":{"heap_used_in_bytes":233}}}}}`, "elasticsearch_jvm_mem_heap_used_in_bytes", 233},
		{"flink", `[{"id":"Status.JVM.Memory.Heap.Used","value":"233"}]`, "flink_status_jvm_memory_heap_used", 233},
		{"clickhouse", `{"data":[{"kind":"current","metric":"Query","value":"233"}]}`, "clickhouse_current_query", 233},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.kind, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
			require.NoError(t, err)
			defer db.DB.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				assert.True(t, ok)
				assert.Equal(t, "metrics", user)
				assert.Equal(t, "secret", password)
				if fixture.kind == "clickhouse" {
					assert.Equal(t, "POST", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, fixture.body)
			}))
			defer server.Close()
			target, err := db.SaveTarget(context.Background(), model.Target{Name: fixture.kind, Kind: fixture.kind, URL: server.URL, Username: "metrics", SecureSettings: map[string]string{"password": "secret"}, IntervalSeconds: 5, Labels: map[string]string{"cluster": "prod"}})
			require.NoError(t, err)
			count, err := New(db).Scrape(context.Background(), target)
			require.NoError(t, err)
			assert.Equal(t, 1, count)
			result, err := db.Query(context.Background(), model.Query{Metric: fixture.metric, Start: time.Now().Add(-time.Minute).UnixMilli(), End: time.Now().UnixMilli() + 1, Step: 1000, Aggregation: "last"})
			require.NoError(t, err)
			require.Len(t, result.Series, 1)
			assert.Equal(t, fixture.value, result.Series[0].Points[0].Value)
			assert.Equal(t, "prod", result.Series[0].Labels["cluster"])
		})
	}
	t.Run("collision safe attributes", func(t *testing.T) {
		value, err := decodeJSON([]byte(`{"a.b":233,"a_b":234}`))
		require.NoError(t, err)
		out := []model.Sample{}
		require.NoError(t, flattenMetrics(model.Target{Kind: "spark"}, value, "", nil, &out, 0))
		require.Len(t, out, 2)
		assert.Equal(t, out[0].Name, out[1].Name)
		assert.NotEqual(t, out[0].Labels["attribute"], out[1].Labels["attribute"])
		value, err = decodeJSON([]byte(`{"a_b":{"c":233},"a":{"b_c":234},"literal/slash":{"~tilde":235}}`))
		require.NoError(t, err)
		out = nil
		require.NoError(t, flattenMetrics(model.Target{Kind: "spark"}, value, "", nil, &out, 0))
		require.Len(t, out, 3)
		assert.Equal(t, out[0].Name, out[1].Name)
		assert.Equal(t, "/a/b_c", out[0].Labels["attribute"])
		assert.Equal(t, "/a_b/c", out[1].Labels["attribute"])
		assert.Equal(t, "/literal~1slash/~0tilde", out[2].Labels["attribute"])
	})
	t.Run("failed Jolokia is not healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":403,"value":{"fake":{"Count":233}}}`)
		}))
		defer server.Close()
		_, err := New(nil).collectJSON(context.Background(), model.Target{Kind: "kafka", URL: server.URL}, nil)
		require.ErrorContains(t, err, "Jolokia read failed")
	})
}
func TestRedisRESPAuthenticationInfoAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	done := make(chan struct{})
	commands := make(chan string, 3)
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for _, response := range []string{"+OK\r\n", ""} {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var parts int
			_, _ = fmt.Sscanf(line, "*%d", &parts)
			args := []string{}
			for i := 0; i < parts; i++ {
				_, _ = reader.ReadString('\n')
				line, _ := reader.ReadString('\n')
				args = append(args, strings.TrimSuffix(line, "\r\n"))
			}
			commands <- strings.Join(args, " ")
			if response == "" {
				info := "used_memory:233\r\nconnected_clients:2\r\ndb0:keys=3,expires=1,avg_ttl=0\r\ncmdstat_get:calls=4,usec=5,usec_per_call=1.25\r\n"
				response = fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)
			}
			_, _ = fmt.Fprint(conn, response)
		}
	}()
	samples, err := collectRedis(context.Background(), model.Target{Name: "cache", Kind: "redis", URL: "redis://" + listener.Addr().String(), Username: "metrics"}, map[string]string{"password": "secret"})
	require.NoError(t, err)
	assert.Equal(t, "AUTH metrics secret", <-commands)
	assert.Equal(t, "INFO ALL", <-commands)
	byName := map[string]model.Sample{}
	for _, sample := range samples {
		byName[sample.Name] = sample
	}
	assert.Equal(t, 233.0, byName["redis_memory_used_bytes"].Value)
	assert.Equal(t, "db0", byName["redis_db_keys"].Labels["db"])
	assert.Equal(t, 4.0, byName["redis_command_calls"].Value)
	assert.Equal(t, "get", byName["redis_command_calls"].Labels["cmd"])
	assert.Equal(t, 1.0, byName["redis_up"].Value)
	<-done
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer blocked.Close()
	released := make(chan struct{})
	go func() {
		conn, err := blocked.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
		buffer := make([]byte, 256)
		for {
			if _, err = reader.Read(buffer); err != nil {
				close(released)
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = collectRedis(ctx, model.Target{URL: "redis://" + blocked.Addr().String()}, nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("Redis socket remained open")
	}
}
func TestOpenMetricsExemplarsTimestampsAndBounds(t *testing.T) {
	text := "# TYPE requests counter\nrequests_total{service=\"api\"} 233 1.5 # {trace_id=\"abc\"} 1 1.5\n# TYPE memory gauge\nmemory 234\n# EOF\n"
	samples, err := ParseExposition(strings.NewReader(text), "application/openmetrics-text; version=1.0.0", model.Target{Name: "go", URL: "http://api/metrics"})
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, "requests_total", samples[0].Name)
	assert.Equal(t, int64(1500), samples[0].Timestamp)
	assert.Equal(t, 233.0, samples[0].Value)
	_, err = ParseExposition(strings.NewReader(strings.TrimSuffix(text, "# EOF\n")), "application/openmetrics-text; version=1.0.0", model.Target{})
	require.Error(t, err)
	_, err = ParseExposition(strings.NewReader(`{"not":"metrics"}`), "application/json", model.Target{})
	require.Error(t, err)
}

func TestProtobufClassicMetricsAndExplicitNativeHistogramFailure(t *testing.T) {
	var buffer bytes.Buffer
	format := expfmt.NewFormat(expfmt.TypeProtoDelim)
	encoder := expfmt.NewEncoder(&buffer, format)
	require.NoError(t, encoder.Encode(&dto.MetricFamily{Name: proto.String("duration_seconds"), Type: dto.MetricType_HISTOGRAM.Enum(), Metric: []*dto.Metric{{Histogram: &dto.Histogram{SampleCount: proto.Uint64(3), SampleSum: proto.Float64(0.25), Bucket: []*dto.Bucket{{UpperBound: proto.Float64(0.1), CumulativeCount: proto.Uint64(2)}}}}}}))
	samples, err := ParseExposition(&buffer, string(format), model.Target{Name: "proto"})
	require.NoError(t, err)
	require.Len(t, samples, 4)
	buffer.Reset()
	require.NoError(t, encoder.Encode(&dto.MetricFamily{Name: proto.String("native_seconds"), Type: dto.MetricType_HISTOGRAM.Enum(), Metric: []*dto.Metric{{Histogram: &dto.Histogram{Schema: proto.Int32(0), ZeroThreshold: proto.Float64(0.001), SampleCount: proto.Uint64(0), SampleSum: proto.Float64(0)}}}}))
	_, err = ParseExposition(&buffer, string(format), model.Target{})
	require.ErrorContains(t, err, "native histograms require histogram storage")
}
