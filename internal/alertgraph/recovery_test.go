package alertgraph

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryOnlyAllowedAtTheCondition(t *testing.T) {
	const raw = `{"condition":"D","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"math","expression":"90"}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]}}]}},{"refId":"D","datasourceUid":"__expr__","model":{"type":"math","expression":"$C"}}]}`
	_, err := Parse(json.RawMessage(raw))
	require.ErrorContains(t, err, "only allowed to be the alert condition")
	var model map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &model))
	model["condition"] = "C"
	encoded, err := json.Marshal(model)
	require.NoError(t, err)
	p, err := Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, "C", p.RecoveryRef)
}
