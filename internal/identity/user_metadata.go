package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
)

func newUserUID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "u_" + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func validUserUID(uid string) bool {
	if len(uid) == 0 || len(uid) > 40 {
		return false
	}
	for _, char := range uid {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func metadataTableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	if table != "users" && table != "browser_sessions" {
		return nil, errors.New("invalid identity metadata table")
	}
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// The catalog calls this initializer in its schema transaction. Existing
// nonempty identities are never rewritten; invalid/duplicate UIDs abort the
// migration. Adding UIDs does not change IDs, revisions, profiles or credentials.
func initializeIdentityMetadata(tx *sql.Tx) error {
	columns, err := metadataTableColumns(tx, "users")
	if err != nil {
		return err
	}
	if !columns["uid"] {
		if _, err := tx.Exec(`ALTER TABLE users ADD COLUMN uid TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// Bound migration memory and close the read cursor before writing its batch.
	var after int64
	for {
		rows, err := tx.Query(`SELECT id,uid FROM users WHERE id>? ORDER BY id LIMIT 1000`, after)
		if err != nil {
			return err
		}
		var missing []int64
		count := 0
		for rows.Next() {
			var id int64
			var uid string
			if err := rows.Scan(&id, &uid); err != nil {
				rows.Close()
				return err
			}
			if uid != "" && !validUserUID(uid) {
				rows.Close()
				return errors.New("invalid existing human user UID")
			}
			if uid == "" {
				missing = append(missing, id)
			}
			after = id
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range missing {
			uid, err := availableUserUID(tx)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE users SET uid=? WHERE id=? AND uid=''`, uid, id); err != nil {
				return err
			}
		}
		if count < 1000 {
			break
		}
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_uid ON users(uid);
CREATE TRIGGER IF NOT EXISTS users_uid_required BEFORE INSERT ON users
WHEN NEW.uid IS NULL OR length(NEW.uid)<1 OR length(NEW.uid)>40 OR NEW.uid GLOB '*[^a-zA-Z0-9_-]*'
BEGIN SELECT RAISE(ABORT,'valid human user UID required'); END;
CREATE TRIGGER IF NOT EXISTS users_uid_immutable BEFORE UPDATE OF uid ON users
WHEN NEW.uid IS NOT OLD.uid
BEGIN SELECT RAISE(ABORT,'human user UID is immutable'); END;`); err != nil {
		return err
	}
	return initializeSessionMetadata(tx)
}

func availableUserUID(tx *sql.Tx) (string, error) {
	for range 5 {
		uid, err := newUserUID()
		if err != nil {
			return "", err
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM users WHERE uid=?`, uid).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return uid, nil
		}
	}
	return "", errors.New("unable to allocate a unique human user UID")
}

// This is a global human lookup for an authorized adapter, not a credential or
// a service-account alias. Resource handlers still need organization access.
func (s *Store) UserByUID(ctx context.Context, uid string) (User, error) {
	if !validUserUID(uid) {
		return User{}, sql.ErrNoRows
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE uid=?`, uid))
}
