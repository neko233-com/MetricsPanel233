package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func permissionAnnotation(d model.Dashboard, text string) model.Annotation {
	return model.Annotation{DashboardUID: d.UID(), DashboardID: d.GrafanaID, PanelID: 2, Time: 1000, Text: text, Tags: []string{"release"}, Data: json.RawMessage(`{"wide":9007199254740993}`)}
}

func captureAnnotation(t *testing.T, s *store.Store, ctx context.Context, id int64) store.AnnotationMutationFence {
	t.Helper()
	f, err := s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: id}, permissionFixtureResolver())
	require.NoError(t, err)
	return f
}

func createPermissionAnnotation(t *testing.T, s *store.Store, ctx context.Context, a model.Annotation, key string) model.Annotation {
	t.Helper()
	f, err := s.CaptureAnnotationCreation(ctx, a, key, permissionFixtureResolver())
	require.NoError(t, err)
	a, err = s.CreateAnnotationWithPermissions(ctx, a, key, f)
	require.NoError(t, err)
	return a
}

func annotationRevision(t *testing.T, s *store.Store, id int64) (string, int64) {
	t.Helper()
	var incarnation string
	var version int64
	require.NoError(t, s.DB.QueryRow(`SELECT incarnation,version FROM annotation_permission_versions WHERE annotation_id=?`, id).Scan(&incarnation, &version))
	return incarnation, version
}

func TestAnnotationPermissionsCRUDRealOwnerAndClonedTargets(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	folder := mutationFolder(t, s, ctx, "owner", "")
	d := model.Dashboard{ID: "native", Name: "Native", FolderUID: folder.UID, Grafana: json.RawMessage(`{"dashboard":{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"external"},"spec":{"elements":{},"opaque":9007199254740993}}}`)}
	f := mutationFence(t, s, ctx, []model.PermissionResource{permissionRef(s, model.PermissionFolder, folder.UID)})
	d, err := s.SaveDashboardWithPermissions(ctx, d, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	a := permissionAnnotation(d, "Deploy")
	a.UserID, a.Login = 999, "forged"
	creation, err := s.CaptureAnnotationCreation(ctx, a, "create", permissionFixtureResolver())
	require.NoError(t, err)
	targets := creation.Targets()
	require.Len(t, targets, 1)
	require.Equal(t, "native", targets[0].Owner.DashboardID)
	require.Equal(t, "external", targets[0].Owner.DashboardUID)
	require.Equal(t, int64(7), targets[0].Annotation.UserID)
	require.Equal(t, "fixture", targets[0].Annotation.Login)
	targets[0].Annotation.Tags[0] = "forged"
	targets[0].Annotation.Data[0] = 'x'
	targets[0].Owner.DashboardID = "forged"
	refs := creation.Resources()
	require.Equal(t, []model.PermissionResource{permissionRef(s, model.PermissionDashboard, d.ID)}, refs)
	effective, err := creation.Effective(refs[0])
	require.NoError(t, err)
	require.Equal(t, model.PermissionAdmin, effective.Permission)
	for key := range effective.Versions {
		effective.Versions[key] = 1000
	}
	refs[0].UID = "forged"
	a, err = s.CreateAnnotationWithPermissions(ctx, a, "create", creation)
	require.NoError(t, err)
	require.Equal(t, []string{"release"}, a.Tags)
	require.JSONEq(t, `{"wide":9007199254740993}`, string(a.Data))
	require.Equal(t, int64(7), a.UserID)
	_, err = s.CreateAnnotationWithPermissions(ctx, a, "create", creation)
	require.ErrorIs(t, err, store.ErrAnnotationPermissionFence, "a capture with an absent key cannot silently return a later row")
	oldIncarnation, oldVersion := annotationRevision(t, s, a.ID)
	text, tags, when := "Updated", []string{"rollout"}, int64(2000)
	patched, err := s.PatchAnnotationWithPermissions(ctx, a.ID, model.AnnotationPatch{Text: &text, Tags: &tags, Time: &when}, captureAnnotation(t, s, ctx, a.ID))
	require.NoError(t, err)
	require.Equal(t, int64(2000), patched.TimeEnd, "point annotations follow a changed point time")
	require.Equal(t, a.Created, patched.Created)
	require.Equal(t, a.DashboardUID, patched.DashboardUID)
	require.Equal(t, a.UserID, patched.UserID)
	incarnation, version := annotationRevision(t, s, a.ID)
	require.Equal(t, oldIncarnation, incarnation)
	require.Greater(t, version, oldVersion)
	require.NoError(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, captureAnnotation(t, s, ctx, a.ID)))
	for _, table := range []string{"annotations", "annotation_tags", "annotation_permission_versions"} {
		var n int
		column := "annotation_id"
		if table == "annotations" {
			column = "id"
		}
		require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`=?`, a.ID).Scan(&n))
		require.Zero(t, n)
	}
}

func TestAnnotationPermissionsOrganizationWideAndMethodBinding(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	input := model.Annotation{Time: 1000, Text: "Global", Tags: []string{"org"}}
	f, err := s.CaptureAnnotationCreation(ctx, input, "", permissionFixtureResolver())
	require.NoError(t, err)
	require.Empty(t, f.Resources(), "organization annotation is not a made-up dashboard/folder grant")
	require.True(t, f.Targets()[0].Owner.OrganizationWide)
	_, err = f.Effective(permissionRef(s, model.PermissionDashboard, "system"))
	require.ErrorIs(t, err, store.ErrPermissionFence)
	a, err := s.CreateAnnotationWithPermissions(ctx, input, "", f)
	require.NoError(t, err)
	captured := captureAnnotation(t, s, ctx, a.ID)
	wrong := identity.WithPrincipal(context.Background(), permissionActor(1, 7, identity.TypeServiceAccount, identity.RoleNone))
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(wrong, a.ID, captured), store.ErrPermissionFence, "same numeric user/SA ID is not the same actor")
	foreign := permissionStore(t, 2)
	require.ErrorIs(t, foreign.DeleteAnnotationWithPermissions(mutationContext(foreign), a.ID, captured), store.ErrPermissionFence)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID+1, captured), store.ErrAnnotationPermissionFence)
	require.ErrorIs(t, s.DeletePanelAnnotationsWithPermissions(ctx, "system", 2, captured), store.ErrAnnotationPermissionFence)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence)
	_, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: a.ID, DashboardUID: "system"}, permissionFixtureResolver())
	require.ErrorIs(t, err, model.ErrInvalidAnnotation)
	_, err = s.CaptureAnnotationMutation(context.Background(), store.AnnotationMutationSelector{ID: a.ID}, permissionFixtureResolver())
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(canceled, a.ID, captured), context.Canceled)
}

func TestAnnotationPermissionsIdempotencyCapturesActualRetryOwner(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "first", "")
	other := mutationDashboard(t, s, ctx, "second", "")
	input := permissionAnnotation(d, "Original")
	a := createPermissionAnnotation(t, s, ctx, input, "retry")
	text := "Patched"
	_, err := s.PatchAnnotation(ctx, a.ID, model.AnnotationPatch{Text: &text})
	require.NoError(t, err)
	f, err := s.CaptureAnnotationCreation(ctx, input, "retry", permissionFixtureResolver())
	require.NoError(t, err)
	require.Len(t, f.Targets(), 2)
	require.False(t, f.Targets()[0].Existing)
	require.True(t, f.Targets()[1].Existing)
	require.Equal(t, "Patched", f.Targets()[1].Annotation.Text)
	actual, err := s.CreateAnnotationWithPermissions(ctx, input, "retry", f)
	require.NoError(t, err)
	require.Equal(t, a.ID, actual.ID)
	require.Equal(t, "Patched", actual.Text)
	input.Text = "Different"
	_, err = s.CaptureAnnotationCreation(ctx, input, "retry", permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrAnnotationConflict)
	input.Text = "Original"
	// An old unguarded writer could have reassigned a row without changing its
	// original request hash. The retry decision must include that real owner.
	actual.DashboardUID, actual.DashboardID = other.UID(), other.GrafanaID
	raw, err := json.Marshal(actual)
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE annotations SET dashboard_uid=?,payload=? WHERE id=?`, actual.DashboardUID, string(raw), actual.ID)
	require.NoError(t, err)
	f, err = s.CaptureAnnotationCreation(ctx, input, "retry", permissionFixtureResolver())
	require.NoError(t, err)
	require.Equal(t, d.ID, f.Targets()[0].Owner.DashboardID)
	require.Equal(t, other.ID, f.Targets()[1].Owner.DashboardID)
	require.Len(t, f.Resources(), 2)
	_, err = s.DB.Exec(`UPDATE annotations SET dashboard_uid='orphan' WHERE id=?`, actual.ID)
	require.NoError(t, err)
	_, err = s.CaptureAnnotationCreation(ctx, input, "retry", permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrAnnotationPermissionOwner)
}

func TestAnnotationPermissionsLegacyABAIncarnationAndMapping(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "stable", "")
	a := createPermissionAnnotation(t, s, ctx, permissionAnnotation(d, "Original"), "")
	f := captureAnnotation(t, s, ctx, a.ID)
	var payload string
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM annotations WHERE id=?`, a.ID).Scan(&payload))
	text := "Temporary"
	_, err := s.PatchAnnotation(ctx, a.ID, model.AnnotationPatch{Text: &text})
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE annotations SET payload=? WHERE id=?`, payload, a.ID)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence, "identical restored payload/updated timestamps do not erase a revision change")
	f = captureAnnotation(t, s, ctx, a.ID)
	incarnation, _ := annotationRevision(t, s, a.ID)
	require.NoError(t, s.DeleteAnnotation(ctx, a.ID))
	_, err = s.DB.Exec(`INSERT INTO annotations(id,dashboard_uid,time,time_end,panel_id,user_id,payload,request_hash) VALUES(?,?,?,?,?,?,?,'')`, a.ID, a.DashboardUID, a.Time, a.TimeEnd, a.PanelID, a.UserID, payload)
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO annotation_tags(annotation_id,tag) VALUES(?,'release')`, a.ID)
	require.NoError(t, err)
	newIncarnation, _ := annotationRevision(t, s, a.ID)
	require.NotEqual(t, incarnation, newIncarnation)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence)
	f = captureAnnotation(t, s, ctx, a.ID)
	duplicate := mutationDashboard(t, s, ctx, "duplicate", "")
	duplicate.Grafana = json.RawMessage(`{"uid":"stable","panels":[]}`)
	_, err = s.SaveDashboardAtRevision(ctx, duplicate, duplicate.UpdatedAt)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence, "even an unchanged selected row cannot use a newly ambiguous UID mapping")
}

func TestAnnotationPermissionsOwnerResolutionAndAutomaticRefusal(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "native", "")
	d.Grafana = json.RawMessage(`{"uid":"external","panels":[]}`)
	var err error
	d, err = s.SaveDashboardAtRevision(ctx, d, d.UpdatedAt)
	require.NoError(t, err)
	for _, a := range []model.Annotation{
		{Time: 1000, Text: "Unknown", DashboardUID: "unknown"},
		{Time: 1000, Text: "Native ID is not external UID", DashboardUID: d.ID},
		{Time: 1000, Text: "Mismatched number", DashboardUID: d.UID(), DashboardID: d.GrafanaID + 99},
		{Time: 1000, Text: "Fake organization scope", DashboardID: d.GrafanaID},
		{Time: 1000, Text: "Alert", AlertUID: "real-rule"},
	} {
		_, err = s.CaptureAnnotationCreation(ctx, a, "", permissionFixtureResolver())
		require.ErrorIs(t, err, store.ErrAnnotationPermissionOwner, a.Text)
	}
	crc := permissionAnnotation(d, "Historic CRC")
	crc.DashboardID = int64(crc32.ChecksumIEEE([]byte(d.UID())))
	a := createPermissionAnnotation(t, s, ctx, crc, "")
	require.Equal(t, d.ID, captureAnnotation(t, s, ctx, a.ID).Targets()[0].Owner.DashboardID)
	manual := createPermissionAnnotation(t, s, ctx, permissionAnnotation(d, "Manual"), "")
	_, err = s.DB.Exec(`INSERT INTO annotation_alerts(annotation_id,event_id,rule_uid,rule_id) VALUES(?,991,'rule',1)`, manual.ID)
	require.NoError(t, err)
	_, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: manual.ID}, permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrAnnotationPermissionOwner, "real projection association is authoritative even if payload looks manual")
	_, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{DashboardUID: d.UID(), PanelID: 2}, permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrAnnotationPermissionOwner, "bulk selection cannot silently skip an automatic row")
}

func TestAnnotationPermissionsBulkFullSelectionCASAndRollback(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "bulk", "")
	selector := store.AnnotationMutationSelector{DashboardUID: d.UID(), PanelID: 2}
	empty, err := s.CaptureAnnotationMutation(ctx, selector, permissionFixtureResolver())
	require.NoError(t, err)
	require.Empty(t, empty.Targets())
	require.Len(t, empty.Resources(), 1, "an empty selection still binds its actual dashboard owner")
	first := createPermissionAnnotation(t, s, ctx, permissionAnnotation(d, "One"), "")
	require.ErrorIs(t, s.DeletePanelAnnotationsWithPermissions(ctx, d.UID(), 2, empty), store.ErrAnnotationPermissionFence)
	second := createPermissionAnnotation(t, s, ctx, permissionAnnotation(d, "Two"), "")
	f, err := s.CaptureAnnotationMutation(ctx, selector, permissionFixtureResolver())
	require.NoError(t, err)
	_, err = s.DB.Exec(`CREATE TRIGGER refuse_bulk BEFORE DELETE ON annotations WHEN OLD.id=` + jsonInt(second.ID) + ` BEGIN SELECT RAISE(ABORT,'fixture refuses last delete'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.DeletePanelAnnotationsWithPermissions(ctx, d.UID(), 2, f), "fixture refuses last delete")
	items, err := s.Annotations(ctx, model.AnnotationQuery{DashboardUID: d.UID()})
	require.NoError(t, err)
	require.Len(t, items, 2)
	_, err = s.DB.Exec(`DROP TRIGGER refuse_bulk`)
	require.NoError(t, err)
	text := "Changed"
	_, err = s.PatchAnnotation(ctx, first.ID, model.AnnotationPatch{Text: &text})
	require.NoError(t, err)
	require.ErrorIs(t, s.DeletePanelAnnotationsWithPermissions(ctx, d.UID(), 2, f), store.ErrAnnotationPermissionFence)
	f, err = s.CaptureAnnotationMutation(ctx, selector, permissionFixtureResolver())
	require.NoError(t, err)
	require.NoError(t, s.DeleteAnnotation(ctx, second.ID))
	require.ErrorIs(t, s.DeletePanelAnnotationsWithPermissions(ctx, d.UID(), 2, f), store.ErrAnnotationPermissionFence)
	// Populate more than the public annotation list limit in one real DB TX.
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	for i := 0; i < 1005; i++ {
		result, e := tx.ExecContext(ctx, `INSERT INTO annotations(dashboard_uid,time,time_end,panel_id,user_id,payload,request_hash) VALUES(?,1000,1000,2,7,'{}','')`, d.UID())
		require.NoError(t, e)
		id, e := result.LastInsertId()
		require.NoError(t, e)
		a := permissionAnnotation(d, "Bulk")
		a.ID, a.UserID, a.Login, a.Created, a.Updated = id, 7, "fixture", 1000, 1000
		require.NoError(t, a.Normalize())
		raw, e := json.Marshal(a)
		require.NoError(t, e)
		_, e = tx.ExecContext(ctx, `UPDATE annotations SET payload=? WHERE id=?`, string(raw), id)
		require.NoError(t, e)
		_, e = tx.ExecContext(ctx, `INSERT INTO annotation_tags(annotation_id,tag) VALUES(?,'release')`, id)
		require.NoError(t, e)
	}
	require.NoError(t, tx.Commit())
	f, err = s.CaptureAnnotationMutation(ctx, selector, permissionFixtureResolver())
	require.NoError(t, err)
	require.Len(t, f.Targets(), 1006)
	require.NoError(t, s.DeletePanelAnnotationsWithPermissions(ctx, d.UID(), 2, f))
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM annotations WHERE dashboard_uid=?`, d.UID()).Scan(&count))
	require.Zero(t, count)
}

func jsonInt(value int64) string { raw, _ := json.Marshal(value); return string(raw) }

func TestAnnotationPermissionsAtomicTagsProjectionAndOverflow(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	a := createPermissionAnnotation(t, s, ctx, model.Annotation{Time: 1000, Text: "Global", Tags: []string{"old"}}, "")
	incarnation, version := annotationRevision(t, s, a.ID)
	f := captureAnnotation(t, s, ctx, a.ID)
	_, err := s.DB.Exec(`CREATE TRIGGER refuse_tag BEFORE INSERT ON annotation_tags WHEN NEW.tag='reject' BEGIN SELECT RAISE(ABORT,'tag fixture'); END`)
	require.NoError(t, err)
	tags := []string{"reject"}
	text := "Replaced"
	_, err = s.PatchAnnotationWithPermissions(ctx, a.ID, model.AnnotationPatch{Text: &text, Tags: &tags}, f)
	require.ErrorContains(t, err, "tag fixture")
	items, err := s.Annotations(ctx, model.AnnotationQuery{ID: a.ID})
	require.NoError(t, err)
	require.Equal(t, a, items[0])
	currentIncarnation, currentVersion := annotationRevision(t, s, a.ID)
	require.Equal(t, incarnation, currentIncarnation)
	require.Equal(t, version, currentVersion)
	_, err = s.DB.Exec(`DROP TRIGGER refuse_tag`)
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE annotation_permission_versions SET version=? WHERE annotation_id=?`, int64(math.MaxInt64), a.ID)
	require.NoError(t, err)
	f = captureAnnotation(t, s, ctx, a.ID)
	_, err = s.PatchAnnotationWithPermissions(ctx, a.ID, model.AnnotationPatch{Text: &text}, f)
	require.ErrorContains(t, err, "version is unavailable or exhausted")
	_, err = s.DB.Exec(`INSERT INTO annotation_alerts(annotation_id,event_id,rule_uid,rule_id) VALUES(?,77,'rule',1)`, a.ID)
	require.ErrorContains(t, err, "version is unavailable or exhausted")
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM annotation_alerts WHERE annotation_id=?`, a.ID).Scan(&count))
	require.Zero(t, count)
	var storageType string
	require.NoError(t, s.DB.QueryRow(`SELECT typeof(version) FROM annotation_permission_versions WHERE annotation_id=?`, a.ID).Scan(&storageType))
	require.Equal(t, "integer", storageType)
	_, currentVersion = annotationRevision(t, s, a.ID)
	require.Equal(t, int64(math.MaxInt64), currentVersion)
	items, err = s.Annotations(ctx, model.AnnotationQuery{ID: a.ID})
	require.NoError(t, err)
	require.Equal(t, a, items[0])
	require.NoError(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), "delete needs no overflowing arithmetic; a future incarnation remains distinct")
}

func TestAnnotationPermissionsInitializerMigrationRestartAndReusedHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "annotation-legacy.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	// Emulate a legacy DB with real rows but no fence side table/triggers.
	rows, err := s.DB.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'annotation_permission_%'`)
	require.NoError(t, err)
	var triggers []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		triggers = append(triggers, name)
	}
	require.NoError(t, rows.Close())
	for _, name := range triggers {
		_, err = s.DB.Exec(`DROP TRIGGER ` + name)
		require.NoError(t, err)
	}
	_, err = s.DB.Exec(`DROP TABLE annotation_permission_versions`)
	require.NoError(t, err)
	a, err := s.CreateAnnotation(context.Background(), model.Annotation{Time: 1000, Text: "Legacy", Tags: []string{"legacy"}}, "legacy")
	require.NoError(t, err)
	var before string
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM annotations WHERE id=?`, a.ID).Scan(&before))
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	incarnation, version := annotationRevision(t, s, a.ID)
	require.Equal(t, int64(1), version)
	require.NoError(t, s.InitializeAnnotationPermissionSchema(context.Background()))
	newIncarnation, newVersion := annotationRevision(t, s, a.ID)
	require.Equal(t, incarnation, newIncarnation)
	require.Equal(t, version, newVersion)
	var after string
	require.NoError(t, s.DB.QueryRow(`SELECT payload FROM annotations WHERE id=?`, a.ID).Scan(&after))
	require.Equal(t, before, after)
	ctx := mutationContext(s)
	f := captureAnnotation(t, s, ctx, a.ID)
	require.NoError(t, s.DB.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	defer reopened.DB.Close()
	newIncarnation, newVersion = annotationRevision(t, reopened, a.ID)
	require.Equal(t, incarnation, newIncarnation)
	require.Equal(t, version, newVersion)
	require.ErrorIs(t, reopened.DeleteAnnotationWithPermissions(mutationContext(reopened), a.ID, f), store.ErrPermissionFence, "same path on a new Store does not accept old opaque handles")
	require.NoError(t, reopened.DeleteAnnotationWithPermissions(mutationContext(reopened), a.ID, captureAnnotation(t, reopened, mutationContext(reopened), a.ID)))
}

func TestAnnotationPermissionsConcurrentCASAndCredentialRevocation(t *testing.T) {
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = catalog.BootstrapAdmin(context.Background(), identity.UserInput{Login: "admin", Password: "annotation-admin-password"})
	require.NoError(t, err)
	u, err := catalog.CreateUser(context.Background(), identity.UserInput{Login: "member", Password: "annotation-member-password", OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	p, session, key, err := catalog.Login(context.Background(), u.Login, "annotation-member-password", 1, time.Hour, time.Hour)
	require.NoError(t, err)
	resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return catalog.AuthenticateSession(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	s := permissionStore(t, 1)
	ctx := identity.WithPrincipal(context.Background(), p)
	creation, err := s.CaptureAnnotationCreation(ctx, model.Annotation{Time: 1000, Text: "Global"}, "", resolver)
	require.NoError(t, err)
	a, err := s.CreateAnnotationWithPermissions(ctx, model.Annotation{Time: 1000, Text: "Global"}, "", creation)
	require.NoError(t, err)
	f, err := s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: a.ID}, resolver)
	require.NoError(t, err)
	var success, conflict atomic.Int64
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, value := range []string{"One", "Two"} {
		group.Add(1)
		go func(text string) {
			defer group.Done()
			_, e := s.PatchAnnotationWithPermissions(ctx, a.ID, model.AnnotationPatch{Text: &text}, f)
			if e == nil {
				success.Add(1)
			} else if errorsIsAnnotationFence(e) {
				conflict.Add(1)
			} else {
				errs <- e
			}
		}(value)
	}
	group.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.Equal(t, int64(1), success.Load())
	require.Equal(t, int64(1), conflict.Load())
	f, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: a.ID}, resolver)
	require.NoError(t, err)
	require.NoError(t, catalog.RevokeSession(ctx, u.ID, session.ID))
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), identity.ErrUnauthenticated)
	items, err := s.Annotations(context.Background(), model.AnnotationQuery{ID: a.ID})
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func errorsIsAnnotationFence(err error) bool {
	return errors.Is(err, store.ErrAnnotationPermissionFence)
}

func TestAnnotationPermissionsCallbacksCannotChangeInputsOrEscapeRowCAS(t *testing.T) {
	s := permissionStore(t, 1)
	ctx, cancel := context.WithTimeout(mutationContext(s), 5*time.Second)
	defer cancel()
	input := model.Annotation{Time: 1000, Text: "Global", Tags: []string{"original"}, Data: json.RawMessage(`{"v":1}`)}
	resolver := permissionFixtureResolver()
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		// A real control-DB callback can read this one-connection store; it must
		// run outside its writer, and cannot modify retained caller buffers.
		var count int
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM annotations`).Scan(&count); err != nil {
			return p, err
		}
		input.Tags[0], input.Data[0] = "changed", 'x'
		return p, nil
	}
	f, err := s.CaptureAnnotationCreation(ctx, input, "callback", resolver)
	require.NoError(t, err)
	require.Equal(t, []string{"original"}, f.Targets()[0].Annotation.Tags)
	require.JSONEq(t, `{"v":1}`, string(f.Targets()[0].Annotation.Data))
	a, err := s.CreateAnnotationWithPermissions(ctx, f.Targets()[0].Annotation, "callback", f)
	require.NoError(t, err)
	var armed atomic.Bool
	text, when, tags, data := "Before", int64(2000), []string{"before"}, json.RawMessage(`{"v":2}`)
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		if armed.Load() {
			text, when, tags[0], data[0] = "After", 3000, "after", 'x'
		}
		return p, nil
	}
	f, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: a.ID}, resolver)
	require.NoError(t, err)
	armed.Store(true)
	a, err = s.PatchAnnotationWithPermissions(ctx, a.ID, model.AnnotationPatch{Text: &text, Time: &when, Tags: &tags, Data: data}, f)
	require.NoError(t, err)
	require.Equal(t, "Before", a.Text)
	require.Equal(t, int64(2000), a.Time)
	require.Equal(t, []string{"before"}, a.Tags)
	require.JSONEq(t, `{"v":2}`, string(a.Data))
	armed.Store(false)
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		if armed.CompareAndSwap(true, false) {
			changed := "Concurrent legacy update"
			_, err := s.PatchAnnotation(ctx, a.ID, model.AnnotationPatch{Text: &changed})
			return p, err
		}
		return p, nil
	}
	f, err = s.CaptureAnnotationMutation(ctx, store.AnnotationMutationSelector{ID: a.ID}, resolver)
	require.NoError(t, err)
	armed.Store(true)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence)
	// Creation is also fenced when another callback creates the formerly absent key.
	resolver = permissionFixtureResolver()
	creation := model.Annotation{Time: 1000, Text: "Later key"}
	f, err = s.CaptureAnnotationCreation(ctx, creation, "later", resolver)
	require.NoError(t, err)
	// A fresh real insert after capture can never be returned by the old capture.
	_, err = s.CreateAnnotation(ctx, model.Annotation{Time: 1000, Text: "Later key", UserID: 7, Login: "fixture"}, "later")
	require.NoError(t, err)
	_, err = s.CreateAnnotationWithPermissions(ctx, creation, "later", f)
	require.ErrorIs(t, err, store.ErrAnnotationPermissionFence)
}

func TestAnnotationPermissionsOwnerAndACLChangedBeforeWriter(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "actual-owner", "")
	a := createPermissionAnnotation(t, s, ctx, permissionAnnotation(d, "Owned"), "")
	f := captureAnnotation(t, s, ctx, a.ID)
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	doc, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, nil, permissionFixtureResolver())
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrPermissionFence)
	f = captureAnnotation(t, s, ctx, a.ID)
	// Change UID directly without updating dashboard.updated_at: row-owner
	// re-resolution still detects the changed external mapping.
	_, err = s.DB.Exec(`UPDATE dashboards SET extras=json_set(extras,'$.grafana',json('{"uid":"renamed","panels":[]}')) WHERE id=?`, d.ID)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteAnnotationWithPermissions(ctx, a.ID, f), store.ErrAnnotationPermissionFence)
	items, err := s.Annotations(ctx, model.AnnotationQuery{ID: a.ID})
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func TestAnnotationPermissionsMigrationFailureIsAtomic(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := context.Background()
	// Remove this additive schema, as a pre-migration control database would.
	rows, err := s.DB.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'annotation_permission_%'`)
	require.NoError(t, err)
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Close())
	for _, name := range names {
		_, err = s.DB.Exec(`DROP TRIGGER ` + name)
		require.NoError(t, err)
	}
	_, err = s.DB.Exec(`DROP TABLE annotation_permission_versions`)
	require.NoError(t, err)
	a, err := s.CreateAnnotation(ctx, model.Annotation{Time: 1000, Text: "Legacy"}, "")
	require.NoError(t, err)
	// A malformed preexisting schema contains an orphan. The complete migration
	// fails and rolls back its backfill and trigger creation together.
	_, err = s.DB.Exec(`CREATE TABLE annotation_permission_versions(annotation_id INTEGER PRIMARY KEY,incarnation TEXT NOT NULL,version INTEGER NOT NULL);
INSERT INTO annotation_permission_versions VALUES(999,'orphan',1)`)
	require.NoError(t, err)
	require.ErrorIs(t, s.InitializeAnnotationPermissionSchema(ctx), store.ErrAnnotationPermissionFence)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM annotation_permission_versions WHERE annotation_id=?`, a.ID).Scan(&count))
	require.Zero(t, count, "failed backfill is rolled back")
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'annotation_permission_%'`).Scan(&count))
	require.Zero(t, count, "failed migration does not leave partially active triggers")
	_, err = s.DB.Exec(`DROP TABLE annotation_permission_versions`)
	require.NoError(t, err)
	require.NoError(t, s.InitializeAnnotationPermissionSchema(ctx))
	_, version := annotationRevision(t, s, a.ID)
	require.Equal(t, int64(1), version)
}

func TestAnnotationPermissionsEveryLegacyRelationWriteAdvancesRevision(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := context.Background()
	a, err := s.CreateAnnotation(ctx, model.Annotation{Time: 1000, Text: "Legacy"}, "")
	require.NoError(t, err)
	incarnation, previous := annotationRevision(t, s, a.ID)
	for _, statement := range []string{
		`INSERT INTO annotation_tags(annotation_id,tag) VALUES(?,'one')`,
		`UPDATE annotation_tags SET tag='two' WHERE annotation_id=?`,
		`DELETE FROM annotation_tags WHERE annotation_id=?`,
		`INSERT INTO annotation_alerts(annotation_id,event_id,rule_uid,rule_id) VALUES(?,99,'one',1)`,
		`UPDATE annotation_alerts SET rule_uid='two' WHERE annotation_id=?`,
		`DELETE FROM annotation_alerts WHERE annotation_id=?`,
	} {
		_, err = s.DB.Exec(statement, a.ID)
		require.NoError(t, err)
		currentIncarnation, version := annotationRevision(t, s, a.ID)
		require.Equal(t, incarnation, currentIncarnation)
		require.Greater(t, version, previous, statement)
		previous = version
	}
	// A failed automatic projection insertion rolls back both data and revision.
	tx, err := s.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO annotation_alerts(annotation_id,event_id,rule_uid,rule_id) VALUES(?,100,'temporary',1)`, a.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	_, version := annotationRevision(t, s, a.ID)
	require.Equal(t, previous, version)
}
