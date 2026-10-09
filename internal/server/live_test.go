package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/live"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveRealSDKAuthenticationMultiplexPublishAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	temp := t.TempDir()
	binary := filepath.Join(temp, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../plugins/testdata/sdk-backend")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	archive := filepath.Join(temp, "fixture.zip")
	out, err = exec.CommandContext(ctx, binary, "--package", archive).CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)
	database, err := store.Open(filepath.Join(temp, "control.db"))
	require.NoError(t, err)
	defer database.DB.Close()
	srv := server.New(database, "live-token-233", 30)
	srv.Plugins = plugins.New(database, "", []string{"metricspanel-sdk-datasource"})
	defer srv.Plugins.Close()
	defer srv.Live.Close()
	_, err = srv.Plugins.Install(ctx, raw, "")
	require.NoError(t, err)
	handler := srv.Handler()
	host := httptest.NewServer(handler)
	defer host.Close()
	token := "live-token-233"
	source := `{"uid":"live-sdk","name":"Live SDK","type":"metricspanel-sdk-datasource","secureJsonData":{"apiKey":"test-secret-233"}}`
	require.Equal(t, 200, call(handler, "POST", "/api/datasources", source, token, "").Code)
	stats := func() map[string]int {
		w := call(handler, "GET", "/api/datasources/uid/live-sdk/resources/stream-stats", "", token, "")
		require.Equal(t, 200, w.Code, w.Body.String())
		var result map[string]int
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		return result
	}
	await := func(check func() bool) { require.Eventually(t, check, 5*time.Second, 20*time.Millisecond) }
	opts := live.WatchOptions{Endpoint: host.URL, Token: token, Channel: "ds/live-sdk/counter"}
	watch := func(options live.WatchOptions) (context.CancelFunc, <-chan live.Event, <-chan error) {
		streamCtx, stop := context.WithCancel(ctx)
		packets := make(chan live.Event, 100)
		done := make(chan error, 1)
		go func() {
			done <- live.Watch(streamCtx, options, func(event live.Event) error {
				select {
				case packets <- event:
					return nil
				case <-streamCtx.Done():
					return streamCtx.Err()
				}
			})
			close(packets)
		}()
		return stop, packets, done
	}
	packet := func(events <-chan live.Event) live.Event {
		select {
		case event, open := <-events:
			require.True(t, open, "stream closed before initial frame")
			return event
		case <-time.After(5 * time.Second):
			t.Fatal("no Live frame received")
			return live.Event{}
		}
	}
	result := func(done <-chan error) error {
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Live watch did not stop")
			return nil
		}
	}
	unauth := opts
	unauth.Token = ""
	require.ErrorContains(t, live.Watch(ctx, unauth, func(live.Event) error { return nil }), "HTTP 401")
	foreign := opts
	foreign.Header = http.Header{"Origin": {"https://evil.example"}}
	require.ErrorContains(t, live.Watch(ctx, foreign, func(live.Event) error { return nil }), "HTTP 403")
	session := call(handler, "POST", "/api/live/session", "", token, "")
	require.Equal(t, 200, session.Code)
	cookies := session.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "/api/live/ws", cookies[0].Path)
	assert.True(t, cookies[0].HttpOnly)
	req := httptest.NewRequest("GET", "/api/live/channels", nil)
	req.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, 401, w.Code, "Live cookie must not authorize data APIs")
	cookieOpts := opts
	cookieOpts.Token = ""
	cookieOpts.Header = http.Header{"Cookie": {cookies[0].String()}}
	cookieOpts.Limit = 2
	require.NoError(t, live.Watch(ctx, cookieOpts, func(live.Event) error { return nil }))
	await(func() bool { return stats()["active"] == 0 && srv.Live.Snapshot().Connections == 0 })
	baseline := stats()["started"]
	for _, path := range []string{"denied", "missing"} {
		denied := opts
		denied.Channel = "ds/live-sdk/" + path
		_, _, done := watch(denied)
		require.ErrorContains(t, result(done), "Live protocol error")
	}
	first := opts
	first.Metadata = json.RawMessage(`{"b":2,"a":1}`)
	stop1, events1, done1 := watch(first)
	defer stop1()
	assert.Equal(t, "initial", packet(events1).Type)
	second := opts
	second.Metadata = json.RawMessage(`{"a":1,"b":2}`)
	stop2, events2, done2 := watch(second)
	defer stop2()
	assert.Equal(t, "initial", packet(events2).Type)
	await(func() bool {
		snapshot := srv.Live.Snapshot()
		return snapshot.Connections == 2 && len(snapshot.Channels) == 1 && snapshot.Channels[0].Subscribers == 2 && snapshot.Channels[0].Packets > 0
	})
	assert.Equal(t, baseline+1, stats()["started"], "two subscribers must share one SDK RunStream")
	mismatch := opts
	mismatch.Metadata = json.RawMessage(`{"a":3}`)
	_, _, mismatchDone := watch(mismatch)
	require.ErrorContains(t, result(mismatchDone), "metadata must match")
	published := `{"channel":"ds/live-sdk/counter","data":{"schema":{"name":"published","fields":[{"name":"Time","type":"time"},{"name":"Value","type":"number"}]},"data":{"values":[[1700000000000],[9000]]}}}`
	w = call(handler, "POST", "/api/live/publish", published, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	for _, events := range []<-chan live.Event{events1, events2} {
		found := false
		for !found {
			found = bytes.Contains(packet(events).Data, []byte("9000"))
		}
	}
	stop1()
	require.ErrorIs(t, result(done1), context.Canceled)
	await(func() bool {
		snapshot := srv.Live.Snapshot()
		return snapshot.Connections == 1 && len(snapshot.Channels) == 1 && snapshot.Channels[0].Subscribers == 1
	})
	assert.Equal(t, 1, stats()["active"])
	w = call(handler, "PUT", "/api/datasources/uid/live-sdk", strings.Replace(source, "Live SDK", "Updated SDK", 1), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.ErrorContains(t, result(done2), "2501")
	await(func() bool { return stats()["active"] == 0 && len(srv.Live.Snapshot().Channels) == 0 })
	stop3, events3, done3 := watch(opts)
	defer stop3()
	packet(events3)
	require.Equal(t, 200, call(handler, "DELETE", "/api/datasources/uid/live-sdk", "", token, "").Code)
	require.ErrorContains(t, result(done3), "2101")
	require.Equal(t, 200, call(handler, "POST", "/api/datasources", source, token, "").Code)
	stop4, events4, done4 := watch(opts)
	defer stop4()
	packet(events4)
	require.Equal(t, 200, call(handler, "PUT", "/api/v1/plugins/metricspanel-sdk-datasource", `{"enabled":false}`, token, "").Code)
	require.Error(t, result(done4))
	await(func() bool { return len(srv.Live.Snapshot().Channels) == 0 })
	require.Equal(t, 200, call(handler, "PUT", "/api/v1/plugins/metricspanel-sdk-datasource", `{"enabled":true}`, token, "").Code)
	stop5, events5, done5 := watch(opts)
	defer stop5()
	packet(events5)
	srv.Live.Close()
	require.Error(t, result(done5))
	assert.Empty(t, srv.Live.Snapshot().Channels)
	assert.Zero(t, srv.Live.Snapshot().Connections)
	require.Equal(t, 200, call(handler, "DELETE", "/api/datasources/uid/live-sdk", "", token, "").Code)
	require.NoError(t, srv.Plugins.Uninstall(ctx, "metricspanel-sdk-datasource"))
	entries, err := os.ReadDir(srv.Plugins.Root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
