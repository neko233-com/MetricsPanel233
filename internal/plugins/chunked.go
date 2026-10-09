package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// QueryChunked forwards the official SDK stream without collecting it in memory.
// Older plugins can still answer through QueryData when the streaming RPC is absent.
func (m *Manager) QueryChunked(ctx context.Context, ds model.DataSource, queries []backend.DataQuery, send backend.ChunkedDataCallback) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case m.requests <- struct{}{}:
		defer func() { <-m.requests }()
	case <-ctx.Done():
		return ctx.Err()
	}
	pc, err := m.PluginContext(ctx, ds)
	if err != nil {
		return err
	}
	host, err := m.getProcess(ctx, ds.Type)
	if err != nil {
		return err
	}
	refs := make(map[string]bool, len(queries))
	for _, query := range queries {
		if query.RefID == "" || refs[query.RefID] {
			return errors.New("queries need unique refId")
		}
		refs[query.RefID] = true
	}
	forward := func(chunk *pluginv2.QueryChunkedDataResponse) error {
		if chunk == nil || !refs[chunk.RefId] {
			return errors.New("plugin returned an unknown query refId")
		}
		if len(chunk.Frame) > 8<<20 {
			return errors.New("query chunk exceeds 8 MiB")
		}
		return send(chunk)
	}
	stream, err := host.data.QueryChunkedData(ctx, backend.ToProto().QueryChunkedDataRequest(&backend.QueryChunkedDataRequest{PluginContext: pc, Queries: queries, Format: backend.DataFrameFormat_JSON}))
	received := false
	if err == nil {
		for {
			var chunk *pluginv2.QueryChunkedDataResponse
			chunk, err = stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				break
			}
			received = true
			if err = forward(chunk); err != nil {
				return err
			}
		}
	}
	if received || status.Code(err) != codes.Unimplemented {
		return err
	}
	response, err := host.data.QueryData(ctx, backend.ToProto().QueryDataRequest(&backend.QueryDataRequest{PluginContext: pc, Queries: queries, Format: backend.DataFrameFormat_JSON}))
	if err != nil {
		return err
	}
	for ref, result := range response.Responses {
		if !refs[ref] {
			return errors.New("plugin returned an unknown query refId")
		}
		for index, frame := range result.Frames {
			format := pluginv2.DataFrameFormat_ARROW
			if len(frame) > 0 && frame[0] == '{' {
				format = pluginv2.DataFrameFormat_JSON
			}
			if err := forward(&pluginv2.QueryChunkedDataResponse{RefId: ref, FrameId: fmt.Sprint(index), Frame: frame, Format: format, Status: result.Status}); err != nil {
				return err
			}
		}
		if result.Error != "" || result.Status >= 300 {
			if err := forward(&pluginv2.QueryChunkedDataResponse{RefId: ref, Error: result.Error, ErrorSource: result.ErrorSource, Status: result.Status}); err != nil {
				return err
			}
		}
	}
	for _, query := range queries {
		if _, exists := response.Responses[query.RefID]; !exists {
			if err := forward(&pluginv2.QueryChunkedDataResponse{RefId: query.RefID, Error: "plugin omitted a query response", Status: int32(backend.StatusBadGateway)}); err != nil {
				return err
			}
		}
	}
	return nil
}
