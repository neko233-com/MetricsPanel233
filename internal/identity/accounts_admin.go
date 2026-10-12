package identity

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
)

// Pointer fields distinguish an omitted field from an explicit empty value.
// This profile input deliberately has no role, disabled or credential fields.
type UserProfileUpdate struct {
	Login *string `json:"login,omitempty"`
	Email *string `json:"email,omitempty"`
	Name  *string `json:"name,omitempty"`
}

type UserSearch struct {
	Query         string
	Page, PerPage int
	Disabled      *bool
	ServerAdmin   *bool
}

type UserSearchResult struct {
	TotalCount int64  `json:"totalCount"`
	Users      []User `json:"users"`
	Page       int    `json:"page"`
	PerPage    int    `json:"perPage"`
}

// UpdateUser only updates supplied profile fields. Alias replacement and the
// user CAS commit together; a duplicate leaves the previous aliases/profile
// intact. Password checks also recheck users.version, so an in-flight check
// made against an old login cannot authenticate after this commit.
// HTTP callers must enforce self-profile or instance-administration access.
func (s *Store) UpdateUser(ctx context.Context, userID, version int64, update UserProfileUpdate) (User, error) {
	if update.Login != nil {
		login := strings.TrimSpace(*update.Login)
		if !accountText(login, false) {
			return User{}, errors.New("valid user login required")
		}
		update.Login = &login
	}
	if update.Email != nil {
		email := strings.TrimSpace(*update.Email)
		if !validUserEmail(email) {
			return User{}, errors.New("valid user email required")
		}
		update.Email = &email
	}
	if update.Name != nil {
		name := strings.TrimSpace(*update.Name)
		if !accountText(name, true) {
			return User{}, errors.New("valid user name required")
		}
		update.Name = &name
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, userID))
	if err != nil {
		return User{}, err
	}
	if version <= 0 || version != user.Version {
		return User{}, ErrVersionConflict
	}
	if update.Login == nil && update.Email == nil && update.Name == nil {
		if err := tx.Commit(); err != nil {
			return User{}, err
		}
		return user, nil
	}
	if update.Login != nil {
		user.Login = *update.Login
	}
	if update.Email != nil {
		user.Email = *update.Email
	}
	if update.Name != nil {
		user.Name = *update.Name
	}
	if update.Login != nil || update.Email != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_identifiers WHERE user_id=?`, userID); err != nil {
			return User{}, err
		}
		if err := registerUserIdentifiers(ctx, tx, user); err != nil {
			return User{}, err
		}
	}
	user, err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET login=?,email=?,name=?,version=version+1,updated_at=max(?,updated_at+1) WHERE id=? AND version=? RETURNING `+userColumns, user.Login, user.Email, user.Name, time.Now().UnixMilli(), userID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrVersionConflict
	}
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

// SetUserServerAdmin changes only the human user's instance-administrator flag.
// Organization roles and service-account permissions are independent. Disabled
// users, removing memberships and inactive organizations do not count as the
// alternative administrator required when an enabled administrator is demoted;
// the same guard is used by SetUserDisabled. Any active membership qualifies,
// even RoleNone, because instance administration is independent of org roles.
// A future HTTP adapter must require current instance-administration authority.
func (s *Store) SetUserServerAdmin(ctx context.Context, userID, version int64, serverAdmin bool) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, userID))
	if err != nil {
		return User{}, err
	}
	if version <= 0 || version != user.Version {
		return User{}, ErrVersionConflict
	}
	if user.IsServerAdmin == serverAdmin {
		if err := tx.Commit(); err != nil {
			return User{}, err
		}
		return user, nil
	}
	if user.IsServerAdmin && !serverAdmin && !user.Disabled {
		if err := ensureOtherInstanceAdmin(ctx, tx, userID); err != nil {
			return User{}, err
		}
	}
	user, err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET is_server_admin=?,version=version+1,updated_at=max(?,updated_at+1) WHERE id=? AND version=? RETURNING `+userColumns, serverAdmin, time.Now().UnixMilli(), userID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrVersionConflict
	}
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

// UserByIdentifier is a global administration lookup, not authentication. It
// can return disabled users and does not infer a service account from a human
// ID or name. The existing User DTO exposes no password, hash or credential.
func (s *Store) UserByIdentifier(ctx context.Context, identifier string) (User, error) {
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if !accountText(identifier, false) {
		return User{}, errors.New("valid user identifier required")
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=(SELECT user_id FROM user_identifiers WHERE identifier=?)`, identifier))
}

// SearchUsers is global human-user administration data. It must not be exposed
// through an organization resource handler without instance authorization.
// Identifiers are searched as normalized literal text; % and _ are not SQL
// wildcards. Names use SQLite's lower() (ASCII case folding).
func (s *Store) SearchUsers(ctx context.Context, query UserSearch) (UserSearchResult, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PerPage == 0 {
		query.PerPage = 1000
	}
	result := UserSearchResult{Users: []User{}, Page: query.Page, PerPage: query.PerPage}
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 1000 || query.Page > math.MaxInt/query.PerPage || !accountText(query.Query, true) {
		return result, errors.New("user search requires a valid query, positive page and perPage 1 to 1000")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	where := `1=1`
	args := []any{}
	if query.Query != "" {
		where += ` AND (EXISTS(SELECT 1 FROM user_identifiers i WHERE i.user_id=u.id AND instr(i.identifier,?)>0) OR instr(lower(u.name),?)>0)`
		key := strings.ToLower(query.Query)
		args = append(args, key, key)
	}
	if query.Disabled != nil {
		where += ` AND u.disabled=?`
		args = append(args, *query.Disabled)
	}
	if query.ServerAdmin != nil {
		where += ` AND u.is_server_admin=?`
		args = append(args, *query.ServerAdmin)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users u WHERE `+where, args...).Scan(&result.TotalCount); err != nil {
		return result, err
	}
	pageArgs := append(append([]any{}, args...), query.PerPage, (query.Page-1)*query.PerPage)
	rows, err := tx.QueryContext(ctx, `SELECT `+userColumns+` FROM users u WHERE `+where+` ORDER BY lower(u.login),u.id LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Users = append(result.Users, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}
