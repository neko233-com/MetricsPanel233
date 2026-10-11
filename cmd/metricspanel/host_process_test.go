package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run the current Go test executable twice so the real OS provider sees two
// long-lived processes with one name. Closing stdin lets each fixture exit.
func TestHostProcessCLIChildFixture(t *testing.T) {
	if os.Getenv("METRICSPANEL_PROCESS_CLI_FIXTURE") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startHostProcessCLIFixture(t *testing.T) (int, func()) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	command := exec.Command(executable, "-test.run=^TestHostProcessCLIChildFixture$")
	command.Env = append(os.Environ(), "METRICSPANEL_PROCESS_CLI_FIXTURE=1")
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	var once sync.Once
	stop := func() {
		once.Do(func() {
			require.NoError(t, stdin.Close())
			require.NoError(t, command.Wait())
		})
	}
	t.Cleanup(stop)
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(ready))
	return command.Process.Pid, stop
}

func TestHostProcessCLIUsesRealServerAndNativeProvider(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native process provider is unavailable on this platform")
	}
	t.Setenv("METRICSPANEL_TOKEN", "native-process-token")
	firstPID, stopFirst := startHostProcessCLIFixture(t)
	secondPID, _ := startHostProcessCLIFixture(t)
	control, err := store.Open(filepath.Join(t.TempDir(), "processes.db"))
	require.NoError(t, err)
	defer control.DB.Close()
	srv := server.New(control, "native-process-token", 30)
	defer srv.Plugins.Close()
	defer srv.Live.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	get := func(pid int, token string) (model.HostProcessDetail, error) {
		args := []string{"processes", "get", "--pid", strconv.Itoa(pid), "--server", h.URL}
		if token != "" {
			args = append(args, "--start-token", token)
		}
		raw, err := capture(t, args...)
		var result model.HostProcessDetail
		if err == nil {
			require.NoError(t, json.Unmarshal(raw, &result))
		}
		return result, err
	}
	first, err := get(firstPID, "")
	require.NoError(t, err)
	second, err := get(secondPID, "")
	require.NoError(t, err)
	require.Equal(t, first.Process.Name, second.Process.Name)
	require.NotEmpty(t, first.Process.StartToken)
	require.NotEmpty(t, second.Process.StartToken)
	require.NotEqual(t, first.Process.ID, second.Process.ID)
	list := func(flags ...string) (model.HostProcessList, error) {
		args := append([]string{"processes", "list", "--server", h.URL}, flags...)
		raw, err := capture(t, args...)
		var result model.HostProcessList
		if err == nil {
			require.NoError(t, json.Unmarshal(raw, &result))
		}
		return result, err
	}
	page, err := list("--prefix", strings.ToUpper(first.Process.Name), "--limit", "1")
	require.NoError(t, err)
	require.GreaterOrEqual(t, page.Total, 2)
	require.Len(t, page.Items, 1)
	require.NotEmpty(t, page.ContinueToken)
	require.Equal(t, "name_asc_pid_asc", page.Order)
	require.Equal(t, int64(60000), page.ExpiresAt-page.SampledAt)
	require.Equal(t, runtime.GOOS, page.Source.OS)
	require.NotEmpty(t, page.Source.ID)
	require.NotEmpty(t, page.Source.Hostname)
	require.Contains(t, []string{"host", "pid_namespace"}, page.Source.Scope)
	initial := page
	items := append([]model.HostProcess{}, page.Items...)
	for page.ContinueToken != "" {
		require.LessOrEqual(t, len(items), initial.Total)
		page, err = list("--continue-token", page.ContinueToken)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, initial.SnapshotID, page.SnapshotID)
		require.Equal(t, initial.SampledAt, page.SampledAt)
		require.Equal(t, initial.ExpiresAt, page.ExpiresAt)
		require.Equal(t, initial.Total, page.Total)
		items = append(items, page.Items...)
	}
	require.Len(t, items, initial.Total)
	seen := map[string]bool{}
	found := map[int]model.HostProcess{}
	for i, item := range items {
		require.False(t, seen[item.ID], "continuation repeated a process identity")
		seen[item.ID] = true
		found[item.PID] = item
		require.True(t, strings.HasPrefix(strings.ToLower(item.Name), strings.ToLower(first.Process.Name)))
		if i > 0 {
			previous := items[i-1]
			left, right := strings.ToLower(previous.Name), strings.ToLower(item.Name)
			require.True(t, left < right || left == right && previous.PID < item.PID, "page boundary broke fixed name/PID ordering")
		}
	}
	require.Equal(t, first.Process.ID, found[firstPID].ID)
	require.Equal(t, second.Process.ID, found[secondPID].ID)
	detail, err := get(firstPID, first.Process.StartToken)
	require.NoError(t, err)
	require.Equal(t, first.Process.ID, detail.Process.ID)
	_, err = get(firstPID, "different-exact-token")
	require.ErrorContains(t, err, "HTTP 409")
	_, err = capture(t, "processes", "list", "--token", "incorrect", "--server", h.URL)
	require.ErrorContains(t, err, "HTTP 401")
	_, err = list("--continue-token", initial.ContinueToken, "--limit", "2")
	require.ErrorContains(t, err, "HTTP 400")
	_, err = list("--continue-token", initial.ContinueToken, "--prefix", "changed")
	require.ErrorContains(t, err, "HTTP 400")
	_, err = list("--continue-token", "malformed+exact/token")
	require.ErrorContains(t, err, "HTTP 400")
	stopFirst()
	_, err = get(firstPID, first.Process.StartToken)
	require.ErrorContains(t, err, "HTTP 404")
	// Bounded snapshot eviction also exercises the API's restart-list response,
	// without a 60-second sleep; manager tests cover expiry against a fixed clock.
	for i := 0; i < 9; i++ {
		_, err = list("--prefix", first.Process.Name, "--limit", "1")
		require.NoError(t, err)
	}
	_, err = list("--continue-token", initial.ContinueToken)
	require.ErrorContains(t, err, "HTTP 410")
}

func TestHostProcessCLIEncodesFiltersAndOpaqueIdentityWithoutRounding(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "process-token")
	const startToken = "windows:18446744073709551615"
	const continueToken = "snapshot+page/==&opaque"
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer process-token", r.Header.Get("Authorization"))
		query := r.URL.Query()
		switch calls.Add(1) {
		case 1:
			assert.Equal(t, "/api/v1/host/processes", r.URL.Path)
			assert.Equal(t, "worker & +", query.Get("prefix"))
			assert.Equal(t, "1", query.Get("limit"))
			assert.Equal(t, "", query.Get("continue"))
		case 2:
			assert.Equal(t, "/api/v1/host/processes", r.URL.Path)
			assert.Equal(t, continueToken, query.Get("continue"))
			assert.False(t, query.Has("prefix"))
			assert.False(t, query.Has("limit"))
		case 3:
			assert.Equal(t, "/api/v1/host/processes/233", r.URL.Path)
			assert.Equal(t, startToken, query.Get("startToken"))
		case 4:
			assert.Equal(t, "/api/v1/host/processes/0", r.URL.Path)
			assert.False(t, query.Has("startToken"))
		case 5:
			assert.Equal(t, "/api/v1/host/processes", r.URL.Path)
			assert.Equal(t, "200", query.Get("limit"))
		default:
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"pid":233,"name":"Agent","startToken":"windows:18446744073709551615"}],"continueToken":"snapshot+page/==&opaque","opaque":9007199254740993}`))
	}))
	defer h.Close()
	args := [][]string{
		{"processes", "list", "--prefix", "Worker & +", "--limit", "1", "--server", h.URL},
		{"processes", "list", "--continue-token", continueToken, "--server", h.URL},
		{"processes", "get", "--pid", "233", "--start-token", startToken, "--server", h.URL},
		{"processes", "get", "--pid", "0", "--server", h.URL},
		{"processes", "list", "--server", h.URL},
	}
	for _, argv := range args {
		data, err := capture(t, argv...)
		require.NoError(t, err)
		require.Contains(t, string(data), startToken)
		require.Contains(t, string(data), "9007199254740993")
	}
	require.Equal(t, int32(5), calls.Load())
}

func TestHostProcessCLIRejectsInvalidAndMutationCommandsBeforeRequest(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer h.Close()
	for _, argv := range [][]string{
		{"processes"},
		{"processes", "kill", "--pid", "233"},
		{"processes", "list", "--prefix", strings.Repeat("a", 129)},
		{"processes", "list", "--prefix", "bad\nname"},
		{"processes", "list", "--prefix", string([]byte{0xff})},
		{"processes", "list", "--filter", "Agent"},
		{"processes", "list", "--cursor", "obsolete"},
		{"processes", "list", "--limit", "0"},
		{"processes", "list", "--limit", "1001"},
		{"processes", "list", "unexpected"},
		{"processes", "get", "--pid", "-1", "--start-token", "exact"},
		{"processes", "get", "--pid", "4294967296", "--start-token", "exact"},
		{"processes", "get", "--pid", "1.5"},
		{"processes", "get", "--pid", "233", "--start-token", strings.Repeat("1", 257)},
		{"processes", "get", "--start-token", "exact"},
		{"processes", "get", "--pid", "233", "--start-token", "exact", "--filter", "Agent"},
	} {
		argv = append(argv, "--server", h.URL)
		_, err := capture(t, argv...)
		require.Error(t, err, "%v", argv)
	}
	require.Zero(t, calls.Load())
}

func TestHostProcessCLISchemaDiscoversReadOnlyScopeAndOpaqueTokens(t *testing.T) {
	data, err := capture(t, "schema")
	require.NoError(t, err)
	var document struct {
		Commands      []string            `json:"commands"`
		API           map[string][]string `json:"api"`
		HostProcesses map[string]any      `json:"host_processes"`
	}
	require.NoError(t, json.Unmarshal(data, &document))
	require.Contains(t, document.API["GET"], "/api/v1/host/processes")
	require.Contains(t, document.API["GET"], "/api/v1/host/processes/{pid}")
	require.Contains(t, document.HostProcesses["scope"], "server host/process namespace")
	require.Contains(t, document.HostProcesses["identity"], "exact string startToken")
	require.Contains(t, document.HostProcesses["freshness"], "expiresAt")
	require.NotEmpty(t, document.HostProcesses["operations"])
}
