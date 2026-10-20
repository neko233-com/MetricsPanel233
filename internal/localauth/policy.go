package localauth

import (
	"path"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

// Protected surfaces reuse the existing authentication boundary. Static SPA
// assets stay public so the login page can load.
func protectedPath(requestPath string) bool {
	return requestPath == "/metrics" ||
		requestPath == "/v1/metrics" ||
		requestPath == "/otlp/v1/metrics" ||
		strings.HasPrefix(requestPath, "/api/") ||
		strings.HasPrefix(requestPath, "/prometheus/") ||
		strings.HasPrefix(requestPath, "/public/plugins/")
}

// POST requests that only read data or establish a browser asset session.
// Everything else that is not listed as an editor operation requires Admin.
var readOnlyPosts = []string{
	"/api/ds/query",
	"/api/live/session",
	"/api/v1/alerts/preview",
	"/api/v1/patterns/search",
	"/api/v1/alerting/mute-timings/preview",
	"/api/v1/alerting/notification-templates/preview",
	"/api/v1/alerting/silences/preview",
	"/api/v1/import/grafana-preview",
	"/api/v1/plugins/assets-session",
}

var readOnlyPostPrefixes = []string{
	"/api/v1/dashboard-compare/",
	"/api/v1/alerting/mute-timings/",
}

// Admin-only configuration surfaces carry encrypted credentials or install
// code. They are never writable by Editors.
var adminPrefixes = []string{
	"/api/admin",
	"/api/plugins",
	"/api/v1/plugins",
	"/api/datasources",
	"/api/probe",
	"/api/v1/alerting/contact-points",
	"/api/v1/alerting/notification-policy",
	"/api/v1/alerting/notification-templates",
	"/api/v1/alerting/notifications",
	"/api/v1/alerting/mute-timings",
	"/api/v1/provisioning/contact-points",
	"/api/v1/provisioning/policies",
}

var editorPrefixes = []string{
	"/api/dashboards",
	"/api/v1/dashboard-",
	"/api/v1/dashboards",
	"/api/folders",
	"/api/v1/folders",
	"/api/library-elements",
	"/api/v1/library-",
	"/api/annotations",
	"/api/v1/annotations",
	"/api/v1/alerts",
	"/api/v1/provisioning/alert-rules",
	"/api/v1/targets",
	"/api/v1/ingest",
	"/v1/metrics",
	"/otlp/v1/metrics",
	"/api/v1/patterns",
	"/api/v1/import/",
	"/api/live/publish",
	"/api/v1/alerting/silences",
	"/api/alertmanager/grafana/api/v2/silences",
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func isReadOnlyPost(requestPath string) bool {
	for _, candidate := range readOnlyPosts {
		if requestPath == candidate {
			return true
		}
	}
	if strings.HasPrefix(requestPath, "/apis/") && strings.HasSuffix(requestPath, "/query") {
		return true
	}
	for _, prefix := range readOnlyPostPrefixes {
		if strings.HasPrefix(requestPath, prefix) && strings.HasSuffix(requestPath, "/preview") {
			return true
		}
	}
	return false
}

// authorize implements the global role matrix. Unknown mutations fail closed
// to Admin. Resource-level ACLs are not implied by this check.
func authorize(p identity.Principal, method, requestPath string) error {
	if p.Validate() != nil {
		return identity.ErrUnauthenticated
	}
	if p.Role == identity.RoleNone {
		return identity.ErrForbidden
	}
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return nil
	}
	if isReadOnlyPost(requestPath) {
		return nil
	}
	if hasAnyPrefix(requestPath, adminPrefixes) {
		if p.Role.AtLeast(identity.RoleAdmin) {
			return nil
		}
		return identity.ErrForbidden
	}
	if hasAnyPrefix(requestPath, editorPrefixes) {
		if p.Role.AtLeast(identity.RoleEditor) {
			return nil
		}
		return identity.ErrForbidden
	}
	if p.Role.AtLeast(identity.RoleAdmin) {
		return nil
	}
	return identity.ErrForbidden
}

func cleanPath(value string) string {
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}
