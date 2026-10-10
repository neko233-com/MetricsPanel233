package alertgraph

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLAlertGraphForcesTransientAlertFormatAndCaptures(t *testing.T) {
	raw := json.RawMessage(`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"sql","format":"table","expression":"SELECT host, CAST(memory > budget AS SIGNED) AS firing FROM A ORDER BY host","customInteger":14695981039346656037}},{"refId":"A","datasourceUid":"sdk","model":{}}]}`)
	p, err := Parse(raw)
	require.NoError(t, err)
	source := func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("host", nil, []string{"go", "mysql"}), data.NewField("memory", nil, []float64{233, 99}), data.NewField("budget", nil, []float64{100, 100}))}}}, "sdk", nil
	}
	out, err := Execute(context.Background(), p, time.Now(), source)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, float64(1), out[0].Value)
	assert.Equal(t, float64(0), out[1].Value)
	assert.Equal(t, "go", out[0].Labels["host"])
	assert.Equal(t, float64(1), *out[0].Captures["C"].Value)
	assert.NotContains(t, out[0].Captures, "A")
	assert.Contains(t, string(p.Data[0].Model), `"format":"table"`)
	assert.Contains(t, string(p.Groups(time.Now())["__expr__"][0].JSON), `14695981039346656037`)
	p.Data[0].Model = json.RawMessage(`{"type":"sql","expression":"SELECT host, memory AS value FROM A WHERE 1=0"}`)
	out, err = Execute(context.Background(), p, time.Now(), source)
	require.NoError(t, err)
	assert.Empty(t, out)
	p.Data[0].Model = json.RawMessage(`{"type":"sql","expression":"SELECT host, NULLIF(memory,memory) AS value FROM A ORDER BY host"}`)
	out, err = Execute(context.Background(), p, time.Now(), source)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.False(t, out[0].Missing)
	assert.True(t, math.IsNaN(out[0].Value))
}
