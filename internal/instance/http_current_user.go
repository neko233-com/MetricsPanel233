package instance

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

// NewCurrentUserHandler exposes only these real authenticated identity operations.
// It is an opt-in handler; login/cookie issuance, the non-human profile variant
// and the rest of the identity administration API are separate implementations.
// Unknown routes are ordinary 404s, never forwarded to a legacy handler.
func NewCurrentUserHandler(authenticator *HTTPAuthenticator) (http.Handler, error) {
	if authenticator == nil || authenticator.catalog == nil {
		return nil, errors.New("an authenticator is required")
	}
	h := &currentUserHTTP{authenticator: authenticator}
	mux := http.NewServeMux()
	mux.Handle("GET /api/login/ping", h.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCurrentUserJSON(w, http.StatusOK, map[string]string{"message": "Logged in"})
	})))
	mux.Handle("GET /api/user", h.protectHuman(http.HandlerFunc(h.profile)))
	mux.Handle("GET /api/user/orgs", h.protectHuman(http.HandlerFunc(h.organizations)))
	mux.Handle("GET /api/user/auth-tokens", h.protectHuman(http.HandlerFunc(h.sessions)))
	mux.Handle("POST /api/user/revoke-auth-token", h.protectHuman(http.HandlerFunc(h.revoke)))
	return mux, nil
}

type currentUserHTTP struct {
	authenticator *HTTPAuthenticator
}

func (h *currentUserHTTP) protectHuman(next http.Handler) http.Handler {
	return h.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		if p.Type != identity.TypeUser {
			writeAuthenticationError(w, identity.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (h *currentUserHTTP) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		authenticated, err := h.authenticator.AuthenticateRequest(r)
		if err != nil {
			writeAuthenticationError(w, err)
			return
		}
		p, _ := identity.FromContext(authenticated.Context())
		current, err := h.authenticator.Revalidate(authenticated.Context(), p)
		if err != nil {
			writeAuthenticationError(w, err)
			return
		}
		next.ServeHTTP(w, authenticated.WithContext(identity.WithPrincipal(authenticated.Context(), current)))
	})
}

func (h *currentUserHTTP) profile(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	profile, err := h.authenticator.catalog.CurrentUserProfile(r.Context(), p)
	if err != nil {
		writeCurrentUserError(w, err)
		return
	}
	writeCurrentUserJSON(w, http.StatusOK, profile)
}

func (h *currentUserHTTP) organizations(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	orgs, err := h.authenticator.catalog.CurrentUserOrganizations(r.Context(), p)
	if err != nil {
		writeCurrentUserError(w, err)
		return
	}
	writeCurrentUserJSON(w, http.StatusOK, orgs)
}

func (h *currentUserHTTP) sessions(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	metadata, ok := h.authenticator.Metadata(r.Context())
	if !ok {
		writeAuthenticationError(w, identity.ErrUnauthenticated)
		return
	}
	sessions, err := h.authenticator.catalog.CurrentUserAuthTokens(r.Context(), p, metadata.SessionID)
	if err != nil {
		writeCurrentUserError(w, err)
		return
	}
	writeCurrentUserJSON(w, http.StatusOK, sessions)
}

func (h *currentUserHTTP) revoke(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	metadata, ok := h.authenticator.Metadata(r.Context())
	if !ok {
		writeAuthenticationError(w, identity.ErrUnauthenticated)
		return
	}
	var input identity.RevokeAuthTokenInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeCurrentUserError(w, identity.ErrInvalidAuthToken)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeCurrentUserError(w, identity.ErrInvalidAuthToken)
		return
	}
	if err := h.authenticator.catalog.RevokeCurrentUserAuthToken(r.Context(), p, metadata.SessionID, input.AuthTokenID); err != nil {
		writeCurrentUserError(w, err)
		return
	}
	writeCurrentUserJSON(w, http.StatusOK, map[string]string{"message": "User auth token revoked"})
}

func writeCurrentUserJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeCurrentUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrActiveAuthToken):
		writeCurrentUserJSON(w, http.StatusBadRequest, map[string]string{"message": "The active session cannot be revoked here"})
	case errors.Is(err, identity.ErrInvalidAuthToken):
		writeCurrentUserJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid auth token ID"})
	case errors.Is(err, sql.ErrNoRows):
		writeCurrentUserJSON(w, http.StatusNotFound, map[string]string{"message": "Auth token not found"})
	default:
		writeAuthenticationError(w, err)
	}
}
