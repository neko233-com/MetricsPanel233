package server_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func otlpFixture() *collectormetrics.ExportMetricsServiceRequest {
	now := uint64(time.Now().UnixNano())
	return &collectormetrics.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "api"}},
			}}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope: &commonpb.InstrumentationScope{Name: "test"},
				Metrics: []*metricspb.Metric{{
					Name: "http.server.requests",
					Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
						TimeUnixNano: now,
						Attributes: []*commonpb.KeyValue{{
							Key: "route", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "/orders"}},
						}},
						Value: &metricspb.NumberDataPoint_AsInt{AsInt: 23},
					}}}},
				}},
			}},
		}},
	}
}

func TestOTLPMetricsAcceptsProtobufAndJSON(t *testing.T) {
	store, handler := setup(t, "")
	for _, test := range []struct {
		name        string
		contentType string
		body        func(*collectormetrics.ExportMetricsServiceRequest) []byte
	}{
		{"protobuf", "application/x-protobuf", func(request *collectormetrics.ExportMetricsServiceRequest) []byte {
			data, err := proto.Marshal(request)
			require.NoError(t, err)
			return data
		}},
		{"json", "application/json", func(request *collectormetrics.ExportMetricsServiceRequest) []byte {
			data, err := protojson.Marshal(request)
			require.NoError(t, err)
			return data
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/metrics", bytes.NewReader(test.body(otlpFixture())))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, 200, response.Code, response.Body.String())
		})
	}
	metrics, err := store.Metrics(context.Background())
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "http_server_requests", metrics[0].Name)
	now := time.Now().UnixMilli()
	result, err := store.Query(context.Background(), model.Query{
		Metric: "http_server_requests", Start: now - 60000, End: now + 1000,
		Step: 1000, Aggregation: "last", Labels: map[string]string{"service_name": "api", "route": "/orders"},
	})
	require.NoError(t, err)
	require.Len(t, result.Series, 1)
	require.Len(t, result.Series[0].Points, 1)
	assert.Equal(t, 23.0, result.Series[0].Points[0].Value)
}
