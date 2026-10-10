package store

import (
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestGroupComparisonPreservesExactPluginQueryIntegers(t *testing.T) {
	a := model.AlertRule{UID: "sdk", Title: "SDK", Execution: "grafana", Grafana: json.RawMessage(`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{"cursor":9007199254740992}}]}`)}
	b := a
	b.Grafana = json.RawMessage(`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{"cursor":9007199254740993}}]}`)
	assert.NotEqual(t, comparableAlertRule(a, false), comparableAlertRule(b, false))
	assert.NotEqual(t, comparableAlertRule(a, true), comparableAlertRule(b, true), "backend query models must not lose int64 precision")
}
