package localauth

import (
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/stretchr/testify/assert"
)

func principal(role identity.Role) identity.Principal {
	return identity.Principal{OrgID: 1, ID: 7, Type: identity.TypeUser, Login: "user", Name: "User", Role: role}
}

func TestAuthorizeRoleMatrix(t *testing.T) {
	viewer, editor, admin := principal(identity.RoleViewer), principal(identity.RoleEditor), principal(identity.RoleAdmin)
	cases := []struct {
		name   string
		who    identity.Principal
		method string
		path   string
		allow  bool
	}{
		{"viewer reads dashboards", viewer, "GET", "/api/v1/dashboards", true},
		{"viewer cannot create dashboards", viewer, "POST", "/api/v1/dashboards", false},
		{"editor creates dashboards", editor, "POST", "/api/v1/dashboards", true},
		{"editor manages collectors", editor, "PUT", "/api/v1/targets/2", true},
		{"viewer cannot push remote write", viewer, "POST", "/api/v1/write", false},
		{"editor pushes remote write", editor, "POST", "/api/v1/write", true},
		{"viewer cannot push prometheus write", viewer, "POST", "/prometheus/api/v1/write", false},
		{"editor pushes prometheus write", editor, "POST", "/prometheus/api/v1/write", true},
		{"editor runs queries", viewer, "POST", "/api/ds/query", true},
		{"viewer previews alerts", viewer, "POST", "/api/v1/alerts/preview", true},
		{"viewer previews silences", viewer, "POST", "/api/v1/alerting/silences/preview", true},
		{"viewer previews mute timings", viewer, "POST", "/api/v1/alerting/mute-timings/night/preview", true},
		{"viewer cannot test contacts", viewer, "POST", "/api/v1/alerting/contact-points/hook/test", false},
		{"editor cannot install plugins", editor, "POST", "/api/plugins/clock/install", false},
		{"editor cannot write datasource secrets", editor, "PUT", "/api/datasources/uid/remote", false},
		{"editor cannot change notification policy", editor, "PUT", "/api/v1/alerting/notification-policy", false},
		{"editor cannot change mute timings", editor, "PUT", "/api/v1/alerting/mute-timings/night", false},
		{"admin installs plugins", admin, "POST", "/api/plugins/clock/install", true},
		{"admin writes datasources", admin, "PUT", "/api/datasources/uid/remote", true},
		{"unknown mutation denies editors", editor, "POST", "/api/new-operation", false},
		{"unknown mutation allows admins", admin, "POST", "/api/new-operation", true},
		{"role none cannot read", principal(identity.RoleNone), "GET", "/api/v1/dashboards", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := authorize(test.who, test.method, test.path)
			if test.allow {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, identity.ErrForbidden)
			}
		})
	}
}

func TestCleanPathCannotEscapeProtectedSurfaces(t *testing.T) {
	assert.True(t, protectedPath(cleanPath("/api/../api/plugins")))
	assert.True(t, protectedPath(cleanPath("//api/v1/dashboards")))
	assert.False(t, protectedPath(cleanPath("/api/../login")))
	assert.False(t, protectedPath(cleanPath("/")))
}
