package server

import (
	"errors"
	"net/http"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

// RouteSurface distinguishes the Prometheus prefix from the native/Grafana API
// and protected assets/Live endpoints, even when their mux patterns overlap.
type RouteSurface string

const (
	RouteAPI        RouteSurface = "api"
	RoutePrometheus RouteSurface = "prometheus"
	RouteEndpoint   RouteSurface = "endpoint"
)

// ProtectedRoute describes the selected ServeMux registration, not a guessed
// method/path category. Request.PathValue and Request.Pattern are already set
// when an authorizer is called. A catch-all registration still needs an explicit
// policy for its actual path and method; it does not grant every child operation.
type ProtectedRoute struct {
	Surface RouteSurface `json:"surface"`
	Pattern string       `json:"pattern"`
}

// RequestAuthorizer must revalidate the request's credential and current grants
// before allowing the operation, including resource/body references and list
// filtering. It returns the current principal so downstream plugin SDK contexts
// and history actors cannot use a stale role. Unknown operations must be denied.
// This is an opt-in integration boundary; a principal in context alone is not
// evidence that a cookie/token is still valid. Configure it before Handler.
type RequestAuthorizer interface {
	AuthorizeRequest(*http.Request, ProtectedRoute) (identity.Principal, error)
}

type RequestAuthorizerFunc func(*http.Request, ProtectedRoute) (identity.Principal, error)

func (f RequestAuthorizerFunc) AuthorizeRequest(r *http.Request, route ProtectedRoute) (identity.Principal, error) {
	if f == nil {
		return identity.Principal{}, errors.New("authorization provider is unavailable")
	}
	return f(r, route)
}

// All route registration helpers accept this interface. The authorization mux
// wraps every registration at construction, after Go has resolved path values
// and before a handler can read its body, touch a store, or call a plugin.
type routeRegistrar interface {
	Handle(string, http.Handler)
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
}

type authorizationMux struct {
	*http.ServeMux
	server     *Server
	authorizer RequestAuthorizer
	surface    RouteSurface
	manifest   *[]ProtectedRoute
}

func (s *Server) authorizationMux(mux *http.ServeMux, surface RouteSurface, manifest *[]ProtectedRoute) *authorizationMux {
	return &authorizationMux{ServeMux: mux, server: s, authorizer: s.Authorizer, surface: surface, manifest: manifest}
}

func (m *authorizationMux) Handle(pattern string, handler http.Handler) {
	if handler == nil {
		panic("http: nil handler")
	}
	if function, ok := handler.(http.HandlerFunc); ok && function == nil {
		panic("http: nil handler")
	}
	route := ProtectedRoute{Surface: m.surface, Pattern: pattern}
	*m.manifest = append(*m.manifest, route)
	if m.authorizer == nil {
		m.ServeMux.Handle(pattern, handler)
		return
	}
	var authorized http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		incoming, ok := identity.FromContext(r.Context())
		if !ok || incoming.Type != identity.TypeUser && incoming.Type != identity.TypeServiceAccount {
			fail(w, http.StatusUnauthorized, identity.ErrUnauthenticated)
			return
		}
		if err := incoming.RequireOrganization(m.server.OrganizationID()); err != nil {
			fail(w, http.StatusForbidden, identity.ErrForbidden)
			return
		}
		current, err := m.authorizer.AuthorizeRequest(r, route)
		if err != nil {
			switch {
			case errors.Is(err, identity.ErrSessionNeedsRotation):
				fail(w, http.StatusUnauthorized, identity.ErrSessionNeedsRotation)
			case errors.Is(err, identity.ErrUnauthenticated):
				fail(w, http.StatusUnauthorized, identity.ErrUnauthenticated)
			case errors.Is(err, identity.ErrForbidden):
				fail(w, http.StatusForbidden, identity.ErrForbidden)
			default:
				// Provider errors can contain credentials or upstream response data.
				// They must never be copied into a browser response.
				fail(w, http.StatusInternalServerError, errors.New("authorization is unavailable"))
			}
			return
		}
		if current.Validate() != nil || current.OrgID != incoming.OrgID || current.Type != incoming.Type || current.ID != incoming.ID {
			fail(w, http.StatusForbidden, identity.ErrForbidden)
			return
		}
		handler.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), current)))
	})
	if m.surface == RouteEndpoint {
		// These registrations sit outside the /api and /prometheus wrappers.
		// Reject foreign origins before the credential provider can update session
		// idle metadata or perform any permission lookups.
		authorized = m.server.protect(authorized)
	}
	m.ServeMux.Handle(pattern, authorized)
}

func (m *authorizationMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	if handler == nil {
		panic("http: nil handler")
	}
	m.Handle(pattern, http.HandlerFunc(handler))
}
