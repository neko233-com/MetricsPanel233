package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrAccountExists       = errors.New("user login or email already exists")
	ErrLastAdministrator   = errors.New("the last active administrator cannot be removed")
	ErrMembershipCleanup   = errors.New("organization membership permission cleanup is pending")
	ErrAlreadyBootstrapped = errors.New("instance administrator already initialized")
)

type User struct {
	ID            int64  `json:"id"`
	Login         string `json:"login"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	CurrentOrgID  int64  `json:"orgId"`
	IsServerAdmin bool   `json:"isGrafanaAdmin"`
	Disabled      bool   `json:"isDisabled"`
	Version       int64  `json:"version"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type UserInput struct {
	Login, Name, Email, Password string
	OrgID                        int64
	Role                         Role
	IsServerAdmin                bool
}

type Membership struct {
	OrgID      int64  `json:"orgId"`
	UserID     int64  `json:"userId"`
	Role       Role   `json:"role"`
	State      string `json:"state"`
	Version    int64  `json:"version"`
	Generation int64  `json:"generation"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

func initializeAccounts(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY AUTOINCREMENT,login TEXT NOT NULL,name TEXT NOT NULL,email TEXT NOT NULL,current_org_id INTEGER NOT NULL REFERENCES organizations(id),is_server_admin INTEGER NOT NULL,disabled INTEGER NOT NULL,version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_identifiers(identifier TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS password_credentials(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,format TEXT NOT NULL,salt BLOB NOT NULL,hash BLOB NOT NULL,version INTEGER NOT NULL CHECK(version>0));
CREATE TABLE IF NOT EXISTS organization_memberships(org_id INTEGER NOT NULL REFERENCES organizations(id),user_id INTEGER NOT NULL REFERENCES users(id),role TEXT NOT NULL CHECK(role IN ('None','Viewer','Editor','Admin')),state TEXT NOT NULL CHECK(state IN ('active','removing')),version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,PRIMARY KEY(org_id,user_id));
CREATE TABLE IF NOT EXISTS browser_sessions(id INTEGER PRIMARY KEY AUTOINCREMENT,token_hash BLOB NOT NULL UNIQUE,user_id INTEGER NOT NULL REFERENCES users(id),current_org_id INTEGER NOT NULL REFERENCES organizations(id),created_at INTEGER NOT NULL,last_seen INTEGER NOT NULL,expires_at INTEGER NOT NULL,idle_timeout_ms INTEGER NOT NULL,revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS sessions_user ON browser_sessions(user_id);
CREATE TABLE IF NOT EXISTS service_accounts(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL REFERENCES organizations(id),name TEXT NOT NULL,name_key TEXT NOT NULL,role TEXT NOT NULL CHECK(role IN ('None','Viewer','Editor','Admin')),disabled INTEGER NOT NULL,version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,UNIQUE(org_id,name_key),UNIQUE(id,org_id));
CREATE TABLE IF NOT EXISTS service_account_tokens(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL REFERENCES organizations(id),account_id INTEGER NOT NULL,name TEXT NOT NULL,name_key TEXT NOT NULL,token_hash BLOB NOT NULL UNIQUE,expires_at INTEGER NOT NULL,revoked_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,last_used INTEGER NOT NULL DEFAULT 0,UNIQUE(org_id,name_key),FOREIGN KEY(account_id,org_id) REFERENCES service_accounts(id,org_id) ON DELETE CASCADE);`)
	if err != nil {
		return err
	}
	return initializeTeams(tx)
}

const userColumns = `id,login,name,email,current_org_id,is_server_admin,disabled,version,created_at,updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.Login, &user.Name, &user.Email, &user.CurrentOrgID, &user.IsServerAdmin, &user.Disabled, &user.Version, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func activeOrganization(ctx context.Context, q rowQuerier, orgID int64) error {
	var state OrganizationState
	if err := q.QueryRowContext(ctx, `SELECT state FROM organizations WHERE id=?`, orgID).Scan(&state); err != nil || state != OrganizationActive {
		return ErrForbidden
	}
	return nil
}

func accountText(value string, allowEmpty bool) bool {
	return utf8.ValidString(value) && len(value) <= 256 && (allowEmpty || strings.TrimSpace(value) != "") && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validUserEmail(email string) bool {
	if !accountText(email, true) {
		return false
	}
	if email == "" {
		return true
	}
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

// Both creation and profile changes use the authentication alias namespace.
// Deduplicate the actual lowercase keys, not Unicode EqualFold: the latter
// can consider two distinct authentication keys equal (for example sigma).
func registerUserIdentifiers(ctx context.Context, tx *sql.Tx, user User) error {
	identifiers := []string{strings.ToLower(user.Login)}
	if email := strings.ToLower(user.Email); email != "" && email != identifiers[0] {
		identifiers = append(identifiers, email)
	}
	for _, identifier := range identifiers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_identifiers(identifier,user_id) VALUES(?,?)`, identifier, user.ID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: user_identifiers.identifier") {
				return ErrAccountExists
			}
			return err
		}
	}
	return nil
}

func ensureOtherInstanceAdmin(ctx context.Context, q rowQuerier, userID int64) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM users u WHERE u.id!=? AND u.is_server_admin=1 AND u.disabled=0 AND EXISTS(SELECT 1 FROM organization_memberships m JOIN organizations o ON o.id=m.org_id WHERE m.user_id=u.id AND m.state='active' AND o.state='active')`, userID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrLastAdministrator
	}
	return nil
}

// Check the state before and after removing an availability relationship in
// the same transaction as that transition. userID=0 excludes the entire org;
// otherwise only that single org/user membership is excluded. Another active
// membership of the same administrator still qualifies. Catalogs without an
// available human administrator are not barred from initialization/cleanup.
func preserveAvailableInstanceAdmin(ctx context.Context, q rowQuerier, orgID, userID int64) error {
	const available = ` FROM users u JOIN organization_memberships m ON m.user_id=u.id JOIN organizations o ON o.id=m.org_id WHERE u.is_server_admin=1 AND u.disabled=0 AND m.state='active' AND o.state='active'`
	var before, after bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+available+`),EXISTS(SELECT 1`+available+` AND (m.org_id!=? OR (?>0 AND m.user_id!=?)))`, orgID, userID, userID).Scan(&before, &after); err != nil {
		return err
	}
	if before && !after {
		return ErrLastAdministrator
	}
	return nil
}

func normalizeUser(input UserInput) (UserInput, error) {
	input.Login = strings.TrimSpace(input.Login)
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(input.Email)
	if input.Name == "" {
		input.Name = input.Login
	}
	if !accountText(input.Login, false) || !accountText(input.Name, false) || !accountText(input.Email, true) || !input.Role.Valid() || input.OrgID <= 0 {
		return input, errors.New("valid login, name, organization and role are required")
	}
	if !validUserEmail(input.Email) {
		return input, errors.New("valid email address required")
	}
	return input, nil
}

func (s *Store) User(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, id))
}

func (s *Store) Users(ctx context.Context, limit, offset int) ([]User, error) {
	if limit <= 0 || limit > 1000 || offset < 0 {
		return nil, errors.New("user list requires limit 1 to 1000 and a nonnegative offset")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) BootstrapAdmin(ctx context.Context, input UserInput) (User, error) {
	input.OrgID, input.Role, input.IsServerAdmin = 1, RoleAdmin, true
	return s.createUser(ctx, input, true)
}

func (s *Store) CreateUser(ctx context.Context, input UserInput) (User, error) {
	return s.createUser(ctx, input, false)
}

func (s *Store) createUser(ctx context.Context, input UserInput, bootstrap bool) (User, error) {
	input, err := normalizeUser(input)
	if err != nil {
		return User{}, err
	}
	salt, hash, err := makePassword(ctx, input.Password)
	if err != nil {
		return User{}, err
	}
	defer clear(hash)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if bootstrap {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
			return User{}, err
		}
		if count != 0 {
			return User{}, ErrAlreadyBootstrapped
		}
	}
	if err := activeOrganization(ctx, tx, input.OrgID); err != nil {
		return User{}, err
	}
	now := time.Now().UnixMilli()
	user, err := scanUser(tx.QueryRowContext(ctx, `INSERT INTO users(login,name,email,current_org_id,is_server_admin,disabled,version,created_at,updated_at) VALUES(?,?,?,?,?,0,1,?,?) RETURNING `+userColumns, input.Login, input.Name, input.Email, input.OrgID, input.IsServerAdmin, now, now))
	if err != nil {
		return User{}, err
	}
	if err := registerUserIdentifiers(ctx, tx, user); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO password_credentials(user_id,format,salt,hash,version) VALUES(?,?,?,?,1)`, user.ID, passwordFormat, salt, hash); err != nil {
		return User{}, err
	}
	generation, err := nextMembershipGeneration(ctx, tx, input.OrgID, user.ID)
	if err != nil {
		return User{}, err
	}
	membershipVersion, err := nextMembershipVersion(ctx, tx, input.OrgID, user.ID)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO organization_memberships(org_id,user_id,role,state,version,generation,created_at,updated_at) VALUES(?,?,?,'active',?,?,?,?)`, input.OrgID, user.ID, input.Role, membershipVersion, generation, now, now); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func principalForUser(ctx context.Context, q rowQuerier, userID, orgID int64) (Principal, error) {
	var p Principal
	p.Type = TypeUser
	err := q.QueryRowContext(ctx, `SELECT u.id,u.login,u.name,u.is_server_admin,m.org_id,m.role FROM users u JOIN organization_memberships m ON m.user_id=u.id JOIN organizations o ON o.id=m.org_id WHERE u.id=? AND m.org_id=? AND u.disabled=0 AND m.state='active' AND o.state='active'`, userID, orgID).Scan(&p.ID, &p.Login, &p.Name, &p.IsServerAdmin, &p.OrgID, &p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	return p, p.Validate()
}

func (s *Store) UserPrincipal(ctx context.Context, userID, orgID int64) (Principal, error) {
	return principalForUser(ctx, s.db, userID, orgID)
}

func scanMembership(row interface{ Scan(...any) error }) (Membership, error) {
	var membership Membership
	err := row.Scan(&membership.OrgID, &membership.UserID, &membership.Role, &membership.State, &membership.Version, &membership.Generation, &membership.CreatedAt, &membership.UpdatedAt)
	return membership, err
}

const membershipColumns = `org_id,user_id,role,state,version,generation,created_at,updated_at`

func (s *Store) Membership(ctx context.Context, orgID, userID int64) (Membership, error) {
	return scanMembership(s.db.QueryRowContext(ctx, `SELECT `+membershipColumns+` FROM organization_memberships WHERE org_id=? AND user_id=?`, orgID, userID))
}

func (s *Store) UserOrganizations(ctx context.Context, userID int64) ([]Organization, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,o.name,o.state,o.version,o.created_at,o.updated_at FROM organizations o JOIN organization_memberships m ON m.org_id=o.id JOIN users u ON u.id=m.user_id WHERE m.user_id=? AND m.state='active' AND u.disabled=0 AND o.state='active' ORDER BY o.id`, userID)
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

func (s *Store) AddOrganizationUser(ctx context.Context, orgID, userID int64, role Role) (Membership, error) {
	if !role.Valid() {
		return Membership{}, errors.New("valid organization role required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Membership{}, err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return Membership{}, err
	}
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT disabled FROM users WHERE id=?`, userID).Scan(&disabled); err != nil {
		return Membership{}, err
	}
	if disabled {
		return Membership{}, ErrForbidden
	}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM organization_memberships WHERE org_id=? AND user_id=?`, orgID, userID).Scan(&state)
	if err == nil {
		if state == "removing" {
			return Membership{}, ErrMembershipCleanup
		}
		return Membership{}, ErrAccountExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Membership{}, err
	}
	generation, err := nextMembershipGeneration(ctx, tx, orgID, userID)
	if err != nil {
		return Membership{}, err
	}
	version, err := nextMembershipVersion(ctx, tx, orgID, userID)
	if err != nil {
		return Membership{}, err
	}
	now := time.Now().UnixMilli()
	membership, err := scanMembership(tx.QueryRowContext(ctx, `INSERT INTO organization_memberships(org_id,user_id,role,state,version,generation,created_at,updated_at) VALUES(?,?,?,'active',?,?,?,?) RETURNING `+membershipColumns, orgID, userID, role, version, generation, now, now))
	if err != nil {
		return Membership{}, err
	}
	if err := tx.Commit(); err != nil {
		return Membership{}, err
	}
	return membership, nil
}

func ensureOtherOrganizationAdmin(ctx context.Context, q rowQuerier, orgID, userID int64) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM organization_memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=? AND m.user_id!=? AND m.role='Admin' AND m.state='active' AND u.disabled=0`, orgID, userID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrLastAdministrator
	}
	return nil
}

func (s *Store) ChangeOrganizationUserRole(ctx context.Context, orgID, userID, version int64, role Role) (Membership, error) {
	if !role.Valid() {
		return Membership{}, errors.New("valid organization role required")
	}
	return s.changeMembership(ctx, orgID, userID, version, role, false)
}

func (s *Store) RemoveOrganizationUser(ctx context.Context, orgID, userID, version int64) (Membership, error) {
	return s.changeMembership(ctx, orgID, userID, version, RoleNone, true)
}

func (s *Store) changeMembership(ctx context.Context, orgID, userID, version int64, role Role, remove bool) (Membership, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Membership{}, err
	}
	defer tx.Rollback()
	membership, err := scanMembership(tx.QueryRowContext(ctx, `SELECT `+membershipColumns+` FROM organization_memberships WHERE org_id=? AND user_id=?`, orgID, userID))
	if err != nil {
		return Membership{}, err
	}
	if membership.Version != version || version <= 0 {
		return Membership{}, ErrVersionConflict
	}
	if membership.State != "active" {
		return Membership{}, ErrMembershipCleanup
	}
	if membership.Role == RoleAdmin && (remove || role != RoleAdmin) {
		if err := ensureOtherOrganizationAdmin(ctx, tx, orgID, userID); err != nil {
			return Membership{}, err
		}
	}
	if remove {
		if err := preserveAvailableInstanceAdmin(ctx, tx, orgID, userID); err != nil {
			return Membership{}, err
		}
	}
	state := "active"
	if remove {
		state = "removing"
	}
	membership, err = scanMembership(tx.QueryRowContext(ctx, `UPDATE organization_memberships SET role=?,state=?,version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND user_id=? AND version=? RETURNING `+membershipColumns, role, state, time.Now().UnixMilli(), orgID, userID, version))
	if err != nil {
		return Membership{}, err
	}
	if remove {
		if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND current_org_id=? AND revoked_at=0`, time.Now().UnixMilli(), userID, orgID); err != nil {
			return Membership{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Membership{}, err
	}
	return membership, nil
}

// CompleteMembershipRemoval is called only after the organization permission
// cleanup succeeds. The durable removing row blocks rejoining with stale ACLs.
// Retaining the final revision in the ledger and deleting this row commit
// together, so a later incarnation cannot match an old draft or cleanup job.
func (s *Store) CompleteMembershipRemoval(ctx context.Context, orgID, userID, version int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanMembership(tx.QueryRowContext(ctx, `SELECT `+membershipColumns+` FROM organization_memberships WHERE org_id=? AND user_id=?`, orgID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVersionConflict
	}
	if err != nil {
		return err
	}
	if version <= 0 || current.Version != version || current.State != "removing" {
		return ErrVersionConflict
	}
	if err := rememberMembershipVersion(ctx, tx, orgID, userID, current.Version); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM organization_memberships WHERE org_id=? AND user_id=? AND version=? AND state='removing'`, orgID, userID, version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrVersionConflict
	}
	return tx.Commit()
}

func (s *Store) ChangePassword(ctx context.Context, userID, version int64, password string) (User, error) {
	salt, hash, err := makePassword(ctx, password)
	if err != nil {
		return User{}, err
	}
	defer clear(hash)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `UPDATE users SET version=version+1,updated_at=max(?,updated_at+1) WHERE id=? AND version=? RETURNING `+userColumns, time.Now().UnixMilli(), userID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrVersionConflict
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_credentials SET format=?,salt=?,hash=?,version=version+1 WHERE user_id=?`, passwordFormat, salt, hash, userID); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND revoked_at=0`, time.Now().UnixMilli(), userID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) SetUserDisabled(ctx context.Context, userID, version int64, disabled bool) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, userID))
	if err != nil {
		return User{}, err
	}
	if user.Version != version || version <= 0 {
		return User{}, ErrVersionConflict
	}
	if disabled && !user.Disabled {
		if user.IsServerAdmin {
			if err := ensureOtherInstanceAdmin(ctx, tx, userID); err != nil {
				return User{}, err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT org_id FROM organization_memberships WHERE user_id=? AND role='Admin' AND state='active'`, userID)
		if err != nil {
			return User{}, err
		}
		var orgIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return User{}, err
			}
			orgIDs = append(orgIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return User{}, err
		}
		for _, id := range orgIDs {
			if err := ensureOtherOrganizationAdmin(ctx, tx, id, userID); err != nil {
				return User{}, err
			}
		}
	}
	user, err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET disabled=?,version=version+1,updated_at=max(?,updated_at+1) WHERE id=? AND version=? RETURNING `+userColumns, disabled, time.Now().UnixMilli(), userID, version))
	if err != nil {
		return User{}, err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `UPDATE browser_sessions SET revoked_at=? WHERE user_id=? AND revoked_at=0`, time.Now().UnixMilli(), userID); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}
