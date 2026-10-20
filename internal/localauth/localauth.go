package localauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
)

type Config struct {
	Catalog         *identity.Store
	Authenticator   *instance.HTTPAuthenticator
	CookieName      string
	LegacyToken     string
	SecureCookie    bool
	SessionLifetime time.Duration
	IdleTimeout     time.Duration
	// TrustedProxies are CIDRs allowed to supply X-Forwarded-For for rate
	// limiting. Empty means the socket peer is always used.
	TrustedProxies []string
}

// Handler authenticates browser sessions and enforces the role matrix before
// requests reach the existing single-workspace server. It also owns the
// first-run setup and local user administration routes.
type Handler struct {
	cfg           Config
	session       http.Handler
	setupRequired atomic.Bool
	setupToken    string
	limiter       *attemptLimiter
	trusted       []*net.IPNet
}

func New(cfg Config) (*Handler, error) {
	if cfg.Catalog == nil || cfg.Authenticator == nil {
		return nil, errors.New("an identity catalog and authenticator are required")
	}
	if cfg.CookieName == "" {
		cfg.CookieName = identity.SessionCookieName
	}
	if cfg.SessionLifetime == 0 {
		cfg.SessionLifetime = 30 * 24 * time.Hour
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = min(7*24*time.Hour, cfg.SessionLifetime)
	}
	session, err := instance.NewSessionHTTPHandler(cfg.Authenticator, instance.SessionHTTPConfig{
		CookieSecure:       cfg.SecureCookie,
		CookieSameSite:     http.SameSiteLaxMode,
		SessionLifetime:    cfg.SessionLifetime,
		SessionIdleTimeout: cfg.IdleTimeout,
		LogoutRedirect:     "/",
	})
	if err != nil {
		return nil, err
	}
	hasUsers, err := cfg.Catalog.HasUsers(context.Background())
	if err != nil {
		return nil, err
	}
	h := &Handler{cfg: cfg, session: session, limiter: newAttemptLimiter(8, 5*time.Minute)}
	for _, cidr := range cfg.TrustedProxies {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, errors.New("trusted proxies must be valid CIDR ranges")
		}
		h.trusted = append(h.trusted, network)
	}
	h.setupRequired.Store(!hasUsers)
	if !hasUsers {
		token, err := newSetupToken()
		if err != nil {
			return nil, err
		}
		h.setupToken = token
	}
	return h, nil
}

// SetupToken is returned exactly once per process while no account exists.
// Callers print it to the operator console and never log it elsewhere.
func (h *Handler) SetupToken() string { return h.setupToken }

func (h *Handler) SetupRequired() bool { return h.setupRequired.Load() }

func (h *Handler) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serve(next, w, r)
	})
}

func (h *Handler) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	requestPath := cleanPath(r.URL.Path)
	switch {
	case r.Method == http.MethodGet && requestPath == "/api/auth/status":
		h.status(w, r)
		return
	case r.Method == http.MethodGet && requestPath == "/api/auth/me":
		h.me(w, r)
		return
	case r.Method == http.MethodPost && requestPath == "/api/auth/logout":
		h.logout(w, r)
		return
	case r.Method == http.MethodGet && requestPath == "/api/user":
		h.grafanaUser(w, r)
		return
	case r.Method == http.MethodGet && requestPath == "/api/user/orgs":
		h.grafanaOrganizations(w, r)
		return
	case r.Method == http.MethodGet && requestPath == "/api/login/ping":
		h.grafanaPing(w, r)
		return
	case r.Method == http.MethodPost && requestPath == "/api/logout":
		h.logoutAPI(w, r)
		return
	case r.Method == http.MethodPost && requestPath == "/api/setup":
		h.setup(w, r)
		return
	case r.Method == http.MethodPost && requestPath == "/login":
		h.login(w, r)
		return
	case r.Method == http.MethodGet && requestPath == "/logout",
		r.Method == http.MethodPost && requestPath == "/api/user/auth-tokens/rotate":
		h.session.ServeHTTP(w, r)
		return
	}

	if requestPath == "/api/health" || !protectedPath(requestPath) {
		next.ServeHTTP(w, r)
		return
	}
	authenticated, principal, err := h.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := authorize(principal, r.Method, requestPath); err != nil {
		writeAuthError(w, err)
		return
	}
	if strings.HasPrefix(requestPath, "/api/admin/users") {
		if principal.Type != identity.TypeUser || !principal.IsServerAdmin {
			writeAuthError(w, identity.ErrForbidden)
			return
		}
		h.users(w, r, principal, requestPath)
		return
	}
	next.ServeHTTP(w, authenticated)
}

func (h *Handler) authenticate(r *http.Request) (*http.Request, identity.Principal, error) {
	if principal, ok := h.legacyPrincipal(r); ok {
		return r.WithContext(identity.WithPrincipal(r.Context(), principal)), principal, nil
	}
	authenticated, err := h.cfg.Authenticator.AuthenticateRequest(r)
	if err != nil {
		return nil, identity.Principal{}, err
	}
	principal, ok := identity.FromContext(authenticated.Context())
	if !ok {
		return nil, identity.Principal{}, identity.ErrUnauthenticated
	}
	return authenticated, principal, nil
}

func (h *Handler) legacyPrincipal(r *http.Request) (identity.Principal, bool) {
	if h.cfg.LegacyToken == "" {
		return identity.Principal{}, false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return identity.Principal{}, false
	}
	provided := strings.TrimPrefix(values[0], "Bearer ")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.cfg.LegacyToken)) != 1 {
		return identity.Principal{}, false
	}
	return identity.LocalPrincipal(), true
}

type userView struct {
	ID            int64         `json:"id"`
	Login         string        `json:"login"`
	Name          string        `json:"name"`
	Email         string        `json:"email,omitempty"`
	Role          identity.Role `json:"role"`
	OrgID         int64         `json:"orgId"`
	IsServerAdmin bool          `json:"isServerAdmin"`
	Disabled      bool          `json:"disabled"`
	Version       int64         `json:"version"`
}

type statusResponse struct {
	Mode          string    `json:"mode"`
	SetupRequired bool      `json:"setupRequired"`
	Authenticated bool      `json:"authenticated"`
	User          *userView `json:"user,omitempty"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	response := statusResponse{Mode: "local", SetupRequired: h.setupRequired.Load()}
	if _, principal, err := h.authenticate(r); err == nil {
		view := h.view(r.Context(), principal)
		response.Authenticated = true
		response.User = &view
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	_, principal, err := h.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.view(r.Context(), principal))
}

type grafanaUser struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Login          string `json:"login"`
	Email          string `json:"email"`
	IsGrafanaAdmin bool   `json:"isGrafanaAdmin"`
	OrgID          int64  `json:"orgId"`
	IsDisabled     bool   `json:"isDisabled"`
	UpdatedAt      int64  `json:"updatedAt"`
}

// Grafana-compatible profile for tools that call GET /api/user.
func (h *Handler) grafanaUser(w http.ResponseWriter, r *http.Request) {
	_, principal, err := h.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	profile := grafanaUser{
		ID: principal.ID, Name: principal.Name, Login: principal.Login,
		IsGrafanaAdmin: principal.IsServerAdmin, OrgID: principal.OrgID,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if principal.Type == identity.TypeUser {
		if user, err := h.cfg.Catalog.User(r.Context(), principal.ID); err == nil {
			profile.Name, profile.Login, profile.Email = user.Name, user.Login, user.Email
			profile.IsGrafanaAdmin, profile.OrgID = user.IsServerAdmin, user.CurrentOrgID
			profile.IsDisabled, profile.UpdatedAt = user.Disabled, user.UpdatedAt
		}
	}
	writeJSON(w, http.StatusOK, profile)
}

func (h *Handler) grafanaOrganizations(w http.ResponseWriter, r *http.Request) {
	_, principal, err := h.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	name := "Main Org."
	if organization, err := h.cfg.Catalog.Organization(r.Context(), principal.OrgID); err == nil {
		name = organization.Name
	}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"orgId": principal.OrgID, "name": name, "role": principal.Role,
	}})
}

func (h *Handler) grafanaPing(w http.ResponseWriter, r *http.Request) {
	if _, _, err := h.authenticate(r); err != nil {
		writeAuthError(w, identity.ErrUnauthenticated)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Logged in"})
}

func (h *Handler) logoutAPI(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeAuthError(w, identity.ErrForbidden)
		return
	}
	if key := sessionCookie(r, h.cfg.CookieName); key != "" {
		_ = h.cfg.Catalog.Logout(r.Context(), key)
	}
	h.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Logged out"})
}

func (h *Handler) view(ctx context.Context, p identity.Principal) userView {
	view := userView{ID: p.ID, Login: p.Login, Name: p.Name, Role: p.Role, OrgID: p.OrgID, IsServerAdmin: p.IsServerAdmin}
	if p.Type == identity.TypeUser {
		if user, err := h.cfg.Catalog.User(ctx, p.ID); err == nil {
			view.Login, view.Name, view.Email = user.Login, user.Name, user.Email
			view.Disabled, view.Version, view.IsServerAdmin = user.Disabled, user.Version, user.IsServerAdmin
		}
	}
	return view
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.limiter.allow(h.clientIP(r)) {
		w.Header().Set("Retry-After", "300")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "Too many login attempts. Try again later."})
		return
	}
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	h.session.ServeHTTP(recorder, r)
	if recorder.status < 400 {
		h.limiter.reset(h.clientIP(r))
	} else {
		slog.Warn("local login failed", "remote", h.clientIP(r), "status", recorder.status)
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeAuthError(w, identity.ErrForbidden)
		return
	}
	key := sessionCookie(r, h.cfg.CookieName)
	if key != "" {
		_ = h.cfg.Catalog.Logout(r.Context(), key)
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type setupInput struct {
	Token    string `json:"token"`
	Login    string `json:"login"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	if !h.setupRequired.Load() {
		writeJSON(w, http.StatusConflict, map[string]any{"message": "Instance is already initialized"})
		return
	}
	if !sameOrigin(r) {
		writeAuthError(w, identity.ErrForbidden)
		return
	}
	if !h.limiter.allow("setup|" + h.clientIP(r)) {
		w.Header().Set("Retry-After", "300")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "Too many setup attempts. Try again later."})
		return
	}
	var input setupInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if h.setupToken == "" || subtle.ConstantTimeCompare([]byte(input.Token), []byte(h.setupToken)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Invalid setup token"})
		return
	}
	if len(input.Password) < 12 || len(input.Password) > 1024 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Password must contain 12 to 1024 characters"})
		return
	}
	user, err := h.cfg.Catalog.BootstrapAdmin(r.Context(), identity.UserInput{
		Login: input.Login, Name: input.Name, Email: input.Email, Password: input.Password,
	})
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
		return
	}
	_, session, key, err := h.cfg.Catalog.LoginWithMetadata(r.Context(), input.Login, input.Password, 1, h.cfg.SessionLifetime, h.cfg.IdleTimeout, identity.SessionMetadataInput{RemoteAddr: r.RemoteAddr, UserAgent: r.UserAgent()})
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"message": "Administrator created; sign in to continue"})
		return
	}
	h.setupRequired.Store(false)
	h.setupToken = ""
	h.limiter.reset("setup|" + h.clientIP(r))
	slog.Info("local administrator created", "login", user.Login, "remote", h.clientIP(r))
	h.writeSessionCookie(w, session, key)
	writeJSON(w, http.StatusOK, map[string]any{"user": userView{ID: user.ID, Login: user.Login, Name: user.Name, Email: user.Email, Role: identity.RoleAdmin, OrgID: 1, IsServerAdmin: true}})
}

func (h *Handler) writeSessionCookie(w http.ResponseWriter, session identity.Session, key string) {
	expires := time.UnixMilli(session.ExpiresAt).UTC()
	http.SetCookie(w, &http.Cookie{
		Name: h.cfg.CookieName, Value: key, Path: "/", HttpOnly: true,
		Secure: h.cfg.SecureCookie, SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: max(1, int(time.Until(expires)/time.Second)),
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cfg.CookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: h.cfg.SecureCookie, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(1, 0).UTC(), MaxAge: -1,
	})
}

func sessionCookie(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 || cookies[0].Value == "" || len(cookies[0].Value) > 4096 {
		return ""
	}
	return cookies[0].Value
}

func (h *Handler) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !h.trustedIP(host) {
		return host
	}
	for _, header := range r.Header.Values("X-Forwarded-For") {
		parts := strings.Split(header, ",")
		for index := len(parts) - 1; index >= 0; index-- {
			candidate := strings.TrimSpace(parts[index])
			if net.ParseIP(candidate) != nil && !h.trustedIP(candidate) {
				return candidate
			}
		}
	}
	return host
}

func (h *Handler) trustedIP(value string) bool {
	ip := net.ParseIP(value)
	if ip == nil {
		return false
	}
	for _, network := range h.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin.Host == r.Host && origin.Scheme == scheme
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON body")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAuthError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusServiceUnavailable, "metricspanel.auth.unavailable", "Authentication is unavailable"
	switch {
	case errors.Is(err, identity.ErrSessionNeedsRotation):
		status, code, message = http.StatusUnauthorized, "metricspanel.auth.rotation_required", "Session rotation required"
	case errors.Is(err, identity.ErrUnauthenticated):
		status, code, message = http.StatusUnauthorized, "metricspanel.auth.required", "Authentication required"
	case errors.Is(err, identity.ErrForbidden):
		status, code, message = http.StatusForbidden, "metricspanel.auth.forbidden", "Permission denied"
	}
	writeJSON(w, status, map[string]any{"message": message, "messageId": code, "statusCode": status})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, identity.ErrAccountExists), errors.Is(err, identity.ErrVersionConflict), errors.Is(err, identity.ErrLastAdministrator):
		return http.StatusConflict
	case errors.Is(err, identity.ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, identity.ErrForbidden):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func newSetupToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

type attemptRecord struct {
	count int
	reset time.Time
}

type attemptLimiter struct {
	mu      sync.Mutex
	entries map[string]*attemptRecord
	max     int
	window  time.Duration
}

func newAttemptLimiter(maximum int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{entries: map[string]*attemptRecord{}, max: maximum, window: window}
}

func (l *attemptLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if record, ok := l.entries[key]; ok {
		if now.After(record.reset) {
			delete(l.entries, key)
		} else if record.count >= l.max {
			return false
		}
	}
	if len(l.entries) > 4096 {
		for candidate, record := range l.entries {
			if now.After(record.reset) {
				delete(l.entries, candidate)
			}
		}
	}
	record, ok := l.entries[key]
	if !ok {
		record = &attemptRecord{reset: now.Add(l.window)}
		l.entries[key] = record
	}
	record.count++
	return true
}

func (l *attemptLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// userAdminView carries both optimistic versions so role and disabled changes
// use the same optimistic concurrency contract as the identity catalog.
type userAdminView struct {
	userView
	MembershipVersion int64 `json:"membershipVersion"`
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request, actor identity.Principal, requestPath string) {
	if requestPath == "/api/admin/users" {
		switch r.Method {
		case http.MethodGet:
			h.listUsers(w, r)
		case http.MethodPost:
			h.createUser(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method not allowed"})
		}
		return
	}
	rest := strings.TrimPrefix(requestPath, "/api/admin/users/")
	if rest == "" || strings.Contains(rest, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Unknown user operation"})
		return
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid user ID"})
		return
	}
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method not allowed"})
		return
	}
	h.updateUser(w, r, actor, id)
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.cfg.Catalog.Users(r.Context(), 1000, 0)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"message": "Could not list users"})
		return
	}
	out := make([]userAdminView, 0, len(users))
	for _, user := range users {
		view := userAdminView{userView: userView{
			ID: user.ID, Login: user.Login, Name: user.Name, Email: user.Email,
			OrgID: user.CurrentOrgID, IsServerAdmin: user.IsServerAdmin,
			Disabled: user.Disabled, Version: user.Version,
		}}
		if membership, err := h.cfg.Catalog.Membership(r.Context(), 1, user.ID); err == nil {
			view.Role, view.MembershipVersion = membership.Role, membership.Version
		} else {
			view.Role = identity.RoleNone
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, out)
}

type createUserInput struct {
	Login    string        `json:"login"`
	Name     string        `json:"name"`
	Email    string        `json:"email"`
	Password string        `json:"password"`
	Role     identity.Role `json:"role"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var input createUserInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if !input.Role.Valid() || input.Role == identity.RoleNone {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Role must be Viewer, Editor or Admin"})
		return
	}
	if len(input.Password) < 12 || len(input.Password) > 1024 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Password must contain 12 to 1024 characters"})
		return
	}
	user, err := h.cfg.Catalog.CreateUser(r.Context(), identity.UserInput{
		Login: input.Login, Name: input.Name, Email: input.Email, Password: input.Password,
		OrgID: 1, Role: input.Role, IsServerAdmin: input.Role == identity.RoleAdmin,
	})
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
		return
	}
	slog.Info("local user created", "login", user.Login, "role", input.Role)
	view := userAdminView{userView: userView{
		ID: user.ID, Login: user.Login, Name: user.Name, Email: user.Email,
		Role: input.Role, OrgID: 1, IsServerAdmin: input.Role == identity.RoleAdmin, Version: user.Version,
	}, MembershipVersion: 1}
	if membership, err := h.cfg.Catalog.Membership(r.Context(), 1, user.ID); err == nil {
		view.MembershipVersion = membership.Version
	}
	writeJSON(w, http.StatusCreated, view)
}

type updateUserInput struct {
	Version           *int64         `json:"version"`
	MembershipVersion *int64         `json:"membershipVersion"`
	Role              *identity.Role `json:"role"`
	Disabled          *bool          `json:"disabled"`
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, actor identity.Principal, id int64) {
	if id == actor.ID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "You cannot change your own role or disable your own account"})
		return
	}
	var input updateUserInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	if input.Version == nil || input.MembershipVersion == nil || input.Role == nil && input.Disabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "version, membershipVersion and at least one change are required"})
		return
	}
	if input.Role != nil && (!input.Role.Valid() || *input.Role == identity.RoleNone) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Role must be Viewer, Editor or Admin"})
		return
	}
	if input.Disabled != nil {
		if _, err := h.cfg.Catalog.SetUserDisabled(r.Context(), id, *input.Version, *input.Disabled); err != nil {
			writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
			return
		}
	}
	if input.Role != nil {
		if _, err := h.cfg.Catalog.ChangeOrganizationUserRole(r.Context(), 1, id, *input.MembershipVersion, *input.Role); err != nil {
			writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
			return
		}
		user, err := h.cfg.Catalog.User(r.Context(), id)
		if err != nil {
			writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
			return
		}
		if _, err := h.cfg.Catalog.SetUserServerAdmin(r.Context(), id, user.Version, *input.Role == identity.RoleAdmin); err != nil {
			writeJSON(w, statusFor(err), map[string]any{"message": err.Error()})
			return
		}
	}
	user, err := h.cfg.Catalog.User(r.Context(), id)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"message": "User updated"})
		return
	}
	view := userAdminView{userView: userView{
		ID: user.ID, Login: user.Login, Name: user.Name, Email: user.Email,
		OrgID: user.CurrentOrgID, IsServerAdmin: user.IsServerAdmin,
		Disabled: user.Disabled, Version: user.Version,
	}}
	if membership, err := h.cfg.Catalog.Membership(r.Context(), 1, id); err == nil {
		view.Role, view.MembershipVersion = membership.Role, membership.Version
	}
	role, disabled := "", false
	if input.Role != nil {
		role = string(*input.Role)
	}
	if input.Disabled != nil {
		disabled = *input.Disabled
	}
	slog.Info("local user updated", "user_id", id, "role", role, "disabled", disabled)
	writeJSON(w, http.StatusOK, view)
}
