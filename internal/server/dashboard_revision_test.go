package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardIfMatchRejectsStaleEditsWithoutChangingSavedData(t *testing.T) {
	s, handler := setup(t, "")
	d, err := s.SaveDashboard(context.Background(), model.Dashboard{ID: "editor", Name: "Initial", Panels: []model.Panel{}})
	require.NoError(t, err)
	put := func(match, name string) *httptest.ResponseRecorder {
		draft := d
		draft.Name = name
		body, err := json.Marshal(draft)
		require.NoError(t, err)
		r := httptest.NewRequest("PUT", "/api/v1/dashboards/editor", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if match != "" {
			r.Header.Set("If-Match", match)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := put(`"`+strconv.FormatInt(d.UpdatedAt, 10)+`"`, "Saved")
	require.Equal(t, 200, w.Code, w.Body.String())
	var saved model.Dashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	assert.Greater(t, saved.UpdatedAt, d.UpdatedAt)
	baseline, err := s.Dashboards(context.Background())
	require.NoError(t, err)
	w = put(strconv.FormatInt(d.UpdatedAt, 10), "Stale")
	assert.Equal(t, 409, w.Code, w.Body.String())
	for _, value := range []string{"*", "W/123", "0", "-1", "1,2", "not-a-revision", `""123""`, `"123`} {
		w = put(value, "Invalid")
		assert.Equal(t, 400, w.Code, value)
	}
	list, err := s.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Equal(t, baseline, list)
	w = put("", "Legacy")
	require.Equal(t, 200, w.Code, w.Body.String())
}
