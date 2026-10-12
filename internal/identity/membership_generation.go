package identity

import (
	"context"
	"database/sql"
	"errors"
)

// The ledger outlives membership removal. A delayed organization-local grant
// must refer to the old incarnation, never to a subsequently rejoined user.
func initializeMembershipGenerations(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(organization_memberships)`)
	if err != nil {
		return err
	}
	hasGeneration := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		hasGeneration = hasGeneration || name == "generation"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasGeneration {
		if _, err := tx.Exec(`ALTER TABLE organization_memberships ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK(generation>0)`); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS membership_generations(
org_id INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
generation INTEGER NOT NULL CHECK(typeof(generation)='integer' AND generation>0),
last_version INTEGER NOT NULL DEFAULT 0 CHECK(typeof(last_version)='integer' AND last_version>=0),
PRIMARY KEY(org_id,user_id));`)
	if err != nil {
		return err
	}
	rows, err = tx.Query(`PRAGMA table_info(membership_generations)`)
	if err != nil {
		return err
	}
	hasLastVersion := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		hasLastVersion = hasLastVersion || name == "last_version"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasLastVersion {
		if _, err := tx.Exec(`ALTER TABLE membership_generations ADD COLUMN last_version INTEGER NOT NULL DEFAULT 0 CHECK(typeof(last_version)='integer' AND last_version>=0)`); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO membership_generations(org_id,user_id,generation,last_version)
SELECT org_id,user_id,generation,version FROM organization_memberships;
UPDATE membership_generations SET last_version=max(last_version,coalesce((SELECT m.version FROM organization_memberships m WHERE m.org_id=membership_generations.org_id AND m.user_id=membership_generations.user_id),last_version));`)
	return err
}

func nextMembershipGeneration(ctx context.Context, tx *sql.Tx, orgID, userID int64) (int64, error) {
	if orgID <= 0 || userID <= 0 {
		return 0, errors.New("positive organization and human user IDs required")
	}
	var generation int64
	err := tx.QueryRowContext(ctx, `INSERT INTO membership_generations(org_id,user_id,generation)
VALUES(?,?,1) ON CONFLICT(org_id,user_id) DO UPDATE SET generation=generation+1
WHERE generation<9223372036854775807 RETURNING generation`, orgID, userID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrVersionConflict
	}
	return generation, err
}

func nextMembershipVersion(ctx context.Context, tx *sql.Tx, orgID, userID int64) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `UPDATE membership_generations SET last_version=last_version+1
WHERE org_id=? AND user_id=? AND last_version<9223372036854775807 RETURNING last_version`, orgID, userID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrVersionConflict
	}
	return version, err
}

// Record the highest content revision before deleting its membership row.
// Rejoin then starts above it, so version-only CAS and cleanup jobs also avoid
// ABA even though content updates do not change authorization generation.
func rememberMembershipVersion(ctx context.Context, tx *sql.Tx, orgID, userID, version int64) error {
	if version <= 0 {
		return ErrVersionConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE membership_generations SET last_version=max(last_version,?) WHERE org_id=? AND user_id=?`, version, orgID, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrVersionConflict
	}
	return err
}
