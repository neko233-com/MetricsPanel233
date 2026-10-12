package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrSessionNeedsRotation = errors.New("session credential rotation required")

const urgentSessionRotation = time.Minute
const skipSessionRotation = 5 * time.Second

func initializeSessionRotation(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(browser_sessions)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{
		{"previous_token_hash", "previous_token_hash BLOB"},
		{"rotated_at", "rotated_at INTEGER NOT NULL DEFAULT 0"},
		{"token_seen", "token_seen INTEGER NOT NULL DEFAULT 1 CHECK(token_seen IN (0,1))"},
	} {
		if !columns[column.name] {
			if _, err := tx.Exec(`ALTER TABLE browser_sessions ADD COLUMN ` + column.definition); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`UPDATE browser_sessions SET rotated_at=created_at WHERE rotated_at=0;
UPDATE browser_sessions SET previous_token_hash=token_hash WHERE previous_token_hash IS NULL`)
	return err
}

// Rotation state follows the tagged OSS token's seen/unseen scheduling. Native
// absolute and idle expiry remain independently enforced and are not extended.
func (session Session) NeedsRotationAt(now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if !session.TokenSeen {
		interval = urgentSessionRotation
	}
	return time.UnixMilli(session.RotatedAt).Before(now.Add(-interval))
}

func (session Session) NextRotation(interval time.Duration) time.Time {
	return time.UnixMilli(session.RotatedAt).Add(interval - skipSessionRotation)
}

func lookupSessionCredential(ctx context.Context, tx *sql.Tx, digest []byte, now int64) (Session, error) {
	var session Session
	var current bool
	err := tx.QueryRowContext(ctx, `SELECT `+sessionColumns+`,token_hash=? FROM browser_sessions
WHERE (token_hash=? OR previous_token_hash=?) AND revoked_at=0 AND expires_at>? AND last_seen+idle_timeout_ms>?`, digest, digest, digest, now, now).
		Scan(&session.ID, &session.UserID, &session.CurrentOrgID, &session.CreatedAt, &session.LastSeen, &session.ExpiresAt, &session.IdleTimeoutMS, &session.RevokedAt, &session.RotatedAt, &session.TokenSeen, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}
	if current && !session.TokenSeen {
		if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET token_seen=1 WHERE id=? AND token_hash=?`, session.ID, digest); err != nil {
			return Session{}, err
		}
		session.TokenSeen = true
	} else if !current && session.TokenSeen {
		// A previously seen replacement followed by the old cookie means a tab
		// lost its replacement. Force its next authenticated request to rotate.
		session.TokenSeen = false
		session.RotatedAt = now - urgentSessionRotation.Milliseconds() - 1
		if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET token_seen=0,rotated_at=?
WHERE id=? AND previous_token_hash=? AND rotated_at<?`, session.RotatedAt, session.ID, digest, session.RotatedAt); err != nil {
			return Session{}, err
		}
	}
	return session, nil
}

// AuthenticateSessionWithRotation returns a typed rotation requirement rather
// than authorizing a request with an overdue cookie. The rotate endpoint must
// call RotateSession directly, without this gate, to replace that cookie.
func (s *Store) AuthenticateSessionWithRotation(ctx context.Context, key string, requestedOrgID int64, interval time.Duration) (Principal, Session, error) {
	if interval < time.Minute || interval > 365*24*time.Hour {
		return Principal{}, Session{}, errors.New("session rotation interval must be between one minute and 365 days")
	}
	return s.authenticateSession(ctx, key, requestedOrgID, interval)
}

// RotateSession retains the device/session ID and current organization. Only
// hashes persist. The preceding credential handles concurrent browser requests;
// a second successful rotation makes the older credential permanently absent.
func (s *Store) RotateSession(ctx context.Context, key string) (Principal, Session, string, error) {
	digest, err := credentialDigest(key, sessionCredentialPrefix)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	replacement, replacementDigest, err := newOpaqueCredential(sessionCredentialPrefix)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	session, err := sessionByCredential(ctx, tx, digest, now)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	p, err := principalForUser(ctx, tx, session.UserID, session.CurrentOrgID)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	if time.UnixMilli(session.RotatedAt).Add(skipSessionRotation).After(time.UnixMilli(now)) {
		if err := tx.Commit(); err != nil {
			return Principal{}, Session{}, "", err
		}
		return p, session, key, nil
	}
	session, err = scanSession(tx.QueryRowContext(ctx, `UPDATE browser_sessions
SET previous_token_hash=token_hash,token_hash=?,token_seen=0,rotated_at=?,last_seen=max(last_seen,?)
WHERE id=? AND revoked_at=0 RETURNING `+sessionColumns, replacementDigest, now, now, session.ID))
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, Session{}, "", err
	}
	return p, session, replacement, nil
}
