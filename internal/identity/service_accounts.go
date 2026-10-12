package identity

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	ErrServiceAccountExists   = errors.New("service account name already exists in this organization")
	ErrServiceTokenExists     = errors.New("service account token name already exists in this organization")
	ErrServiceTokenLimit      = errors.New("service account has reached its token limit")
	ErrServiceTokenExpiration = errors.New("invalid service account token expiration")
)

// These are native storage DTOs. They never contain a credential or its digest.
type ServiceAccount struct {
	ID        int64  `json:"id"`
	OrgID     int64  `json:"orgId"`
	Name      string `json:"name"`
	Login     string `json:"login"`
	Role      Role   `json:"role"`
	Disabled  bool   `json:"isDisabled"`
	Version   int64  `json:"version"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ServiceAccountInput struct {
	Name     string
	Role     Role
	Disabled bool
}

type ServiceAccountUpdate struct {
	Name     *string
	Role     *Role
	Disabled *bool
}

type ServiceAccountSearch struct {
	Query         string
	Page          int
	PerPage       int
	Disabled      *bool
	ExpiredTokens bool
}

type ServiceAccountSearchResult struct {
	TotalCount      int64            `json:"totalCount"`
	ServiceAccounts []ServiceAccount `json:"serviceAccounts"`
	Page            int              `json:"page"`
	PerPage         int              `json:"perPage"`
}

type ServiceAccountToken struct {
	ID        int64  `json:"id"`
	OrgID     int64  `json:"orgId"`
	AccountID int64  `json:"serviceAccountId"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
	RevokedAt int64  `json:"revoked_at"`
	CreatedAt int64  `json:"created_at"`
	LastUsed  int64  `json:"last_used"`
}

const serviceAccountColumns = `id,org_id,name,role,disabled,version,created_at,updated_at`
const serviceTokenColumns = `id,org_id,account_id,name,expires_at,revoked_at,created_at,last_used`
const maxServiceAccountTokens = 256

func scanServiceAccount(row interface{ Scan(...any) error }) (ServiceAccount, error) {
	var account ServiceAccount
	err := row.Scan(&account.ID, &account.OrgID, &account.Name, &account.Role, &account.Disabled, &account.Version, &account.CreatedAt, &account.UpdatedAt)
	if err == nil {
		account.Login = "sa-" + strconv.FormatInt(account.ID, 10)
	}
	return account, err
}

func scanServiceToken(row interface{ Scan(...any) error }) (ServiceAccountToken, error) {
	var token ServiceAccountToken
	err := row.Scan(&token.ID, &token.OrgID, &token.AccountID, &token.Name, &token.ExpiresAt, &token.RevokedAt, &token.CreatedAt, &token.LastUsed)
	return token, err
}

func serviceAccountWriteError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: service_accounts.org_id, service_accounts.name_key") {
		return ErrServiceAccountExists
	}
	return err
}

func (s *Store) CreateServiceAccount(ctx context.Context, orgID int64, input ServiceAccountInput) (ServiceAccount, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Role == "" {
		input.Role = RoleViewer
	}
	if !accountText(input.Name, false) || !input.Role.Valid() {
		return ServiceAccount{}, errors.New("valid service account name and role required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return ServiceAccount{}, err
	}
	now := time.Now().UnixMilli()
	account, err := scanServiceAccount(tx.QueryRowContext(ctx, `INSERT INTO service_accounts(org_id,name,name_key,role,disabled,version,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?) RETURNING `+serviceAccountColumns, orgID, input.Name, strings.ToLower(input.Name), input.Role, input.Disabled, now, now))
	if err != nil {
		return ServiceAccount{}, serviceAccountWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return ServiceAccount{}, err
	}
	return account, nil
}

func (s *Store) ServiceAccount(ctx context.Context, orgID, id int64) (ServiceAccount, error) {
	return scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE org_id=? AND id=?`, orgID, id))
}

func (s *Store) SearchServiceAccounts(ctx context.Context, orgID int64, query ServiceAccountSearch) (ServiceAccountSearchResult, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PerPage == 0 {
		query.PerPage = 1000
	}
	result := ServiceAccountSearchResult{ServiceAccounts: []ServiceAccount{}, Page: query.Page, PerPage: query.PerPage}
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 1000 || query.Page > math.MaxInt/query.PerPage || !accountText(query.Query, true) {
		return result, errors.New("service account search requires a valid query, positive page and perPage 1 to 1000")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	where := `org_id=?`
	args := []any{orgID}
	if query.Query != "" {
		where += ` AND instr(name_key,?)>0`
		args = append(args, strings.ToLower(query.Query))
	}
	if query.Disabled != nil {
		where += ` AND disabled=?`
		args = append(args, *query.Disabled)
	}
	if query.ExpiredTokens {
		where += ` AND EXISTS(SELECT 1 FROM service_account_tokens t WHERE t.org_id=service_accounts.org_id AND t.account_id=service_accounts.id AND t.expires_at>0 AND t.expires_at<=?)`
		args = append(args, time.Now().UnixMilli())
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM service_accounts WHERE `+where, args...).Scan(&result.TotalCount); err != nil {
		return result, err
	}
	pageArgs := append(append([]any{}, args...), query.PerPage, (query.Page-1)*query.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE `+where+` ORDER BY name_key,id LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		account, scanErr := scanServiceAccount(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
		}
		result.ServiceAccounts = append(result.ServiceAccounts, account)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) UpdateServiceAccount(ctx context.Context, orgID, id, version int64, update ServiceAccountUpdate) (ServiceAccount, error) {
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if !accountText(name, false) {
			return ServiceAccount{}, errors.New("valid service account name required")
		}
		update.Name = &name
	}
	if update.Role != nil && !update.Role.Valid() {
		return ServiceAccount{}, errors.New("valid service account role required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceAccount{}, err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return ServiceAccount{}, err
	}
	account, err := scanServiceAccount(tx.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE org_id=? AND id=?`, orgID, id))
	if err != nil {
		return ServiceAccount{}, err
	}
	if version <= 0 || version != account.Version {
		return ServiceAccount{}, ErrVersionConflict
	}
	if update.Name == nil && update.Role == nil && update.Disabled == nil {
		return account, tx.Commit()
	}
	if update.Name != nil {
		account.Name = *update.Name
	}
	if update.Role != nil {
		account.Role = *update.Role
	}
	if update.Disabled != nil {
		account.Disabled = *update.Disabled
	}
	account, err = scanServiceAccount(tx.QueryRowContext(ctx, `UPDATE service_accounts SET name=?,name_key=?,role=?,disabled=?,version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND id=? AND version=? RETURNING `+serviceAccountColumns, account.Name, strings.ToLower(account.Name), account.Role, account.Disabled, time.Now().UnixMilli(), orgID, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceAccount{}, ErrVersionConflict
	}
	if err != nil {
		return ServiceAccount{}, serviceAccountWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return ServiceAccount{}, err
	}
	return account, nil
}

func (s *Store) DeleteServiceAccount(ctx context.Context, orgID, id, version int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return err
	}
	account, err := scanServiceAccount(tx.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE org_id=? AND id=?`, orgID, id))
	if err != nil {
		return err
	}
	if version <= 0 || account.Version != version {
		return ErrVersionConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM service_accounts WHERE org_id=? AND id=? AND version=?`, orgID, id, version)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrVersionConflict
	}
	return tx.Commit()
}

// The key is returned only after the credential transaction commits. Its digest
// is private to authentication and is absent from all token DTOs and lists.
func (s *Store) CreateServiceAccountToken(ctx context.Context, orgID, accountID int64, name string, secondsToLive int64) (ServiceAccountToken, string, error) {
	name = strings.TrimSpace(name)
	if !accountText(name, false) {
		return ServiceAccountToken{}, "", errors.New("valid service account token name required")
	}
	now := time.Now().UnixMilli()
	if secondsToLive < 0 || secondsToLive > (math.MaxInt64-now)/1000 {
		return ServiceAccountToken{}, "", ErrServiceTokenExpiration
	}
	key, digest, err := newOpaqueCredential(serviceCredentialPrefix)
	if err != nil {
		return ServiceAccountToken{}, "", err
	}
	defer clear(digest)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceAccountToken{}, "", err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return ServiceAccountToken{}, "", err
	}
	account, err := scanServiceAccount(tx.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE org_id=? AND id=?`, orgID, accountID))
	if err != nil {
		return ServiceAccountToken{}, "", err
	}
	if account.Disabled {
		return ServiceAccountToken{}, "", ErrForbidden
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM service_account_tokens WHERE org_id=? AND account_id=?`, orgID, accountID).Scan(&count); err != nil {
		return ServiceAccountToken{}, "", err
	}
	if count >= maxServiceAccountTokens {
		return ServiceAccountToken{}, "", ErrServiceTokenLimit
	}
	// The lifetime starts when the transaction can issue the credential, not
	// while it is waiting for another catalog write to finish.
	now = time.Now().UnixMilli()
	if secondsToLive > (math.MaxInt64-now)/1000 {
		return ServiceAccountToken{}, "", ErrServiceTokenExpiration
	}
	expires := int64(0)
	if secondsToLive > 0 {
		expires = now + secondsToLive*1000
	}
	token, err := scanServiceToken(tx.QueryRowContext(ctx, `INSERT INTO service_account_tokens(org_id,account_id,name,name_key,token_hash,expires_at,created_at) VALUES(?,?,?,?,?,?,?) RETURNING `+serviceTokenColumns, orgID, accountID, name, strings.ToLower(name), digest, expires, now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: service_account_tokens.org_id, service_account_tokens.name_key") {
			return ServiceAccountToken{}, "", ErrServiceTokenExists
		}
		return ServiceAccountToken{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ServiceAccountToken{}, "", err
	}
	return token, key, nil
}

func (s *Store) ServiceAccountTokens(ctx context.Context, orgID, accountID int64) ([]ServiceAccountToken, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := scanServiceAccount(tx.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE org_id=? AND id=?`, orgID, accountID)); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+serviceTokenColumns+` FROM service_account_tokens WHERE org_id=? AND account_id=? ORDER BY name_key,id`, orgID, accountID)
	if err != nil {
		return nil, err
	}
	tokens := []ServiceAccountToken{}
	for rows.Next() {
		token, scanErr := scanServiceToken(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		tokens = append(tokens, token)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return tokens, tx.Commit()
}

func (s *Store) RevokeServiceAccountToken(ctx context.Context, orgID, accountID, tokenID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE service_account_tokens SET revoked_at=CASE WHEN revoked_at=0 THEN ? ELSE revoked_at END WHERE org_id=? AND account_id=? AND id=?`, time.Now().UnixMilli(), orgID, accountID, tokenID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AuthenticateServiceToken(ctx context.Context, key string, requestedOrgID int64) (Principal, error) {
	if requestedOrgID < 0 {
		return Principal{}, ErrUnauthenticated
	}
	digest, err := credentialDigest(key, serviceCredentialPrefix)
	if err != nil {
		return Principal{}, err
	}
	defer clear(digest)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	var principal Principal
	var tokenID int64
	now := time.Now().UnixMilli()
	err = tx.QueryRowContext(ctx, `SELECT a.id,a.org_id,a.name,a.role,t.id FROM service_account_tokens t JOIN service_accounts a ON a.id=t.account_id AND a.org_id=t.org_id JOIN organizations o ON o.id=a.org_id WHERE t.token_hash=? AND t.revoked_at=0 AND (t.expires_at=0 OR t.expires_at>?) AND a.disabled=0 AND o.state='active'`, digest, now).Scan(&principal.ID, &principal.OrgID, &principal.Name, &principal.Role, &tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if requestedOrgID != 0 && principal.OrgID != requestedOrgID {
		return Principal{}, ErrUnauthenticated
	}
	principal.Type = TypeServiceAccount
	principal.Login = "sa-" + strconv.FormatInt(principal.ID, 10)
	if err := principal.Validate(); err != nil {
		return Principal{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE service_account_tokens SET last_used=max(last_used,?) WHERE org_id=? AND account_id=? AND id=? AND revoked_at=0`, now, principal.OrgID, principal.ID, tokenID); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, err
	}
	return principal, nil
}
