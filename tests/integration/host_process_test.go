//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e environment) verifyHostProcesses(address, service string) {
	e.t.Helper()
	// The existing suite already runs these two actual Go fixture processes in
	// one container; their supervisor PID files are an independent OS identity.
	fixturePIDs := map[int]bool{}
	for _, fixture := range []string{"go-sqlite", "go-clickhouse"} {
		raw, err := e.compose("exec", "-T", "suite", "cat", "/run/metricspanel-test/"+fixture+".pid")
		require.NoError(e.t, err)
		pid, err := strconv.Atoi(raw)
		require.NoError(e.t, err)
		fixturePIDs[pid] = true
	}
	require.Len(e.t, fixturePIDs, 2)
	var first model.HostProcessList
	firstRaw := e.must(address, http.MethodGet, "/api/v1/host/processes?prefix=GO-service&limit=1", nil)
	require.NoError(e.t, json.Unmarshal(firstRaw, &first))
	require.Equal(e.t, 2, first.Total)
	require.Len(e.t, first.Items, 1)
	require.NotEmpty(e.t, first.ContinueToken)
	require.Equal(e.t, "name_asc_pid_asc", first.Order)
	require.Equal(e.t, "linux", first.Source.OS)
	require.Equal(e.t, "pid_namespace", first.Source.Scope)
	require.NotEmpty(e.t, first.Source.ID)
	require.Equal(e.t, int64(60000), first.ExpiresAt-first.SampledAt)
	var second model.HostProcessList
	secondRaw := e.must(address, http.MethodGet, "/api/v1/host/processes?continue="+url.QueryEscape(first.ContinueToken), nil)
	require.NoError(e.t, json.Unmarshal(secondRaw, &second))
	require.Len(e.t, second.Items, 1)
	require.Empty(e.t, second.ContinueToken)
	require.Equal(e.t, first.SnapshotID, second.SnapshotID)
	require.Equal(e.t, first.SampledAt, second.SampledAt)
	require.Equal(e.t, first.ExpiresAt, second.ExpiresAt)
	require.Equal(e.t, first.Source, second.Source)
	require.Equal(e.t, first.Items[0].Name, second.Items[0].Name)
	require.Less(e.t, first.Items[0].PID, second.Items[0].PID)
	for _, item := range []model.HostProcess{first.Items[0], second.Items[0]} {
		require.True(e.t, fixturePIDs[item.PID], "API PID did not match a running Go fixture")
		delete(fixturePIDs, item.PID)
		require.NotEmpty(e.t, item.StartToken)
		require.NotEmpty(e.t, item.ID)
		require.Equal(e.t, "executable", item.NameSource)
		require.NotNil(e.t, item.CPUSecondsTotal)
		require.NotNil(e.t, item.MemoryRSSBytes)
		memory, err := strconv.ParseUint(*item.MemoryRSSBytes, 10, 64)
		require.NoError(e.t, err)
		require.Greater(e.t, memory, uint64(0))
		path := "/api/v1/host/processes/" + strconv.Itoa(item.PID) + "?startToken=" + url.QueryEscape(item.StartToken)
		var detail model.HostProcessDetail
		require.NoError(e.t, json.Unmarshal(e.must(address, http.MethodGet, path, nil), &detail))
		require.Equal(e.t, item.ID, detail.Process.ID)
		require.Equal(e.t, item.StartToken, detail.Process.StartToken)
		status, raw, err := e.request(address, http.MethodGet, "/api/v1/host/processes/"+strconv.Itoa(item.PID)+"?startToken=incorrect-identity", nil)
		require.NoError(e.t, err)
		require.Equal(e.t, http.StatusConflict, status, string(raw))
	}
	require.Empty(e.t, fixturePIDs, "pagination omitted a same-name Go fixture")
	status, raw, err := e.request(address, http.MethodGet, "/api/v1/host/processes?continue="+url.QueryEscape(first.ContinueToken)+"&limit=2", nil)
	require.NoError(e.t, err)
	require.Equal(e.t, http.StatusBadRequest, status, string(raw))
	if key := first.Items[0].IconKey; key != "" {
		status, icon, err := e.request(address, http.MethodGet, "/api/v1/host/process-icons/"+url.PathEscape(key), nil)
		require.NoError(e.t, err)
		require.Equal(e.t, http.StatusOK, status)
		require.True(e.t, bytes.HasPrefix(icon, []byte{137, 80, 78, 71, 13, 10, 26, 10}), "icon response is not a PNG")
	}
	cliRaw, err := e.compose("exec", "-T", service, "metricspanel", "processes", "list", "--prefix", "GO-service", "--limit", "1")
	require.NoError(e.t, err, cliRaw)
	var cliFirst model.HostProcessList
	require.NoError(e.t, json.Unmarshal([]byte(cliRaw), &cliFirst))
	require.Len(e.t, cliFirst.Items, 1)
	require.Equal(e.t, first.Items[0].ID, cliFirst.Items[0].ID)
	cliRaw, err = e.compose("exec", "-T", service, "metricspanel", "processes", "list", "--continue-token", cliFirst.ContinueToken)
	require.NoError(e.t, err, cliRaw)
	var cliSecond model.HostProcessList
	require.NoError(e.t, json.Unmarshal([]byte(cliRaw), &cliSecond))
	require.Len(e.t, cliSecond.Items, 1)
	require.Equal(e.t, second.Items[0].ID, cliSecond.Items[0].ID)
	require.Equal(e.t, cliFirst.SnapshotID, cliSecond.SnapshotID)
	cliRaw, err = e.compose("exec", "-T", service, "metricspanel", "processes", "get", "--pid", strconv.Itoa(first.Items[0].PID), "--start-token", first.Items[0].StartToken)
	require.NoError(e.t, err, cliRaw)
	var cliDetail model.HostProcessDetail
	require.NoError(e.t, json.Unmarshal([]byte(cliRaw), &cliDetail))
	require.Equal(e.t, first.Items[0].ID, cliDetail.Process.ID)
	require.Equal(e.t, first.Items[0].StartToken, cliDetail.Process.StartToken)
	cliRaw, err = e.compose("exec", "-T", service, "metricspanel", "processes", "list", "--token", "incorrect")
	require.Error(e.t, err)
	assert.Contains(e.t, cliRaw, "HTTP 401")
	e.t.Log("real same-name Go process fixtures, namespace metadata, snapshot pagination, icons, identity checks and authenticated JSON CLI passed")
}
