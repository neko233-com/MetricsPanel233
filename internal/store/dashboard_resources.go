package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

var ErrResourceConflict = errors.New("resource version changed; reload before saving")
var ErrResourceExists = errors.New("resource with this name or UID already exists")
var ErrLibraryConnected = errors.New("library panel is used by a dashboard; unlink it before deleting")
var ErrFolderAlertRules = errors.New("folder contains alert rules; forceDeleteRules=true is required")

type resourceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type rowScanner interface{ Scan(...any) error }

func scanFolder(row rowScanner) (model.Folder, error) {
	var f model.Folder
	var created, updated int64
	err := row.Scan(&f.ID, &f.UID, &f.Title, &f.ParentUID, &f.Description, &f.Version, &created, &updated)
	f.Created, f.Updated = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	f.URL = "/dashboards/f/" + f.UID
	f.CanSave, f.CanEdit, f.CanAdmin, f.CanDelete = true, true, true, true
	return f, err
}

const folderColumns = `id,uid,title,parent_uid,description,version,created,updated`

func folderByUID(ctx context.Context, q resourceQuery, uid string) (model.Folder, error) {
	uid = model.RootFolderUID(uid)
	if uid == "" {
		return model.Folder{Title: "General", URL: "/dashboards", CanSave: true, CanEdit: true}, nil
	}
	return scanFolder(q.QueryRowContext(ctx, `SELECT `+folderColumns+` FROM folders WHERE uid=?`, uid))
}
func (s *Store) Folder(ctx context.Context, uid string) (model.Folder, error) {
	return folderByUID(ctx, s.DB, uid)
}
func (s *Store) FolderByID(ctx context.Context, id int64) (model.Folder, error) {
	if id == 0 {
		return s.Folder(ctx, "")
	}
	return scanFolder(s.DB.QueryRowContext(ctx, `SELECT `+folderColumns+` FROM folders WHERE id=?`, id))
}
func (s *Store) Folders(ctx context.Context) ([]model.Folder, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+folderColumns+` FROM folders ORDER BY title COLLATE NOCASE,uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Folder{}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func resourceConstraint(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrResourceExists
	}
	return err
}
func validateFolderParent(ctx context.Context, q resourceQuery, uid, parent string) error {
	seen := map[string]bool{uid: true}
	for parent != "" {
		if seen[parent] {
			return errors.New("a folder cannot be moved inside itself or one of its descendants")
		}
		seen[parent] = true
		f, err := folderByUID(ctx, q, parent)
		if err != nil {
			return err
		}
		parent = f.ParentUID
	}
	return nil
}
func (s *Store) CreateFolder(ctx context.Context, f model.Folder) (model.Folder, error) {
	if f.UID == "" {
		f.UID = uuid.NewString()
	}
	f.ParentUID = model.RootFolderUID(f.ParentUID)
	if err := f.Validate(); err != nil {
		return f, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	if err = validateFolderParent(ctx, tx, f.UID, f.ParentUID); err != nil {
		return f, err
	}
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO folders(uid,title,parent_uid,description,version,created,updated) VALUES(?,?,?,?,1,?,?)`, f.UID, f.Title, f.ParentUID, f.Description, now, now)
	if err != nil {
		return f, resourceConstraint(err)
	}
	f, err = folderByUID(ctx, tx, f.UID)
	if err != nil {
		return f, err
	}
	return f, tx.Commit()
}
func (s *Store) UpdateFolder(ctx context.Context, f model.Folder, overwrite bool) (model.Folder, error) {
	if err := f.Validate(); err != nil {
		return f, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	old, err := folderByUID(ctx, tx, f.UID)
	if err != nil {
		return f, err
	}
	if !overwrite && f.Version != old.Version {
		return f, ErrResourceConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE folders SET title=?,description=?,version=version+1,updated=MAX(?,updated+1) WHERE uid=?`, f.Title, f.Description, time.Now().UnixMilli(), f.UID)
	if err != nil {
		return f, resourceConstraint(err)
	}
	f, err = folderByUID(ctx, tx, f.UID)
	if err != nil {
		return f, err
	}
	return f, tx.Commit()
}
func (s *Store) MoveFolder(ctx context.Context, uid, parent string) (model.Folder, error) {
	parent = model.RootFolderUID(parent)
	if model.RootFolderUID(uid) == "" {
		return model.Folder{}, errors.New("General cannot be moved")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Folder{}, err
	}
	defer tx.Rollback()
	f, err := folderByUID(ctx, tx, uid)
	if err != nil {
		return f, err
	}
	if err = validateFolderParent(ctx, tx, uid, parent); err != nil {
		return f, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE folders SET parent_uid=?,version=version+1,updated=MAX(?,updated+1) WHERE uid=?`, parent, time.Now().UnixMilli(), uid)
	if err != nil {
		return f, resourceConstraint(err)
	}
	f, err = folderByUID(ctx, tx, uid)
	if err != nil {
		return f, err
	}
	return f, tx.Commit()
}
func (s *Store) DeleteFolder(ctx context.Context, uid string, forceDeleteRules bool) error {
	return s.DeleteFolderWithRuleGate(ctx, uid, forceDeleteRules, nil)
}

// DeleteFolderWithRuleGate lets the alert scheduler stop affected evaluations
// inside the same database transaction used to determine folder descendants.
func (s *Store) DeleteFolderWithRuleGate(ctx context.Context, uid string, forceDeleteRules bool, gate func([]string) (func(), error)) error {
	if model.RootFolderUID(uid) == "" {
		return errors.New("General cannot be deleted")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = folderByUID(ctx, tx, uid); err != nil {
		return err
	}
	const descendants = `WITH RECURSIVE tree(uid) AS (SELECT uid FROM folders WHERE uid=? UNION ALL SELECT f.uid FROM folders f JOIN tree ON f.parent_uid=tree.uid) `
	var count int
	if err = tx.QueryRowContext(ctx, descendants+`SELECT count(*) FROM dashboards WHERE id='system' AND json_extract(extras,'$.folder_uid') IN (SELECT uid FROM tree)`, uid).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("folder contains the system dashboard; move it to General before deleting the folder")
	}
	if err = tx.QueryRowContext(ctx, descendants+`SELECT count(*) FROM library_panel_connections c JOIN library_elements e ON e.uid=c.library_uid WHERE e.folder_uid IN (SELECT uid FROM tree)`, uid).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrLibraryConnected
	}
	if err = tx.QueryRowContext(ctx, descendants+`SELECT count(*) FROM alert_rules WHERE json_extract(config,'$.folder_uid') IN (SELECT uid FROM tree)`, uid).Scan(&count); err != nil {
		return err
	}
	if count > 0 && !forceDeleteRules {
		return ErrFolderAlertRules
	}
	if gate != nil {
		rows, queryErr := tx.QueryContext(ctx, descendants+`SELECT uid FROM alert_rules WHERE json_extract(config,'$.folder_uid') IN (SELECT uid FROM tree)`, uid)
		if queryErr != nil {
			return queryErr
		}
		uids := []string{}
		for rows.Next() {
			var ruleUID string
			if err = rows.Scan(&ruleUID); err != nil {
				rows.Close()
				return err
			}
			uids = append(uids, ruleUID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		release, gateErr := gate(uids)
		if gateErr != nil {
			return gateErr
		}
		defer release()
	}
	for _, statement := range []string{
		`DELETE FROM alert_rules WHERE json_extract(config,'$.folder_uid') IN (SELECT uid FROM tree)`,
		`DELETE FROM dashboards WHERE json_extract(extras,'$.folder_uid') IN (SELECT uid FROM tree)`,
		`DELETE FROM library_elements WHERE folder_uid IN (SELECT uid FROM tree)`,
		`DELETE FROM folders WHERE uid IN (SELECT uid FROM tree)`,
	} {
		if _, err = tx.ExecContext(ctx, descendants+statement, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DashboardByGrafanaID(ctx context.Context, id int64) (model.Dashboard, error) {
	dashboards, err := s.Dashboards(ctx)
	if err != nil {
		return model.Dashboard{}, err
	}
	for _, d := range dashboards {
		if d.GrafanaID == id {
			return d, nil
		}
	}
	return model.Dashboard{}, sql.ErrNoRows
}
func (s *Store) Dashboard(ctx context.Context, id string) (model.Dashboard, error) {
	dashboards, err := s.Dashboards(ctx)
	if err != nil {
		return model.Dashboard{}, err
	}
	for _, d := range dashboards {
		if d.ID == id {
			return d, nil
		}
	}
	return model.Dashboard{}, sql.ErrNoRows
}

const libraryColumns = `e.id,e.uid,e.name,e.kind,e.folder_uid,e.version,e.model,e.created,e.updated,COALESCE(f.id,0),COALESCE(f.title,'General'),(SELECT count(*) FROM library_panel_connections c WHERE c.library_uid=e.uid)`

func scanLibrary(row rowScanner) (model.LibraryElement, error) {
	var e model.LibraryElement
	var raw string
	var created, updated int64
	err := row.Scan(&e.ID, &e.UID, &e.Name, &e.Kind, &e.FolderUID, &e.Version, &raw, &created, &updated, &e.FolderID, &e.Meta.FolderName, &e.Meta.ConnectedDashboards)
	e.Model = json.RawMessage(raw)
	e.OrgID = 1
	e.Meta.FolderUID = e.FolderUID
	e.Meta.Created, e.Meta.Updated = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	e.Meta.CreatedBy, e.Meta.UpdatedBy = model.LibraryElementUser{ID: 1, Name: "admin"}, model.LibraryElementUser{ID: 1, Name: "admin"}
	var m struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(e.Model, &m)
	e.Type, e.Description = m.Type, m.Description
	return e, err
}
func libraryByUID(ctx context.Context, q resourceQuery, uid string) (model.LibraryElement, error) {
	return scanLibrary(q.QueryRowContext(ctx, `SELECT `+libraryColumns+` FROM library_elements e LEFT JOIN folders f ON f.uid=e.folder_uid WHERE e.uid=?`, uid))
}
func (s *Store) LibraryElement(ctx context.Context, uid string) (model.LibraryElement, error) {
	return libraryByUID(ctx, s.DB, uid)
}
func (s *Store) LibraryElements(ctx context.Context) ([]model.LibraryElement, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+libraryColumns+` FROM library_elements e LEFT JOIN folders f ON f.uid=e.folder_uid ORDER BY e.name COLLATE NOCASE,e.uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.LibraryElement{}
	for rows.Next() {
		e, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func saveLibraryTx(ctx context.Context, tx *sql.Tx, e model.LibraryElement, create bool) (model.LibraryElement, error) {
	if e.UID == "" {
		e.UID = uuid.NewString()
	}
	e.FolderUID = model.RootFolderUID(e.FolderUID)
	if err := e.Validate(); err != nil {
		return e, err
	}
	if _, err := folderByUID(ctx, tx, e.FolderUID); err != nil {
		return e, err
	}
	now := time.Now().UnixMilli()
	if create {
		_, err := tx.ExecContext(ctx, `INSERT INTO library_elements(uid,name,kind,folder_uid,version,model,created,updated) VALUES(?,?,?,?,1,?,?,?)`, e.UID, e.Name, e.Kind, e.FolderUID, string(e.Model), now, now)
		if err != nil {
			return e, resourceConstraint(err)
		}
	} else {
		old, err := libraryByUID(ctx, tx, e.UID)
		if err != nil {
			return e, err
		}
		if old.Version != e.Version {
			return e, ErrResourceConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE library_elements SET name=?,kind=?,folder_uid=?,model=?,version=version+1,updated=MAX(?,updated+1) WHERE uid=?`, e.Name, e.Kind, e.FolderUID, string(e.Model), now, e.UID)
		if err != nil {
			return e, resourceConstraint(err)
		}
	}
	return libraryByUID(ctx, tx, e.UID)
}
func (s *Store) SaveLibraryElement(ctx context.Context, e model.LibraryElement, create bool) (model.LibraryElement, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	e, err = saveLibraryTx(ctx, tx, e, create)
	if err != nil {
		return e, err
	}
	return e, tx.Commit()
}
func (s *Store) DeleteLibraryElement(ctx context.Context, uid string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e, err := libraryByUID(ctx, tx, uid)
	if err != nil {
		return err
	}
	if e.Meta.ConnectedDashboards > 0 {
		return ErrLibraryConnected
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM library_elements WHERE uid=?`, uid); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) LibraryConnections(ctx context.Context, uid string) ([]model.LibraryConnection, error) {
	e, err := s.LibraryElement(ctx, uid)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id,i.id,d.id,d.name,c.created,d.extras FROM library_panel_connections c JOIN dashboards d ON d.id=c.dashboard_id JOIN dashboard_identity i ON i.dashboard_id=d.id WHERE c.library_uid=? ORDER BY d.name,d.id`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.LibraryConnection{}
	for rows.Next() {
		var c model.LibraryConnection
		var created int64
		var extras string
		if err = rows.Scan(&c.ID, &c.ConnectionID, &c.ConnectionUID, &c.Name, &created, &extras); err != nil {
			return nil, err
		}
		c.Kind = 1
		c.ElementID = e.ID
		c.DashboardUID = c.ConnectionUID
		c.DashboardID = c.ConnectionID
		var source struct {
			Grafana json.RawMessage `json:"grafana"`
		}
		_ = json.Unmarshal([]byte(extras), &source)
		c.ConnectionUID = (model.Dashboard{ID: c.ConnectionUID, Grafana: source.Grafana}).UID()
		c.URL = "/d/" + c.ConnectionUID
		c.Created = time.UnixMilli(created).UTC()
		c.CreatedBy = model.LibraryElementUser{ID: 1, Name: "admin"}
		out = append(out, c)
	}
	return out, rows.Err()
}

func libraryUIDs(raw json.RawMessage) []string {
	seen := map[string]bool{}
	var walk func(json.RawMessage)
	walk = func(value json.RawMessage) {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) == nil && object != nil {
			if ref := object["libraryPanel"]; len(ref) > 0 {
				var link struct {
					UID string `json:"uid"`
				}
				if json.Unmarshal(ref, &link) == nil && link.UID != "" {
					seen[link.UID] = true
				}
			}
			var kind string
			_ = json.Unmarshal(object["kind"], &kind)
			if kind == "LibraryPanelKind" {
				var ref struct {
					LibraryPanel struct {
						UID string `json:"uid"`
					} `json:"libraryPanel"`
					UID string `json:"uid"`
				}
				if json.Unmarshal(object["spec"], &ref) == nil {
					uid := ref.LibraryPanel.UID
					if uid == "" {
						uid = ref.UID
					}
					if uid != "" {
						seen[uid] = true
					}
				}
			}
			for key, child := range object {
				if key != "__elements" {
					walk(child)
				}
			}
			return
		}
		var array []json.RawMessage
		if json.Unmarshal(value, &array) == nil {
			for _, child := range array {
				walk(child)
			}
		}
	}
	walk(raw)
	out := []string{}
	for uid := range seen {
		out = append(out, uid)
	}
	sort.Strings(out)
	return out
}
func syncLibraryConnections(ctx context.Context, tx *sql.Tx, d model.Dashboard) error {
	seen := map[string]bool{}
	for _, uid := range libraryUIDs(d.Grafana) {
		seen[uid] = true
	}
	for _, panel := range d.Panels {
		for _, uid := range libraryUIDs(panel.Config) {
			seen[uid] = true
		}
	}
	for uid := range seen {
		if _, err := libraryByUID(ctx, tx, uid); err != nil {
			return fmt.Errorf("library panel %s: %w", uid, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT library_uid FROM library_panel_connections WHERE dashboard_id=?`, d.ID)
	if err != nil {
		return err
	}
	remove := []string{}
	for rows.Next() {
		var uid string
		if err = rows.Scan(&uid); err != nil {
			rows.Close()
			return err
		}
		if !seen[uid] {
			remove = append(remove, uid)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, uid := range remove {
		if _, err = tx.ExecContext(ctx, `DELETE FROM library_panel_connections WHERE dashboard_id=? AND library_uid=?`, d.ID, uid); err != nil {
			return err
		}
	}
	for uid := range seen {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO library_panel_connections(library_uid,dashboard_id,created) VALUES(?,?,?)`, uid, d.ID, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// SaveDashboardWithLibraryElements commits imported library definitions and their
// dashboard together. A failed version check never leaves unused definitions.
func (s *Store) SaveDashboardWithLibraryElements(ctx context.Context, d model.Dashboard, elements []model.LibraryElement, revision *int64) (model.Dashboard, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	for _, e := range elements {
		_, lookup := libraryByUID(ctx, tx, e.UID)
		if errors.Is(lookup, sql.ErrNoRows) {
			if _, err = saveLibraryTx(ctx, tx, e, true); err != nil {
				return d, err
			}
		} else if lookup != nil {
			return d, lookup
		}
	}
	d, err = saveDashboardTx(ctx, tx, d, revision)
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}
