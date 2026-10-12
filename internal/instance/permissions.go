package instance

import (
	"context"
	"errors"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

// PermissionResolverOptions is trusted server configuration, never request JSON.
// Revalidate must recheck the request's actual session/token/password credential.
// Looking up a user ID in the catalog is not authentication or token revocation.
type PermissionResolverOptions struct {
	Revalidate func(context.Context, identity.Principal) (identity.Principal, error)
	// Exactly one canonical local/background principal may be explicitly trusted
	// by an internal caller. Its exact value must also be present in the context.
	TrustedInternalPrincipal *identity.Principal
}

// NewPermissionIdentityResolver adapts the global identity catalog to an org
// store's permission evaluator. HTTP callers must supply request-bound Revalidate;
// there is no identity-only or role/type-based authentication fallback.
func NewPermissionIdentityResolver(catalog *identity.Store, options PermissionResolverOptions) (store.PermissionIdentityResolver, error) {
	if catalog == nil || (options.Revalidate == nil) == (options.TrustedInternalPrincipal == nil) {
		return store.PermissionIdentityResolver{}, errors.New("a catalog and exactly one credential revalidator or trusted internal principal are required")
	}
	adapter := &permissionCatalogAdapter{catalog: catalog, revalidate: options.Revalidate}
	if options.TrustedInternalPrincipal != nil {
		p := *options.TrustedInternalPrincipal // Caller mutation cannot alter the trust binding.
		if p.Validate() != nil || (p != identity.LocalPrincipal() && p != identity.BackgroundPrincipal(p.OrgID)) {
			return store.PermissionIdentityResolver{}, errors.New("internal permission trust requires a canonical local or background principal")
		}
		adapter.internal = &p
	}
	return store.PermissionIdentityResolver{
		CurrentPrincipal:  adapter.currentPrincipal,
		ValidateSubject:   adapter.validateSubject,
		TeamsForPrincipal: adapter.teamsForPrincipal,
	}, nil
}

type permissionCatalogAdapter struct {
	catalog    *identity.Store
	revalidate func(context.Context, identity.Principal) (identity.Principal, error)
	internal   *identity.Principal
}

func samePermissionIdentity(a, b identity.Principal) bool {
	return a.OrgID == b.OrgID && a.Type == b.Type && a.ID == b.ID
}

func (a *permissionCatalogAdapter) activeOrganization(ctx context.Context, orgID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	org, err := a.catalog.Organization(ctx, orgID)
	if err != nil {
		return err
	}
	if org.ID != orgID || org.State != identity.OrganizationActive {
		return ErrUnavailable
	}
	return nil
}

func (a *permissionCatalogAdapter) servicePrincipal(ctx context.Context, orgID, accountID int64) (identity.Principal, error) {
	account, err := a.catalog.ServiceAccount(ctx, orgID, accountID)
	if err != nil {
		return identity.Principal{}, err
	}
	if account.OrgID != orgID || account.ID != accountID || account.Disabled || !account.Role.Valid() {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	p := identity.Principal{OrgID: account.OrgID, ID: account.ID, Type: identity.TypeServiceAccount, Login: account.Login, Name: account.Name, Role: account.Role}
	return p, p.Validate()
}

func (a *permissionCatalogAdapter) currentPrincipal(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
	if err := incoming.Validate(); err != nil {
		return identity.Principal{}, err
	}
	bound, present := identity.FromContext(ctx)
	if !present || bound.Validate() != nil || !samePermissionIdentity(bound, incoming) {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	if a.internal != nil {
		if bound != *a.internal || incoming != *a.internal {
			return identity.Principal{}, identity.ErrForbidden
		}
		if err := a.activeOrganization(ctx, incoming.OrgID); err != nil {
			return identity.Principal{}, err
		}
		return *a.internal, nil
	}
	if incoming.Type != identity.TypeUser && incoming.Type != identity.TypeServiceAccount {
		return identity.Principal{}, identity.ErrForbidden
	}
	validated, err := a.revalidate(ctx, incoming)
	if err != nil {
		return identity.Principal{}, err
	}
	if validated.Validate() != nil || !samePermissionIdentity(validated, incoming) {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	if err := a.activeOrganization(ctx, incoming.OrgID); err != nil {
		return identity.Principal{}, err
	}
	// Credentials prove identity; the catalog remains authoritative for current
	// names, role, memberships, organization state and human server-admin flag.
	if incoming.Type == identity.TypeUser {
		return a.catalog.UserPrincipal(ctx, incoming.ID, incoming.OrgID)
	}
	return a.servicePrincipal(ctx, incoming.OrgID, incoming.ID)
}

func (a *permissionCatalogAdapter) actor(ctx context.Context, orgID int64) (identity.Principal, error) {
	p, present := identity.FromContext(ctx)
	if !present {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	if p.OrgID != orgID {
		return identity.Principal{}, identity.ErrForbidden
	}
	return a.currentPrincipal(ctx, p)
}

func (a *permissionCatalogAdapter) subjectGeneration(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) (int64, error) {
	if err := subject.Validate(); err != nil {
		return 0, err
	}
	switch subject.Kind {
	case model.PermissionUser:
		if _, err := a.catalog.UserPrincipal(ctx, subject.ID, orgID); err != nil {
			return 0, err
		}
		membership, err := a.catalog.Membership(ctx, orgID, subject.ID)
		if err != nil {
			return 0, err
		}
		if membership.OrgID != orgID || membership.UserID != subject.ID || membership.State != "active" || membership.Generation <= 0 || !membership.Role.Valid() {
			return 0, identity.ErrForbidden
		}
		return membership.Generation, nil
	case model.PermissionServiceAccount:
		if _, err := a.servicePrincipal(ctx, orgID, subject.ID); err != nil {
			return 0, err
		}
		return 1, nil // IDs are immutable, nonreused AUTOINCREMENT identities.
	case model.PermissionTeam:
		team, err := a.catalog.Team(ctx, orgID, subject.ID)
		if err != nil {
			return 0, err
		}
		if team.OrgID != orgID || team.ID != subject.ID || team.State != identity.TeamActive || team.Version <= 0 {
			return 0, identity.ErrForbidden
		}
		return 1, nil
	case model.PermissionRole:
		return 0, nil // Role grant matching is independent of an identity incarnation.
	default:
		return 0, identity.ErrForbidden
	}
}

func (a *permissionCatalogAdapter) validateSubject(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) (int64, error) {
	if _, err := a.actor(ctx, orgID); err != nil {
		return 0, err
	}
	return a.subjectGeneration(ctx, orgID, subject)
}

func (a *permissionCatalogAdapter) teamsForPrincipal(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) ([]identity.TeamGrant, error) {
	p, err := a.actor(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if subject.Kind != model.PermissionUser && subject.Kind != model.PermissionServiceAccount {
		return nil, identity.ErrForbidden
	}
	if a.internal == nil && (p.ID != subject.ID || string(p.Type) != string(subject.Kind)) {
		return nil, identity.ErrForbidden
	}
	if _, err := a.subjectGeneration(ctx, orgID, subject); err != nil {
		return nil, err
	}
	return a.catalog.TeamsForPrincipal(ctx, orgID, identity.TeamPrincipal{Kind: identity.PrincipalType(subject.Kind), ID: subject.ID})
}
