package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrActiveAuthToken  = errors.New("cannot revoke active user auth token")
	ErrInvalidAuthToken = errors.New("invalid user auth token id")
)

// These are the local-human subset of the pinned Grafana 13.2.3 DTOs. Numeric
// identities stay int64 and milliseconds become UTC RFC3339 timestamps. No
// credential, raw device header, native revision, or permission claim is exposed.
type GrafanaUserProfile struct {
	ID                             int64     `json:"id"`
	UID                            string    `json:"uid"`
	Email                          string    `json:"email"`
	Name                           string    `json:"name"`
	Login                          string    `json:"login"`
	Theme                          string    `json:"theme"`
	OrgID                          int64     `json:"orgId,omitempty"`
	IsGrafanaAdmin                 bool      `json:"isGrafanaAdmin"`
	IsDisabled                     bool      `json:"isDisabled"`
	IsExternal                     bool      `json:"isExternal"`
	IsExternallySynced             bool      `json:"isExternallySynced"`
	IsGrafanaAdminExternallySynced bool      `json:"isGrafanaAdminExternallySynced"`
	AuthLabels                     []string  `json:"authLabels"`
	UpdatedAt                      time.Time `json:"updatedAt"`
	CreatedAt                      time.Time `json:"createdAt"`
	AvatarURL                      string    `json:"avatarUrl"`
	IsProvisioned                  bool      `json:"isProvisioned"`
}

type GrafanaUserOrganization struct {
	OrgID int64  `json:"orgId"`
	Name  string `json:"name"`
	Role  Role   `json:"role"`
}

type RevokeAuthTokenInput struct {
	AuthTokenID int64 `json:"authTokenId"`
}

// A context principal alone is NOT credential authentication. The opt-in HTTP
// dispatcher must first revalidate the actual request credential. These helpers
// additionally fence typed/context identity and read live catalog state in the
// same transaction as the current-user operation; they never infer a human from
// a numerically equal service-account ID.
func currentAdapterUser(ctx context.Context, tx *sql.Tx, incoming Principal) (User, error) {
	bound, ok := FromContext(ctx)
	if incoming.Validate() != nil || !ok {
		return User{}, ErrUnauthenticated
	}
	if bound.ID != incoming.ID || bound.Type != incoming.Type || bound.OrgID != incoming.OrgID {
		return User{}, ErrForbidden
	}
	if incoming.Type != TypeUser {
		return User{}, ErrForbidden
	}
	if _, err := principalForUser(ctx, tx, incoming.ID, incoming.OrgID); err != nil {
		return User{}, err
	}
	return scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, incoming.ID))
}

func (s *Store) CurrentUserProfile(ctx context.Context, p Principal) (GrafanaUserProfile, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GrafanaUserProfile{}, err
	}
	defer tx.Rollback()
	u, err := currentAdapterUser(ctx, tx, p)
	if err != nil {
		return GrafanaUserProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return GrafanaUserProfile{}, err
	}
	// Profile orgId is the user's persisted active organization in the pinned
	// GetProfile implementation, not a temporary request org-header selection.
	return GrafanaUserProfile{ID: u.ID, UID: u.UID, Email: u.Email, Name: u.Name, Login: u.Login,
		OrgID: u.CurrentOrgID, IsGrafanaAdmin: u.IsServerAdmin, IsDisabled: u.Disabled,
		AuthLabels: []string{}, CreatedAt: time.UnixMilli(u.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(u.UpdatedAt).UTC()}, nil
}

func (s *Store) CurrentUserOrganizations(ctx context.Context, p Principal) ([]GrafanaUserOrganization, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := currentAdapterUser(ctx, tx, p); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.name,m.role FROM organizations o JOIN organization_memberships m ON m.org_id=o.id WHERE m.user_id=? AND m.state='active' AND o.state='active' ORDER BY o.id`, p.ID)
	if err != nil {
		return nil, err
	}
	result := []GrafanaUserOrganization{}
	for rows.Next() {
		var item GrafanaUserOrganization
		if err = rows.Scan(&item.OrgID, &item.Name, &item.Role); err != nil {
			break
		}
		if !item.Role.Valid() {
			err = ErrForbidden
			break
		}
		result = append(result, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func validateAdapterSession(ctx context.Context, tx *sql.Tx, userID, activeSessionID, now int64) error {
	if activeSessionID < 0 {
		return ErrUnauthenticated
	}
	if activeSessionID == 0 {
		return nil
	} // Basic authentication has no active device.
	var found int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM browser_sessions WHERE user_id=? AND id=? AND revoked_at=0 AND expires_at>? AND last_seen+idle_timeout_ms>?`, userID, activeSessionID, now, now).Scan(&found)
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrUnauthenticated
	}
	return nil
}

// Session IDs come exclusively from the dispatcher credential binding. Target
// ownership, availability, and the active-device exclusion are checked atomically
// with revocation. The native tombstone remains available to private audit APIs.
func (s *Store) RevokeCurrentUserAuthToken(ctx context.Context, p Principal, activeSessionID, targetID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := currentAdapterUser(ctx, tx, p); err != nil {
		return err
	}
	if targetID <= 0 {
		return ErrInvalidAuthToken
	}
	now := time.Now().UnixMilli()
	if err := validateAdapterSession(ctx, tx, p.ID, activeSessionID, now); err != nil {
		return err
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM browser_sessions WHERE user_id=? AND id=? AND revoked_at=0 AND expires_at>? AND last_seen+idle_timeout_ms>?`, p.ID, targetID, now, now).Scan(&found)
	if err != nil {
		return err
	}
	if found != 1 {
		return sql.ErrNoRows
	}
	if targetID == activeSessionID {
		return ErrActiveAuthToken
	}
	result, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND id=? AND revoked_at=0`, now, p.ID, targetID)
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
	return tx.Commit()
}
