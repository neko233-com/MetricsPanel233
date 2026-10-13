package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrPermissionFence = errors.New("resource permission mutation snapshot changed or is not covered")

type preparedPermissionSeed struct {
	policy      model.ResourcePermissionCreationPolicy
	creator     *model.ResourcePermissionSubject
	generation  int64
	provisioned bool
}

// Identity resolution always precedes the organization writer transaction.
func preparePermissionSeed(ctx context.Context, orgID int64, seed model.ResourcePermissionSeed, resolver PermissionIdentityResolver) (preparedPermissionSeed, error) {
	p := preparedPermissionSeed{policy: seed.Policy, provisioned: seed.Provisioned}
	if p.policy != model.PermissionRootDefaults && p.policy != model.PermissionLegacyDefaults {
		return p, errors.New("explicit permission creation policy is required")
	}
	if seed.Creator != nil {
		creator := *seed.Creator
		if creator.Kind != model.PermissionUser && creator.Kind != model.PermissionServiceAccount {
			return p, errors.New("only a human or service account can receive creator permissions")
		}
		validated, err := validatePermissionGrants(ctx, orgID, []model.ResourcePermissionGrant{{Subject: creator, Permission: model.PermissionAdmin}}, resolver)
		if err != nil {
			return p, err
		}
		p.creator, p.generation = &creator, validated[0].IdentityGeneration
	}
	return p, nil
}

type permissionActorSnapshot struct {
	principal  identity.Principal
	generation int64
	teams      []identity.TeamGrant
}

func samePermissionActor(a, b permissionActorSnapshot) bool {
	return a.principal.OrgID == b.principal.OrgID && a.principal.Type == b.principal.Type && a.principal.ID == b.principal.ID && a.principal.Role == b.principal.Role && a.principal.IsServerAdmin == b.principal.IsServerAdmin && a.generation == b.generation && slices.Equal(a.teams, b.teams)
}

func (s *Store) permissionActor(ctx context.Context, resolver PermissionIdentityResolver) (permissionActorSnapshot, error) {
	var out permissionActorSnapshot
	p, present := identity.FromContext(ctx)
	if !present {
		return out, identity.ErrUnauthenticated
	}
	if err := p.RequireOrganization(s.OrganizationID()); err != nil {
		return out, err
	}
	if resolver.CurrentPrincipal == nil {
		return out, ErrPermissionIdentity
	}
	fresh, err := resolver.CurrentPrincipal(ctx, p)
	if err != nil {
		return out, err
	}
	if fresh.Validate() != nil || fresh.OrgID != p.OrgID || fresh.Type != p.Type || fresh.ID != p.ID {
		return out, ErrPermissionIdentity
	}
	out.principal = fresh
	if subject, ok := permissionPrincipalSubject(fresh); ok {
		out.generation, err = permissionSubjectGeneration(ctx, fresh.OrgID, subject, resolver)
		if err != nil {
			return out, err
		}
		if resolver.TeamsForPrincipal == nil {
			return out, ErrPermissionIdentity
		}
		out.teams, err = resolver.TeamsForPrincipal(ctx, fresh.OrgID, subject)
		if err != nil {
			return out, err
		}
		out.teams = slices.Clone(out.teams)
		for _, team := range out.teams {
			if team.TeamID <= 0 || team.TeamVersion <= 0 || team.MemberVersion <= 0 || !team.Role.Valid() {
				return out, ErrPermissionIdentity
			}
			if _, err = permissionSubjectGeneration(ctx, fresh.OrgID, model.ResourcePermissionSubject{Kind: model.PermissionTeam, ID: team.TeamID}, resolver); err != nil {
				return out, err
			}
		}
		sort.Slice(out.teams, func(i, j int) bool { return out.teams[i].TeamID < out.teams[j].TeamID })
	}
	return out, nil
}

type permissionLibrarySnapshot struct {
	id, version int64
	folder      string
}

// PermissionMutationFence is request-local, opaque trusted state. It is bound
// to its issuing Store and identity; JSON or a zero value cannot supply a fence.
// Effective exposes a clone for the service's action checks, not authority by
// itself. Revalidation occurs immediately before the writer, but catalog and
// control DBs do not share a cross-database atomic transaction.
type PermissionMutationFence struct {
	owner       *Store
	actor       permissionActorSnapshot
	resolver    PermissionIdentityResolver
	effective   map[model.PermissionResource]model.EffectiveResourcePermission
	libraries   map[string]permissionLibrarySnapshot
	connections map[string][]string
}

func (f PermissionMutationFence) Effective(ref model.PermissionResource) (model.EffectiveResourcePermission, error) {
	value, ok := f.effective[ref]
	if f.owner == nil || !ok {
		return model.EffectiveResourcePermission{}, ErrPermissionFence
	}
	value.Items = slices.Clone(value.Items)
	value.Versions = maps.Clone(value.Versions)
	return value, nil
}

// Explicit library UIDs include incoming links/import definitions. Existing
// stored dashboard connections and their authoritative owner folders are added
// automatically; absent library identities are recorded to fence creation.
func (s *Store) CapturePermissionMutation(ctx context.Context, refs []model.PermissionResource, resolver PermissionIdentityResolver, libraryUIDs ...string) (PermissionMutationFence, error) {
	f := PermissionMutationFence{owner: s, resolver: resolver, effective: map[model.PermissionResource]model.EffectiveResourcePermission{}, libraries: map[string]permissionLibrarySnapshot{}, connections: map[string][]string{}}
	actor, err := s.permissionActor(ctx, resolver)
	if err != nil {
		return PermissionMutationFence{}, err
	}
	f.actor = actor
	queue := slices.Clone(refs)
	libs := map[string]bool{}
	for _, uid := range libraryUIDs {
		if uid == "" {
			return PermissionMutationFence{}, ErrPermissionFence
		}
		libs[uid] = true
	}
	if len(libs) > 4096 {
		return PermissionMutationFence{}, ErrPermissionFence
	}
	for i := 0; i < len(queue); i++ {
		ref := queue[i]
		if _, exists := f.effective[ref]; exists {
			continue
		}
		if len(f.effective) >= 4096 {
			return PermissionMutationFence{}, ErrPermissionFence
		}
		if err := s.permissionResource(ctx, ref); err != nil {
			return PermissionMutationFence{}, err
		}
		effective, err := s.EffectiveResourcePermission(ctx, ref, actor.principal, resolver)
		if err != nil {
			return PermissionMutationFence{}, err
		}
		f.effective[ref] = effective
		if ref.Kind == model.PermissionDashboard {
			connections, err := permissionDashboardConnections(ctx, s.DB, ref.UID)
			if err != nil {
				return PermissionMutationFence{}, err
			}
			f.connections[ref.UID] = connections
			for _, uid := range connections {
				libs[uid] = true
			}
		}
	}
	if len(libs) > 4096 {
		return PermissionMutationFence{}, ErrPermissionFence
	}
	for uid := range libs {
		snapshot, err := permissionLibrary(ctx, s.DB, uid)
		if err != nil {
			return PermissionMutationFence{}, err
		}
		f.libraries[uid] = snapshot
		if snapshot.id > 0 && snapshot.folder != "" {
			ref := model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: snapshot.folder}
			if _, exists := f.effective[ref]; !exists {
				if len(f.effective) >= 4096 {
					return PermissionMutationFence{}, ErrPermissionFence
				}
				effective, err := s.EffectiveResourcePermission(ctx, ref, actor.principal, resolver)
				if err != nil {
					return PermissionMutationFence{}, err
				}
				f.effective[ref] = effective
			}
		}
	}
	fresh, err := s.permissionActor(ctx, resolver)
	if err != nil {
		return PermissionMutationFence{}, err
	}
	if !samePermissionActor(actor, fresh) {
		return PermissionMutationFence{}, ErrPermissionFence
	}
	// Ensure multiple per-resource reads still correspond to one usable control
	// snapshot. Overlapping ancestor revisions cannot silently disagree.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PermissionMutationFence{}, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return PermissionMutationFence{}, err
	}
	return f, nil
}

func permissionDashboardConnections(ctx context.Context, q resourceQuery, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT library_uid FROM library_panel_connections WHERE dashboard_id=? ORDER BY library_uid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	uids := []string{}
	for rows.Next() {
		var uid string
		if err = rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

func permissionLibrary(ctx context.Context, q resourceQuery, uid string) (permissionLibrarySnapshot, error) {
	var p permissionLibrarySnapshot
	err := q.QueryRowContext(ctx, `SELECT id,version,folder_uid FROM library_elements WHERE uid=?`, uid).Scan(&p.id, &p.version, &p.folder)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	p.folder = model.RootFolderUID(p.folder)
	return p, err
}

func (s *Store) validatePermissionFence(ctx context.Context, f PermissionMutationFence) error {
	if f.owner != s || f.actor.principal.OrgID != s.OrganizationID() {
		return ErrPermissionFence
	}
	fresh, err := s.permissionActor(ctx, f.resolver)
	if err != nil {
		return err
	}
	if !samePermissionActor(f.actor, fresh) {
		return ErrPermissionFence
	}
	return nil
}

func permissionVersions(ctx context.Context, q resourceQuery, ref model.PermissionResource) (map[string]int64, error) {
	versions := map[string]int64{}
	seen := map[model.PermissionResource]bool{}
	for depth := 0; ; depth++ {
		if depth > 64 || seen[ref] {
			return nil, ErrPermissionAncestry
		}
		seen[ref] = true
		doc, err := permissionDocument(ctx, q, ref)
		if err != nil {
			return nil, err
		}
		if !doc.DefaultsInitialized {
			return nil, ErrPermissionUninitialized
		}
		key := string(ref.Kind) + ":" + ref.UID
		versions["acl:"+key] = doc.Version
		parent, revision, err := permissionResourceParent(ctx, q, ref)
		if err != nil {
			return nil, err
		}
		versions["resource:"+key] = revision
		if parent == "" {
			return versions, nil
		}
		ref = model.PermissionResource{OrgID: ref.OrgID, Kind: model.PermissionFolder, UID: parent}
	}
}

func (s *Store) checkPermissionFenceTx(ctx context.Context, tx *sql.Tx, f PermissionMutationFence) error {
	if f.owner != s {
		return ErrPermissionFence
	}
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT organization_id FROM store_identity WHERE id=1`).Scan(&owner); err != nil || owner != f.actor.principal.OrgID {
		return identity.ErrForbidden
	}
	for ref, value := range f.effective {
		versions, err := permissionVersions(ctx, tx, ref)
		if err != nil || !maps.Equal(versions, value.Versions) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrPermissionFence
		}
	}
	for uid, old := range f.libraries {
		current, err := permissionLibrary(ctx, tx, uid)
		if err != nil {
			return err
		}
		if current != old {
			return ErrPermissionFence
		}
	}
	for id, old := range f.connections {
		current, err := permissionDashboardConnections(ctx, tx, id)
		if err != nil {
			return err
		}
		if !slices.Equal(old, current) {
			return ErrPermissionFence
		}
	}
	return nil
}

func (s *Store) requirePermissionRef(f PermissionMutationFence, kind model.PermissionResourceKind, uid string) error {
	if kind == model.PermissionFolder {
		uid = model.RootFolderUID(uid)
		if uid == "" {
			return nil
		}
	}
	_, ok := f.effective[model.PermissionResource{OrgID: s.OrganizationID(), Kind: kind, UID: uid}]
	if !ok {
		return ErrPermissionFence
	}
	return nil
}

func prepareMutationSeed(ctx context.Context, f PermissionMutationFence, seed model.ResourcePermissionSeed) (preparedPermissionSeed, error) {
	if creator, ok := permissionPrincipalSubject(f.actor.principal); ok {
		if seed.Creator != nil && *seed.Creator != creator {
			return preparedPermissionSeed{}, identity.ErrForbidden
		}
		seed.Creator = &creator
	} else if seed.Creator != nil {
		return preparedPermissionSeed{}, identity.ErrForbidden
	}
	p, err := preparePermissionSeed(ctx, f.actor.principal.OrgID, seed, f.resolver)
	if err != nil {
		return p, err
	}
	if p.creator != nil && p.generation != f.actor.generation {
		return p, ErrPermissionFence
	}
	return p, nil
}

func validatePermissionFolderParent(ctx context.Context, q resourceQuery, uid, parent string) error {
	seen := map[string]bool{uid: true}
	for depth := 1; parent != ""; depth++ {
		if depth >= 64 || seen[parent] {
			return ErrPermissionAncestry
		}
		seen[parent] = true
		folder, err := folderByUID(ctx, q, parent)
		if err != nil {
			return err
		}
		parent = folder.ParentUID
	}
	return nil
}

func (s *Store) CreateFolderWithPermissions(ctx context.Context, folder model.Folder, seed model.ResourcePermissionSeed, f PermissionMutationFence) (model.Folder, error) {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return folder, err
	}
	prepared, err := prepareMutationSeed(ctx, f, seed)
	if err != nil {
		return folder, err
	}
	if err = s.validatePermissionFence(ctx, f); err != nil {
		return folder, err
	}
	if folder.UID == "" {
		folder.UID = uuid.NewString()
	}
	folder.ParentUID = model.RootFolderUID(folder.ParentUID)
	if err = folder.Validate(); err != nil {
		return folder, err
	}
	if err = (model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: folder.UID}).Validate(); err != nil {
		return folder, err
	}
	if err = s.requirePermissionRef(f, model.PermissionFolder, folder.ParentUID); err != nil {
		return folder, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return folder, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return folder, err
	}
	if err = validatePermissionFolderParent(ctx, tx, folder.UID, folder.ParentUID); err != nil {
		return folder, err
	}
	folder, err = createFolderTx(ctx, tx, folder)
	if err != nil {
		return folder, err
	}
	_, err = initializeResourcePermissionsTx(ctx, tx, model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: folder.UID}, prepared)
	if err != nil {
		return folder, err
	}
	return folder, tx.Commit()
}

func (s *Store) SaveDashboardWithPermissions(ctx context.Context, d model.Dashboard, revision *int64, seed model.ResourcePermissionSeed, f PermissionMutationFence) (model.Dashboard, error) {
	return s.SaveDashboardWithLibraryElementsAndPermissions(ctx, d, nil, revision, seed, f)
}

func dashboardLibraryUIDs(d model.Dashboard) map[string]bool {
	seen := map[string]bool{}
	for _, uid := range libraryUIDs(d.Grafana) {
		seen[uid] = true
	}
	for _, p := range d.Panels {
		for _, uid := range libraryUIDs(p.Config) {
			seen[uid] = true
		}
	}
	return seen
}

func (s *Store) SaveDashboardWithLibraryElementsAndPermissions(ctx context.Context, d model.Dashboard, elements []model.LibraryElement, revision *int64, seed model.ResourcePermissionSeed, f PermissionMutationFence) (model.Dashboard, error) {
	if revision != nil {
		expected := *revision
		revision = &expected
	}
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return d, err
	}
	var prepared preparedPermissionSeed
	var err error
	if revision == nil {
		prepared, err = prepareMutationSeed(ctx, f, seed)
		if err != nil {
			return d, err
		}
	} else {
		if *revision <= 0 {
			return d, ErrDashboardConflict
		}
		if err = s.requirePermissionRef(f, model.PermissionDashboard, d.ID); err != nil {
			return d, err
		}
	}
	if err = s.validatePermissionFence(ctx, f); err != nil {
		return d, err
	}
	d.FolderUID = model.RootFolderUID(d.FolderUID)
	if err = d.Validate(); err != nil {
		return d, err
	}
	if err = (model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionDashboard, UID: d.ID}).Validate(); err != nil {
		return d, err
	}
	if err = s.requirePermissionRef(f, model.PermissionFolder, d.FolderUID); err != nil {
		return d, err
	}
	links := dashboardLibraryUIDs(d)
	imports := map[string]model.LibraryElement{}
	for _, e := range elements {
		if e.UID == "" || !links[e.UID] {
			return d, ErrPermissionFence
		}
		if _, duplicate := imports[e.UID]; duplicate {
			return d, ErrPermissionFence
		}
		imports[e.UID] = e
		if err = s.requirePermissionRef(f, model.PermissionFolder, e.FolderUID); err != nil {
			return d, err
		}
	}
	for uid := range links {
		if _, ok := f.libraries[uid]; !ok {
			return d, ErrPermissionFence
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return d, err
	}
	var previous int64
	lookup := tx.QueryRowContext(ctx, `SELECT updated_at FROM dashboards WHERE id=?`, d.ID).Scan(&previous)
	if revision == nil {
		if lookup == nil {
			return d, ErrResourceExists
		}
		if !errors.Is(lookup, sql.ErrNoRows) {
			return d, lookup
		}
	} else if lookup != nil || previous != *revision {
		return d, ErrDashboardConflict
	}
	if d.FolderUID != "" {
		if err = validatePermissionFolderParent(ctx, tx, "", d.FolderUID); err != nil {
			return d, err
		}
	}
	for uid, e := range imports {
		if f.libraries[uid].id == 0 {
			if _, err = saveLibraryTx(ctx, tx, e, true); err != nil {
				return d, err
			}
		}
	}
	for uid := range links {
		library, err := permissionLibrary(ctx, tx, uid)
		if err != nil {
			return d, err
		}
		if library.id == 0 {
			return d, sql.ErrNoRows
		}
		if err = s.requirePermissionRef(f, model.PermissionFolder, library.folder); err != nil {
			return d, err
		}
	}
	d, err = saveDashboardTx(ctx, tx, d, revision)
	if err != nil {
		return d, err
	}
	if revision == nil {
		_, err = initializeResourcePermissionsTx(ctx, tx, model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionDashboard, UID: d.ID}, prepared)
		if err != nil {
			return d, err
		}
	}
	return d, tx.Commit()
}

func (s *Store) UpdateFolderWithPermissions(ctx context.Context, folder model.Folder, f PermissionMutationFence) (model.Folder, error) {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return folder, err
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, folder.UID); err != nil {
		return folder, err
	}
	if err := folder.Validate(); err != nil {
		return folder, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return folder, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return folder, err
	}
	stored, err := folderByUID(ctx, tx, folder.UID)
	if err != nil {
		return folder, err
	}
	if model.RootFolderUID(folder.ParentUID) != model.RootFolderUID(stored.ParentUID) {
		return folder, ErrPermissionFence
	}
	folder, err = updateFolderTx(ctx, tx, folder, false)
	if err != nil {
		return folder, err
	}
	return folder, tx.Commit()
}

func (s *Store) MoveFolderWithPermissions(ctx context.Context, uid, parent string, version int64, f PermissionMutationFence) (model.Folder, error) {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return model.Folder{}, err
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, uid); err != nil {
		return model.Folder{}, err
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, parent); err != nil {
		return model.Folder{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Folder{}, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return model.Folder{}, err
	}
	folder, err := folderByUID(ctx, tx, uid)
	if err != nil {
		return folder, err
	}
	if version <= 0 || folder.Version != version {
		return folder, ErrResourceConflict
	}
	if err = s.requirePermissionSubtreeTx(ctx, tx, uid, f); err != nil {
		return folder, err
	}
	if err = validatePermissionFolderParent(ctx, tx, uid, model.RootFolderUID(parent)); err != nil {
		return folder, err
	}
	folder, err = moveFolderTx(ctx, tx, uid, parent)
	if err != nil {
		return folder, err
	}
	refs, _, err := permissionSubtree(ctx, tx, s.OrganizationID(), uid)
	if err != nil {
		return folder, err
	}
	for _, ref := range refs {
		owner, _, err := permissionResourceParent(ctx, tx, ref)
		if err != nil {
			return folder, err
		}
		id := ""
		if ref.Kind == model.PermissionFolder {
			id = ref.UID
		}
		if err = validatePermissionFolderParent(ctx, tx, id, owner); err != nil {
			return folder, err
		}
	}
	return folder, tx.Commit()
}

func (s *Store) DeleteDashboardWithPermissions(ctx context.Context, id string, revision int64, f PermissionMutationFence) error {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return err
	}
	if err := s.requirePermissionRef(f, model.PermissionDashboard, id); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return err
	}
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM dashboards WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	if revision <= 0 || revision != current {
		return ErrDashboardConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM dashboards WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// gate is the existing trusted scheduler pause callback, never an identity
// resolver or HTTP action authorizer. All catalog callbacks finished earlier.
func (s *Store) DeleteFolderWithPermissions(ctx context.Context, uid string, version int64, forceRules bool, f PermissionMutationFence, gate func([]string) (func(), error)) error {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return err
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, uid); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return err
	}
	folder, err := folderByUID(ctx, tx, uid)
	if err != nil {
		return err
	}
	if version <= 0 || version != folder.Version {
		return ErrResourceConflict
	}
	if err = s.requirePermissionSubtreeTx(ctx, tx, uid, f); err != nil {
		return err
	}
	heldGate, release := holdFolderRuleGate(gate)
	defer func() { _ = tx.Rollback(); release() }()
	if err = deleteFolderTx(ctx, tx, uid, forceRules, heldGate); err != nil {
		return err
	}
	return tx.Commit()
}

const permissionSubtreeSQL = `WITH RECURSIVE tree(uid) AS (SELECT uid FROM folders WHERE uid=? UNION SELECT f.uid FROM folders f JOIN tree ON f.parent_uid=tree.uid) `

func permissionSubtree(ctx context.Context, q resourceQuery, orgID int64, uid string) ([]model.PermissionResource, []string, error) {
	if model.RootFolderUID(uid) == "" {
		return nil, nil, ErrPermissionFence
	}
	if _, err := folderByUID(ctx, q, uid); err != nil {
		return nil, nil, err
	}
	rows, err := q.QueryContext(ctx, permissionSubtreeSQL+`SELECT 'folder',uid FROM tree UNION ALL SELECT 'dashboard',id FROM dashboards WHERE json_extract(extras,'$.folder_uid') IN (SELECT uid FROM tree) ORDER BY 1,2`, uid)
	if err != nil {
		return nil, nil, err
	}
	refs := []model.PermissionResource{}
	for rows.Next() {
		ref := model.PermissionResource{OrgID: orgID}
		if err = rows.Scan(&ref.Kind, &ref.UID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = q.QueryContext(ctx, permissionSubtreeSQL+`SELECT uid FROM library_elements WHERE folder_uid IN (SELECT uid FROM tree) ORDER BY uid`, uid)
	if err != nil {
		return nil, nil, err
	}
	libs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		libs = append(libs, id)
	}
	err = rows.Err()
	rows.Close()
	return refs, libs, err
}

func (s *Store) PermissionSubtreeResources(ctx context.Context, uid string) ([]model.PermissionResource, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	refs, _, err := permissionSubtree(ctx, tx, s.OrganizationID(), uid)
	return refs, err
}

func (s *Store) CapturePermissionSubtreeMutation(ctx context.Context, uid string, destinations []model.PermissionResource, resolver PermissionIdentityResolver) (PermissionMutationFence, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PermissionMutationFence{}, err
	}
	refs, libs, err := permissionSubtree(ctx, tx, s.OrganizationID(), uid)
	tx.Rollback()
	if err != nil {
		return PermissionMutationFence{}, err
	}
	return s.CapturePermissionMutation(ctx, append(refs, destinations...), resolver, libs...)
}

func (s *Store) requirePermissionSubtreeTx(ctx context.Context, tx *sql.Tx, uid string, f PermissionMutationFence) error {
	refs, libs, err := permissionSubtree(ctx, tx, s.OrganizationID(), uid)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if _, ok := f.effective[ref]; !ok {
			return ErrPermissionFence
		}
	}
	for _, uid := range libs {
		if _, ok := f.libraries[uid]; !ok {
			return ErrPermissionFence
		}
	}
	return nil
}

type ResourcePermissionReadiness struct {
	OrgID      int64  `json:"orgId"`
	Version    int    `json:"version"`
	Ready      bool   `json:"ready"`
	Phase      string `json:"phase"`
	Remaining  int64  `json:"remaining"`
	CursorKind string `json:"cursor_kind"`
	CursorUID  string `json:"cursor_uid"`
}

const permissionResourceListSQL = `SELECT 'folder' AS kind,uid FROM folders UNION ALL SELECT 'dashboard' AS kind,id AS uid FROM dashboards`

func permissionReadiness(ctx context.Context, q resourceQuery, orgID int64) (ResourcePermissionReadiness, error) {
	out := ResourcePermissionReadiness{OrgID: orgID, Version: 1, Phase: "pending"}
	var owner int64
	err := q.QueryRowContext(ctx, `SELECT org_id,schema_version,phase,cursor_kind,cursor_uid FROM resource_acl_migration WHERE id=1`).Scan(&owner, &out.Version, &out.Phase, &out.CursorKind, &out.CursorUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil && (owner != orgID || out.Version != 1) {
		return out, identity.ErrForbidden
	}
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM (`+permissionResourceListSQL+`) r LEFT JOIN resource_acl_resources a ON a.org_id=? AND a.resource_kind=r.kind AND a.resource_uid=r.uid WHERE a.resource_uid IS NULL OR a.defaults_initialized!=1`, orgID).Scan(&out.Remaining); err != nil {
		return out, err
	}
	out.Ready = out.Phase == "ready" && out.Remaining == 0
	if out.Remaining > 0 && out.Phase == "ready" {
		out.Phase = "pending"
	}
	return out, nil
}

func (s *Store) ResourcePermissionReadiness(ctx context.Context) (ResourcePermissionReadiness, error) {
	if err := s.permissionOwner(ctx, s.OrganizationID()); err != nil {
		return ResourcePermissionReadiness{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResourcePermissionReadiness{}, err
	}
	defer tx.Rollback()
	out, err := permissionReadiness(ctx, tx, s.OrganizationID())
	if err != nil {
		return out, err
	}
	if out.Ready {
		if err = validatePermissionMigrationAncestry(ctx, tx, s.OrganizationID()); err != nil {
			out.Ready = false
			return out, err
		}
	}
	return out, nil
}

func validatePermissionMigrationAncestry(ctx context.Context, q resourceQuery, orgID int64) error {
	rows, err := q.QueryContext(ctx, permissionResourceListSQL)
	if err != nil {
		return err
	}
	refs := []model.PermissionResource{}
	for rows.Next() {
		ref := model.PermissionResource{OrgID: orgID}
		if err = rows.Scan(&ref.Kind, &ref.UID); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = q.QueryContext(ctx, `SELECT DISTINCT folder_uid,typeof(folder_uid) FROM library_elements`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var owner, kind string
		if err = rows.Scan(&owner, &kind); err != nil {
			rows.Close()
			return err
		}
		if kind != "text" {
			rows.Close()
			return ErrPermissionAncestry
		}
		if owner = model.RootFolderUID(owner); owner != "" {
			refs = append(refs, model.PermissionResource{OrgID: orgID, Kind: model.PermissionFolder, UID: owner})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		seen := map[model.PermissionResource]bool{}
		for depth := 0; ; depth++ {
			if ref.Validate() != nil {
				return ErrPermissionAncestry
			}
			if depth > 64 || seen[ref] {
				return ErrPermissionAncestry
			}
			seen[ref] = true
			parent, _, err := permissionResourceParent(ctx, q, ref)
			if err != nil {
				return err
			}
			if parent == "" {
				break
			}
			ref = model.PermissionResource{OrgID: orgID, Kind: model.PermissionFolder, UID: parent}
		}
	}
	return nil
}

// Explicit org-admin migration seeds only missing ACLs and never guesses a
// creator from historical names. Each bounded batch and cursor commit together;
// a final fresh completeness/ancestry check gates readiness. Legacy methods do
// not opt into this migration or action authorization automatically.
func (s *Store) MigrateResourcePermissions(ctx context.Context, batchSize int, resolver PermissionIdentityResolver) (ResourcePermissionReadiness, error) {
	if batchSize < 1 || batchSize > 1000 {
		return ResourcePermissionReadiness{}, errors.New("permission migration batch must contain 1 to 1000 resources")
	}
	actor, err := s.permissionActor(ctx, resolver)
	if err != nil {
		return ResourcePermissionReadiness{}, err
	}
	if actor.principal.Role != identity.RoleAdmin {
		return ResourcePermissionReadiness{}, identity.ErrForbidden
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ResourcePermissionReadiness{}, err
	}
	defer tx.Rollback()
	out, err := permissionReadiness(ctx, tx, s.OrganizationID())
	if err != nil {
		return out, err
	}
	if out.Ready {
		if err = validatePermissionMigrationAncestry(ctx, tx, s.OrganizationID()); err != nil {
			out.Ready = false
			return out, err
		}
		return out, nil
	}
	query := `SELECT r.kind,r.uid FROM (` + permissionResourceListSQL + `) r LEFT JOIN resource_acl_resources a ON a.org_id=? AND a.resource_kind=r.kind AND a.resource_uid=r.uid WHERE a.resource_uid IS NULL AND (r.kind>? OR r.kind=? AND r.uid>?) ORDER BY r.kind,r.uid LIMIT ?`
	load := func(kind, uid string) ([]model.PermissionResource, error) {
		rows, err := tx.QueryContext(ctx, query, s.OrganizationID(), kind, kind, uid, batchSize)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		refs := []model.PermissionResource{}
		for rows.Next() {
			ref := model.PermissionResource{OrgID: s.OrganizationID()}
			if err = rows.Scan(&ref.Kind, &ref.UID); err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
		return refs, rows.Err()
	}
	refs, err := load(out.CursorKind, out.CursorUID)
	if err != nil {
		return out, err
	}
	if len(refs) == 0 && out.Remaining > 0 {
		refs, err = load("", "")
		if err != nil {
			return out, err
		}
		out.CursorKind, out.CursorUID = "", ""
	}
	if len(refs) == 0 && out.Remaining > 0 {
		return out, ErrPermissionUninitialized
	}
	for _, ref := range refs {
		if _, err = initializeResourcePermissionsTx(ctx, tx, ref, preparedPermissionSeed{policy: model.PermissionLegacyDefaults}); err != nil {
			return out, err
		}
		out.CursorKind, out.CursorUID = string(ref.Kind), ref.UID
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM (`+permissionResourceListSQL+`) r LEFT JOIN resource_acl_resources a ON a.org_id=? AND a.resource_kind=r.kind AND a.resource_uid=r.uid WHERE a.resource_uid IS NULL OR a.defaults_initialized!=1`, s.OrganizationID()).Scan(&out.Remaining); err != nil {
		return out, err
	}
	out.Phase = "running"
	out.Ready = false
	if out.Remaining == 0 {
		if err = validatePermissionMigrationAncestry(ctx, tx, s.OrganizationID()); err != nil {
			return out, err
		}
		out.Phase = "ready"
		out.Ready = true
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO resource_acl_migration(id,org_id,schema_version,phase,cursor_kind,cursor_uid,updated_at) VALUES(1,?,1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,cursor_kind=excluded.cursor_kind,cursor_uid=excluded.cursor_uid,updated_at=excluded.updated_at`, s.OrganizationID(), out.Phase, out.CursorKind, out.CursorUID, time.Now().UnixMilli())
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
