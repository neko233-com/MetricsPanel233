package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeAnnotationQueriesSurviveRestartAndDisableDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	config := json.RawMessage(`[{"name":"Deployments","datasource":{"uid":"events","type":"test"},"target":{"refId":"Anno","unknown":{"value":233}},"mappings":{"text":{"value":"Detail"}}}]`)
	saved, err := s.SaveDashboard(ctx, model.Dashboard{ID: "notes", Name: "Native", Panels: []model.Panel{}, Annotations: config})
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	all, err := s.Dashboards(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.JSONEq(t, string(config), string(all[1].Annotations))
	assert.JSONEq(t, `null`, string(all[1].Grafana))
	assert.NoError(t, all[0].Validate(), "old dashboards without annotations remain editable")
	saved.Annotations = json.RawMessage(`[]`)
	_, err = s.SaveDashboard(ctx, saved)
	require.NoError(t, err)
	all, err = s.Dashboards(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(all[1].Annotations), "an explicit empty list must not revive builtin queries")
}

func TestNativeAnnotationQueryLimitsRejectInvalidStorage(t *testing.T) {
	s := open(t)
	for _, value := range []string{`null`, `{}`, `[null]`, `[233]`, `[{]`, `[{"text":"` + strings.Repeat("x", 128*1024) + `"}]`, `[` + strings.Repeat(`{},`, 32) + `{}]`} {
		_, err := s.SaveDashboard(context.Background(), model.Dashboard{ID: "bad", Name: "Invalid", Annotations: json.RawMessage(value)})
		require.Error(t, err)
	}
	all, err := s.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 1, "invalid configurations create no dashboard")
}
