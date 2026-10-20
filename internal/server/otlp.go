package server

import (
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const otlpBodyLimit = 16 << 20

// OTLP/HTTP metrics receiver. Prometheus scraping and native SQL/Redis
// collectors remain separate; all three write into the same metric store.
func (s *Server) otlpMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, errors.New("OTLP metrics endpoint accepts POST only"))
		return
	}
	body, err := readOTLPBody(r)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	jsonRequest := strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "json")
	var request collectormetrics.ExportMetricsServiceRequest
	if jsonRequest {
		if err := protojson.Unmarshal(body, &request); err != nil {
			fail(w, http.StatusBadRequest, fmt.Errorf("invalid OTLP JSON: %w", err))
			return
		}
	} else if err := proto.Unmarshal(body, &request); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("invalid OTLP protobuf: %w", err))
		return
	}
	samples := otlpSamples(request.ResourceMetrics)
	if len(samples) > 0 {
		for offset := 0; offset < len(samples); offset += 10000 {
			end := min(offset+10000, len(samples))
			if err := s.Store.Ingest(r.Context(), samples[offset:end]); err != nil {
				fail(w, http.StatusBadRequest, err)
				return
			}
		}
	}
	if jsonRequest {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	response, _ := proto.Marshal(&collectormetrics.ExportMetricsServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(response)
}

func readOTLPBody(r *http.Request) ([]byte, error) {
	raw := io.LimitReader(r.Body, otlpBodyLimit+1)
	var reader io.Reader = raw
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, err := gzip.NewReader(raw)
		if err != nil {
			return nil, errors.New("invalid gzip OTLP body")
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	data, err := io.ReadAll(io.LimitReader(reader, otlpBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > otlpBodyLimit {
		return nil, errors.New("OTLP request exceeds 16 MiB")
	}
	return data, nil
}

func otlpSamples(resources []*metricspb.ResourceMetrics) []model.Sample {
	// ResourceMetrics lives in the collector package in some proto versions;
	// use the shared shape through an adapter below.
	samples := []model.Sample{}
	for _, resource := range resources {
		resourceLabels := otlpAttributes(resource.GetResource())
		for _, scope := range resource.GetScopeMetrics() {
			scopeLabels := map[string]string{}
			for key, value := range resourceLabels {
				scopeLabels[key] = value
			}
			if name := scope.GetScope().GetName(); name != "" {
				scopeLabels["otel_scope_name"] = name
			}
			if version := scope.GetScope().GetVersion(); version != "" {
				scopeLabels["otel_scope_version"] = version
			}
			for _, metric := range scope.GetMetrics() {
				samples = append(samples, otlpMetricSamples(metric, scopeLabels)...)
			}
		}
	}
	return samples
}

func otlpMetricSamples(metric *metricspb.Metric, base map[string]string) []model.Sample {
	name := sanitizeMetricName(metric.GetName())
	if name == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	add := func(destination []model.Sample, pointName string, labels map[string]string, value float64, nanoseconds uint64) []model.Sample {
		if value != value { // NaN is not a storeable sample.
			return destination
		}
		timestamp := now
		if nanoseconds > 0 {
			timestamp = int64(nanoseconds / 1_000_000)
		}
		return append(destination, model.Sample{Name: pointName, Labels: labels, Value: value, Timestamp: timestamp})
	}
	samples := []model.Sample{}
	if gauge := metric.GetGauge(); gauge != nil {
		for _, point := range gauge.GetDataPoints() {
			samples = add(samples, name, otlpLabels(base, point.GetAttributes()), otlpNumber(point), point.GetTimeUnixNano())
		}
	}
	if sum := metric.GetSum(); sum != nil {
		for _, point := range sum.GetDataPoints() {
			samples = add(samples, name, otlpLabels(base, point.GetAttributes()), otlpNumber(point), point.GetTimeUnixNano())
		}
	}
	if histogram := metric.GetHistogram(); histogram != nil {
		for _, point := range histogram.GetDataPoints() {
			labels := otlpLabels(base, point.GetAttributes())
			cumulative := uint64(0)
			for index, count := range point.GetBucketCounts() {
				cumulative += count
				bucket := map[string]string{"le": "+Inf"}
				for key, value := range labels {
					bucket[key] = value
				}
				if index < len(point.GetExplicitBounds()) {
					bucket["le"] = strconv.FormatFloat(point.GetExplicitBounds()[index], 'f', -1, 64)
				}
				samples = add(samples, name+"_bucket", bucket, float64(cumulative), point.GetTimeUnixNano())
			}
			samples = add(samples, name+"_count", labels, float64(point.GetCount()), point.GetTimeUnixNano())
			samples = add(samples, name+"_sum", labels, point.GetSum(), point.GetTimeUnixNano())
		}
	}
	if summary := metric.GetSummary(); summary != nil {
		for _, point := range summary.GetDataPoints() {
			labels := otlpLabels(base, point.GetAttributes())
			samples = add(samples, name+"_count", labels, float64(point.GetCount()), point.GetTimeUnixNano())
			samples = add(samples, name+"_sum", labels, point.GetSum(), point.GetTimeUnixNano())
			for _, quantile := range point.GetQuantileValues() {
				quantileLabels := map[string]string{"quantile": strconv.FormatFloat(quantile.GetQuantile(), 'f', -1, 64)}
				for key, value := range labels {
					quantileLabels[key] = value
				}
				samples = add(samples, name, quantileLabels, quantile.GetValue(), point.GetTimeUnixNano())
			}
		}
	}
	return samples
}

func otlpNumber(point interface {
	GetAsDouble() float64
	GetAsInt() int64
}) float64 {
	if point.GetAsInt() != 0 {
		return float64(point.GetAsInt())
	}
	return point.GetAsDouble()
}

func otlpLabels(base map[string]string, attributes []*commonpb.KeyValue) map[string]string {
	labels := make(map[string]string, len(base)+len(attributes))
	for key, value := range base {
		labels[key] = value
	}
	for _, attribute := range attributes {
		key := sanitizeLabelKey(attribute.GetKey())
		if key == "" {
			continue
		}
		if value, ok := otlpAttributeValue(attribute.GetValue()); ok {
			labels[key] = value
		}
	}
	return labels
}

func otlpAttributes(resource *resourcepb.Resource) map[string]string {
	if resource == nil {
		return map[string]string{}
	}
	return otlpLabels(nil, resource.GetAttributes())
}

func otlpAttributeValue(value *commonpb.AnyValue) (string, bool) {
	if value == nil {
		return "", false
	}
	switch typed := value.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return typed.StringValue, true
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(typed.BoolValue), true
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(typed.IntValue, 10), true
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(typed.DoubleValue, 'f', -1, 64), true
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(typed.BytesValue), true
	case *commonpb.AnyValue_ArrayValue:
		parts := []string{}
		for _, item := range typed.ArrayValue.GetValues() {
			if text, ok := otlpAttributeValue(item); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ","), true
	default:
		return "", false
	}
}

func sanitizeMetricName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for index, character := range value {
		valid := character == '_' || character == ':' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	name := builder.String()
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

func sanitizeLabelKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for index, character := range value {
		valid := character == '_' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	key := builder.String()
	if key == "" || key[0] >= '0' && key[0] <= '9' {
		key = "_" + key
	}
	return key
}
