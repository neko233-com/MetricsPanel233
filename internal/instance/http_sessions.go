package instance

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

const sessionExpiryCookieName = "grafana_session_expiry"

var internalSessionPath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]*$`)

// SessionHTTPConfig is trusted application configuration, never request data.
// This local-password adapter does not install a login page, bootstrap an
// account, authorize organization resources, or activate the CLI HTTP server.
type SessionHTTPConfig struct {
	AppSubURL              string
	CookieSecure           bool
	CookieSameSite         http.SameSite
	SessionLifetime        time.Duration
	SessionIdleTimeout     time.Duration
	LogoutRedirect         string
	EnableRotationRedirect bool
}

type sessionHTTP struct {
	auth   *HTTPAuthenticator
	config SessionHTTPConfig
	path   string
}

// NewSessionHTTPHandler registers only real password login, cookie rotation and
// logout operations. Mount it explicitly alongside the current-user handler;
// GET /login stays absent until the PC login page is implemented. Paths include
// AppSubURL, and cookies are scoped to its canonical trailing-slash path.
func NewSessionHTTPHandler(a *HTTPAuthenticator, config SessionHTTPConfig) (http.Handler, error) {
	if a == nil || a.catalog == nil || a.cookieName == sessionExpiryCookieName || a.cookieName == "redirect_to" {
		return nil, errors.New("an authenticator with a distinct session cookie name is required")
	}
	if strings.Contains(config.AppSubURL, "//") {
		return nil, errors.New("a canonical internal application subpath is required")
	}
	if config.AppSubURL == "/" {
		config.AppSubURL = ""
	}
	config.AppSubURL = strings.TrimSuffix(config.AppSubURL, "/")
	if config.AppSubURL != "" && (!internalSessionPath.MatchString(config.AppSubURL) || path.Clean(config.AppSubURL) != config.AppSubURL || strings.Contains(config.AppSubURL, "..")) {
		return nil, errors.New("a canonical internal application subpath is required")
	}
	if config.CookieSameSite == 0 {
		config.CookieSameSite = http.SameSiteLaxMode
	}
	if config.CookieSameSite != http.SameSiteLaxMode && config.CookieSameSite != http.SameSiteStrictMode && config.CookieSameSite != http.SameSiteNoneMode || config.CookieSameSite == http.SameSiteNoneMode && !config.CookieSecure {
		return nil, errors.New("a valid SameSite mode is required; None requires secure cookies")
	}
	if strings.HasPrefix(a.cookieName, "__Secure-") && !config.CookieSecure || strings.HasPrefix(a.cookieName, "__Host-") && (!config.CookieSecure || config.AppSubURL != "") {
		return nil, errors.New("session cookie prefix requirements are not met")
	}
	if config.SessionLifetime == 0 {
		config.SessionLifetime = 30 * 24 * time.Hour
	}
	if config.SessionIdleTimeout == 0 {
		config.SessionIdleTimeout = min(7*24*time.Hour, config.SessionLifetime)
	}
	if config.SessionLifetime < time.Second || config.SessionLifetime > 365*24*time.Hour || config.SessionIdleTimeout < time.Second || config.SessionIdleTimeout > config.SessionLifetime {
		return nil, errors.New("valid absolute and idle session lifetimes are required")
	}
	if config.LogoutRedirect == "" {
		config.LogoutRedirect = config.AppSubURL + "/login"
	}
	h := &sessionHTTP{auth: a, config: config, path: config.AppSubURL + "/"}
	if _, err := h.internalRedirect(config.LogoutRedirect); err != nil {
		return nil, errors.New("a valid internal logout destination is required")
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+config.AppSubURL+"/login", h.guard(http.HandlerFunc(h.login)))
	mux.Handle("POST "+config.AppSubURL+"/api/user/auth-tokens/rotate", h.guard(http.HandlerFunc(h.rotateAPI)))
	mux.Handle("GET "+config.AppSubURL+"/logout", h.guard(http.HandlerFunc(h.logout)))
	if config.EnableRotationRedirect {
		mux.Handle("GET "+config.AppSubURL+"/user/auth-tokens/rotate", h.guard(http.HandlerFunc(h.rotateRedirect)))
	}
	return mux, nil
}

func (h *sessionHTTP) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if err := checkRequestOrigin(r); err != nil {
			writeAuthenticationError(w, err)
			return
		}
		// Form login and cookie lifecycle operations do not borrow Basic/SA
		// credentials, including a bad explicit header plus an ambient cookie.
		if len(r.Header.Values("Authorization")) != 0 || len(r.CookiesNamed(h.auth.cookieName)) > 1 {
			writeAuthenticationError(w, identity.ErrUnauthenticated)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || hasSessionQueryCredential(query) {
			h.badRequest(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasSessionQueryCredential(query url.Values) bool {
	for name := range query {
		switch strings.ToLower(name) {
		case "user", "password", "token", "access_token", "id_token", "session", "secret", "authorization":
			return true
		}
	}
	return false
}

// Like the pinned redirect validator, destinations must remain inside the
// configured application. Native handling additionally rejects ambiguous or
// credential-bearing query strings, controls and backslashes.
func (h *sessionHTTP) internalRedirect(value string) (string, error) {
	if len(value) == 0 || len(value) > 4096 || strings.ContainsAny(value, "\\\x00\r\n\t") {
		return "", errors.New("invalid internal redirect")
	}
	u, err := url.Parse(value)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" || !internalSessionPath.MatchString(u.Path) || strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") || strings.Contains(u.Fragment, "//") || strings.Contains(u.Fragment, "..") || strings.ContainsAny(u.Fragment, "\\\x00\r\n\t") {
		return "", errors.New("invalid internal redirect")
	}
	if h.config.AppSubURL != "" && !strings.HasPrefix(u.Path, h.path) {
		return "", errors.New("redirect is outside the application")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || hasSessionQueryCredential(query) {
		return "", errors.New("invalid internal redirect")
	}
	return u.String(), nil
}

func (h *sessionHTTP) destination(r *http.Request, fallback string, useCookie bool) (string, error) {
	values := r.URL.Query()["redirectTo"]
	if len(values) > 1 {
		return "", errors.New("ambiguous redirect")
	}
	if len(values) == 1 && values[0] != "" {
		return h.internalRedirect(values[0])
	}
	if useCookie {
		cookies := r.CookiesNamed("redirect_to")
		if len(cookies) > 1 {
			return "", errors.New("ambiguous redirect")
		}
		if len(cookies) == 1 && cookies[0].Value != "" {
			value, err := url.QueryUnescape(cookies[0].Value)
			if err != nil {
				return "", errors.New("invalid internal redirect")
			}
			return h.internalRedirect(value)
		}
	}
	return h.internalRedirect(fallback)
}

func (h *sessionHTTP) badRequest(w http.ResponseWriter) {
	writeCurrentUserJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid session request"})
}

func (h *sessionHTTP) login(w http.ResponseWriter, r *http.Request) {
	destination, err := h.destination(r, h.path, true)
	if err != nil {
		h.badRequest(w)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.badRequest(w)
		return
	}
	var input struct {
		User     *string `json:"user"`
		Password *string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.User == nil || input.Password == nil || strings.TrimSpace(*input.User) == "" || len(*input.User) > 256 || len(*input.Password) == 0 || len(*input.Password) > 1024 {
		h.badRequest(w)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		h.badRequest(w)
		return
	}
	_, session, key, err := h.auth.catalog.LoginWithMetadata(r.Context(), *input.User, *input.Password, RequestOrganization(r), h.config.SessionLifetime, h.config.SessionIdleTimeout, identity.SessionMetadataInput{RemoteAddr: r.RemoteAddr, UserAgent: r.UserAgent()})
	if err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			writeCurrentUserJSON(w, http.StatusUnauthorized, map[string]string{"message": "Invalid username or password"})
		} else {
			writeAuthenticationError(w, err)
		}
		return
	}
	h.writeSessionCookies(w, session, key)
	if len(r.CookiesNamed("redirect_to")) != 0 {
		h.deleteCookie(w, "redirect_to", true)
	}
	writeCurrentUserJSON(w, http.StatusOK, map[string]string{"message": "Logged in", "redirectUrl": destination})
}

func (h *sessionHTTP) sessionCookie(r *http.Request) (string, error) {
	cookies := r.CookiesNamed(h.auth.cookieName)
	if len(cookies) != 1 || len(cookies[0].Value) == 0 || len(cookies[0].Value) > 4096 {
		return "", identity.ErrUnauthenticated
	}
	return cookies[0].Value, nil
}

func (h *sessionHTTP) rotate(w http.ResponseWriter, r *http.Request) error {
	key, err := h.sessionCookie(r)
	if err != nil {
		return err
	}
	// Cookie-only and deliberately outside the ordinary overdue-session gate.
	// The native store rechecks ownership, active identity and both expiry bounds.
	_, session, replacement, err := h.auth.catalog.RotateSession(r.Context(), key)
	if err != nil {
		return err
	}
	h.writeSessionCookies(w, session, replacement)
	return nil
}

func (h *sessionHTTP) rotateAPI(w http.ResponseWriter, r *http.Request) {
	if err := h.rotate(w, r); err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			h.deleteSessionCookies(w)
		}
		writeAuthenticationError(w, err)
		return
	}
	writeCurrentUserJSON(w, http.StatusOK, struct{}{})
}

func (h *sessionHTTP) rotateRedirect(w http.ResponseWriter, r *http.Request) {
	destination, err := h.destination(r, h.path, true)
	if err != nil {
		h.badRequest(w)
		return
	}
	if err := h.rotate(w, r); err != nil {
		if errors.Is(err, identity.ErrUnauthenticated) {
			h.deleteSessionCookies(w)
			http.Redirect(w, r, h.config.AppSubURL+"/login", http.StatusFound)
		} else {
			writeAuthenticationError(w, err)
		}
		return
	}
	http.Redirect(w, r, destination, http.StatusFound)
}

func (h *sessionHTTP) logout(w http.ResponseWriter, r *http.Request) {
	destination, err := h.destination(r, h.config.LogoutRedirect, false)
	if err != nil {
		h.badRequest(w)
		return
	}
	key, err := h.sessionCookie(r)
	if err == nil {
		err = h.auth.catalog.Logout(r.Context(), key)
	}
	if err != nil && !errors.Is(err, identity.ErrUnauthenticated) {
		writeAuthenticationError(w, err)
		return
	}
	h.deleteSessionCookies(w)
	http.Redirect(w, r, destination, http.StatusFound)
}

func (h *sessionHTTP) cookie(name, value string, httpOnly bool) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: h.path, Secure: h.config.CookieSecure, HttpOnly: httpOnly, SameSite: h.config.CookieSameSite}
}

func (h *sessionHTTP) writeSessionCookies(w http.ResponseWriter, session identity.Session, key string) {
	expires := time.UnixMilli(session.ExpiresAt).UTC()
	remaining := max(1, int(time.Until(expires)/time.Second))
	interval := h.auth.rotationInterval
	if !session.TokenSeen {
		interval = time.Minute // Native seen/unseen scheduling; hint is never a grant.
	}
	hint := min(session.NextRotation(interval).Unix(), expires.Unix())
	for _, cookie := range []*http.Cookie{h.cookie(h.auth.cookieName, key, true), h.cookie(sessionExpiryCookieName, strconv.FormatInt(hint, 10), false)} {
		cookie.Expires, cookie.MaxAge = expires, remaining
		http.SetCookie(w, cookie)
	}
}

func (h *sessionHTTP) deleteCookie(w http.ResponseWriter, name string, httpOnly bool) {
	cookie := h.cookie(name, "", httpOnly)
	cookie.Expires, cookie.MaxAge = time.Unix(1, 0).UTC(), -1
	http.SetCookie(w, cookie)
}

func (h *sessionHTTP) deleteSessionCookies(w http.ResponseWriter) {
	h.deleteCookie(w, h.auth.cookieName, true)
	h.deleteCookie(w, sessionExpiryCookieName, false)
}
