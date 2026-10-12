package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Session struct {
	ID            int64 `json:"id"`
	UserID        int64 `json:"userId"`
	CurrentOrgID  int64 `json:"orgId"`
	CreatedAt     int64 `json:"created_at"`
	LastSeen      int64 `json:"last_seen"`
	ExpiresAt     int64 `json:"expires_at"`
	IdleTimeoutMS int64 `json:"idle_timeout_ms"`
	RevokedAt     int64 `json:"revoked_at"`
	RotatedAt     int64 `json:"rotated_at"`
	TokenSeen     bool  `json:"token_seen"`
}

type passwordCandidate struct {
	userID, userVersion, credentialVersion, orgID int64
	format                                        string
	salt, hash                                    []byte
}

func (s *Store) passwordCandidate(ctx context.Context, login, password string) (passwordCandidate, error) {
	var candidate passwordCandidate
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.version,c.version,u.current_org_id,c.format,c.salt,c.hash FROM user_identifiers i JOIN users u ON u.id=i.user_id JOIN password_credentials c ON c.user_id=u.id WHERE i.identifier=?`, strings.ToLower(strings.TrimSpace(login))).Scan(&candidate.userID, &candidate.userVersion, &candidate.credentialVersion, &candidate.orgID, &candidate.format, &candidate.salt, &candidate.hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return candidate, err
	}
	valid, hashErr := verifyPassword(ctx, password, candidate.salt, candidate.hash, candidate.format)
	clear(candidate.hash)
	if hashErr != nil {
		return passwordCandidate{}, hashErr
	}
	if !valid || err != nil {
		return passwordCandidate{}, ErrUnauthenticated
	}
	return candidate, nil
}

func (candidate passwordCandidate) principal(ctx context.Context, tx *sql.Tx, requestedOrgID int64) (Principal, error) {
	var userVersion, credentialVersion int64
	var disabled bool
	err := tx.QueryRowContext(ctx, `SELECT u.version,c.version,u.disabled FROM users u JOIN password_credentials c ON c.user_id=u.id WHERE u.id=?`, candidate.userID).Scan(&userVersion, &credentialVersion, &disabled)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (disabled || userVersion != candidate.userVersion || credentialVersion != candidate.credentialVersion) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if requestedOrgID == 0 {
		requestedOrgID = candidate.orgID
	}
	return principalForUser(ctx, tx, candidate.userID, requestedOrgID)
}

// AuthenticatePassword also resolves current membership, role and organization
// state after the expensive password check. Header selection cannot add a grant.
func (s *Store) AuthenticatePassword(ctx context.Context, login, password string, requestedOrgID int64) (Principal, error) {
	candidate, err := s.passwordCandidate(ctx, login, password)
	if err != nil {
		return Principal{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	p, err := candidate.principal(ctx, tx, requestedOrgID)
	if err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, err
	}
	return p, nil
}

const sessionColumns = `id,user_id,current_org_id,created_at,last_seen,expires_at,idle_timeout_ms,revoked_at,rotated_at,token_seen`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var session Session
	err := row.Scan(&session.ID, &session.UserID, &session.CurrentOrgID, &session.CreatedAt, &session.LastSeen, &session.ExpiresAt, &session.IdleTimeoutMS, &session.RevokedAt, &session.RotatedAt, &session.TokenSeen)
	return session, err
}

// Login returns an opaque credential exactly once; the session table contains
// only its SHA-256 digest. Session issuance and credential recheck are atomic.
func (s *Store) Login(ctx context.Context, login, password string, orgID int64, lifetime, idleTimeout time.Duration) (Principal, Session, string, error) {
	if lifetime < time.Second || lifetime > 365*24*time.Hour || idleTimeout < time.Second || idleTimeout > lifetime {
		return Principal{}, Session{}, "", errors.New("session lifetime and idle timeout must be between one second and 365 days, with idle timeout no longer than lifetime")
	}
	candidate, err := s.passwordCandidate(ctx, login, password)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	key, digest, err := newOpaqueCredential(sessionCredentialPrefix)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	defer tx.Rollback()
	p, err := candidate.principal(ctx, tx, orgID)
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	now := time.Now().UnixMilli()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM browser_sessions WHERE user_id=? AND revoked_at=0 AND expires_at>? AND last_seen+idle_timeout_ms>?`, p.ID, now, now).Scan(&count); err != nil {
		return Principal{}, Session{}, "", err
	}
	if count >= 256 {
		return Principal{}, Session{}, "", errors.New("active browser session limit reached")
	}
	session, err := scanSession(tx.QueryRowContext(ctx, `INSERT INTO browser_sessions(token_hash,previous_token_hash,user_id,current_org_id,created_at,last_seen,expires_at,idle_timeout_ms,rotated_at,token_seen) VALUES(?,?,?,?,?,?,?,?,?,0) RETURNING `+sessionColumns, digest, digest, p.ID, p.OrgID, now, now, now+lifetime.Milliseconds(), idleTimeout.Milliseconds(), now))
	if err != nil {
		return Principal{}, Session{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, Session{}, "", err
	}
	return p, session, key, nil
}

func sessionByCredential(ctx context.Context, tx *sql.Tx, digest []byte, now int64) (Session, error) {
	return lookupSessionCredential(ctx, tx, digest, now)
}

func (s *Store) AuthenticateSession(ctx context.Context, key string, requestedOrgID int64) (Principal, error) {
	p, _, err := s.authenticateSession(ctx, key, requestedOrgID, 0)
	return p, err
}

func (s *Store) authenticateSession(ctx context.Context, key string, requestedOrgID int64, rotationInterval time.Duration) (Principal, Session, error) {
	digest, err := credentialDigest(key, sessionCredentialPrefix)
	if err != nil {
		return Principal{}, Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, Session{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	session, err := sessionByCredential(ctx, tx, digest, now)
	if err != nil {
		return Principal{}, Session{}, err
	}
	if _, err := principalForUser(ctx, tx, session.UserID, session.CurrentOrgID); err != nil {
		return Principal{}, Session{}, err
	}
	if requestedOrgID == 0 {
		requestedOrgID = session.CurrentOrgID
	}
	p, err := principalForUser(ctx, tx, session.UserID, requestedOrgID)
	if err != nil {
		return Principal{}, Session{}, err
	}
	if rotationInterval > 0 && session.NeedsRotationAt(time.UnixMilli(now), rotationInterval) {
		if err := tx.Commit(); err != nil {
			return Principal{}, Session{}, err
		}
		return Principal{}, session, ErrSessionNeedsRotation
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET last_seen=max(last_seen,?) WHERE id=?`, now, session.ID); err != nil {
		return Principal{}, Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, Session{}, err
	}
	session.LastSeen = max(session.LastSeen, now)
	return p, session, nil
}

func (s *Store) SwitchSessionOrganization(ctx context.Context, key string, orgID int64) (Principal, error) {
	digest, err := credentialDigest(key, sessionCredentialPrefix)
	if err != nil {
		return Principal{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	session, err := sessionByCredential(ctx, tx, digest, now)
	if err != nil {
		return Principal{}, err
	}
	// Recheck the original context too: a disabled organization or removed
	// membership cannot turn its stale cookie into an authorized switch.
	if _, err := principalForUser(ctx, tx, session.UserID, session.CurrentOrgID); err != nil {
		return Principal{}, err
	}
	p, err := principalForUser(ctx, tx, session.UserID, orgID)
	if err != nil {
		return Principal{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET current_org_id=?,last_seen=max(last_seen,?) WHERE id=?`, p.OrgID, now, session.ID); err != nil {
		return Principal{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET current_org_id=?,version=version+1,updated_at=max(?,updated_at+1) WHERE id=?`, p.OrgID, now, p.ID); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, err
	}
	return p, nil
}

func (s *Store) Sessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM browser_sessions WHERE user_id=? ORDER BY id DESC LIMIT 1000`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []Session{}
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) RevokeSession(ctx context.Context, userID, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=CASE WHEN revoked_at=0 THEN ? ELSE revoked_at END WHERE user_id=? AND id=?`, time.Now().UnixMilli(), userID, sessionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Logout(ctx context.Context, key string) error {
	digest, err := credentialDigest(key, sessionCredentialPrefix)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=CASE WHEN revoked_at=0 THEN ? ELSE revoked_at END WHERE token_hash=? OR previous_token_hash=?`, time.Now().UnixMilli(), digest, digest)
	return err
}
