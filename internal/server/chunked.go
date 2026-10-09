package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/gtime"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	queriesAPI "github.com/grafana/grafana-plugin-sdk-go/experimental/apis/datasource/v0alpha1"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

const (
	chunkedContentType = "text/jsonl"
	// 32 group failures plus 32 terminal failures, including JSON escaping.
	chunkErrorReserve = 512 << 10
)

func requestsChunks(accept string) bool {
	for _, media := range strings.FieldsFunc(accept, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if media == chunkedContentType {
			return true
		}
	}
	return false
}

func (s *Server) chunkedRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /apis/{group}/v0alpha1/namespaces/{namespace}/connections/{uid}/query", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("namespace") != "default" {
			fail(w, 404, errors.New("namespace not found"))
			return
		}
		ds, err := s.dataSource(r.Context(), r.PathValue("uid"))
		if err != nil {
			resourceError(w, err)
			return
		}
		if r.PathValue("group") != ds.Type+".datasource.grafana.app" {
			fail(w, 404, errors.New("datasource API group does not match its plugin"))
			return
		}
		var input queriesAPI.QueryDataRequest
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if len(input.Queries) == 0 || len(input.Queries) > 32 {
			fail(w, 400, errors.New("request needs 1–32 queries"))
			return
		}
		validateRange := func(from, to string) error {
			if from == "" && to == "" {
				return nil
			}
			tr := gtime.NewTimeRange(from, to)
			start, err := tr.ParseFrom()
			if err != nil {
				return errors.New("invalid query start time")
			}
			end, err := tr.ParseTo()
			if err != nil || start.UnixMilli() < 0 || end.Before(start) || end.Sub(start) > 31*24*time.Hour {
				return errors.New("query range must be valid and at most 31 days")
			}
			return nil
		}
		if err := validateRange(input.From, input.To); err != nil {
			fail(w, 400, err)
			return
		}
		seen := map[string]bool{}
		visible := input.Queries[:0]
		for _, query := range input.Queries {
			if query.Hide {
				continue
			}
			if query.RefID == "" || len(query.RefID) > 100 || seen[query.RefID] {
				fail(w, 400, errors.New("queries need unique refId"))
				return
			}
			seen[query.RefID] = true
			if query.Datasource != nil && (query.Datasource.UID != "" && query.Datasource.UID != ds.UID || query.Datasource.Type != "" && query.Datasource.Type != ds.Type) || query.DatasourceID != 0 && query.DatasourceID != ds.ID {
				fail(w, 400, errors.New("query datasource must match the connection"))
				return
			}
			if query.TimeRange != nil {
				if err := validateRange(query.TimeRange.From, query.TimeRange.To); err != nil {
					fail(w, 400, err)
					return
				}
			}
			if query.MaxDataPoints < 0 || query.MaxDataPoints > 10000 {
				fail(w, 400, errors.New("maxDataPoints must be 1–10000"))
				return
			}
			if query.IntervalMS < 0 || query.IntervalMS > float64(math.MaxInt64/int64(time.Millisecond)) || math.Trunc(query.IntervalMS) != query.IntervalMS {
				fail(w, 400, errors.New("intervalMs must be a nonnegative integer within the duration limit"))
				return
			}
			visible = append(visible, query)
		}
		input.Queries = visible
		queries, _, err := queriesAPI.ToDataSourceQueries(input)
		if err != nil {
			fail(w, 400, err)
			return
		}
		s.writeQueryGroups(w, r, map[string][]backend.DataQuery{ds.UID: queries})
	})
}

type queryChunk struct {
	RefID       string          `json:"refId"`
	FrameID     string          `json:"frameId,omitempty"`
	Frame       json.RawMessage `json:"frame,omitempty"`
	Error       string          `json:"error,omitempty"`
	ErrorSource string          `json:"errorSource,omitempty"`
}
type chunkFrameKey struct{ ref, frame string }
type queryChunkWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	started bool
	bytes   int
	fields  map[chunkFrameKey]string
	fatal   error
	broken  bool
}

func (writer *queryChunkWriter) onChunk(chunk *pluginv2.QueryChunkedDataResponse) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.fatal != nil {
		return writer.fatal
	}
	line := queryChunk{RefID: chunk.RefId, FrameID: chunk.FrameId, Error: chunk.Error, ErrorSource: chunk.ErrorSource}
	if line.Error == "" && chunk.Status >= 300 {
		line.Error = fmt.Sprintf("plugin query returned status %d", chunk.Status)
	}
	if len(chunk.Frame) > 0 {
		if line.FrameID == "" || len(line.FrameID) > 256 || len(chunk.Frame) > 8<<20 {
			return errors.New("query chunks need a frameId up to 256 bytes and a frame under 8 MiB")
		}
		frame := chunk.Frame
		if chunk.Format != pluginv2.DataFrameFormat_JSON {
			decoded, err := data.UnmarshalArrowFrame(frame)
			if err != nil {
				return fmt.Errorf("invalid Arrow query chunk: %w", err)
			}
			decoded.RefID = line.RefID
			frame, err = decoded.MarshalJSON()
			if err != nil {
				return err
			}
		}
		if err := writer.checkFrame(line.RefID, line.FrameID, frame); err != nil {
			return err
		}
		line.Frame = frame
	}
	if line.Frame == nil && line.Error == "" {
		return errors.New("query chunk omitted both frame and error")
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if len(encoded) > 8<<20 || writer.bytes+len(encoded)+1 > (32<<20)-chunkErrorReserve {
		writer.fatal = errors.New("chunked query exceeds 8 MiB per chunk or 32 MiB per request")
		return writer.fatal
	}
	return writer.writeLine(encoded)
}

func (writer *queryChunkWriter) checkFrame(ref, id string, raw []byte) error {
	var frame struct {
		Schema *struct {
			RefID  string           `json:"refId"`
			Fields []map[string]any `json:"fields"`
		} `json:"schema"`
		Data *struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return errors.New("invalid JSON query frame")
	}
	if frame.Schema == nil && frame.Data == nil {
		return errors.New("query frame omitted schema and data")
	}
	key := chunkFrameKey{ref, id}
	if writer.fields == nil {
		writer.fields = map[chunkFrameKey]string{}
	}
	previous, exists := writer.fields[key]
	if !exists && frame.Schema == nil {
		return errors.New("first query chunk for a frameId must include schema")
	}
	if frame.Schema != nil {
		if frame.Schema.RefID != "" && frame.Schema.RefID != ref || len(frame.Schema.Fields) > 1024 {
			return errors.New("query frame schema refId or field count is invalid")
		}
		schema, err := json.Marshal(frame.Schema.Fields)
		if err != nil {
			return err
		}
		if exists && previous != string(schema) {
			return errors.New("query frame fields changed within the same frameId")
		}
		if !exists && len(writer.fields) >= 1024 {
			return errors.New("query exceeds 1024 distinct frames")
		}
		previous = string(schema)
		writer.fields[key] = previous
	}
	if frame.Data != nil {
		var fields []json.RawMessage
		if json.Unmarshal([]byte(previous), &fields) != nil || len(frame.Data.Values) != len(fields) {
			return errors.New("query chunk columns do not match the initial schema")
		}
		rows := -1
		for _, column := range frame.Data.Values {
			if rows >= 0 && rows != len(column) {
				return errors.New("query chunk column lengths differ")
			}
			rows = len(column)
		}
	}
	return nil
}

func (writer *queryChunkWriter) writeLine(encoded []byte) error {
	if !writer.started {
		writer.w.Header().Set("Content-Type", chunkedContentType)
		writer.w.Header().Set("X-Accel-Buffering", "no")
		writer.started = true
	}
	controller := http.NewResponseController(writer.w)
	_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err := writer.w.Write(append(encoded, '\n')); err != nil {
		writer.fatal = err
		writer.broken = true
		return err
	}
	writer.bytes += len(encoded) + 1
	if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writer.fatal = err
		writer.broken = true
		return err
	}
	return nil
}

func (writer *queryChunkWriter) writeErrors(queries []backend.DataQuery, failure error) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.broken {
		return writer.fatal
	}
	message := failure.Error()
	if len(message) > 1024 {
		message = message[:1024] + " (truncated)"
	}
	for _, query := range queries {
		encoded, err := json.Marshal(queryChunk{RefID: query.RefID, Error: message, ErrorSource: "plugin"})
		if err != nil {
			return err
		}
		if writer.bytes+len(encoded)+1 > 32<<20 {
			return errors.New("error response exceeds query byte limit")
		}
		if err := writer.writeLine(encoded); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) streamQueryGroups(w http.ResponseWriter, r *http.Request, groups map[string][]backend.DataQuery) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
	writer := &queryChunkWriter{w: w}
	ids := make([]string, 0, len(groups))
	sources := map[string]model.DataSource{}
	for uid := range groups {
		ds, err := s.dataSource(ctx, uid)
		if err != nil {
			resourceError(w, err)
			return
		}
		sources[uid] = ds
		ids = append(ids, uid)
	}
	sort.Strings(ids)
	var work sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, uid := range ids {
		work.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			queries := groups[uid]
			var err error
			if ds := sources[uid]; ds.Type == "prometheus" {
				producer := backend.NewChunkedDataWriter(backend.DataFrameFormat_JSON, writer.onChunk)
				for _, query := range queries {
					frames, queryErr := s.queryPrometheusSource(ctx, ds, query)
					if queryErr != nil {
						err = producer.WriteError(ctx, query.RefID, backend.StatusBadRequest, queryErr)
					} else {
						for index, frame := range frames {
							if err = producer.WriteFrame(ctx, query.RefID, fmt.Sprint(index), frame); err != nil {
								break
							}
						}
					}
					if err != nil {
						break
					}
				}
			} else {
				err = s.Plugins.QueryChunked(ctx, ds, queries, writer.onChunk)
			}
			if err != nil && ctx.Err() == nil {
				if writer.writeErrors(queries, err) != nil {
					cancel()
					return
				}
				writer.mu.Lock()
				fatal := writer.fatal != nil
				writer.mu.Unlock()
				if fatal {
					cancel()
				}
			}
		})
	}
	work.Wait()
	if r.Context().Err() == nil {
		var terminal error
		if ctx.Err() != nil {
			terminal = ctx.Err()
		}
		if writer.fatal != nil {
			terminal = writer.fatal
		}
		if terminal != nil {
			queries := []backend.DataQuery{}
			for _, uid := range ids {
				queries = append(queries, groups[uid]...)
			}
			_ = writer.writeErrors(queries, terminal)
		}
	}
	if !writer.started {
		w.Header().Set("Content-Type", chunkedContentType)
	}
}
