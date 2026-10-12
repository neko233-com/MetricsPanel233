package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrustedPrincipalOrganizationAndServiceAccountBoundaries(t *testing.T) {
	p := Principal{OrgID: 2, ID: 17, Type: TypeServiceAccount, Login: "dashboard-agent", Role: RoleEditor}
	require.NoError(t, p.RequireOrganization(2))
	require.ErrorIs(t, p.RequireOrganization(1), ErrForbidden)
	ctx := WithPrincipal(context.Background(), p)
	stored, ok := FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, p, stored)
	_, ok = FromContext(context.Background())
	assert.False(t, ok)
	p.IsServerAdmin = true
	_, ok = FromContext(WithPrincipal(ctx, p))
	assert.False(t, ok)
	_, err := ForOrganization(WithPrincipal(ctx, p), 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = ForOrganization(context.Background(), 2)
	require.ErrorIs(t, err, ErrUnauthenticated)
	p = Principal{OrgID: 2, ID: 3, Type: TypeUser, Login: "reader", Role: RoleNone}
	require.NoError(t, p.Validate())
	assert.False(t, p.Role.AtLeast(RoleViewer))
	assert.True(t, RoleAdmin.AtLeast(RoleEditor))
	assert.False(t, Role("Owner").AtLeast(RoleViewer))
}

func TestBackgroundAndLegacyContextsStayInTheirOrganization(t *testing.T) {
	p := BackgroundPrincipal(2)
	require.NoError(t, p.Validate())
	assert.Equal(t, TypeSystem, p.Type)
	assert.False(t, p.IsServerAdmin)
	require.ErrorIs(t, p.RequireOrganization(1), ErrForbidden)
	local := LocalPrincipal()
	require.NoError(t, local.Validate())
	local.OrgID = 2
	require.ErrorIs(t, local.Validate(), ErrUnauthenticated)
	assert.Equal(t, "default", Namespace(1))
	assert.Equal(t, "org-2", Namespace(2))
	assert.Empty(t, Namespace(0))
}
