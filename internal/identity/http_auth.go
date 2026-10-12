package identity

import (
	"net/http"
	"strconv"
	"strings"
)

const SessionCookieName = "metricspanel-session"

type AuthenticationMethod string

const (
	AuthenticationPassword AuthenticationMethod = "password"
	AuthenticationSession  AuthenticationMethod = "session"
	AuthenticationService  AuthenticationMethod = "service-account"
)

type Authentication struct {
	Principal Principal            `json:"principal"`
	Method    AuthenticationMethod `json:"method"`
}

// AuthenticateRequest only resolves credentials and membership. A dispatcher
// must enforce resource permissions before exposing an organization handler.
// This method never treats an organization/user header as a credential.
func (s *Store) AuthenticateRequest(r *http.Request) (Authentication, error) {
	orgID := int64(0)
	if values := r.Header.Values("X-Grafana-Org-Id"); len(values) > 0 {
		if len(values) != 1 {
			return Authentication{}, ErrUnauthenticated
		}
		var err error
		orgID, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || orgID <= 0 {
			return Authentication{}, ErrUnauthenticated
		}
	}
	if values := r.Header.Values("Authorization"); len(values) > 0 {
		if len(values) != 1 {
			return Authentication{}, ErrUnauthenticated
		}
		scheme, credential, found := strings.Cut(values[0], " ")
		if !found || credential == "" {
			return Authentication{}, ErrUnauthenticated
		}
		switch strings.ToLower(scheme) {
		case "bearer":
			p, err := s.AuthenticateServiceToken(r.Context(), credential, orgID)
			return Authentication{Principal: p, Method: AuthenticationService}, err
		case "basic":
			login, password, ok := r.BasicAuth()
			if !ok {
				return Authentication{}, ErrUnauthenticated
			}
			if login == "api_key" {
				p, err := s.AuthenticateServiceToken(r.Context(), password, orgID)
				return Authentication{Principal: p, Method: AuthenticationService}, err
			}
			p, err := s.AuthenticatePassword(r.Context(), login, password, orgID)
			return Authentication{Principal: p, Method: AuthenticationPassword}, err
		default:
			return Authentication{}, ErrUnauthenticated
		}
	}
	var key string
	for _, cookie := range r.Cookies() {
		if cookie.Name != SessionCookieName {
			continue
		}
		if key != "" || cookie.Value == "" {
			return Authentication{}, ErrUnauthenticated
		}
		key = cookie.Value
	}
	if key == "" {
		return Authentication{}, ErrUnauthenticated
	}
	p, err := s.AuthenticateSession(r.Context(), key, orgID)
	return Authentication{Principal: p, Method: AuthenticationSession}, err
}
