package instance

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/server"
)

type organizationHTTPHandler struct {
	registry      *Registry
	authenticator *HTTPAuthenticator
	authorizer    *CredentialRequestAuthorizer
	mu            sync.Mutex
	handlers      map[int64]organizationHandlerEntry
}

type organizationHandlerEntry struct {
	server  *server.Server
	handler http.Handler
}

// NewProtectedOrganizationHandler dispatches only authenticated requests to the
// matching physical organization server. Configure the exact authorizer on the
// primary and every child before building their Handlers. A legacy/nil/different
// provider is refused, even for an authenticated administrator. This explicit
// gate prevents partially wired multi-user deployments from silently falling
// back to the existing single-workspace handler.
//
// This handler is not installed by the CLI yet. Production activation additionally
// requires a complete route policy, resource migration and Live revocation gates.
func NewProtectedOrganizationHandler(registry *Registry, authenticator *HTTPAuthenticator, authorizer *CredentialRequestAuthorizer) (http.Handler, error) {
	if registry == nil || authenticator == nil || authorizer == nil || authorizer.authenticator != authenticator || authorizer.policy == nil {
		return nil, errors.New("a registry, authenticator and its exact route authorizer are required")
	}
	return &organizationHTTPHandler{registry: registry, authenticator: authenticator, authorizer: authorizer, handlers: make(map[int64]organizationHandlerEntry)}, nil
}

func (h *organizationHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	authenticated, err := h.authenticator.AuthenticateRequest(r)
	if err != nil {
		writeAuthenticationError(w, err)
		return
	}
	p, _ := identity.FromContext(authenticated.Context())
	// Role None is a valid authenticated identity: explicit resource/action
	// grants decide whether it can use this route, rather than a Viewer shortcut.
	selected, err := h.registry.Resolve(authenticated.Context(), p.OrgID)
	if err != nil {
		writeAuthenticationError(w, err)
		return
	}
	if selected.OrganizationID() != p.OrgID || selected.Authorizer != h.authorizer {
		writeAuthenticationError(w, ErrUnavailable)
		return
	}
	h.mu.Lock()
	entry, found := h.handlers[p.OrgID]
	if !found || entry.server != selected {
		entry = organizationHandlerEntry{server: selected, handler: selected.Handler()}
		h.handlers[p.OrgID] = entry
	}
	h.mu.Unlock()
	entry.handler.ServeHTTP(w, authenticated)
}

// Error responses never interpolate provider/storage errors or credentials.
// These codes are native adapter codes, not a claim of Grafana's error encoding.
func writeAuthenticationError(w http.ResponseWriter, err error) {
	status, message, code := http.StatusServiceUnavailable, "Authentication is unavailable", "metricspanel.auth.unavailable"
	switch {
	case errors.Is(err, identity.ErrSessionNeedsRotation):
		status, message, code = http.StatusUnauthorized, "Session rotation required", "metricspanel.auth.rotation_required"
	case errors.Is(err, identity.ErrUnauthenticated):
		status, message, code = http.StatusUnauthorized, "Authentication required", "metricspanel.auth.required"
	case errors.Is(err, identity.ErrForbidden):
		status, message, code = http.StatusForbidden, "Permission denied", "metricspanel.auth.forbidden"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Message    string `json:"message"`
		MessageID  string `json:"messageId"`
		StatusCode int    `json:"statusCode"`
	}{message, code, status})
}
