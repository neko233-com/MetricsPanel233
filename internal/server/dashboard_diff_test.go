package server

import (
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDashboardDiffPreservesNumberValuesAndPointerPaths(t *testing.T) {
	base, err := historyJSON(json.RawMessage(`{"a/b~c":9007199254740993,"array":[null,1],"equal":1e10000,"zero":-0}`))
	require.NoError(t, err)
	next, err := historyJSON(json.RawMessage(`{"a/b~c":9007199254740994,"array":[233],"equal":10e9999,"zero":0.00,"added":null}`))
	require.NoError(t, err)
	changes := diffDashboardJSON(base, next)
	require.Len(t, changes, 4)
	require.Equal(t, "/a~1b~0c", changes[0].Path)
	require.Equal(t, "9007199254740993", string(changes[0].Before))
	require.Equal(t, "9007199254740994", string(changes[0].After))
	require.Equal(t, "null", string(changes[1].After))
	require.Equal(t, "null", string(changes[2].Before))
	require.Equal(t, "remove", changes[3].Operation)
	require.Equal(t, "1", string(changes[3].Before))
}

func TestDashboardDiffIgnoresResourceIdentityAndDetectsRenderedTitle(t *testing.T) {
	base := model.Dashboard{Name: "Dashboard", Grafana: json.RawMessage(`{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"before","generation":7},"spec":{"uid":"before","id":1,"version":7,"title":"Dashboard","panels":[]}}`)}
	next := model.Dashboard{Name: "Dashboard", Grafana: json.RawMessage(`{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"after","generation":9},"spec":{"uid":"after","id":99,"version":9,"title":"Dashboard","panels":[]}}`)}
	changes, err := compareDashboardSpecs(base, next)
	require.NoError(t, err)
	require.Empty(t, changes)
	next.Name = "Renamed"
	changes, err = compareDashboardSpecs(base, next)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "/title", changes[0].Path)
}
