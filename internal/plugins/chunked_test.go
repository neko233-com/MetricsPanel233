package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealSDKChunkedFallbackPartialFailureAndCallbackCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/sdk-backend").CombinedOutput()
	require.NoError(t, err, string(output))
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "streaming", true: "unary fallback"}[legacy], func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "plugin.zip")
			mode := "--package"
			if legacy {
				mode = "--package-legacy"
			}
			output, err := exec.CommandContext(ctx, binary, mode, archive).CombinedOutput()
			require.NoError(t, err, string(output))
			raw, err := os.ReadFile(archive)
			require.NoError(t, err)
			s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
			require.NoError(t, err)
			manager := New(s, "", []string{"metricspanel-sdk-datasource"})
			t.Cleanup(func() { manager.Close(); s.DB.Close() })
			_, err = manager.Install(ctx, raw, "")
			require.NoError(t, err)
			ds, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "sdk-test", Name: "SDK test", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
			require.NoError(t, err)
			stats := func() map[string]int64 {
				response, err := manager.Resource(ctx, ds, &backend.CallResourceRequest{Path: "chunked-stats"})
				require.NoError(t, err)
				var result map[string]int64
				require.NoError(t, json.Unmarshal(response[0].Body, &result))
				return result
			}
			chunks := []*pluginv2.QueryChunkedDataResponse{}
			collect := func(chunk *pluginv2.QueryChunkedDataResponse) error { chunks = append(chunks, chunk); return nil }
			err = manager.QueryChunked(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"value":233,"chunks":2}`)}, {RefID: "B", JSON: json.RawMessage(`{"fail":true}`)}}, collect)
			require.NoError(t, err)
			if legacy {
				require.Len(t, chunks, 2)
				assert.Equal(t, int64(0), stats()["started"])
				assert.Equal(t, int64(1), stats()["static_queries"])
				assert.True(t, chunks[0].Error != "" || chunks[1].Error != "")
				return
			}
			require.Len(t, chunks, 3)
			assert.Contains(t, string(chunks[0].Frame), `"schema"`)
			assert.NotContains(t, string(chunks[1].Frame), `"schema"`)
			assert.Equal(t, "fixture chunked query failed", chunks[2].Error)
			chunks = nil
			err = manager.QueryChunked(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"unimplementedAfterChunk":true}`)}}, collect)
			require.ErrorContains(t, err, "fixture failed after first chunk")
			assert.Len(t, chunks, 1)
			assert.Zero(t, stats()["static_queries"], "partial streaming error incorrectly ran the unary fallback")
			before := stats()["cancelled"]
			callbackError := errors.New("consumer stopped")
			err = manager.QueryChunked(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"chunks":1000,"delayMs":100}`)}}, func(*pluginv2.QueryChunkedDataResponse) error { return callbackError })
			require.ErrorIs(t, err, callbackError)
			require.Eventually(t, func() bool { current := stats(); return current["active"] == 0 && current["cancelled"] > before }, 2*time.Second, 10*time.Millisecond)
			err = manager.QueryChunked(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"unknownRef":true}`)}}, collect)
			require.ErrorContains(t, err, "unknown query refId")
		})
	}
}
