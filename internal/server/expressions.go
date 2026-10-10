package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/expressions"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func builtinExpressionSource() model.DataSource {
	return model.DataSource{ID: -100, UID: "__expr__", OrgID: 1, Name: "Expression", Type: "__expr__", Access: "proxy", ReadOnly: true, JSONData: json.RawMessage("{}"), SecureJSONFields: map[string]bool{}, Version: 1}
}
func (s *Server) expressionSourceQuery(ctx context.Context, uid string, queries []backend.DataQuery) (backend.Responses, string, error) {
	ds, err := s.dataSource(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	response := backend.Responses{}
	if ds.Type == "prometheus" || ds.Type == "grafana" {
		for _, q := range queries {
			var frames data.Frames
			var err error
			if ds.Type == "prometheus" {
				frames, err = s.queryPrometheusSource(ctx, ds, q)
			} else {
				frames, err = s.queryGrafanaSource(ctx, q)
			}
			response[q.RefID] = backend.DataResponse{Frames: frames, Error: err}
			if err != nil {
				r := response[q.RefID]
				r.Status = backend.StatusBadRequest
				response[q.RefID] = r
			}
		}
		return response, ds.Type, nil
	}
	output, err := s.Plugins.Query(ctx, ds, queries)
	if err != nil {
		return nil, ds.Type, err
	}
	allowed := map[string]bool{}
	for _, q := range queries {
		allowed[q.RefID] = true
	}
	for ref := range output.Responses {
		if !allowed[ref] {
			return nil, ds.Type, errors.New("plugin returned an unknown query refId")
		}
	}
	return output.Responses, ds.Type, nil
}
func (s *Server) writeExpressionGroups(w http.ResponseWriter, r *http.Request, groups map[string][]backend.DataQuery) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	response := expressions.Execute(ctx, groups, s.expressionSourceQuery)
	if requestsChunks(r.Header.Get("Accept")) {
		writer := &queryChunkWriter{w: w}
		producer := backend.NewChunkedDataWriter(backend.DataFrameFormat_JSON, writer.onChunk)
		writeContext := r.Context()
		refs := make([]string, 0, len(response.Responses))
		for ref := range response.Responses {
			refs = append(refs, ref)
		}
		slices.Sort(refs)
		for _, ref := range refs {
			result := response.Responses[ref]
			if result.Error != nil {
				if err := producer.WriteError(writeContext, ref, result.Status, result.Error); err != nil {
					return
				}
				continue
			}
			for index, frame := range result.Frames {
				if err := producer.WriteFrame(writeContext, ref, strconv.Itoa(index), frame); err != nil {
					_ = writer.writeErrors([]backend.DataQuery{{RefID: ref}}, err)
					return
				}
			}
		}
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		fail(w, 502, err)
		return
	}
	if len(encoded) > 32<<20 {
		fail(w, 502, errors.New("query response exceeds 32 MiB"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}
