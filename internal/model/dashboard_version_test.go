package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDashboardSourceVersionUsesDocumentContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int64
	}{
		{"native", "", 1},
		{"null", `null`, 1},
		{"classic", `{"version":5,"metadata":{"generation":17}}`, 5},
		{"classic precision", `{"version":9007199254740993}`, 9007199254740993},
		{"V1", `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"generation":17},"spec":{"version":5}}`, 5},
		{"wrapped V1", `{"dashboard":{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"generation":17},"spec":{"version":5}}}`, 5},
		{"wrapper fields are not document fields", `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"generation":17},"dashboard":{"version":5}}`, 5},
		{"V1 root fields are not spec fields", `{"apiVersion":"dashboard.grafana.app/v1beta1","elements":{},"metadata":{"generation":17},"spec":{"version":5}}`, 5},
		{"V1 missing spec version ignores root version", `{"apiVersion":"dashboard.grafana.app/v1beta1","version":5,"metadata":{"generation":17},"spec":{}}`, 17},
		{"V1 generation fallback", `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"generation":17},"spec":{}}`, 17},
		{"V2 API", `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"generation":17},"spec":{"version":5}}`, 17},
		{"V2 elements", `{"metadata":{"generation":17},"spec":{"version":5,"elements":{}}}`, 17},
		{"raw V2 spec", `{"version":5,"elements":{}}`, 5},
		{"V2 version fallback", `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{},"spec":{"version":5,"elements":{}}}`, 5},
		{"empty version", `{"version":0,"metadata":{"generation":0}}`, 1},
		{"negative version", `{"version":-1,"metadata":{"generation":-2}}`, 1},
		{"invalid version", `{"version":"future","metadata":{"generation":17}}`, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, (Dashboard{Grafana: json.RawMessage(tc.raw)}).SourceVersion())
		})
	}
}
