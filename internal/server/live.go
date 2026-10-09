package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) liveSession(expires int64) string {
	payload := strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, []byte(s.Token))
	mac.Write([]byte("live-websocket:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) validLiveSession(r *http.Request) bool {
	cookie, err := r.Cookie("metricspanel-live")
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
	return hmac.Equal([]byte(cookie.Value), []byte(s.liveSession(expires)))
}
func (s *Server) liveRoutes(api, mux *http.ServeMux) {
	api.HandleFunc("POST /api/live/session", func(w http.ResponseWriter, r *http.Request) {
		expires := time.Now().Add(15 * time.Minute)
		http.SetCookie(w, &http.Cookie{Name: "metricspanel-live", Value: s.liveSession(expires.Unix()), Path: "/api/live/ws", Expires: expires, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]int64{"expires_at": expires.UnixMilli()})
	})
	mux.Handle("GET /api/live/ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && s.validLiveSession(r) {
			clone := r.Clone(r.Context())
			clone.Header = r.Header.Clone()
			clone.Header.Set("Authorization", "Bearer "+s.Token)
			s.protect(s.Live).ServeHTTP(w, clone)
			return
		}
		s.protect(s.Live).ServeHTTP(w, r)
	}))
	api.HandleFunc("GET /api/live/channels", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Live.Snapshot()) })
	api.HandleFunc("POST /api/live/publish", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Channel string          `json:"channel"`
			Data    json.RawMessage `json:"data"`
		}
		if err := decode(w, r, &input); err != nil {
			fail(w, 400, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		result, err := s.Live.Publish(ctx, input.Channel, input.Data)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "result": result})
	})
}
