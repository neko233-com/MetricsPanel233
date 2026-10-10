package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

func publicPlugin(p model.Plugin) model.Plugin { p.Files = nil; return p }

func (s *Server) filterPluginWarnings(ctx context.Context, warnings []string) []string {
	installed, err := s.Store.Plugins(ctx)
	if err != nil {
		return warnings
	}
	result := []string{}
	for _, warning := range warnings {
		handled := false
		for _, p := range installed {
			if p.Enabled && p.Type == "panel" && strings.Contains(warning, fmt.Sprintf("plugin %q requires a compatible renderer", p.ID)) {
				handled = true
				break
			}
		}
		if !handled {
			result = append(result, warning)
		}
	}
	return result
}
func (s *Server) assetSession(expires int64) string {
	payload := strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, []byte(s.Token))
	mac.Write([]byte("plugin-assets:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) validAssetSession(r *http.Request) bool {
	cookie, err := r.Cookie("metricspanel-plugin-assets")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires < time.Now().Unix() || expires > time.Now().Add(16*time.Minute).Unix() {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(s.assetSession(expires)))
}
func (s *Server) pluginRoutes(api, mux *http.ServeMux) {
	api.HandleFunc("GET /api/v1/plugins", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Store.Plugins(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		for i := range items {
			effective, err := s.Store.PluginEffective(r.Context(), items[i].ID)
			if err != nil {
				fail(w, 500, err)
				return
			}
			items[i] = effective
			items[i] = publicPlugin(items[i])
		}
		writeJSON(w, 200, items)
	})
	api.HandleFunc("GET /api/v1/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Store.PluginEffective(r.Context(), r.PathValue("id"))
		if err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, publicPlugin(p))
	})
	api.HandleFunc("POST /api/v1/plugins/assets-session", func(w http.ResponseWriter, r *http.Request) {
		expires := time.Now().Add(15 * time.Minute)
		http.SetCookie(w, &http.Cookie{Name: "metricspanel-plugin-assets", Value: s.assetSession(expires.Unix()), Path: "/public/plugins/", Expires: expires, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]int64{"expires_at": expires.UnixMilli()})
	})
	api.HandleFunc("POST /api/v1/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/zip") {
			fail(w, 400, errors.New("Content-Type must be application/zip"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, plugins.MaxArchiveBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, 400, err)
			return
		}
		p, err := s.Plugins.Install(r.Context(), raw, r.Header.Get("X-Archive-SHA256"))
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, publicPlugin(p))
	})
	api.HandleFunc("POST /api/v1/plugins/catalog", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := s.Plugins.CatalogInstall(r.Context(), input.ID, input.Version)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, publicPlugin(p))
	})
	api.HandleFunc("PUT /api/v1/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		p, err := s.Plugins.SetEnabled(r.Context(), r.PathValue("id"), input.Enabled)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if !input.Enabled {
			s.invalidatePlugin(r.Context(), p.ID, false)
		}
		writeJSON(w, 200, publicPlugin(p))
	})
	api.HandleFunc("DELETE /api/v1/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Plugins.Uninstall(r.Context(), r.PathValue("id")); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				resourceError(w, err)
			} else {
				fail(w, 409, err)
			}
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	api.HandleFunc("GET /api/plugins", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Store.Plugins(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		out := []any{}
		for _, p := range items {
			metadata, err := s.pluginMetadata(r.Context(), p.ID)
			if err != nil {
				fail(w, 500, err)
				return
			}
			out = append(out, metadata)
		}
		writeJSON(w, 200, out)
	})
	api.HandleFunc("GET /api/plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		metadata, err := s.pluginMetadata(r.Context(), r.PathValue("id"))
		if err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, metadata)
	})
	assets := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := s.Plugins.Asset(r.Context(), r.PathValue("id"), r.PathValue("rest"))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, io.EOF) {
				resourceError(w, sql.ErrNoRows)
			} else {
				fail(w, 403, err)
			}
			return
		}
		contentType := mime.TypeByExtension(path.Ext(r.PathValue("rest")))
		if strings.HasSuffix(r.PathValue("rest"), ".js") {
			contentType = "text/javascript; charset=utf-8"
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		w.Write(data)
	})
	mux.Handle("GET /public/plugins/{id}/{rest...}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && s.validAssetSession(r) {
			clone := r.Clone(r.Context())
			clone.Header = r.Header.Clone()
			clone.Header.Set("Authorization", "Bearer "+s.Token)
			s.protect(assets).ServeHTTP(w, clone)
			return
		}
		s.protect(assets).ServeHTTP(w, r)
	}))
	api.HandleFunc("POST /api/ds/query", s.queryDataSources)
}

func builtinDataSource() model.DataSource {
	return model.DataSource{ID: 1, UID: "metricspanel", OrgID: 1, Name: "MetricsPanel233", Type: "prometheus", Access: "proxy", URL: "/prometheus", IsDefault: true, ReadOnly: true, JSONData: json.RawMessage(`{"httpMethod":"POST","timeInterval":"5s"}`), SecureJSONFields: map[string]bool{}, Version: 1}
}
func (s *Server) dataSource(ctx context.Context, uid string) (model.DataSource, error) {
	if uid == "metricspanel" {
		return builtinDataSource(), nil
	}
	if uid == "grafana" || uid == "-- Grafana --" || uid == "-1" {
		return builtinGrafanaDataSource(), nil
	}
	return s.Store.DataSource(ctx, uid)
}
func (s *Server) datasourceRoutes(api *http.ServeMux, prometheus http.Handler) {
	api.HandleFunc("GET /api/datasources", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Store.DataSources(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		builtin := builtinDataSource()
		for _, ds := range items {
			if ds.IsDefault {
				builtin.IsDefault = false
			}
		}
		writeJSON(w, 200, append([]model.DataSource{builtin, builtinGrafanaDataSource()}, items...))
	})
	api.HandleFunc("GET /api/datasources/uid/{uid}", func(w http.ResponseWriter, r *http.Request) {
		ds, err := s.dataSource(r.Context(), r.PathValue("uid"))
		if err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, ds)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		if uid := r.PathValue("uid"); uid == "metricspanel" || uid == "grafana" || uid == "-- Grafana --" || uid == "-1" {
			fail(w, 400, errors.New("built-in datasource is read-only"))
			return
		}
		var input model.DataSourceInput
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		if uid := r.PathValue("uid"); uid != "" {
			input.UID = uid
			if input.Version == 0 {
				previous, err := s.Store.DataSource(r.Context(), uid)
				if err != nil {
					resourceError(w, err)
					return
				}
				input.Version = previous.Version
			}
		}
		input.Defaults()
		if input.UID == "" && r.Method == http.MethodPost {
			input.UID = uuid.NewString()
		}
		if err := input.Validate(); err != nil {
			fail(w, 400, err)
			return
		}
		ds, err := s.Store.SaveDataSource(r.Context(), input)
		if err != nil {
			if errors.Is(err, store.ErrDataSourceConflict) {
				fail(w, 409, err)
			} else {
				fail(w, 400, err)
			}
			return
		}
		s.Live.Invalidate("ds", ds.UID, true)
		writeJSON(w, 200, map[string]any{"id": ds.ID, "uid": ds.UID, "name": ds.Name, "message": "Datasource saved", "datasource": ds})
	}
	api.HandleFunc("POST /api/datasources", save)
	api.HandleFunc("PUT /api/datasources/uid/{uid}", save)
	api.HandleFunc("DELETE /api/datasources/uid/{uid}", func(w http.ResponseWriter, r *http.Request) {
		if uid := r.PathValue("uid"); uid == "metricspanel" || uid == "grafana" || uid == "-- Grafana --" || uid == "-1" {
			fail(w, 400, errors.New("built-in datasource cannot be deleted"))
			return
		}
		if err := s.Store.DeleteDataSource(r.Context(), r.PathValue("uid")); err != nil {
			resourceError(w, err)
			return
		}
		s.Live.Invalidate("ds", r.PathValue("uid"), false)
		writeJSON(w, 200, map[string]string{"message": "Datasource deleted"})
	})
	api.HandleFunc("GET /api/datasources/uid/{uid}/health", func(w http.ResponseWriter, r *http.Request) {
		ds, err := s.dataSource(r.Context(), r.PathValue("uid"))
		if err != nil {
			resourceError(w, err)
			return
		}
		if ds.UID == "metricspanel" || ds.Type == "grafana" {
			if err := s.Store.Health(r.Context()); err != nil {
				fail(w, 503, err)
				return
			}
			writeJSON(w, 200, map[string]string{"status": "OK", "message": "Data source is working"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if ds.Type == "prometheus" {
			now := time.Now()
			_, err := s.queryPrometheusSource(ctx, ds, backend.DataQuery{RefID: "health", JSON: json.RawMessage(`{"expr":"vector(1)","instant":true}`), TimeRange: backend.TimeRange{From: now, To: now}, MaxDataPoints: 1})
			if err != nil {
				fail(w, 502, err)
				return
			}
			writeJSON(w, 200, map[string]string{"status": "OK", "message": "Data source is working"})
			return
		}
		health, err := s.Plugins.Health(ctx, ds)
		if err != nil {
			fail(w, 502, err)
			return
		}
		var details any
		if len(health.JSONDetails) > 0 {
			if err := json.Unmarshal(health.JSONDetails, &details); err != nil {
				fail(w, 502, errors.New("plugin returned invalid health details JSON"))
				return
			}
		}
		writeJSON(w, 200, map[string]any{"status": health.Status.String(), "message": health.Message, "details": details})
	})
	api.HandleFunc("/api/datasources/uid/{uid}/resources/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		ds, err := s.dataSource(r.Context(), r.PathValue("uid"))
		if err != nil {
			resourceError(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, 400, err)
			return
		}
		target := r.PathValue("rest")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		responses, err := s.Plugins.Resource(ctx, ds, &backend.CallResourceRequest{Method: r.Method, Path: r.PathValue("rest"), URL: target, Body: body, Headers: map[string][]string{"Content-Type": {r.Header.Get("Content-Type")}}})
		if err != nil {
			fail(w, 502, err)
			return
		}
		if len(responses) == 0 {
			w.WriteHeader(204)
			return
		}
		for key, values := range responses[0].Headers {
			if strings.EqualFold(key, "Set-Cookie") || strings.EqualFold(key, "Access-Control-Allow-Origin") {
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		status := responses[0].Status
		if status < 100 || status > 599 {
			status = 200
		}
		w.WriteHeader(status)
		for _, response := range responses {
			w.Write(response.Body)
		}
	})
	api.HandleFunc("/api/datasources/proxy/uid/{uid}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		ds, err := s.dataSource(r.Context(), r.PathValue("uid"))
		if err != nil {
			resourceError(w, err)
			return
		}
		clone := r.Clone(r.Context())
		target := *r.URL
		target.Path = "/" + r.PathValue("rest")
		target.RawPath = ""
		clone.URL = &target
		if ds.UID == "metricspanel" {
			prometheus.ServeHTTP(w, clone)
		} else {
			s.proxyDataSource(w, clone, ds)
		}
	})
}
func (s *Server) proxyDataSource(w http.ResponseWriter, r *http.Request, ds model.DataSource) {
	base, err := url.Parse(ds.URL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		fail(w, 400, errors.New("datasource proxy needs an HTTP(S) URL without embedded credentials"))
		return
	}
	if strings.Contains(r.URL.Path, "..") || strings.HasPrefix(r.URL.Path, "//") {
		fail(w, 400, errors.New("invalid datasource proxy path"))
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + r.URL.Path
	base.RawQuery = r.URL.RawQuery
	var body io.Reader
	if r.Body != nil {
		body = io.LimitReader(r.Body, 4<<20)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, base.String(), body)
	if err != nil {
		fail(w, 400, err)
		return
	}
	request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	secrets, err := s.Store.DataSourceSecrets(r.Context(), ds.UID)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if ds.BasicAuth {
		request.SetBasicAuth(ds.BasicAuthUser, secrets["basicAuthPassword"])
	}
	var settings map[string]any
	_ = json.Unmarshal(ds.JSONData, &settings)
	for i := 1; i <= 32; i++ {
		key := fmt.Sprintf("httpHeaderName%d", i)
		if name, ok := settings[key].(string); ok && name != "" && !strings.EqualFold(name, "Host") && !strings.EqualFold(name, "Cookie") {
			request.Header.Set(name, secrets[fmt.Sprintf("httpHeaderValue%d", i)])
		}
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		fail(w, 502, err)
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil || len(data) > 32<<20 {
		fail(w, 502, errors.New("datasource response exceeds 32 MiB or could not be read"))
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	w.Write(data)
}
func (s *Server) queryDataSources(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Queries       []json.RawMessage `json:"queries"`
		From          string            `json:"from"`
		To            string            `json:"to"`
		Range         json.RawMessage   `json:"range"`
		Interval      string            `json:"interval"`
		IntervalMS    int64             `json:"intervalMs"`
		MaxDataPoints int64             `json:"maxDataPoints"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, 400, err)
		return
	}
	// listFiles is a core SDK query without a time range. Only public file-list
	// requests may omit both endpoints; metric and plugin queries remain strict.
	if input.From == "" && input.To == "" && len(input.Queries) > 0 {
		onlyLists := true
		for _, raw := range input.Queries {
			var query struct {
				QueryType  string `json:"queryType"`
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
			}
			if json.Unmarshal(raw, &query) != nil || query.QueryType != "list" || (query.Datasource.UID != "grafana" && query.Datasource.UID != "-- Grafana --" && query.Datasource.UID != "-1") {
				onlyLists = false
				break
			}
		}
		if onlyLists {
			input.From, input.To = "0", "0"
		}
	}
	from, err := strconv.ParseInt(input.From, 10, 64)
	if err != nil {
		fail(w, 400, errors.New("from must be Unix milliseconds"))
		return
	}
	to, err := strconv.ParseInt(input.To, 10, 64)
	if err != nil || to < from || from < 0 || to-from > 31*24*60*60*1000 {
		fail(w, 400, errors.New("query range must be valid and at most 31 days"))
		return
	}
	if len(input.Queries) == 0 || len(input.Queries) > 32 {
		fail(w, 400, errors.New("request needs 1–32 queries"))
		return
	}
	groups := map[string][]backend.DataQuery{}
	seen := map[string]bool{}
	for _, raw := range input.Queries {
		var query struct {
			RefID      string `json:"refId"`
			Datasource struct {
				UID  string `json:"uid"`
				Type string `json:"type"`
			} `json:"datasource"`
			QueryType     string `json:"queryType"`
			IntervalMS    int64  `json:"intervalMs"`
			MaxDataPoints int64  `json:"maxDataPoints"`
			Hide          bool   `json:"hide"`
		}
		if err := json.Unmarshal(raw, &query); err != nil {
			fail(w, 400, err)
			return
		}
		if query.Hide {
			continue
		}
		if query.RefID == "" || len(query.RefID) > 100 || seen[query.RefID] || query.Datasource.UID == "" {
			fail(w, 400, errors.New("queries need unique refId and datasource UID"))
			return
		}
		seen[query.RefID] = true
		if query.IntervalMS == 0 {
			query.IntervalMS = input.IntervalMS
		}
		if query.IntervalMS < 0 || query.IntervalMS > int64((1<<63-1)/time.Millisecond) {
			fail(w, 400, errors.New("intervalMs must be nonnegative and within the duration limit"))
			return
		}
		if query.IntervalMS < 1000 {
			query.IntervalMS = 15000
		}
		if query.MaxDataPoints == 0 {
			query.MaxDataPoints = input.MaxDataPoints
		}
		if query.MaxDataPoints == 0 {
			query.MaxDataPoints = 1000
		}
		if query.MaxDataPoints < 1 || query.MaxDataPoints > 10000 {
			fail(w, 400, errors.New("maxDataPoints must be 1–10000"))
			return
		}
		groups[query.Datasource.UID] = append(groups[query.Datasource.UID], backend.DataQuery{RefID: query.RefID, QueryType: query.QueryType, JSON: raw, Interval: time.Duration(query.IntervalMS) * time.Millisecond, MaxDataPoints: query.MaxDataPoints, TimeRange: backend.TimeRange{From: time.UnixMilli(from), To: time.UnixMilli(to)}})
	}
	s.writeQueryGroups(w, r, groups)
}
func (s *Server) writeQueryGroups(w http.ResponseWriter, r *http.Request, groups map[string][]backend.DataQuery) {
	if requestsChunks(r.Header.Get("Accept")) {
		s.streamQueryGroups(w, r, groups)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	results := backend.NewQueryDataResponse()
	for uid, queries := range groups {
		ds, err := s.dataSource(ctx, uid)
		if err != nil {
			resourceError(w, err)
			return
		}
		if ds.Type == "prometheus" || ds.Type == "grafana" {
			for _, q := range queries {
				var frames data.Frames
				var err error
				if ds.Type == "grafana" {
					frames, err = s.queryGrafanaSource(ctx, q)
				} else {
					frames, err = s.queryPrometheusSource(ctx, ds, q)
				}
				results.Responses[q.RefID] = backend.DataResponse{Frames: frames, Error: err}
				if err != nil {
					value := results.Responses[q.RefID]
					value.Status = backend.StatusBadRequest
					results.Responses[q.RefID] = value
				}
			}
			continue
		}
		response, err := s.Plugins.Query(ctx, ds, queries)
		if err != nil {
			for _, q := range queries {
				results.Responses[q.RefID] = backend.DataResponse{Error: err, Status: backend.StatusBadGateway}
			}
			continue
		}
		groupRefs := map[string]bool{}
		for _, q := range queries {
			groupRefs[q.RefID] = true
		}
		for ref, value := range response.Responses {
			if !groupRefs[ref] {
				fail(w, 502, errors.New("plugin returned an unknown query refId"))
				return
			}
			results.Responses[ref] = value
		}
		for _, q := range queries {
			if _, exists := response.Responses[q.RefID]; !exists {
				results.Responses[q.RefID] = backend.DataResponse{Error: errors.New("plugin omitted a query response"), Status: backend.StatusBadGateway}
			}
		}
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		fail(w, 502, err)
		return
	}
	if len(encoded) > 32<<20 {
		fail(w, 502, errors.New("query response exceeds 32 MiB"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(encoded)
}
