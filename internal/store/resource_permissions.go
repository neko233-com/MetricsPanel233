package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var (
	ErrPermissionUninitialized = errors.New("resource permissions have not been initialized")
	ErrPermissionVersion       = errors.New("resource permission version changed")
	ErrPermissionIdentity      = errors.New("resource permission identity is unavailable")
	ErrPermissionAncestry      = errors.New("invalid or excessive permission folder ancestry")
	ErrPermissionCleanup       = errors.New("permission cleanup job does not match its receipt")
)

// PermissionIdentityResolver loads current catalog state without holding the
// organization database writer. HTTP/service authorization is a separate layer.
type PermissionIdentityResolver struct {
	CurrentPrincipal  func(context.Context, identity.Principal) (identity.Principal, error)
	ValidateSubject   func(context.Context, int64, model.ResourcePermissionSubject) (int64, error)
	TeamsForPrincipal func(context.Context, int64, model.ResourcePermissionSubject) ([]identity.TeamGrant, error)
}

// InitializeResourcePermissionSchema only adds the detached store representation.
// It does not seed resources or activate authorization on existing API routes.
func (s *Store) InitializeResourcePermissionSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("organization store is unavailable")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT organization_id FROM store_identity WHERE id=1`).Scan(&owner); err != nil || owner != s.OrganizationID() {
		return identity.ErrForbidden
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS resource_acl_resources(
org_id INTEGER NOT NULL CHECK(org_id>0),resource_kind TEXT NOT NULL CHECK(resource_kind IN ('folder','dashboard')),resource_uid TEXT NOT NULL,
version INTEGER NOT NULL CHECK(version>0),defaults_initialized INTEGER NOT NULL CHECK(defaults_initialized IN (0,1)),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
PRIMARY KEY(org_id,resource_kind,resource_uid));
CREATE TABLE IF NOT EXISTS resource_acl_grants(
org_id INTEGER NOT NULL,resource_kind TEXT NOT NULL,resource_uid TEXT NOT NULL,
subject_kind TEXT NOT NULL CHECK(subject_kind IN ('user','service-account','team','role')),subject_key TEXT NOT NULL,
permission INTEGER NOT NULL CHECK(permission IN (1,2,4)),identity_generation INTEGER NOT NULL CHECK((subject_kind='role' AND identity_generation=0) OR(subject_kind!='role' AND identity_generation>0)),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
PRIMARY KEY(org_id,resource_kind,resource_uid,subject_kind,subject_key),
FOREIGN KEY(org_id,resource_kind,resource_uid) REFERENCES resource_acl_resources(org_id,resource_kind,resource_uid) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS resource_acl_subject ON resource_acl_grants(org_id,subject_kind,subject_key);
CREATE TABLE IF NOT EXISTS resource_acl_cleanup_receipts(
job_key TEXT PRIMARY KEY,org_id INTEGER NOT NULL,subject_kind TEXT NOT NULL,subject_key TEXT NOT NULL,identity_version INTEGER NOT NULL,identity_generation INTEGER NOT NULL CHECK(identity_generation>0),completed_at INTEGER NOT NULL);
CREATE TRIGGER IF NOT EXISTS resource_acl_resources_owner BEFORE INSERT ON resource_acl_resources WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS resource_acl_grants_owner BEFORE INSERT ON resource_acl_grants WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS resource_acl_cleanup_owner BEFORE INSERT ON resource_acl_cleanup_receipts WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS resource_acl_resources_owner_update BEFORE UPDATE OF org_id ON resource_acl_resources WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS resource_acl_grants_owner_update BEFORE UPDATE OF org_id ON resource_acl_grants WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;
CREATE TRIGGER IF NOT EXISTS resource_acl_cleanup_owner_update BEFORE UPDATE OF org_id ON resource_acl_cleanup_receipts WHEN NEW.org_id!=(SELECT organization_id FROM store_identity WHERE id=1) BEGIN SELECT RAISE(ABORT,'permission organization mismatch'); END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) permissionOwner(ctx context.Context, orgID int64) error {
	if s == nil || s.DB == nil || orgID != s.OrganizationID() {
		return identity.ErrForbidden
	}
	if identity.HasPrincipal(ctx) {
		principal, ok := identity.FromContext(ctx)
		if !ok {
			return identity.ErrUnauthenticated
		}
		return principal.RequireOrganization(orgID)
	}
	return nil // Internal store operations; this is not public authentication.
}

func (s *Store) permissionResource(ctx context.Context, ref model.PermissionResource) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	return s.permissionOwner(ctx, ref.OrgID)
}

// permissionResourceParent reads stored ownership rather than draft JSON.
func permissionResourceParent(ctx context.Context, q resourceQuery, ref model.PermissionResource) (string, int64, error) {
	if ref.Kind == model.PermissionFolder {
		var parent string
		var version int64
		err := q.QueryRowContext(ctx, `SELECT parent_uid,version FROM folders WHERE uid=?`, ref.UID).Scan(&parent, &version)
		return model.RootFolderUID(parent), version, err
	}
	var parent, kind sql.NullString
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT json_extract(extras,'$.folder_uid'),json_type(extras,'$.folder_uid'),updated_at FROM dashboards WHERE id=?`, ref.UID).Scan(&parent, &kind, &revision)
	if err == nil && kind.Valid && kind.String != "text" && kind.String != "null" {
		return "", 0, ErrPermissionAncestry
	}
	return model.RootFolderUID(parent.String), revision, err
}

func permissionDocument(ctx context.Context, q resourceQuery, ref model.PermissionResource) (model.ResourcePermissionDocument, error) {
	doc := model.ResourcePermissionDocument{Resource: ref, Items: []model.ResourcePermissionGrant{}}
	if _, _, err := permissionResourceParent(ctx, q, ref); err != nil {
		return doc, err
	}
	err := q.QueryRowContext(ctx, `SELECT version,defaults_initialized FROM resource_acl_resources WHERE org_id=? AND resource_kind=? AND resource_uid=?`, ref.OrgID, ref.Kind, ref.UID).Scan(&doc.Version, &doc.DefaultsInitialized)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, ErrPermissionUninitialized
	}
	if err != nil {
		return doc, err
	}
	rows, err := q.QueryContext(ctx, `SELECT subject_kind,subject_key,permission,identity_generation FROM resource_acl_grants WHERE org_id=? AND resource_kind=? AND resource_uid=? ORDER BY subject_kind,subject_key`, ref.OrgID, ref.Kind, ref.UID)
	if err != nil {
		return doc, err
	}
	defer rows.Close()
	for rows.Next() {
		var grant model.ResourcePermissionGrant
		var key string
		if err := rows.Scan(&grant.Subject.Kind, &key, &grant.Permission, &grant.IdentityGeneration); err != nil {
			return doc, err
		}
		if grant.Subject.Kind == model.PermissionRole {
			grant.Subject.Role = key
		} else {
			grant.Subject.ID, err = strconv.ParseInt(key, 10, 64)
			if err != nil || strconv.FormatInt(grant.Subject.ID, 10) != key {
				return doc, errors.New("invalid stored permission identity")
			}
		}
		if err := grant.Subject.Validate(); err != nil || !grant.Permission.ValidGrant() || (grant.Subject.Kind == model.PermissionRole && grant.IdentityGeneration != 0) || (grant.Subject.Kind != model.PermissionRole && grant.IdentityGeneration <= 0) {
			return doc, errors.New("invalid stored permission grant")
		}
		doc.Items = append(doc.Items, grant)
	}
	return doc, rows.Err()
}

func (s *Store) ResourcePermissions(ctx context.Context, ref model.PermissionResource) (model.ResourcePermissionDocument, error) {
	if err := s.permissionResource(ctx, ref); err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	defer tx.Rollback()
	return permissionDocument(ctx, tx, ref)
}

func validatePermissionGrants(ctx context.Context, orgID int64, input []model.ResourcePermissionGrant, resolver PermissionIdentityResolver) ([]model.ResourcePermissionGrant, error) {
	grants := append([]model.ResourcePermissionGrant(nil), input...)
	if len(grants) > 1000 {
		return nil, errors.New("at most 1000 direct permission grants are allowed")
	}
	seen := map[model.ResourcePermissionSubject]bool{}
	for i, grant := range grants {
		if err := grant.Subject.Validate(); err != nil {
			return nil, err
		}
		if !grant.Permission.ValidGrant() || seen[grant.Subject] {
			return nil, errors.New("permission grants require distinct subjects and View, Edit or Admin")
		}
		seen[grant.Subject] = true
		if grant.Subject.Kind == model.PermissionRole {
			if grant.IdentityGeneration != 0 {
				return nil, ErrPermissionIdentity
			}
			continue
		}
		generation, err := permissionSubjectGeneration(ctx, orgID, grant.Subject, resolver)
		if err != nil {
			return nil, err
		}
		if grant.IdentityGeneration != 0 && grant.IdentityGeneration != generation {
			return nil, ErrPermissionIdentity
		}
		grants[i].IdentityGeneration = generation
	}
	return grants, nil
}

func permissionSubjectGeneration(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject, resolver PermissionIdentityResolver) (int64, error) {
	if resolver.ValidateSubject == nil {
		return 0, ErrPermissionIdentity
	}
	generation, err := resolver.ValidateSubject(ctx, orgID, subject)
	if err != nil {
		return 0, fmt.Errorf("validate permission subject: %w", err)
	}
	if generation <= 0 {
		return 0, ErrPermissionIdentity
	}
	return generation, nil
}

func insertPermissionGrants(ctx context.Context, tx *sql.Tx, ref model.PermissionResource, grants []model.ResourcePermissionGrant, now int64) error {
	for _, grant := range grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_acl_grants(org_id,resource_kind,resource_uid,subject_kind,subject_key,permission,identity_generation,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, ref.OrgID, ref.Kind, ref.UID, grant.Subject.Kind, grant.Subject.Key(), grant.Permission, grant.IdentityGeneration, now, now); err != nil {
			return err
		}
	}
	return nil
}

// InitializeResourcePermissions is idempotent. Existing edited grants are never
// reset. Resource creation + seeding must later use a shared transaction helper.
func (s *Store) InitializeResourcePermissions(ctx context.Context, ref model.PermissionResource, seed model.ResourcePermissionSeed, resolver PermissionIdentityResolver) (model.ResourcePermissionDocument, error) {
	if err := s.permissionResource(ctx, ref); err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	if seed.Policy != model.PermissionRootDefaults && seed.Policy != model.PermissionLegacyDefaults {
		return model.ResourcePermissionDocument{}, errors.New("explicit permission creation policy is required")
	}
	if existing, err := s.ResourcePermissions(ctx, ref); !errors.Is(err, ErrPermissionUninitialized) {
		return existing, err
	}
	var creatorGeneration int64
	if seed.Creator != nil {
		if seed.Creator.Kind != model.PermissionUser && seed.Creator.Kind != model.PermissionServiceAccount {
			return model.ResourcePermissionDocument{}, errors.New("only a human or service account can receive creator permissions")
		}
		resolved, err := validatePermissionGrants(ctx, ref.OrgID, []model.ResourcePermissionGrant{{Subject: *seed.Creator, Permission: model.PermissionAdmin}}, resolver)
		if err != nil {
			return model.ResourcePermissionDocument{}, err
		}
		creatorGeneration = resolved[0].IdentityGeneration
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	defer tx.Rollback()
	if doc, err := permissionDocument(ctx, tx, ref); !errors.Is(err, ErrPermissionUninitialized) {
		return doc, err
	}
	parent, _, err := permissionResourceParent(ctx, tx, ref)
	if err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	grants := []model.ResourcePermissionGrant{}
	if parent == "" {
		grants = append(grants,
			model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Viewer"}, Permission: model.PermissionView},
			model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Editor"}, Permission: model.PermissionEdit})
	}
	if seed.Creator != nil && !seed.Provisioned && (parent == "" || seed.Policy == model.PermissionLegacyDefaults) {
		grants = append(grants, model.ResourcePermissionGrant{Subject: *seed.Creator, Permission: model.PermissionAdmin, IdentityGeneration: creatorGeneration})
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_acl_resources(org_id,resource_kind,resource_uid,version,defaults_initialized,created_at,updated_at) VALUES(?,?,?,1,1,?,?)`, ref.OrgID, ref.Kind, ref.UID, now, now); err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	if err := insertPermissionGrants(ctx, tx, ref, grants, now); err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	doc, err := permissionDocument(ctx, tx, ref)
	if err != nil {
		return doc, err
	}
	return doc, tx.Commit()
}

// ReplaceResourcePermissions changes only direct ACL data, without authorizing
// the caller to administer the resource. A future service action guard is required.
func (s *Store) ReplaceResourcePermissions(ctx context.Context, ref model.PermissionResource, expectedVersion int64, grants []model.ResourcePermissionGrant, resolver PermissionIdentityResolver) (model.ResourcePermissionDocument, error) {
	if err := s.permissionResource(ctx, ref); err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	if expectedVersion <= 0 {
		return model.ResourcePermissionDocument{}, ErrPermissionVersion
	}
	grants, err := validatePermissionGrants(ctx, ref.OrgID, grants, resolver)
	if err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ResourcePermissionDocument{}, err
	}
	defer tx.Rollback()
	doc, err := permissionDocument(ctx, tx, ref)
	if err != nil {
		return doc, err
	}
	if doc.Version != expectedVersion {
		return doc, ErrPermissionVersion
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE resource_acl_resources SET version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND resource_kind=? AND resource_uid=? AND version=?`, now, ref.OrgID, ref.Kind, ref.UID, expectedVersion)
	if err != nil {
		return doc, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return doc, ErrPermissionVersion
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_acl_grants WHERE org_id=? AND resource_kind=? AND resource_uid=?`, ref.OrgID, ref.Kind, ref.UID); err != nil {
		return doc, err
	}
	if err := insertPermissionGrants(ctx, tx, ref, grants, now); err != nil {
		return doc, err
	}
	doc, err = permissionDocument(ctx, tx, ref)
	if err != nil {
		return doc, err
	}
	return doc, tx.Commit()
}

func permissionPrincipalSubject(p identity.Principal) (model.ResourcePermissionSubject, bool) {
	switch p.Type {
	case identity.TypeUser:
		return model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: p.ID}, true
	case identity.TypeServiceAccount:
		return model.ResourcePermissionSubject{Kind: model.PermissionServiceAccount, ID: p.ID}, true
	default:
		return model.ResourcePermissionSubject{}, false
	}
}

// EffectiveResourcePermission evaluates positive direct/ancestor grants only.
// It deliberately accepts authenticated None identities with explicit grants.
func (s *Store) EffectiveResourcePermission(ctx context.Context, ref model.PermissionResource, principal identity.Principal, resolver PermissionIdentityResolver) (model.EffectiveResourcePermission, error) {
	out := model.EffectiveResourcePermission{Resource: ref, Items: []model.ResolvedResourcePermissionGrant{}, Versions: map[string]int64{}}
	if err := s.permissionResource(ctx, ref); err != nil {
		return out, err
	}
	if err := principal.RequireOrganization(ref.OrgID); err != nil {
		return out, err
	}
	teams := map[int64]int64{}
	var principalGeneration int64
	if subject, ok := permissionPrincipalSubject(principal); ok {
		if resolver.CurrentPrincipal == nil || resolver.TeamsForPrincipal == nil {
			return out, ErrPermissionIdentity
		}
		current, err := resolver.CurrentPrincipal(ctx, principal)
		if err != nil {
			return out, err
		}
		if current.OrgID != principal.OrgID || current.Type != principal.Type || current.ID != principal.ID || current.Validate() != nil {
			return out, ErrPermissionIdentity
		}
		principal = current
		principalGeneration, err = permissionSubjectGeneration(ctx, ref.OrgID, subject, resolver)
		if err != nil {
			return out, err
		}
		currentTeams, err := resolver.TeamsForPrincipal(ctx, ref.OrgID, subject)
		if err != nil {
			return out, err
		}
		for _, grant := range currentTeams {
			if grant.TeamID <= 0 || grant.TeamVersion <= 0 || grant.MemberVersion <= 0 || !grant.Role.Valid() {
				return out, ErrPermissionIdentity
			}
			generation, err := permissionSubjectGeneration(ctx, ref.OrgID, model.ResourcePermissionSubject{Kind: model.PermissionTeam, ID: grant.TeamID}, resolver)
			if err != nil {
				return out, err
			}
			teams[grant.TeamID] = generation
		}
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	current := ref
	for depth := 0; ; depth++ {
		key := string(current.Kind) + ":" + current.UID
		if depth > 64 || seen[key] {
			return out, ErrPermissionAncestry
		}
		seen[key] = true
		doc, err := permissionDocument(ctx, tx, current)
		if err != nil {
			return out, err
		}
		out.Versions["acl:"+key] = doc.Version
		for _, grant := range doc.Items {
			matches := false
			switch grant.Subject.Kind {
			case model.PermissionRole:
				matches = grant.Subject.Role == string(principal.Role)
			case model.PermissionUser:
				matches = principal.Type == identity.TypeUser && grant.Subject.ID == principal.ID && grant.IdentityGeneration == principalGeneration
			case model.PermissionServiceAccount:
				matches = principal.Type == identity.TypeServiceAccount && grant.Subject.ID == principal.ID && grant.IdentityGeneration == principalGeneration
			case model.PermissionTeam:
				matches = teams[grant.Subject.ID] > 0 && teams[grant.Subject.ID] == grant.IdentityGeneration
			}
			if matches {
				out.Items = append(out.Items, model.ResolvedResourcePermissionGrant{ResourcePermissionGrant: grant, Source: current, Inherited: current != ref})
				out.Permission = max(out.Permission, grant.Permission)
			}
		}
		parent, revision, err := permissionResourceParent(ctx, tx, current)
		if err != nil {
			return out, err
		}
		out.Versions["resource:"+key] = revision
		if parent == "" {
			break
		}
		current = model.PermissionResource{OrgID: ref.OrgID, Kind: model.PermissionFolder, UID: parent}
		if current.Validate() != nil {
			return out, ErrPermissionAncestry
		}
	}
	if principal.Role == identity.RoleAdmin {
		out.Permission = model.PermissionAdmin // Immutable OSS organization baseline.
	}
	return out, nil
}

// CleanupResourcePermissionSubject is replay-safe after the catalog has fenced
// the removed identity. It never completes the identity database's own job.
func (s *Store) CleanupResourcePermissionSubject(ctx context.Context, job model.ResourcePermissionCleanup) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if err := s.permissionOwner(ctx, job.OrgID); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var orgID, version, generation int64
	var kind, key string
	err = tx.QueryRowContext(ctx, `SELECT org_id,subject_kind,subject_key,identity_version,identity_generation FROM resource_acl_cleanup_receipts WHERE job_key=?`, job.JobID).Scan(&orgID, &kind, &key, &version, &generation)
	if err == nil {
		if orgID != job.OrgID || kind != string(job.Subject.Kind) || key != job.Subject.Key() || version != job.IdentityVersion || generation != job.IdentityGeneration {
			return ErrPermissionCleanup
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE resource_acl_resources SET version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND EXISTS(SELECT 1 FROM resource_acl_grants g WHERE g.org_id=resource_acl_resources.org_id AND g.resource_kind=resource_acl_resources.resource_kind AND g.resource_uid=resource_acl_resources.resource_uid AND g.subject_kind=? AND g.subject_key=? AND g.identity_generation<=?)`, now, job.OrgID, job.Subject.Kind, job.Subject.Key(), job.IdentityGeneration); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_acl_grants WHERE org_id=? AND subject_kind=? AND subject_key=? AND identity_generation<=?`, job.OrgID, job.Subject.Kind, job.Subject.Key(), job.IdentityGeneration); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_acl_cleanup_receipts(job_key,org_id,subject_kind,subject_key,identity_version,identity_generation,completed_at) VALUES(?,?,?,?,?,?,?)`, job.JobID, job.OrgID, job.Subject.Kind, job.Subject.Key(), job.IdentityVersion, job.IdentityGeneration, now); err != nil {
		return err
	}
	return tx.Commit()
}
