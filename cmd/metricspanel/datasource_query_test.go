package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatasourceQueryCLIFlushPartialErrorsAndCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	neverRelease := make(chan struct{})
	stopped := make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token-233" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"type":"example-datasource"}`)
			return
		}
		assert.Contains(t, r.URL.Path, "/apis/example-datasource.datasource.grafana.app/v0alpha1/namespaces/default/connections/")
		w.Header().Set("Content-Type", "text/jsonl; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, "/truncated/query") {
			fmt.Fprint(w, `{"refId":"A","error":"unfinished record"}`)
			return
		}
		fmt.Fprintln(w, `{"refId":"A","frameId":"first","frame":{"schema":{"fields":[]},"data":{"values":[]}}}`)
		w.(http.Flusher).Flush()
		ready := release
		if strings.HasSuffix(r.URL.Path, "/cancel/query") {
			ready = neverRelease
		}
		select {
		case <-ready:
			fmt.Fprintln(w, `{"refId":"B","error":"上海🌍 query failed"}`)
		case <-r.Context().Done():
			close(stopped)
		}
	}))
	defer host.Close()
	client := apiClient{endpoint: host.URL, token: "agent-token-233"}
	reader, writer := io.Pipe()
	result := make(chan error, 1)
	go func() {
		err := client.queryDatasource(ctx, "source", []byte(`{"queries":[{"refId":"A"}]}`), true, writer)
		writer.Close()
		result <- err
	}()
	first := make([]byte, 1024)
	count, err := reader.Read(first)
	require.NoError(t, err)
	assert.Contains(t, string(first[:count]), `"refId":"A"`)
	close(release)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Contains(t, string(rest), "上海🌍 query failed")
	require.ErrorContains(t, <-result, "1 datasource queries failed")
	// A consumer cancellation closes the HTTP request instead of leaving the SDK running.
	streamCtx, stop := context.WithCancel(ctx)
	reader, writer = io.Pipe()
	go func() {
		err := client.queryDatasource(streamCtx, "cancel", []byte(`{"queries":[{"refId":"A"}]}`), true, writer)
		writer.Close()
		result <- err
	}()
	_, err = reader.Read(first)
	require.NoError(t, err)
	stop()
	require.Error(t, <-result)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("query cancellation did not reach the HTTP server")
	}
	var output bytes.Buffer
	require.ErrorContains(t, client.queryDatasource(ctx, "truncated", []byte(`{"queries":[{"refId":"A"}]}`), true, &output), "truncated line")
	require.ErrorContains(t, client.queryDatasource(ctx, "source", []byte(`{`), true, &output), "input must be JSON")
}
