package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrTeamExists       = errors.New("team name already exists in this organization")
	ErrTeamMemberExists = errors.New("principal is already a member of this team")
	ErrTeamCleanup      = errors.New("team permission cleanup is pending or complete")
)

type TeamState string

const (
	TeamActive   TeamState = "active"
	TeamDeleting TeamState = "deleting"
	TeamDeleted  TeamState = "deleted"
)

// TeamRole only controls this team. It never changes a principal's organization
// role or grants instance-administrator access.
type TeamRole int

const (
	TeamMemberRole TeamRole = 0
	TeamAdminRole  TeamRole = 4
)

func (r TeamRole) Valid() bool { return r == TeamMemberRole || r == TeamAdminRole }

type Team struct {
	ID          int64     `json:"id"`
	UID         string    `json:"uid"`
	OrgID       int64     `json:"orgId"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	State       TeamState `json:"state"`
	Version     int64     `json:"version"`
	CreatedAt   int64     `json:"created_at"`
	UpdatedAt   int64     `json:"updated_at"`
	MemberCount int64     `json:"memberCount"`
}

type TeamInput struct{ Name, Email string }

type TeamUpdate struct{ Name, Email *string }

type TeamSearch struct {
	Query, Name   string
	ID            int64
	Page, PerPage int
}

type TeamSearchResult struct {
	TotalCount int64  `json:"totalCount"`
	Teams      []Team `json:"teams"`
	Page       int    `json:"page"`
	PerPage    int    `json:"perPage"`
}

// Native identities are explicitly typed. User and service-account IDs are
// independent sequences, so legacy Grafana's bare userId cannot be used here.
type TeamPrincipal struct {
	Kind PrincipalType `json:"kind"`
	ID   int64         `json:"id"`
}

func (p TeamPrincipal) Valid() bool {
	return p.ID > 0 && (p.Kind == TypeUser || p.Kind == TypeServiceAccount)
}

type TeamMember struct {
	OrgID     int64         `json:"orgId"`
	TeamID    int64         `json:"teamId"`
	Principal TeamPrincipal `json:"principal"`
	Role      TeamRole      `json:"permission"`
	Version   int64         `json:"version"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
	Login     string        `json:"login"`
	Name      string        `json:"name"`
	Email     string        `json:"email"`
}

type TeamGrant struct {
	TeamID        int64    `json:"teamId"`
	TeamUID       string   `json:"teamUid"`
	TeamVersion   int64    `json:"teamVersion"`
	MemberVersion int64    `json:"memberVersion"`
	Role          TeamRole `json:"permission"`
}

// A removal marker is a durable cross-store recovery job, not evidence that
// organization-local ACLs have been cleaned. The caller must finish that work
// before invoking CompleteTeamRemoval. Version identifies this immutable job.
type TeamRemoval struct {
	OrgID         int64  `json:"orgId"`
	TeamID        int64  `json:"teamId"`
	TeamUID       string `json:"teamUid"`
	TargetVersion int64  `json:"targetVersion"`
	Version       int64  `json:"version"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at"`
}

func initializeTeams(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS teams(
id INTEGER PRIMARY KEY AUTOINCREMENT,uid TEXT NOT NULL,
org_id INTEGER NOT NULL REFERENCES organizations(id),name TEXT NOT NULL,name_key TEXT NOT NULL,email TEXT NOT NULL,
state TEXT NOT NULL CHECK(state IN ('active','deleting','deleted')),
version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
UNIQUE(org_id,uid),UNIQUE(id,org_id));
CREATE UNIQUE INDEX IF NOT EXISTS teams_active_name ON teams(org_id,name_key) WHERE state!='deleted';
CREATE TABLE IF NOT EXISTS team_members(
id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,team_id INTEGER NOT NULL,
principal_type TEXT NOT NULL CHECK(principal_type IN ('user','service-account')),user_id INTEGER,account_id INTEGER,
role INTEGER NOT NULL CHECK(role IN (0,4)),version INTEGER NOT NULL CHECK(version>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
CHECK((principal_type='user' AND user_id IS NOT NULL AND user_id>0 AND account_id IS NULL)
   OR (principal_type='service-account' AND account_id IS NOT NULL AND account_id>0 AND user_id IS NULL)),
FOREIGN KEY(team_id,org_id) REFERENCES teams(id,org_id) ON DELETE CASCADE,
FOREIGN KEY(org_id,user_id) REFERENCES organization_memberships(org_id,user_id) ON DELETE CASCADE,
FOREIGN KEY(account_id,org_id) REFERENCES service_accounts(id,org_id) ON DELETE CASCADE);
CREATE UNIQUE INDEX IF NOT EXISTS team_members_user ON team_members(org_id,team_id,user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS team_members_account ON team_members(org_id,team_id,account_id) WHERE account_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS team_removal_cleanup(
org_id INTEGER NOT NULL,team_id INTEGER NOT NULL,team_uid TEXT NOT NULL,
target_version INTEGER NOT NULL CHECK(target_version>0),version INTEGER NOT NULL CHECK(version>0),
created_at INTEGER NOT NULL,completed_at INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(org_id,team_id),FOREIGN KEY(team_id,org_id) REFERENCES teams(id,org_id));`)
	return err
}

const teamColumns = `t.id,t.uid,t.org_id,t.name,t.email,t.state,t.version,t.created_at,t.updated_at`
const activeTeamMember = `((m.principal_type='user' AND EXISTS(
SELECT 1 FROM users u JOIN organization_memberships om ON om.user_id=u.id
WHERE u.id=m.user_id AND u.disabled=0 AND om.org_id=m.org_id AND om.state='active'))
OR (m.principal_type='service-account' AND EXISTS(
SELECT 1 FROM service_accounts a WHERE a.id=m.account_id AND a.org_id=m.org_id AND a.disabled=0)))`
const teamMemberCount = `(SELECT count(*) FROM team_members m WHERE m.org_id=t.org_id AND m.team_id=t.id AND ` + activeTeamMember + `)`
const teamRemovalColumns = `org_id,team_id,team_uid,target_version,version,created_at,completed_at`

func scanTeam(row interface{ Scan(...any) error }) (Team, error) {
	var team Team
	err := row.Scan(&team.ID, &team.UID, &team.OrgID, &team.Name, &team.Email, &team.State, &team.Version, &team.CreatedAt, &team.UpdatedAt, &team.MemberCount)
	return team, err
}

func scanTeamRemoval(row interface{ Scan(...any) error }) (TeamRemoval, error) {
	var removal TeamRemoval
	err := row.Scan(&removal.OrgID, &removal.TeamID, &removal.TeamUID, &removal.TargetVersion, &removal.Version, &removal.CreatedAt, &removal.CompletedAt)
	return removal, err
}

func validTeamEmail(email string) bool {
	if !accountText(email, true) {
		return false
	}
	if email == "" {
		return true
	}
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

func teamWriteError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: teams.org_id, teams.name_key") {
		return ErrTeamExists
	}
	return err
}

func teamCAS(result sql.Result, err error) error {
	if err != nil {
		return teamWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrVersionConflict
	}
	return nil
}

func loadTeam(ctx context.Context, q rowQuerier, orgID, id int64) (Team, error) {
	return scanTeam(q.QueryRowContext(ctx, `SELECT `+teamColumns+`,`+teamMemberCount+` FROM teams t WHERE t.org_id=? AND t.id=?`, orgID, id))
}

func activeTeam(ctx context.Context, q rowQuerier, orgID, id int64) (Team, error) {
	if err := activeOrganization(ctx, q, orgID); err != nil {
		return Team{}, err
	}
	team, err := loadTeam(ctx, q, orgID, id)
	if err != nil {
		return Team{}, err
	}
	if team.State != TeamActive {
		return Team{}, ErrTeamCleanup
	}
	return team, nil
}

func (s *Store) CreateTeam(ctx context.Context, orgID int64, input TeamInput) (Team, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(input.Email)
	if !accountText(input.Name, false) || !validTeamEmail(input.Email) {
		return Team{}, errors.New("valid team name and email required")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Team{}, err
	}
	uid := base64.RawURLEncoding.EncodeToString(random[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Team{}, err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return Team{}, err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO teams(uid,org_id,name,name_key,email,state,version,created_at,updated_at) VALUES(?,?,?,?,?,'active',1,?,?)`, uid, orgID, input.Name, strings.ToLower(input.Name), input.Email, now, now)
	if err != nil {
		return Team{}, teamWriteError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Team{}, err
	}
	team, err := loadTeam(ctx, tx, orgID, id)
	if err != nil {
		return Team{}, err
	}
	return team, tx.Commit()
}

func (s *Store) Team(ctx context.Context, orgID, id int64) (Team, error) {
	return scanTeam(s.db.QueryRowContext(ctx, `SELECT `+teamColumns+`,`+teamMemberCount+` FROM teams t JOIN organizations o ON o.id=t.org_id WHERE t.org_id=? AND t.id=? AND t.state='active' AND o.state='active'`, orgID, id))
}

func (s *Store) TeamByUID(ctx context.Context, orgID int64, uid string) (Team, error) {
	return scanTeam(s.db.QueryRowContext(ctx, `SELECT `+teamColumns+`,`+teamMemberCount+` FROM teams t JOIN organizations o ON o.id=t.org_id WHERE t.org_id=? AND t.uid=? AND t.state='active' AND o.state='active'`, orgID, uid))
}

func (s *Store) SearchTeams(ctx context.Context, orgID int64, query TeamSearch) (TeamSearchResult, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PerPage == 0 {
		query.PerPage = 1000
	}
	result := TeamSearchResult{Teams: []Team{}, Page: query.Page, PerPage: query.PerPage}
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 1000 || query.Page > math.MaxInt/query.PerPage || query.ID < 0 || !accountText(query.Query, true) || !accountText(query.Name, true) {
		return result, errors.New("team search requires a valid query, positive page and perPage 1 to 1000")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := activeOrganization(ctx, tx, orgID); err != nil {
		return result, err
	}
	where := `t.org_id=? AND t.state='active'`
	args := []any{orgID}
	if query.Query != "" {
		where += ` AND instr(t.name_key,?)>0`
		args = append(args, strings.ToLower(query.Query))
	}
	if query.Name != "" {
		where += ` AND t.name_key=?`
		args = append(args, strings.ToLower(query.Name))
	}
	if query.ID != 0 {
		where += ` AND t.id=?`
		args = append(args, query.ID)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM teams t WHERE `+where, args...).Scan(&result.TotalCount); err != nil {
		return result, err
	}
	args = append(args, query.PerPage, (query.Page-1)*query.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT `+teamColumns+`,`+teamMemberCount+` FROM teams t WHERE `+where+` ORDER BY t.name_key,t.id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		team, err := scanTeam(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Teams = append(result.Teams, team)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) UpdateTeam(ctx context.Context, orgID, id, version int64, update TeamUpdate) (Team, error) {
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if !accountText(name, false) {
			return Team{}, errors.New("valid team name required")
		}
		update.Name = &name
	}
	if update.Email != nil {
		email := strings.TrimSpace(*update.Email)
		if !validTeamEmail(email) {
			return Team{}, errors.New("valid team email required")
		}
		update.Email = &email
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Team{}, err
	}
	defer tx.Rollback()
	team, err := activeTeam(ctx, tx, orgID, id)
	if err != nil {
		return Team{}, err
	}
	if version <= 0 || version != team.Version {
		return Team{}, ErrVersionConflict
	}
	if update.Name == nil && update.Email == nil {
		return team, tx.Commit()
	}
	if update.Name != nil {
		team.Name = *update.Name
	}
	if update.Email != nil {
		team.Email = *update.Email
	}
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE teams SET name=?,name_key=?,email=?,version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND id=? AND version=? AND state='active'`, team.Name, strings.ToLower(team.Name), team.Email, time.Now().UnixMilli(), orgID, id, version)); err != nil {
		return Team{}, err
	}
	team, err = loadTeam(ctx, tx, orgID, id)
	if err != nil {
		return Team{}, err
	}
	return team, tx.Commit()
}

func (s *Store) DeleteTeam(ctx context.Context, orgID, id, version int64) (TeamRemoval, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamRemoval{}, err
	}
	defer tx.Rollback()
	team, err := activeTeam(ctx, tx, orgID, id)
	if err != nil {
		return TeamRemoval{}, err
	}
	if version <= 0 || version != team.Version {
		return TeamRemoval{}, ErrVersionConflict
	}
	now := max(time.Now().UnixMilli(), team.UpdatedAt+1)
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE teams SET state='deleting',version=version+1,updated_at=? WHERE org_id=? AND id=? AND version=? AND state='active'`, now, orgID, id, version)); err != nil {
		return TeamRemoval{}, err
	}
	removal, err := scanTeamRemoval(tx.QueryRowContext(ctx, `INSERT INTO team_removal_cleanup(org_id,team_id,team_uid,target_version,version,created_at) VALUES(?,?,?,?,1,?) RETURNING `+teamRemovalColumns, orgID, id, team.UID, version+1, now))
	if err != nil {
		return TeamRemoval{}, err
	}
	return removal, tx.Commit()
}

// Recovery deliberately works while an organization is disabled or deleting.
// Organization-local ACL cleanup must be idempotent and use the original team
// identity from this queue; a name is never an authorization identity.
func (s *Store) PendingTeamRemovals(ctx context.Context, orgID int64, limit int) ([]TeamRemoval, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 || orgID <= 0 {
		return nil, errors.New("organization and cleanup limit 1 to 1000 required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+teamRemovalColumns+` FROM team_removal_cleanup WHERE org_id=? AND completed_at=0 ORDER BY created_at,team_id LIMIT ?`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	removals := []TeamRemoval{}
	for rows.Next() {
		removal, err := scanTeamRemoval(rows)
		if err != nil {
			return nil, err
		}
		removals = append(removals, removal)
	}
	return removals, rows.Err()
}

func (s *Store) CompleteTeamRemoval(ctx context.Context, orgID, teamID, version int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	removal, err := scanTeamRemoval(tx.QueryRowContext(ctx, `SELECT `+teamRemovalColumns+` FROM team_removal_cleanup WHERE org_id=? AND team_id=?`, orgID, teamID))
	if err != nil {
		return err
	}
	if version <= 0 || removal.Version != version {
		return ErrVersionConflict
	}
	team, err := loadTeam(ctx, tx, orgID, teamID)
	if err != nil {
		return err
	}
	if team.UID != removal.TeamUID {
		return ErrVersionConflict
	}
	if removal.CompletedAt != 0 {
		if team.State != TeamDeleted || team.Version != removal.TargetVersion+1 {
			return ErrVersionConflict
		}
		return tx.Commit()
	}
	if team.State != TeamDeleting || team.Version != removal.TargetVersion {
		return ErrVersionConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM team_members WHERE org_id=? AND team_id=?`, orgID, teamID); err != nil {
		return err
	}
	now := max(time.Now().UnixMilli(), team.UpdatedAt+1)
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE teams SET state='deleted',version=version+1,updated_at=? WHERE org_id=? AND id=? AND version=? AND state='deleting'`, now, orgID, teamID, removal.TargetVersion)); err != nil {
		return err
	}
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE team_removal_cleanup SET completed_at=? WHERE org_id=? AND team_id=? AND version=? AND completed_at=0`, now, orgID, teamID, version)); err != nil {
		return err
	}
	return tx.Commit()
}

func validateTeamPrincipal(ctx context.Context, q rowQuerier, orgID int64, principal TeamPrincipal) error {
	if !principal.Valid() {
		return errors.New("explicit user or service-account principal with a positive ID required")
	}
	var id int64
	var err error
	if principal.Kind == TypeUser {
		err = q.QueryRowContext(ctx, `SELECT u.id FROM users u JOIN organization_memberships om ON om.user_id=u.id WHERE u.id=? AND u.disabled=0 AND om.org_id=? AND om.state='active'`, principal.ID, orgID).Scan(&id)
	} else {
		err = q.QueryRowContext(ctx, `SELECT id FROM service_accounts WHERE id=? AND org_id=? AND disabled=0`, principal.ID, orgID).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}

const teamMemberColumns = `m.org_id,m.team_id,m.principal_type,coalesce(m.user_id,m.account_id),m.role,m.version,m.created_at,m.updated_at,
coalesce(u.login,'sa-'||a.id),coalesce(u.name,a.name),coalesce(u.email,'')`
const teamMemberJoins = ` FROM team_members m JOIN teams t ON t.id=m.team_id AND t.org_id=m.org_id
JOIN organizations o ON o.id=m.org_id LEFT JOIN users u ON u.id=m.user_id
LEFT JOIN service_accounts a ON a.id=m.account_id AND a.org_id=m.org_id`

func scanTeamMember(row interface{ Scan(...any) error }) (TeamMember, error) {
	var member TeamMember
	err := row.Scan(&member.OrgID, &member.TeamID, &member.Principal.Kind, &member.Principal.ID, &member.Role, &member.Version, &member.CreatedAt, &member.UpdatedAt, &member.Login, &member.Name, &member.Email)
	return member, err
}

func memberIdentity(principal TeamPrincipal) (string, int64) {
	if principal.Kind == TypeUser {
		return "m.user_id", principal.ID
	}
	return "m.account_id", principal.ID
}

func loadTeamMember(ctx context.Context, q rowQuerier, orgID, teamID int64, principal TeamPrincipal, active bool) (TeamMember, error) {
	if !principal.Valid() {
		return TeamMember{}, errors.New("explicit user or service-account principal with a positive ID required")
	}
	column, id := memberIdentity(principal)
	where := ` WHERE m.org_id=? AND m.team_id=? AND m.principal_type=? AND ` + column + `=?`
	if active {
		where += ` AND t.state='active' AND o.state='active' AND ` + activeTeamMember
	}
	return scanTeamMember(q.QueryRowContext(ctx, `SELECT `+teamMemberColumns+teamMemberJoins+where, orgID, teamID, principal.Kind, id))
}

func (s *Store) TeamMember(ctx context.Context, orgID, teamID int64, principal TeamPrincipal) (TeamMember, error) {
	return loadTeamMember(ctx, s.db, orgID, teamID, principal, true)
}

func (s *Store) TeamMembers(ctx context.Context, orgID, teamID int64) ([]TeamMember, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := activeTeam(ctx, tx, orgID, teamID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+teamMemberColumns+teamMemberJoins+` WHERE m.org_id=? AND m.team_id=? AND `+activeTeamMember+` ORDER BY lower(coalesce(u.name,a.name)),m.principal_type,coalesce(m.user_id,m.account_id)`, orgID, teamID)
	if err != nil {
		return nil, err
	}
	members := []TeamMember{}
	for rows.Next() {
		member, err := scanTeamMember(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return members, tx.Commit()
}

// Authorization must query this live view rather than trust cached team IDs
// from a session/token. Disabled principals and removing org members never
// retain a team grant; deleting teams stop granting before ACL cleanup starts.
func (s *Store) TeamsForPrincipal(ctx context.Context, orgID int64, principal TeamPrincipal) ([]TeamGrant, error) {
	if !principal.Valid() {
		return nil, errors.New("explicit user or service-account principal with a positive ID required")
	}
	column, id := memberIdentity(principal)
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.uid,t.version,m.version,m.role`+teamMemberJoins+` WHERE m.org_id=? AND m.principal_type=? AND `+column+`=? AND t.state='active' AND o.state='active' AND `+activeTeamMember+` ORDER BY t.id`, orgID, principal.Kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []TeamGrant{}
	for rows.Next() {
		var grant TeamGrant
		if err := rows.Scan(&grant.TeamID, &grant.TeamUID, &grant.TeamVersion, &grant.MemberVersion, &grant.Role); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// Every membership mutation advances the team's durable revision, which is the
// cache-revocation hook for callers. Member revisions come from that sequence,
// so removing and readding a principal cannot reuse the deleted revision.
func advanceTeam(ctx context.Context, tx *sql.Tx, team Team) (Team, error) {
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE teams SET version=version+1,updated_at=max(?,updated_at+1) WHERE org_id=? AND id=? AND version=? AND state='active'`, time.Now().UnixMilli(), team.OrgID, team.ID, team.Version)); err != nil {
		return Team{}, err
	}
	return loadTeam(ctx, tx, team.OrgID, team.ID)
}

func (s *Store) AddTeamMember(ctx context.Context, orgID, teamID, teamVersion int64, principal TeamPrincipal, role TeamRole) (TeamMember, error) {
	if !role.Valid() {
		return TeamMember{}, errors.New("team permission must be Member (0) or Admin (4)")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamMember{}, err
	}
	defer tx.Rollback()
	team, err := activeTeam(ctx, tx, orgID, teamID)
	if err != nil {
		return TeamMember{}, err
	}
	if teamVersion <= 0 || teamVersion != team.Version {
		return TeamMember{}, ErrVersionConflict
	}
	if err := validateTeamPrincipal(ctx, tx, orgID, principal); err != nil {
		return TeamMember{}, err
	}
	if _, err := loadTeamMember(ctx, tx, orgID, teamID, principal, false); err == nil {
		return TeamMember{}, ErrTeamMemberExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TeamMember{}, err
	}
	team, err = advanceTeam(ctx, tx, team)
	if err != nil {
		return TeamMember{}, err
	}
	var userID, accountID any
	if principal.Kind == TypeUser {
		userID = principal.ID
	} else {
		accountID = principal.ID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_members(org_id,team_id,principal_type,user_id,account_id,role,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, orgID, teamID, principal.Kind, userID, accountID, role, team.Version, team.UpdatedAt, team.UpdatedAt); err != nil {
		return TeamMember{}, err
	}
	member, err := loadTeamMember(ctx, tx, orgID, teamID, principal, false)
	if err != nil {
		return TeamMember{}, err
	}
	return member, tx.Commit()
}

func (s *Store) UpdateTeamMember(ctx context.Context, orgID, teamID, memberVersion int64, principal TeamPrincipal, role TeamRole) (TeamMember, error) {
	if !role.Valid() {
		return TeamMember{}, errors.New("team permission must be Member (0) or Admin (4)")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamMember{}, err
	}
	defer tx.Rollback()
	team, err := activeTeam(ctx, tx, orgID, teamID)
	if err != nil {
		return TeamMember{}, err
	}
	member, err := loadTeamMember(ctx, tx, orgID, teamID, principal, false)
	if err != nil {
		return TeamMember{}, err
	}
	if memberVersion <= 0 || memberVersion != member.Version {
		return TeamMember{}, ErrVersionConflict
	}
	if err := validateTeamPrincipal(ctx, tx, orgID, principal); err != nil {
		return TeamMember{}, err
	}
	team, err = advanceTeam(ctx, tx, team)
	if err != nil {
		return TeamMember{}, err
	}
	column, id := memberIdentity(principal)
	if err := teamCAS(tx.ExecContext(ctx, `UPDATE team_members AS m SET role=?,version=?,updated_at=? WHERE m.org_id=? AND m.team_id=? AND m.principal_type=? AND `+column+`=? AND m.version=?`, role, team.Version, team.UpdatedAt, orgID, teamID, principal.Kind, id, memberVersion)); err != nil {
		return TeamMember{}, err
	}
	member, err = loadTeamMember(ctx, tx, orgID, teamID, principal, false)
	if err != nil {
		return TeamMember{}, err
	}
	return member, tx.Commit()
}

// Removing a member revokes their team grant immediately. It does not remove
// unrelated direct permissions belonging to that principal. Disabled/removing
// principals can still be removed using the prior membership revision.
func (s *Store) RemoveTeamMember(ctx context.Context, orgID, teamID, memberVersion int64, principal TeamPrincipal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	team, err := activeTeam(ctx, tx, orgID, teamID)
	if err != nil {
		return err
	}
	member, err := loadTeamMember(ctx, tx, orgID, teamID, principal, false)
	if err != nil {
		return err
	}
	if memberVersion <= 0 || memberVersion != member.Version {
		return ErrVersionConflict
	}
	if _, err := advanceTeam(ctx, tx, team); err != nil {
		return err
	}
	column, id := memberIdentity(principal)
	if err := teamCAS(tx.ExecContext(ctx, `DELETE FROM team_members AS m WHERE m.org_id=? AND m.team_id=? AND m.principal_type=? AND `+column+`=? AND m.version=?`, orgID, teamID, principal.Kind, id, memberVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
