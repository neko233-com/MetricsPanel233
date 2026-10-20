package server_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func remoteWriteFixture(timestamp int64) *prompb.WriteRequest {
	return &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{
		Labels: []prompb.Label{
			{Name: "__name__", Value: "remote_write_total"},
			{Name: "job", Value: "node"},
			{Name: "instance", Value: "host:9100"},
		},
		Samples: []prompb.Sample{{Value: 42, Timestamp: timestamp}},
	}}}
}

func TestRemoteWriteAcceptsSnappyAndRawProtobuf(t *testing.T) {
	store, handler := setup(t, "")
	now := time.Now().UnixMilli()
	raw, err := proto.Marshal(remoteWriteFixture(now))
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		path     string
		encoding string
		body     []byte
	}{
		{"snappy", "/api/v1/write", "snappy", snappy.Encode(nil, raw)},
		{"raw", "/prometheus/api/v1/write", "", raw},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", test.path, bytes.NewReader(test.body))
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, 204, response.Code, response.Body.String())
			assert.Equal(t, "1", response.Header().Get("X-Accepted-Samples"))
		})
	}
	result, err := store.Query(context.Background(), model.Query{
		Metric: "remote_write_total", Start: now - 60000, End: now + 1000,
		Step: 1000, Aggregation: "last",
		Labels: map[string]string{"job": "node", "instance": "host:9100"},
	})
	require.NoError(t, err)
	require.Len(t, result.Series, 1)
	require.Len(t, result.Series[0].Points, 1)
	assert.Equal(t, 42.0, result.Series[0].Points[0].Value)
}

func TestRemoteWriteRejectsMalformedAndUnsupportedBodies(t *testing.T) {
	_, handler := setup(t, "")
	for _, test := range []struct {
		name     string
		encoding string
		body     []byte
		status   int
	}{
		{"invalid snappy", "snappy", []byte("not snappy"), 400},
		{"invalid protobuf", "", []byte("not protobuf"), 400},
		{"unsupported encoding", "gzip", []byte("body"), 415},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/v1/write", bytes.NewReader(test.body))
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code, response.Body.String())
		})
	}
}

func TestRemoteWriteSkipsSeriesWithoutMetricName(t *testing.T) {
	store, handler := setup(t, "")
	now := time.Now().UnixMilli()
	raw, err := proto.Marshal(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{
		Labels:  []prompb.Label{{Name: "__name__", Value: ""}},
		Samples: []prompb.Sample{{Value: 1, Timestamp: now}},
	}}})
	require.NoError(t, err)
	request := httptest.NewRequest("POST", "/api/v1/write", bytes.NewReader(raw))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, 204, response.Code, response.Body.String())
	assert.Equal(t, "0", response.Header().Get("X-Accepted-Samples"))
	metrics, err := store.Metrics(context.Background())
	require.NoError(t, err)
	assert.Empty(t, metrics)
}
