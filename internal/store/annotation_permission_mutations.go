package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var (
	ErrAnnotationPermissionFence = errors.New("annotation mutation snapshot changed or is not covered")
	ErrAnnotationPermissionOwner = errors.New("annotation owner is unavailable or is not a manual annotation")
)

// InitializeAnnotationPermissionSchema is additive and does not enable HTTP
// authorization. Triggers cover legacy and alert-projection writes as well as
// guarded writes. A fresh incarnation prevents a reused numeric ID from
// inheriting a previously captured permission snapshot.
func (s *Store) InitializeAnnotationPermissionSchema(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var orgID int64
	if err = tx.QueryRowContext(ctx, `SELECT organization_id FROM store_identity WHERE id=1`).Scan(&orgID); err != nil || orgID != s.OrganizationID() {
		return identity.ErrForbidden
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS annotation_permission_versions(
annotation_id INTEGER PRIMARY KEY REFERENCES annotations(id) ON DELETE CASCADE,
incarnation TEXT NOT NULL UNIQUE CHECK(length(incarnation)=64),
version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0));
INSERT INTO annotation_permission_versions(annotation_id,incarnation,version)
SELECT id,lower(hex(randomblob(32))),1 FROM annotations
WHERE NOT EXISTS(SELECT 1 FROM annotation_permission_versions v WHERE v.annotation_id=annotations.id);
CREATE TRIGGER IF NOT EXISTS annotation_permission_insert AFTER INSERT ON annotations BEGIN
 INSERT INTO annotation_permission_versions(annotation_id,incarnation,version) VALUES(NEW.id,lower(hex(randomblob(32))),1);
END;
CREATE TRIGGER IF NOT EXISTS annotation_permission_identity BEFORE UPDATE OF id ON annotations WHEN NEW.id!=OLD.id BEGIN
 SELECT RAISE(ABORT,'annotation identity is immutable');
END;`); err != nil {
		return err
	}
	// Fail before arithmetic: SQLite would otherwise promote MaxInt64+1 to REAL.
	for _, table := range []string{"annotations", "annotation_tags", "annotation_alerts"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			if table == "annotations" && event != "UPDATE" {
				continue
			}
			var ids string
			switch {
			case table == "annotations":
				ids = "NEW.id"
			case event == "INSERT":
				ids = "NEW.annotation_id"
			case event == "DELETE":
				ids = "OLD.annotation_id"
			default:
				ids = "OLD.annotation_id,NEW.annotation_id"
			}
			name := "annotation_permission_" + table + "_" + strings.ToLower(event)
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s BEGIN
 SELECT CASE WHEN EXISTS(SELECT 1 FROM annotations a WHERE a.id IN (%s) AND NOT EXISTS(
   SELECT 1 FROM annotation_permission_versions v WHERE v.annotation_id=a.id AND typeof(v.version)='integer' AND v.version>0 AND v.version<9223372036854775807))
   THEN RAISE(ABORT,'annotation permission version is unavailable or exhausted') END;
 UPDATE annotation_permission_versions SET version=version+1
 WHERE annotation_id IN (%s) AND EXISTS(SELECT 1 FROM annotations a WHERE a.id=annotation_permission_versions.annotation_id);
END;`, name, event, table, ids, ids)
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	var malformed int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM annotation_permission_versions v LEFT JOIN annotations a ON a.id=v.annotation_id
WHERE a.id IS NULL OR typeof(v.version)!='integer' OR v.version<1 OR length(v.incarnation)!=64`).Scan(&malformed); err != nil {
		return err
	}
	if malformed != 0 {
		return ErrAnnotationPermissionFence
	}
	return tx.Commit()
}

// AnnotationMutationSelector supports the exact legacy mass-delete selections.
// ID and dashboard/panel selection are mutually exclusive; no LIMIT can turn a
// captured selection into an incomplete bulk authorization decision.
type AnnotationMutationSelector struct {
	ID           int64
	DashboardUID string
	PanelID      int64
}

func (q AnnotationMutationSelector) validate() error {
	if q.ID > 0 && q.DashboardUID == "" && q.PanelID == 0 {
		return nil
	}
	if q.ID == 0 && q.DashboardUID != "" && len(q.DashboardUID) <= 128 && q.PanelID > 0 {
		return nil
	}
	return fmt.Errorf("%w: select one annotation or a dashboard panel", model.ErrInvalidAnnotation)
}

// AnnotationPermissionOwner describes stored ownership, not a client-selected
// permission scope. DashboardID is the native resource ID. Organization-wide
// annotations have no fabricated dashboard or General-folder resource.
type AnnotationPermissionOwner struct {
	OrganizationID     int64
	OrganizationWide   bool
	DashboardID        string
	DashboardUID       string
	DashboardGrafanaID int64
}

type AnnotationMutationTarget struct {
	Annotation model.Annotation
	Owner      AnnotationPermissionOwner
	Existing   bool
}

type annotationPermissionSnapshot struct {
	target                          AnnotationMutationTarget
	payload, incarnation, key, hash string
	version                         int64
	tags                            []string
}

// AnnotationMutationFence is an opaque, request-local capability tied to a
// physical Store, credential/catalog actor, exact rows and owner mappings.
// Capturing it does not authorize an action. The service must check every
// target's annotation action (including real idempotency-retry targets).
// Catalog credentials are revalidated immediately before the organization
// writer; the two physical databases do not form a distributed transaction.
type AnnotationMutationFence struct {
	resource      PermissionMutationFence
	selector      AnnotationMutationSelector
	rows          []annotationPermissionSnapshot
	creation      *model.Annotation
	creationOwner AnnotationPermissionOwner
	key, hash     string
}

func clonePermissionAnnotation(a model.Annotation) model.Annotation {
	a.Tags = slices.Clone(a.Tags)
	a.Data = slices.Clone(a.Data)
	return a
}

func (f AnnotationMutationFence) Targets() []AnnotationMutationTarget {
	if f.resource.owner == nil {
		return nil
	}
	out := make([]AnnotationMutationTarget, 0, len(f.rows)+1)
	if f.creation != nil {
		out = append(out, AnnotationMutationTarget{Annotation: clonePermissionAnnotation(*f.creation), Owner: f.creationOwner})
	}
	for _, row := range f.rows {
		target := row.target
		target.Annotation = clonePermissionAnnotation(target.Annotation)
		out = append(out, target)
	}
	return out
}

func (f AnnotationMutationFence) Resources() []model.PermissionResource {
	out := slices.Collect(maps.Keys(f.resource.effective))
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].UID < out[j].UID
	})
	return out
}

func (f AnnotationMutationFence) Effective(ref model.PermissionResource) (model.EffectiveResourcePermission, error) {
	return f.resource.Effective(ref)
}

// annotationPermissionOwner resolves the exact external UID semantics used by
// Dashboard.UID, requiring a unique real owner. A stored allocated numeric ID
// must agree; the historic CRC32 ID is accepted only for that same unique UID.
func (s *Store) annotationPermissionOwner(ctx context.Context, q resourceQuery, a model.Annotation) (AnnotationPermissionOwner, error) {
	owner := AnnotationPermissionOwner{OrganizationID: s.OrganizationID()}
	if a.DashboardUID == "" {
		if a.DashboardID != 0 || a.PanelID != 0 {
			return owner, ErrAnnotationPermissionOwner
		}
		owner.OrganizationWide = true
		return owner, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT d.id,d.extras,i.id FROM dashboards d JOIN dashboard_identity i ON i.dashboard_id=d.id ORDER BY d.id`)
	if err != nil {
		return owner, err
	}
	defer rows.Close()
	var matches int
	for rows.Next() {
		var d model.Dashboard
		var raw string
		if err = rows.Scan(&d.ID, &raw, &d.GrafanaID); err != nil {
			return owner, err
		}
		var extra struct {
			Grafana json.RawMessage `json:"grafana"`
		}
		if err = json.Unmarshal([]byte(raw), &extra); err != nil {
			return owner, ErrAnnotationPermissionOwner
		}
		d.Grafana = extra.Grafana
		if len(d.Grafana) > 0 && string(d.Grafana) != "null" {
			var document map[string]json.RawMessage
			if json.Unmarshal(d.Grafana, &document) != nil || document == nil {
				return owner, ErrAnnotationPermissionOwner
			}
			if wrapped, ok := document["dashboard"]; ok {
				if json.Unmarshal(wrapped, &document) != nil || document == nil {
					return owner, ErrAnnotationPermissionOwner
				}
			}
		}
		if d.UID() != a.DashboardUID {
			continue
		}
		matches++
		owner.DashboardID, owner.DashboardUID, owner.DashboardGrafanaID = d.ID, a.DashboardUID, d.GrafanaID
	}
	if err = rows.Err(); err != nil {
		return owner, err
	}
	if matches != 1 || (a.DashboardID != 0 && a.DashboardID != owner.DashboardGrafanaID && a.DashboardID != int64(crc32.ChecksumIEEE([]byte(a.DashboardUID)))) {
		return owner, ErrAnnotationPermissionOwner
	}
	return owner, nil
}

func annotationManual(a model.Annotation, projected bool) bool {
	return !projected && a.AlertID == 0 && a.AlertUID == "" && a.AlertName == "" && a.PrevState == "" && a.NewState == ""
}

func (s *Store) annotationSnapshots(ctx context.Context, q resourceQuery, selector AnnotationMutationSelector, key string) ([]annotationPermissionSnapshot, error) {
	where, args := "a.id=?", []any{selector.ID}
	if key != "" {
		where, args = "a.idempotency_key=?", []any{key}
	} else if selector.ID == 0 {
		where, args = "a.dashboard_uid=? AND a.panel_id=?", []any{selector.DashboardUID, selector.PanelID}
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id,a.dashboard_uid,a.time,a.time_end,a.panel_id,a.user_id,a.payload,COALESCE(a.idempotency_key,''),a.request_hash,
v.incarnation,v.version,EXISTS(SELECT 1 FROM annotation_alerts x WHERE x.annotation_id=a.id)
FROM annotations a LEFT JOIN annotation_permission_versions v ON v.annotation_id=a.id WHERE `+where+` ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []annotationPermissionSnapshot{}
	for rows.Next() {
		var row annotationPermissionSnapshot
		var indexed model.Annotation
		var incarnation sql.NullString
		var version sql.NullInt64
		var projected bool
		if err = rows.Scan(&indexed.ID, &indexed.DashboardUID, &indexed.Time, &indexed.TimeEnd, &indexed.PanelID, &indexed.UserID, &row.payload, &row.key, &row.hash, &incarnation, &version, &projected); err != nil {
			rows.Close()
			return nil, err
		}
		if !incarnation.Valid || len(incarnation.String) != 64 || !version.Valid || version.Int64 < 1 {
			rows.Close()
			return nil, ErrAnnotationPermissionFence
		}
		row.incarnation, row.version = incarnation.String, version.Int64
		if err = json.Unmarshal([]byte(row.payload), &row.target.Annotation); err != nil {
			rows.Close()
			return nil, ErrAnnotationPermissionOwner
		}
		a := row.target.Annotation
		if !annotationManual(a, projected) || a.ID != indexed.ID || a.DashboardUID != indexed.DashboardUID || a.Time != indexed.Time || a.TimeEnd != indexed.TimeEnd || a.PanelID != indexed.PanelID || a.UserID != indexed.UserID {
			rows.Close()
			return nil, ErrAnnotationPermissionOwner
		}
		valid := clonePermissionAnnotation(a)
		if valid.Normalize() != nil {
			rows.Close()
			return nil, ErrAnnotationPermissionOwner
		}
		row.target.Existing = true
		out = append(out, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	type ownerKey struct {
		uid       string
		numericID int64
	}
	owners := map[ownerKey]AnnotationPermissionOwner{}
	for i := range out {
		a := out[i].target.Annotation
		key := ownerKey{a.DashboardUID, a.DashboardID}
		owner, exists := owners[key]
		if !exists {
			owner, err = s.annotationPermissionOwner(ctx, q, a)
			if err != nil {
				return nil, err
			}
			owners[key] = owner
		}
		out[i].target.Owner = owner
		tags, err := q.QueryContext(ctx, `SELECT tag FROM annotation_tags WHERE annotation_id=? ORDER BY tag`, out[i].target.Annotation.ID)
		if err != nil {
			return nil, err
		}
		for tags.Next() {
			var tag string
			if err = tags.Scan(&tag); err != nil {
				tags.Close()
				return nil, err
			}
			out[i].tags = append(out[i].tags, tag)
		}
		err = tags.Err()
		tags.Close()
		if err != nil {
			return nil, err
		}
		if !slices.Equal(out[i].tags, a.Tags) {
			return nil, ErrAnnotationPermissionOwner
		}
	}
	return out, nil
}

func (s *Store) annotationResourceRefs(owners ...AnnotationPermissionOwner) []model.PermissionResource {
	refs := []model.PermissionResource{}
	seen := map[model.PermissionResource]bool{}
	for _, owner := range owners {
		if owner.OrganizationWide {
			continue
		}
		ref := model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionDashboard, UID: owner.DashboardID}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	return refs
}

func (s *Store) CaptureAnnotationMutation(ctx context.Context, selector AnnotationMutationSelector, resolver PermissionIdentityResolver) (AnnotationMutationFence, error) {
	var f AnnotationMutationFence
	if err := selector.validate(); err != nil {
		return f, err
	}
	// Authentication precedes even the initial row lookup.
	actor, err := s.permissionActor(ctx, resolver)
	if err != nil {
		return f, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return f, err
	}
	rows, err := s.annotationSnapshots(ctx, tx, selector, "")
	if err != nil {
		tx.Rollback()
		return f, err
	}
	owners := []AnnotationPermissionOwner{}
	if selector.ID > 0 && len(rows) == 0 {
		tx.Rollback()
		return f, sql.ErrNoRows
	}
	for _, row := range rows {
		owners = append(owners, row.target.Owner)
	}
	if selector.ID == 0 {
		owner, e := s.annotationPermissionOwner(ctx, tx, model.Annotation{DashboardUID: selector.DashboardUID, PanelID: selector.PanelID})
		if e != nil {
			tx.Rollback()
			return f, e
		}
		owners = append(owners, owner)
	}
	if err = tx.Commit(); err != nil {
		return f, err
	}
	resource, err := s.CapturePermissionMutation(ctx, s.annotationResourceRefs(owners...), resolver)
	if err != nil {
		return f, err
	}
	if !samePermissionActor(actor, resource.actor) {
		return f, ErrAnnotationPermissionFence
	}
	f = AnnotationMutationFence{resource: resource, selector: selector, rows: rows}
	if err = s.validateAnnotationCapture(ctx, f); err != nil {
		return AnnotationMutationFence{}, err
	}
	return f, nil
}

func preparePermissionAnnotation(a model.Annotation, p identity.Principal) (model.Annotation, string, error) {
	a = clonePermissionAnnotation(a)
	if !annotationManual(a, false) {
		return a, "", ErrAnnotationPermissionOwner
	}
	a.ID, a.Created, a.Updated = 0, 0, 0
	a.UserID, a.Login = p.ID, p.Login
	if err := a.Normalize(); err != nil {
		return a, "", err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return a, "", err
	}
	digest := sha256.Sum256(raw)
	return a, hex.EncodeToString(digest[:]), nil
}

func (s *Store) CaptureAnnotationCreation(ctx context.Context, a model.Annotation, key string, resolver PermissionIdentityResolver) (AnnotationMutationFence, error) {
	var f AnnotationMutationFence
	a = clonePermissionAnnotation(a)
	if len(key) > 128 {
		return f, fmt.Errorf("%w: idempotency key exceeds 128 bytes", model.ErrInvalidAnnotation)
	}
	actor, err := s.permissionActor(ctx, resolver)
	if err != nil {
		return f, err
	}
	a, hash, err := preparePermissionAnnotation(a, actor.principal)
	if err != nil {
		return f, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return f, err
	}
	owner, err := s.annotationPermissionOwner(ctx, tx, a)
	if err != nil {
		tx.Rollback()
		return f, err
	}
	rows := []annotationPermissionSnapshot{}
	if key != "" {
		rows, err = s.annotationSnapshots(ctx, tx, AnnotationMutationSelector{}, key)
		if err != nil {
			tx.Rollback()
			return f, err
		}
		if len(rows) > 0 && rows[0].hash != hash {
			tx.Rollback()
			return f, ErrAnnotationConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return f, err
	}
	owners := []AnnotationPermissionOwner{owner}
	for _, row := range rows {
		owners = append(owners, row.target.Owner)
	}
	resource, err := s.CapturePermissionMutation(ctx, s.annotationResourceRefs(owners...), resolver)
	if err != nil {
		return f, err
	}
	if !samePermissionActor(actor, resource.actor) {
		return f, ErrAnnotationPermissionFence
	}
	f = AnnotationMutationFence{resource: resource, rows: rows, creation: &a, creationOwner: owner, key: key, hash: hash}
	if err = s.validateAnnotationCapture(ctx, f); err != nil {
		return AnnotationMutationFence{}, err
	}
	return f, nil
}

func sameAnnotationSnapshots(a, b []annotationPermissionSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].incarnation != b[i].incarnation || a[i].version != b[i].version || a[i].payload != b[i].payload || a[i].key != b[i].key || a[i].hash != b[i].hash || a[i].target.Owner != b[i].target.Owner || !slices.Equal(a[i].tags, b[i].tags) {
			return false
		}
	}
	return true
}

func (s *Store) checkAnnotationFenceTx(ctx context.Context, tx *sql.Tx, f AnnotationMutationFence) error {
	if err := s.checkPermissionFenceTx(ctx, tx, f.resource); err != nil {
		return err
	}
	if f.creation != nil {
		owner, err := s.annotationPermissionOwner(ctx, tx, *f.creation)
		if err != nil || owner != f.creationOwner {
			return ErrAnnotationPermissionFence
		}
		if f.key == "" {
			return nil
		}
	} else if f.selector.ID == 0 {
		owner, err := s.annotationPermissionOwner(ctx, tx, model.Annotation{DashboardUID: f.selector.DashboardUID, PanelID: f.selector.PanelID})
		if err != nil {
			return ErrAnnotationPermissionFence
		}
		if err = s.requirePermissionRef(f.resource, model.PermissionDashboard, owner.DashboardID); err != nil {
			return err
		}
	}
	rows, err := s.annotationSnapshots(ctx, tx, f.selector, f.key)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrAnnotationPermissionFence
	}
	if !sameAnnotationSnapshots(f.rows, rows) {
		return ErrAnnotationPermissionFence
	}
	return nil
}

func (s *Store) validateAnnotationCapture(ctx context.Context, f AnnotationMutationFence) error {
	if err := s.validatePermissionFence(ctx, f.resource); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.checkAnnotationFenceTx(ctx, tx, f)
}

func (s *Store) beginAnnotationMutation(ctx context.Context, f AnnotationMutationFence) (*sql.Tx, error) {
	if err := s.validatePermissionFence(ctx, f.resource); err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = s.checkAnnotationFenceTx(ctx, tx, f); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) CreateAnnotationWithPermissions(ctx context.Context, a model.Annotation, key string, f AnnotationMutationFence) (model.Annotation, error) {
	if f.creation == nil || key != f.key || f.resource.owner != s {
		return model.Annotation{}, ErrAnnotationPermissionFence
	}
	a, hash, err := preparePermissionAnnotation(a, f.resource.actor.principal)
	if err != nil {
		return model.Annotation{}, err
	}
	if hash != f.hash {
		return model.Annotation{}, ErrAnnotationPermissionFence
	}
	tx, err := s.beginAnnotationMutation(ctx, f)
	if err != nil {
		return model.Annotation{}, err
	}
	defer tx.Rollback()
	if len(f.rows) > 0 {
		return clonePermissionAnnotation(f.rows[0].target.Annotation), nil
	}
	a, err = insertAnnotation(ctx, tx, a, key, hash)
	if err != nil {
		return model.Annotation{}, err
	}
	return a, tx.Commit()
}

func copyAnnotationPatch(p model.AnnotationPatch) model.AnnotationPatch {
	if p.Time != nil {
		value := *p.Time
		p.Time = &value
	}
	if p.TimeEnd != nil {
		value := *p.TimeEnd
		p.TimeEnd = &value
	}
	if p.Text != nil {
		value := *p.Text
		p.Text = &value
	}
	if p.Tags != nil {
		value := slices.Clone(*p.Tags)
		p.Tags = &value
	}
	p.Data = slices.Clone(p.Data)
	return p
}

func (s *Store) PatchAnnotationWithPermissions(ctx context.Context, id int64, p model.AnnotationPatch, f AnnotationMutationFence) (model.Annotation, error) {
	p = copyAnnotationPatch(p)
	if f.creation != nil || f.selector.ID != id || id <= 0 || len(f.rows) != 1 {
		return model.Annotation{}, ErrAnnotationPermissionFence
	}
	tx, err := s.beginAnnotationMutation(ctx, f)
	if err != nil {
		return model.Annotation{}, err
	}
	defer tx.Rollback()
	a, err := patchAnnotationTx(ctx, tx, id, p)
	if err != nil {
		return model.Annotation{}, err
	}
	return a, tx.Commit()
}

func (s *Store) DeleteAnnotationWithPermissions(ctx context.Context, id int64, f AnnotationMutationFence) error {
	if f.creation != nil || f.selector.ID != id || id <= 0 || len(f.rows) != 1 {
		return ErrAnnotationPermissionFence
	}
	tx, err := s.beginAnnotationMutation(ctx, f)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM annotations WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeletePanelAnnotationsWithPermissions(ctx context.Context, uid string, panelID int64, f AnnotationMutationFence) error {
	if f.creation != nil || f.selector.ID != 0 || f.selector.DashboardUID != uid || f.selector.PanelID != panelID || uid == "" || panelID <= 0 {
		return ErrAnnotationPermissionFence
	}
	tx, err := s.beginAnnotationMutation(ctx, f)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM annotations WHERE dashboard_uid=? AND panel_id=?`, uid, panelID); err != nil {
		return err
	}
	return tx.Commit()
}
