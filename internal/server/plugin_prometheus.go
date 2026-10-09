package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// The capture is bounded because external datasources can return untrusted data.
type boundedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *boundedResponse) Header() http.Header { return r.header }
func (r *boundedResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *boundedResponse) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	if r.body.Len()+len(p) > 32<<20 {
		return 0, errors.New("datasource response exceeds 32 MiB")
	}
	return r.body.Write(p)
}

func (s *Server) queryPrometheusSource(ctx context.Context, ds model.DataSource, q backend.DataQuery) (data.Frames, error) {
	var target struct {
		Expr    string `json:"expr"`
		Instant bool   `json:"instant"`
		Range   bool   `json:"range"`
		Legend  string `json:"legendFormat"`
		Format  string `json:"format"`
	}
	if err := json.Unmarshal(q.JSON, &target); err != nil {
		return nil, err
	}
	if target.Expr == "" || len(target.Expr) > 10000 {
		return nil, errors.New("PromQL expression required (max 10000 bytes)")
	}
	if target.Format != "" && target.Format != "time_series" && target.Format != "table" {
		return nil, errors.New("unsupported Prometheus format")
	}
	frames := data.Frames{}
	modes := []bool{target.Instant && !target.Range}
	if target.Instant && target.Range {
		modes = []bool{false, true}
	}
	for _, instant := range modes {
		params := url.Values{"query": {target.Expr}}
		endpoint := "query_range"
		if instant {
			endpoint = "query"
			params.Set("time", strconv.FormatFloat(float64(q.TimeRange.To.UnixMilli())/1000, 'f', 3, 64))
		} else {
			points := min(max(q.MaxDataPoints, 1), 2000)
			step := max(q.Interval, time.Duration(math.Ceil(float64(q.TimeRange.To.Sub(q.TimeRange.From))/float64(points))))
			step = max(step, time.Second)
			params.Set("start", strconv.FormatFloat(float64(q.TimeRange.From.UnixMilli())/1000, 'f', 3, 64))
			params.Set("end", strconv.FormatFloat(float64(q.TimeRange.To.UnixMilli())/1000, 'f', 3, 64))
			params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', 3, 64))
		}
		request, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/api/v1/"+endpoint+"?"+params.Encode(), nil)
		output := &boundedResponse{header: http.Header{}}
		if ds.UID == "metricspanel" {
			s.Prometheus.Handler().ServeHTTP(output, request)
		} else {
			s.proxyDataSource(output, request, ds)
		}
		var result struct {
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
			Data   struct {
				ResultType string          `json:"resultType"`
				Result     json.RawMessage `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.body.Bytes(), &result); err != nil {
			return nil, fmt.Errorf("invalid Prometheus response: %w", err)
		}
		if output.status != 200 || result.Status != "success" {
			return nil, fmt.Errorf("Prometheus query failed: %s", result.Error)
		}
		type series struct {
			Metric data.Labels         `json:"metric"`
			Value  []json.RawMessage   `json:"value"`
			Values [][]json.RawMessage `json:"values"`
		}
		items := []series{}
		if result.Data.ResultType == "scalar" {
			var point []json.RawMessage
			if err := json.Unmarshal(result.Data.Result, &point); err != nil {
				return nil, err
			}
			items = append(items, series{Metric: data.Labels{}, Value: point})
		} else if result.Data.ResultType == "vector" || result.Data.ResultType == "matrix" {
			if err := json.Unmarshal(result.Data.Result, &items); err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("unsupported Prometheus result type")
		}
		if len(items) > 10000 {
			return nil, errors.New("Prometheus query exceeds 10000 series")
		}
		for _, item := range items {
			points := item.Values
			if len(item.Value) > 0 {
				points = [][]json.RawMessage{item.Value}
			}
			times := make([]time.Time, 0, len(points))
			values := make([]*float64, 0, len(points))
			for _, point := range points {
				if len(point) != 2 {
					return nil, errors.New("invalid Prometheus sample")
				}
				var seconds float64
				var raw string
				if json.Unmarshal(point[0], &seconds) != nil || json.Unmarshal(point[1], &raw) != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 253402300799 {
					return nil, errors.New("invalid Prometheus sample")
				}
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return nil, err
				}
				times = append(times, time.UnixMilli(int64(seconds*1000)))
				if math.IsNaN(value) || math.IsInf(value, 0) {
					values = append(values, nil)
				} else {
					values = append(values, &value)
				}
			}
			field := data.NewField("Value", item.Metric, values)
			if target.Legend != "" && target.Legend != "__auto" {
				name := target.Legend
				for key, value := range item.Metric {

					name = strings.ReplaceAll(name, "{{"+key+"}}", value)
				}
				field.Config = &data.FieldConfig{DisplayNameFromDS: name}
			}
			frame := data.NewFrame("", data.NewField("Time", nil, times), field)
			frame.RefID = q.RefID
			frames = append(frames, frame)
		}
	}
	if target.Format == "table" {
		return data.Frames{prometheusTable(frames, q.RefID)}, nil
	}
	return frames, nil
}

func prometheusTable(frames data.Frames, ref string) *data.Frame {
	keys := map[string]bool{}
	for _, frame := range frames {
		for key := range frame.Fields[1].Labels {
			keys[key] = true
		}
	}
	labels := make([]string, 0, len(keys))
	for key := range keys {
		labels = append(labels, key)
	}
	sort.Strings(labels)
	times := []time.Time{}
	values := []*float64{}
	columns := map[string][]string{}
	for _, key := range labels {
		columns[key] = []string{}
	}
	for _, frame := range frames {
		for row := 0; row < frame.Fields[0].Len(); row++ {
			times = append(times, frame.Fields[0].At(row).(time.Time))
			values = append(values, frame.Fields[1].At(row).(*float64))
			for _, key := range labels {
				columns[key] = append(columns[key], frame.Fields[1].Labels[key])
			}
		}
	}
	fields := []*data.Field{data.NewField("Time", nil, times)}
	for _, key := range labels {
		fields = append(fields, data.NewField(key, nil, columns[key]))
	}
	fields = append(fields, data.NewField("Value #"+ref, nil, values))
	frame := data.NewFrame("", fields...)
	frame.RefID = ref
	frame.Meta = &data.FrameMeta{PreferredVisualization: data.VisTypeTable}
	return frame
}
