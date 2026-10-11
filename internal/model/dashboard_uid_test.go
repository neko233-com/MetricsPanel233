package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDashboardUIDOnlyUsesActualDocumentIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"native", "", "native-id"},
		{"classic", `{"uid":"classic"}`, "classic"},
		{"V1 spec", `{"spec":{"uid":"v1"}}`, "v1"},
		{"V2 metadata", `{"metadata":{"name":"v2"},"spec":{"elements":{}}}`, "v2"},
		{"wrapped classic", `{"uid":"outside","metadata":{"name":"outside-metadata"},"dashboard":{"uid":"inside"}}`, "inside"},
		{"wrapped metadata", `{"uid":"outside","dashboard":{"metadata":{"name":"inside-resource"},"spec":{}}}`, "inside-resource"},
		{"wrapped spec", `{"metadata":{"name":"outside-metadata"},"dashboard":{"spec":{"uid":"inside-spec"}}}`, "inside-spec"},
		{"wrapped empty identity", `{"uid":"outside","metadata":{"name":"outside-metadata"},"dashboard":{"title":"No UID"}}`, "native-id"},
		{"wrapped empty UID", `{"metadata":{"name":"outside-metadata"},"dashboard":{"uid":"","spec":{}}}`, "native-id"},
		{"wrapped invalid object", `{"uid":"outside","dashboard":[]}`, "native-id"},
		{"wrapped null", `{"uid":"outside","dashboard":null}`, "native-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, (Dashboard{ID: "native-id", Grafana: json.RawMessage(tc.raw)}).UID())
		})
	}
}
