package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignedPluginAssetsAuthenticationScopeAndLifecycle(t *testing.T) {
	token := "plugin-test-token-233"
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, token, 30)
	defer srv.Plugins.Close()
	h := srv.Handler()
	raw, err := os.ReadFile("../plugins/testdata/grafana-clock-panel-3.2.4.zip")
	require.NoError(t, err)
	install := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/plugins/install", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/zip")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := install()
	require.Equal(t, 200, first.Code, first.Body.String())
	assert.NotContains(t, first.Body.String(), `"files"`)
	assert.JSONEq(t, first.Body.String(), install().Body.String())
	path := "/public/plugins/grafana-clock-panel/module.js"
	assert.Equal(t, 401, call(h, "GET", path, "", "", "").Code)
	assert.Equal(t, 200, call(h, "GET", path, "", token, "").Code)
	session := call(h, "POST", "/api/v1/plugins/assets-session", "", token, "")
	require.Equal(t, 200, session.Code)
	cookies := session.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, "/public/plugins/", cookies[0].Path)
	request := httptest.NewRequest("GET", path, nil)
	request.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "define(")
	assert.Contains(t, w.Header().Get("Content-Type"), "javascript")
	request = httptest.NewRequest("GET", "/api/v1/plugins", nil)
	request.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	assert.Equal(t, 401, w.Code, "asset cookie must not authorize data APIs")
	request = httptest.NewRequest("GET", path, nil)
	request.AddCookie(cookies[0])
	request.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	assert.Equal(t, 403, w.Code)
	assert.Equal(t, 200, call(h, "PUT", "/api/v1/plugins/grafana-clock-panel", `{"enabled":false}`, token, "").Code)
	assert.Equal(t, 403, call(h, "GET", path, "", token, "").Code)
	assert.Equal(t, 200, call(h, "PUT", "/api/v1/plugins/grafana-clock-panel", `{"enabled":true}`, token, "").Code)
	assert.Equal(t, 200, call(h, "DELETE", "/api/v1/plugins/grafana-clock-panel", "", token, "").Code)
	entries, err := os.ReadDir(srv.Plugins.Root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDataSourceCRUDProxySecretsAndNativeQueryProtocol(t *testing.T) {
	s, h := setup(t, "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer external-secret", r.Header.Get("Authorization"))
		assert.NotEqual(t, "root-secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"remote"},"value":[1700000000,"233"]}]}}`))
	}))
	defer upstream.Close()
	body := `{"name":"Remote","type":"prometheus","url":"` + upstream.URL + `","jsonData":{"httpHeaderName1":"Authorization"},"secureJsonData":{"httpHeaderValue1":"Bearer external-secret"}}`
	w := call(h, "POST", "/api/datasources", body, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "external-secret")
	var saved struct {
		UID string `json:"uid"`
		ID  int64  `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	require.NotEmpty(t, saved.UID)
	assert.Greater(t, saved.ID, int64(1))
	w = call(h, "GET", "/api/datasources/proxy/uid/"+saved.UID+"/api/v1/query?query=vector(1)", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "233")
	now := time.Now().UnixMilli()
	request := `{"from":"` + strconv.FormatInt(now-60000, 10) + `","to":"` + strconv.FormatInt(now, 10) + `","queries":[{"refId":"A","expr":"vector(233)","instant":true,"datasource":{"uid":"metricspanel","type":"prometheus"}},{"refId":"B","expr":"vector(1)","instant":true,"datasource":{"uid":"` + saved.UID + `","type":"prometheus"}}]}`
	w = call(h, "POST", "/api/ds/query", request, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var response backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Responses, 2)
	require.Len(t, response.Responses["A"].Frames, 1)
	assert.Equal(t, float64(233), *response.Responses["A"].Frames[0].Fields[1].At(0).(*float64))
	require.Len(t, response.Responses["B"].Frames, 1)
	w = call(h, "POST", "/api/ds/query", strings.ReplaceAll(request, `"instant":true`, `"instant":true,"format":"table"`), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	table := response.Responses["B"].Frames[0]
	require.Len(t, table.Fields, 3)
	assert.Equal(t, "job", table.Fields[1].Name)
	assert.Equal(t, "remote", table.Fields[1].At(0))
	assert.Equal(t, float64(233), *table.Fields[2].At(0).(*float64))
	assert.Equal(t, 400, call(h, "DELETE", "/api/datasources/uid/metricspanel", "", "", "").Code)
	assert.Equal(t, 200, call(h, "DELETE", "/api/datasources/uid/"+saved.UID, "", "", "").Code)
	_, err := s.DataSourceSecrets(context.Background(), saved.UID)
	require.Error(t, err)
}
