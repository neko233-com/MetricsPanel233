package instance

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type AuthenticationMethod string

const (
	AuthenticationPassword AuthenticationMethod = "basic-password"
	AuthenticationToken    AuthenticationMethod = "service-token"
	AuthenticationSession  AuthenticationMethod = "browser-session"
)

// AuthenticationMetadata contains no credential, digest, password or raw user
// agent. SessionID is the authenticated device, rather than a request parameter.
type AuthenticationMetadata struct {
	Method    AuthenticationMethod
	SessionID int64
}

type HTTPAuthenticationConfig struct {
	CookieName       string
	RotationInterval time.Duration
}

// HTTPAuthenticator is an opt-in authentication boundary. It neither installs
// a login page nor grants a route: the selected server must also use its exact
// CredentialRequestAuthorizer before any organization HTTP dispatch is allowed.
type HTTPAuthenticator struct {
	catalog          *identity.Store
	cookieName       string
	rotationInterval time.Duration
}

func NewHTTPAuthenticator(catalog *identity.Store, config HTTPAuthenticationConfig) (*HTTPAuthenticator, error) {
	if catalog == nil {
		return nil, errors.New("an identity catalog is required")
	}
	if config.CookieName == "" {
		config.CookieName = "grafana_session"
	}
	if err := (&http.Cookie{Name: config.CookieName, Value: "valid"}).Valid(); err != nil {
		return nil, errors.New("a valid session cookie name is required")
	}
	if config.RotationInterval == 0 {
		config.RotationInterval = 10 * time.Minute
	}
	if config.RotationInterval < time.Minute || config.RotationInterval > 365*24*time.Hour {
		return nil, errors.New("session rotation interval must be between one minute and 365 days")
	}
	return &HTTPAuthenticator{catalog: catalog, cookieName: config.CookieName, rotationInterval: config.RotationInterval}, nil
}

type requestCredentialKey struct{}

type requestCredential struct {
	issuer    *HTTPAuthenticator
	method    AuthenticationMethod
	login     string
	secret    string
	principal identity.Principal
	sessionID int64
}

func (requestCredential) String() string   { return "[redacted request credential]" }
func (requestCredential) GoString() string { return "[redacted request credential]" }

// RequestOrganization follows namespace, positive orgId/targetOrgId query, then
// header precedence. Invalid numeric selectors fall through; none is a grant.
// Namespace encoding is the existing native default/org-ID mapping, not a
// Grafana stack namespace. Other namespace forms remain a compatibility gap.
func RequestOrganization(r *http.Request) int64 {
	if r == nil || r.URL == nil {
		return 0
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) >= 6 && parts[1] == "apis" && parts[4] == "namespaces" {
		namespace := parts[5]
		if namespace == "default" {
			return 1
		}
		if strings.HasPrefix(namespace, "org-") {
			if id := positiveOrganizationID(strings.TrimPrefix(namespace, "org-")); id > 0 && identity.Namespace(id) == namespace {
				return id
			}
		}
	}
	for _, key := range []string{"orgId", "targetOrgId"} {
		if id := positiveOrganizationID(r.URL.Query().Get(key)); id > 0 {
			return id
		}
	}
	return positiveOrganizationID(r.Header.Get("X-Grafana-Org-Id"))
}

func positiveOrganizationID(value string) int64 {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// checkRequestOrigin precedes catalog calls, including session last-seen/token
// seen updates. Proxy headers do not establish a trusted origin or client IP.
func checkRequestOrigin(r *http.Request) error {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return identity.ErrForbidden
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return nil
	}
	if len(origins) != 1 {
		return identity.ErrForbidden
	}
	origin, err := url.Parse(origins[0])
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if err != nil || origin.User != nil || origin.Host != r.Host || origin.Scheme != scheme || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" {
		return identity.ErrForbidden
	}
	return nil
}

func (a *HTTPAuthenticator) readCredential(r *http.Request) (requestCredential, error) {
	credential := requestCredential{issuer: a}
	headers := r.Header.Values("Authorization")
	if len(headers) != 0 {
		// An explicit bad credential cannot fall back to an ambient valid cookie.
		// This deliberate stricter ordering is documented in the HTTP contract.
		if len(headers) != 1 || len(headers[0]) > 8192 {
			return credential, identity.ErrUnauthenticated
		}
		if login, password, ok := r.BasicAuth(); ok {
			if len(login) > 256 || len(password) > 4096 {
				return credential, identity.ErrUnauthenticated
			}
			credential.method, credential.login, credential.secret = AuthenticationPassword, login, password
			if login == "api_key" {
				credential.method = AuthenticationToken
			}
			return credential, nil
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.ContainsAny(parts[1], "\x00\r\n") {
			return credential, identity.ErrUnauthenticated
		}
		credential.method, credential.secret = AuthenticationToken, parts[1]
		return credential, nil
	}
	cookies := r.CookiesNamed(a.cookieName)
	if len(cookies) != 1 || cookies[0].Value == "" || len(cookies[0].Value) > 4096 {
		return credential, identity.ErrUnauthenticated
	}
	credential.method, credential.secret = AuthenticationSession, cookies[0].Value
	return credential, nil
}

func (a *HTTPAuthenticator) authenticate(ctx context.Context, credential requestCredential, orgID int64) (identity.Principal, identity.Session, error) {
	switch credential.method {
	case AuthenticationPassword:
		p, err := a.catalog.AuthenticatePassword(ctx, credential.login, credential.secret, orgID)
		return p, identity.Session{}, err
	case AuthenticationToken:
		p, err := a.catalog.AuthenticateServiceToken(ctx, credential.secret, orgID)
		return p, identity.Session{}, err
	case AuthenticationSession:
		return a.catalog.AuthenticateSessionWithRotation(ctx, credential.secret, orgID, a.rotationInterval)
	default:
		return identity.Principal{}, identity.Session{}, identity.ErrUnauthenticated
	}
}

// AuthenticateRequest replaces any caller-supplied principal with a credential-
// verified identity, and privately binds this request to the selected org. Header
// or context edits after this point cannot turn it into a different identity.
func (a *HTTPAuthenticator) AuthenticateRequest(r *http.Request) (*http.Request, error) {
	if a == nil || a.catalog == nil || r == nil || r.URL == nil {
		return nil, identity.ErrUnauthenticated
	}
	if err := checkRequestOrigin(r); err != nil {
		return nil, err
	}
	credential, err := a.readCredential(r)
	if err != nil {
		return nil, err
	}
	p, session, err := a.authenticate(r.Context(), credential, RequestOrganization(r))
	if err != nil {
		return nil, err
	}
	if p.Validate() != nil || p.Type != identity.TypeUser && p.Type != identity.TypeServiceAccount {
		return nil, identity.ErrUnauthenticated
	}
	credential.principal, credential.sessionID = p, session.ID
	ctx := identity.WithPrincipal(r.Context(), p)
	ctx = context.WithValue(ctx, requestCredentialKey{}, credential)
	return r.WithContext(ctx), nil
}

// Revalidate is suitable for PermissionResolverOptions. It validates the actual
// original cookie/token/password, not merely a current user/account ID. It never
// trusts mutable request headers or a later context's role/admin fields.
func (a *HTTPAuthenticator) Revalidate(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
	if a == nil || a.catalog == nil || ctx == nil {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	credential, ok := ctx.Value(requestCredentialKey{}).(requestCredential)
	bound, present := identity.FromContext(ctx)
	if !ok || credential.issuer != a || !present || !samePermissionIdentity(bound, incoming) || !samePermissionIdentity(credential.principal, incoming) {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	p, session, err := a.authenticate(ctx, credential, credential.principal.OrgID)
	if err != nil {
		return identity.Principal{}, err
	}
	if !samePermissionIdentity(p, credential.principal) || credential.method == AuthenticationSession && session.ID != credential.sessionID {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return p, nil
}

func (a *HTTPAuthenticator) Metadata(ctx context.Context) (AuthenticationMetadata, bool) {
	if a == nil || ctx == nil {
		return AuthenticationMetadata{}, false
	}
	credential, ok := ctx.Value(requestCredentialKey{}).(requestCredential)
	p, present := identity.FromContext(ctx)
	if !ok || credential.issuer != a || !present || !samePermissionIdentity(p, credential.principal) {
		return AuthenticationMetadata{}, false
	}
	return AuthenticationMetadata{Method: credential.method, SessionID: credential.sessionID}, true
}

func (a *HTTPAuthenticator) PermissionResolver() (store.PermissionIdentityResolver, error) {
	if a == nil {
		return store.PermissionIdentityResolver{}, identity.ErrUnauthenticated
	}
	return NewPermissionIdentityResolver(a.catalog, PermissionResolverOptions{Revalidate: a.Revalidate})
}

// RoutePermissionPolicy must implement the complete operation/body/resource and
// list-filtering policy for each supported registration; unknown actions deny.
// Authentication itself does not imply any dashboard/datasource/admin grant.
type RoutePermissionPolicy func(*http.Request, server.ProtectedRoute, identity.Principal) error

type CredentialRequestAuthorizer struct {
	authenticator *HTTPAuthenticator
	policy        RoutePermissionPolicy
}

func (a *HTTPAuthenticator) NewRequestAuthorizer(policy RoutePermissionPolicy) (*CredentialRequestAuthorizer, error) {
	if a == nil || a.catalog == nil || policy == nil {
		return nil, errors.New("an authenticator and explicit route permission policy are required")
	}
	return &CredentialRequestAuthorizer{authenticator: a, policy: policy}, nil
}

func (a *CredentialRequestAuthorizer) AuthorizeRequest(r *http.Request, route server.ProtectedRoute) (identity.Principal, error) {
	if a == nil || a.authenticator == nil || a.policy == nil {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	incoming, ok := identity.FromContext(r.Context())
	if !ok {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	p, err := a.authenticator.Revalidate(r.Context(), incoming)
	if err != nil {
		return identity.Principal{}, err
	}
	// Policy installs request-local action/fence state for the selected handler.
	// Preserve those context additions on the actual request, as well as its
	// original private credential binding, rather than a discarded local copy.
	*r = *r.WithContext(identity.WithPrincipal(r.Context(), p))
	if err := a.policy(r, route, p); err != nil {
		return identity.Principal{}, err
	}
	return p, nil
}
