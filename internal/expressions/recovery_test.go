package expressions

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryDimensionsBoundariesNullAndNonfinite(t *testing.T) {
	labels := data.Labels{"host": "go", "zone": "233"}
	fp := strconv.FormatUint(uint64(labels.Fingerprint()), 10)
	var m queryModel
	require.NoError(t, json.Unmarshal([]byte(`{"type":"threshold","expression":"A","invert":true,"conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]}}]}`), &m))
	m.Conditions[0].LoadedFingerprints = []string{fp, "18446744073709551615"}
	op, err := compile(m)
	require.NoError(t, err)
	for _, test := range []struct {
		name        string
		labels      data.Labels
		value, want *float64
	}{
		{"new dead band", data.Labels{"host": "new"}, ptr(90), ptr(0)},
		{"new load boundary", data.Labels{"host": "new"}, ptr(100), ptr(0)},
		{"new firing", data.Labels{"host": "new"}, ptr(101), ptr(1)},
		{"loaded dead band", labels, ptr(90), ptr(1)},
		{"loaded recovery boundary", labels, ptr(80), ptr(1)},
		{"loaded recovery", labels, ptr(79), ptr(0)},
		{"loaded null", labels, nil, nil},
		{"loaded NaN", labels, ptr(math.NaN()), ptr(1)},
		{"new NaN", data.Labels{}, ptr(math.NaN()), ptr(0)},
		{"loaded positive infinity", labels, ptr(math.Inf(1)), ptr(1)},
		{"loaded negative infinity", labels, ptr(math.Inf(-1)), ptr(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := value{labels: test.labels, points: []*float64{test.value}}
			out, err := op.execute(map[string]values{"A": {input}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
			require.NoError(t, err)
			require.Len(t, out, 1)
			assert.Equal(t, test.want, out[0].points[0])
			assert.Equal(t, test.labels, out[0].labels)
			assert.Equal(t, test.value, input.points[0])
		})
	}
	// Ranges are not reordered; inclusive recovery boundaries belong to recovery.
	m.Conditions[0].Evaluator = thresholdEvaluator{Type: "outside_range", Params: []float64{0, 100}}
	m.Conditions[0].UnloadEvaluator = json.RawMessage(`{"type":"within_range_included","params":[20,80]}`)
	op, err = compile(m)
	require.NoError(t, err)
	input := value{labels: labels, times: []time.Time{time.Unix(1, 0), time.Unix(2, 0), time.Unix(3, 0)}, points: []*float64{ptr(90), ptr(80), nil}}
	out, err := op.execute(map[string]values{"A": {input}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, []*float64{ptr(1), ptr(0), nil}, out[0].points)
	assert.Equal(t, input.times, out[0].times)
}

func TestRecoveryLegacyFramesPrecisionPrecedenceAndValidation(t *testing.T) {
	labels := data.Labels{"host": "go"}
	fp := uint64(labels.Fingerprint())
	require.Greater(t, fp, uint64(1<<53))
	f := data.NewFrame("", data.NewField("fingerprints", nil, []uint64{fp}))
	f.SetMeta(&data.FrameMeta{Type: "fingerprints", TypeVersion: data.FrameTypeVersion{1, 0}})
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	var reordered map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &reordered))
	raw, err = json.Marshal(reordered)
	require.NoError(t, err)
	var m queryModel
	require.NoError(t, json.Unmarshal([]byte(`{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lte","params":[80]}}]}`), &m))
	m.Conditions[0].LoadedDimensions = raw
	op, err := compile(m)
	require.NoError(t, err)
	_, exists := op.loaded[data.Fingerprint(fp)]
	assert.True(t, exists)
	m.Conditions[0].LoadedFingerprints = []string{}
	op, err = compile(m)
	require.NoError(t, err)
	assert.Empty(t, op.loaded, "an explicit new list overrides legacy state")
	m.Conditions[0].LoadedFingerprints = []string{"18446744073709551616"}
	_, err = compile(m)
	require.ErrorContains(t, err, "fingerprint")
	m.Conditions[0].LoadedFingerprints = nil
	m.Conditions[0].LoadedDimensions = json.RawMessage(`{"schema":{},"data":{}}`)
	_, err = compile(m)
	require.Error(t, err)
	m.Conditions[0].UnloadEvaluator = json.RawMessage(`{"type":"sql","params":[80]}`)
	_, err = compile(m)
	require.ErrorContains(t, err, "recovery")
	m.Conditions[0].UnloadEvaluator = json.RawMessage(`null`)
	_, err = compile(m)
	require.NoError(t, err, "plain thresholds ignore cached recovery data")
}

func TestRecoveryStatePatchingRetainsUnknownJSONAndOriginalModel(t *testing.T) {
	raw := json.RawMessage(`{"type":"threshold","expression":"A","opaque":18446744073709551615,"conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]},"loadedFingerprints":["invalid cached state"],"loadedDimensions":{"schema":{},"data":{}}}]}`)
	original := append(json.RawMessage(nil), raw...)
	patched, err := WithLoadedFingerprints(raw, []string{"23"})
	require.NoError(t, err)
	assert.Equal(t, original, raw)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(patched, &fields))
	assert.Equal(t, "18446744073709551615", string(fields["opaque"]))
	var conditions []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["conditions"], &conditions))
	assert.Equal(t, `["23"]`, string(conditions[0]["loadedFingerprints"]))
	assert.NotContains(t, conditions[0], "loadedDimensions")
	assert.True(t, HasRecovery(patched))
	_, _, err = Describe(patched)
	require.NoError(t, err)
}
