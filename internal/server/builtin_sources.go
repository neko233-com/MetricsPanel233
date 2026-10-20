package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func isBuiltinSource(kind string) bool {
	return kind == "prometheus" || kind == "grafana" || kind == "loki" || kind == "tempo" || isSQLSource(kind)
}

func (s *Server) queryBuiltinSource(ctx context.Context, ds model.DataSource, query backend.DataQuery) (data.Frames, error) {
	switch {
	case isSQLSource(ds.Type):
		return s.querySQLSource(ctx, ds, query)
	case ds.Type == "grafana":
		return s.queryGrafanaSource(ctx, query)
	case ds.Type == "loki":
		return s.queryLokiSource(ctx, ds, query)
	case ds.Type == "tempo":
		return s.queryTempoSource(ctx, ds, query)
	default:
		return s.queryPrometheusSource(ctx, ds, query)
	}
}

type lokiTarget struct {
	Expr      string `json:"expr"`
	Query     string `json:"query"`
	QueryType string `json:"queryType"`
	Limit     int    `json:"limit"`
	Direction string `json:"direction"`
	Step      string `json:"step"`
	Instant   bool   `json:"instant"`
}

type lokiPoint struct {
	at   time.Time
	line string
}

// Loki returns log streams. Each stream becomes one frame with Time, Line and
// stream labels so Grafana logs panels and CLI consumers can both use it.
func (s *Server) queryLokiSource(ctx context.Context, ds model.DataSource, query backend.DataQuery) (data.Frames, error) {
	var target lokiTarget
	if err := json.Unmarshal(query.JSON, &target); err != nil {
		return nil, err
	}
	expression := target.Expr
	if expression == "" {
		expression = target.Query
	}
	if expression == "" || len(expression) > 10000 {
		return nil, errors.New("LogQL expression required (max 10000 bytes)")
	}
	limit := target.Limit
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	direction := target.Direction
	if direction == "" {
		direction = "backward"
	}
	if direction != "forward" && direction != "backward" {
		return nil, errors.New("Loki direction must be forward or backward")
	}
	instant := target.Instant || target.QueryType == "instant"
	path := "/loki/api/v1/query_range"
	params := url.Values{"query": {expression}, "limit": {strconv.Itoa(limit)}, "direction": {direction}}
	if instant {
		path = "/loki/api/v1/query"
		params.Set("time", strconv.FormatInt(query.TimeRange.To.UnixNano(), 10))
	} else {
		params.Set("start", strconv.FormatInt(query.TimeRange.From.UnixNano(), 10))
		params.Set("end", strconv.FormatInt(query.TimeRange.To.UnixNano(), 10))
		step := target.Step
		if step == "" && query.Interval > 0 {
			step = query.Interval.String()
		}
		if step != "" {
			params.Set("step", step)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://datasource"+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	output := &boundedResponse{header: http.Header{}}
	s.proxyDataSource(output, request, ds)
	if output.status >= 400 {
		return nil, fmt.Errorf("Loki query failed with HTTP %d", output.status)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string   `json:"stream"`
				Metric map[string]string   `json:"metric"`
				Values [][]json.RawMessage `json:"values"`
				Value  []json.RawMessage   `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.body.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("invalid Loki response: %w", err)
	}
	if response.Status != "" && response.Status != "success" {
		return nil, fmt.Errorf("Loki query failed: %s", response.Error)
	}
	frames := data.Frames{}
	for _, stream := range response.Data.Result {
		labels := stream.Stream
		if len(labels) == 0 {
			labels = stream.Metric
		}
		points := []lokiPoint{}
		for _, value := range stream.Values {
			if len(value) < 2 {
				continue
			}
			at, ok := lokiTimestamp(value[0])
			if !ok {
				continue
			}
			var line string
			if json.Unmarshal(value[1], &line) != nil {
				continue
			}
			points = append(points, lokiPoint{at: at, line: line})
		}
		if len(stream.Value) >= 2 {
			if at, ok := lokiTimestamp(stream.Value[0]); ok {
				var line string
				if json.Unmarshal(stream.Value[1], &line) == nil {
					points = append(points, lokiPoint{at: at, line: line})
				}
			}
		}
		sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
		times := make([]time.Time, len(points))
		lines := make([]string, len(points))
		for index, point := range points {
			times[index], lines[index] = point.at, point.line
		}
		frame := data.NewFrame("",
			data.NewField("Time", nil, times),
			data.NewField("Line", data.Labels(labels), lines),
		)
		frame.RefID = query.RefID
		frame.Meta = &data.FrameMeta{PreferredVisualization: data.VisTypeLogs}
		frames = append(frames, frame)
	}
	return frames, nil
}

func lokiTimestamp(raw json.RawMessage) (time.Time, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		nanoseconds, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(0, nanoseconds), true
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return time.Unix(0, int64(number)), true
	}
	return time.Time{}, false
}

type tempoTarget struct {
	Query     string `json:"query"`
	TraceQL   string `json:"traceql"`
	QueryType string `json:"queryType"`
	TraceID   string `json:"traceId"`
	Limit     int    `json:"limit"`
}

// Tempo search returns one table row per trace. Full trace-detail rendering is
// intentionally not fabricated; use TraceQL search for the compatibility path.
func (s *Server) queryTempoSource(ctx context.Context, ds model.DataSource, query backend.DataQuery) (data.Frames, error) {
	var target tempoTarget
	if err := json.Unmarshal(query.JSON, &target); err != nil {
		return nil, err
	}
	if target.TraceID != "" || target.QueryType == "traceId" {
		return nil, errors.New("Tempo trace ID lookup is not implemented; use TraceQL search")
	}
	expression := target.Query
	if expression == "" {
		expression = target.TraceQL
	}
	if expression == "" || len(expression) > 10000 {
		return nil, errors.New("TraceQL expression required (max 10000 bytes)")
	}
	limit := target.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 1000 {
		limit = 1000
	}
	params := url.Values{
		"q":     {expression},
		"start": {strconv.FormatInt(query.TimeRange.From.Unix(), 10)},
		"end":   {strconv.FormatInt(query.TimeRange.To.Unix(), 10)},
		"limit": {strconv.Itoa(limit)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://datasource/api/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	output := &boundedResponse{header: http.Header{}}
	s.proxyDataSource(output, request, ds)
	if output.status >= 400 {
		return nil, fmt.Errorf("Tempo query failed with HTTP %d", output.status)
	}
	var response struct {
		Traces []struct {
			TraceID           string          `json:"traceID"`
			RootServiceName   string          `json:"rootServiceName"`
			RootTraceName     string          `json:"rootTraceName"`
			StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"`
			DurationMs        float64         `json:"durationMs"`
		} `json:"traces"`
	}
	if err := json.Unmarshal(output.body.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("invalid Tempo response: %w", err)
	}
	traceIDs := make([]string, 0, len(response.Traces))
	services := make([]string, 0, len(response.Traces))
	names := make([]string, 0, len(response.Traces))
	starts := make([]time.Time, 0, len(response.Traces))
	durations := make([]float64, 0, len(response.Traces))
	for _, trace := range response.Traces {
		traceIDs = append(traceIDs, trace.TraceID)
		services = append(services, trace.RootServiceName)
		names = append(names, trace.RootTraceName)
		starts = append(starts, tempoStart(trace.StartTimeUnixNano))
		durations = append(durations, trace.DurationMs)
	}
	frame := data.NewFrame("Traces",
		data.NewField("traceID", nil, traceIDs),
		data.NewField("rootServiceName", nil, services),
		data.NewField("rootTraceName", nil, names),
		data.NewField("startTime", nil, starts),
		data.NewField("durationMs", nil, durations),
	)
	frame.RefID = query.RefID
	frame.Meta = &data.FrameMeta{PreferredVisualization: data.VisTypeTable}
	return data.Frames{frame}, nil
}

func tempoStart(raw json.RawMessage) time.Time {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if nanoseconds, err := strconv.ParseInt(text, 10, 64); err == nil {
			return time.Unix(0, nanoseconds)
		}
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return time.Unix(0, int64(number))
	}
	return time.Time{}
}

func (s *Server) checkHTTPDataSource(ctx context.Context, ds model.DataSource) error {
	path := "/ready"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://datasource"+path, nil)
	if err != nil {
		return err
	}
	output := &boundedResponse{header: http.Header{}}
	s.proxyDataSource(output, request, ds)
	if output.status >= 400 {
		return fmt.Errorf("%s health returned HTTP %d", ds.Type, output.status)
	}
	return nil
}
