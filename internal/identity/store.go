package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrVersionConflict          = errors.New("organization version conflict")
	ErrInvalidOrganizationState = errors.New("invalid organization state transition")
	ErrPrimaryOrganization      = errors.New("the primary organization cannot be disabled or deleted")
	ErrOrganizationName         = errors.New("organization name already exists")
)

// Store is the instance identity catalog. Organization data and credentials are
// stored separately; this catalog never opens an organization's control store.
type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, errors.New("a persistent identity catalog path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initializeCatalog(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: abs}, nil
}

func initializeCatalog(db *sql.DB) error {
	var owned int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='identity_schema'`).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		var tables int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("the identity catalog must not reuse an existing organization or unrelated database")
		}
	} else {
		var version int
		if err := db.QueryRow(`SELECT version FROM identity_schema WHERE id=1`).Scan(&version); err != nil || (version < 1 || version > 6) {
			return errors.New("unsupported or incomplete identity catalog schema")
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS identity_schema(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS organizations(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,name_key TEXT NOT NULL UNIQUE,state TEXT NOT NULL CHECK(state IN ('creating','active','disabled','deleting')),version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);`); err != nil {
		return err
	}
	if err := initializeAccounts(tx); err != nil {
		return err
	}
	if err := initializeSessionRotation(tx); err != nil {
		return err
	}
	if err := initializeMembershipGenerations(tx); err != nil {
		return err
	}
	if err := initializeIdentityMetadata(tx); err != nil {
		return err
	}
	if owned == 0 {
		now := time.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO organizations(id,name,name_key,state,version,created_at,updated_at) VALUES(1,'Main Org.','main org.','active',1,?,?); INSERT INTO identity_schema(id,version) VALUES(1,6);`, now, now); err != nil {
			return err
		}
	} else {
		var state string
		if err := tx.QueryRow(`SELECT state FROM organizations WHERE id=1`).Scan(&state); err != nil || state != string(OrganizationActive) {
			return errors.New("identity catalog primary organization is missing or inactive")
		}
		if _, err := tx.Exec(`UPDATE identity_schema SET version=6 WHERE id=1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

func organizationName(name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", errors.New("organization name must contain 1 to 256 bytes without control characters")
	}
	return name, strings.ToLower(name), nil
}

func scanOrganization(row interface{ Scan(...any) error }) (Organization, error) {
	var org Organization
	err := row.Scan(&org.ID, &org.Name, &org.State, &org.Version, &org.CreatedAt, &org.UpdatedAt)
	return org, err
}

const organizationColumns = `id,name,state,version,created_at,updated_at`

func (s *Store) Organization(ctx context.Context, id int64) (Organization, error) {
	if id <= 0 {
		return Organization{}, sql.ErrNoRows
	}
	return scanOrganization(s.db.QueryRowContext(ctx, `SELECT `+organizationColumns+` FROM organizations WHERE id=?`, id))
}

func (s *Store) Organizations(ctx context.Context) ([]Organization, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+organizationColumns+` FROM organizations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orgs := []Organization{}
	for rows.Next() {
		org, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

// BeginOrganization persists the recovery marker before creating any data files.
// Only ActivateOrganization makes a fully initialized organization routable.
func (s *Store) BeginOrganization(ctx context.Context, name string) (Organization, error) {
	name, key, err := organizationName(name)
	if err != nil {
		return Organization{}, err
	}
	now := time.Now().UnixMilli()
	org, err := scanOrganization(s.db.QueryRowContext(ctx, `INSERT INTO organizations(name,name_key,state,version,created_at,updated_at) VALUES(?,?,'creating',1,?,?) RETURNING `+organizationColumns, name, key, now, now))
	return org, catalogWriteError(err)
}

func catalogWriteError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: organizations.name_key") {
		return ErrOrganizationName
	}
	return err
}

func (s *Store) ActivateOrganization(ctx context.Context, id, version int64) (Organization, error) {
	return s.changeOrganization(ctx, id, version, OrganizationActive, true)
}

func (s *Store) SetOrganizationState(ctx context.Context, id, version int64, state OrganizationState) (Organization, error) {
	return s.changeOrganization(ctx, id, version, state, false)
}

func (s *Store) changeOrganization(ctx context.Context, id, version int64, state OrganizationState, initialize bool) (Organization, error) {
	if version <= 0 {
		return Organization{}, ErrVersionConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Organization{}, err
	}
	defer tx.Rollback()
	org, err := scanOrganization(tx.QueryRowContext(ctx, `SELECT `+organizationColumns+` FROM organizations WHERE id=?`, id))
	if err != nil {
		return Organization{}, err
	}
	if org.Version != version {
		return Organization{}, ErrVersionConflict
	}
	if id == 1 && state != OrganizationActive {
		return Organization{}, ErrPrimaryOrganization
	}
	allowed := initialize && org.State == OrganizationCreating && state == OrganizationActive
	if !initialize {
		switch org.State {
		case OrganizationCreating:
			allowed = state == OrganizationDeleting
		case OrganizationActive:
			allowed = state == OrganizationDisabled || state == OrganizationDeleting
		case OrganizationDisabled:
			allowed = state == OrganizationActive || state == OrganizationDeleting
		}
	}
	if !allowed {
		return Organization{}, ErrInvalidOrganizationState
	}
	if org.State == OrganizationActive && state != OrganizationActive {
		if err := preserveAvailableInstanceAdmin(ctx, tx, id, 0); err != nil {
			return Organization{}, err
		}
	}
	updated := max(time.Now().UnixMilli(), org.UpdatedAt+1)
	org, err = scanOrganization(tx.QueryRowContext(ctx, `UPDATE organizations SET state=?,version=version+1,updated_at=? WHERE id=? AND version=? RETURNING `+organizationColumns, state, updated, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return Organization{}, ErrVersionConflict
	}
	if err != nil {
		return Organization{}, err
	}
	if err := tx.Commit(); err != nil {
		return Organization{}, err
	}
	return org, nil
}

func (s *Store) RenameOrganization(ctx context.Context, id, version int64, name string) (Organization, error) {
	name, key, err := organizationName(name)
	if err != nil {
		return Organization{}, err
	}
	if version <= 0 {
		return Organization{}, ErrVersionConflict
	}
	org, err := scanOrganization(s.db.QueryRowContext(ctx, `UPDATE organizations SET name=?,name_key=?,version=version+1,updated_at=max(?,updated_at+1) WHERE id=? AND version=? AND state!='deleting' RETURNING `+organizationColumns, name, key, time.Now().UnixMilli(), id, version))
	if !errors.Is(err, sql.ErrNoRows) {
		return org, catalogWriteError(err)
	}
	current, lookupErr := s.Organization(ctx, id)
	if lookupErr != nil {
		return Organization{}, lookupErr
	}
	if current.Version != version {
		return Organization{}, ErrVersionConflict
	}
	return Organization{}, fmt.Errorf("%w: deleting organizations cannot be renamed", ErrInvalidOrganizationState)
}
