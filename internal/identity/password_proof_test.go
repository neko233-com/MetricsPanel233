package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const proofTestPassword = "proof-password-233"

func passwordProofFixture(t *testing.T) (*Store, User) {
	t.Helper()
	catalog, err := Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	t.Cleanup(func() { catalog.Close() })
	_, err = catalog.BootstrapAdmin(context.Background(), UserInput{Login: "admin", Password: "admin-password-233"})
	require.NoError(t, err)
	user, err := catalog.CreateUser(context.Background(), UserInput{Login: "reader", Email: "reader@example.com", Name: "Reader", Password: proofTestPassword, OrgID: 1, Role: RoleViewer})
	require.NoError(t, err)
	return catalog, user
}

func issuePasswordProof(t *testing.T, catalog *Store, alias string, orgID int64) (Principal, *PasswordProof) {
	t.Helper()
	p, proof, err := catalog.AuthenticatePasswordWithProof(context.Background(), alias, proofTestPassword, orgID)
	require.NoError(t, err)
	require.NotNil(t, proof)
	return p, proof
}

func TestPasswordProofRevalidatesFreshAuthorizationWithoutArgon2(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	p, proof := issuePasswordProof(t, catalog, "  READER@EXAMPLE.COM ", 0)
	require.Equal(t, user.ID, p.ID)
	require.EqualValues(t, 1, p.OrgID)
	require.Equal(t, RoleViewer, p.Role)
	name := "Fresh name"
	user, err := catalog.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Name: &name})
	require.NoError(t, err)
	membership, err := catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	_, err = catalog.ChangeOrganizationUserRole(ctx, 1, user.ID, membership.Version, RoleEditor)
	require.NoError(t, err)
	user, err = catalog.SetUserServerAdmin(ctx, user.ID, user.Version, true)
	require.NoError(t, err)

	// A held Argon2 permit would block any repeated password verification. The
	// request-local proof must still perform current DB reads and finish promptly.
	passwordWork <- struct{}{}
	defer func() { <-passwordWork }()
	for range 5 {
		checkCtx, cancel := context.WithTimeout(ctx, time.Second)
		fresh, err := catalog.RevalidatePasswordProof(checkCtx, proof)
		cancel()
		require.NoError(t, err)
		require.Equal(t, user.ID, fresh.ID)
		require.Equal(t, name, fresh.Name)
		require.Equal(t, RoleEditor, fresh.Role)
		require.True(t, fresh.IsServerAdmin)
	}
	user, err = catalog.SetUserServerAdmin(ctx, user.ID, user.Version, false)
	require.NoError(t, err)
	membership, err = catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	_, err = catalog.ChangeOrganizationUserRole(ctx, 1, user.ID, membership.Version, RoleNone)
	require.NoError(t, err)
	fresh, err := catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err)
	require.Equal(t, RoleNone, fresh.Role)
	require.False(t, fresh.IsServerAdmin)
}

func TestPasswordProofRequiresActualPasswordAndCannotSerializeOrBorrow(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	for _, input := range []struct {
		login, password string
		orgID           int64
	}{{"reader", "wrong-password-233", 1}, {"missing", proofTestPassword, 1}, {"reader", proofTestPassword, 99}} {
		p, proof, err := catalog.AuthenticatePasswordWithProof(ctx, input.login, input.password, input.orgID)
		require.ErrorIs(t, err, ErrUnauthenticated)
		require.Nil(t, proof)
		require.Empty(t, p)
	}
	_, proof := issuePasswordProof(t, catalog, "reader", 1)
	for _, invalid := range []*PasswordProof{nil, {}, {state: &passwordProofState{userID: user.ID, orgID: 1}}} {
		_, err := catalog.RevalidatePasswordProof(ctx, invalid)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
	other, err := Open(catalog.Path())
	require.NoError(t, err)
	t.Cleanup(func() { other.Close() })
	_, err = other.RevalidatePasswordProof(ctx, proof)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err, "another Store rejection cannot burn the issuer's proof")
	encoded, err := json.Marshal(proof)
	require.NoError(t, err)
	require.Equal(t, `"[redacted password proof]"`, string(encoded))
	for _, logged := range []string{fmt.Sprint(proof), fmt.Sprintf("%+v", proof), fmt.Sprintf("%#v", proof), string(encoded)} {
		require.NotContains(t, logged, proofTestPassword)
		require.NotContains(t, logged, "reader")
		require.NotContains(t, logged, fmt.Sprintf("%x", proof.state.fingerprint))
	}
	var restored PasswordProof
	require.ErrorIs(t, json.Unmarshal(encoded, &restored), ErrUnauthenticated)
	_, err = catalog.RevalidatePasswordProof(ctx, &restored)
	require.ErrorIs(t, err, ErrUnauthenticated)
	require.NoError(t, catalog.Close())
	reopened, err := Open(catalog.Path())
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close() })
	_, err = reopened.RevalidatePasswordProof(ctx, proof)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestPasswordProofCredentialVersionAndSameVersionFingerprintFence(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	_, versionProof := issuePasswordProof(t, catalog, "reader", 1)
	user, err := catalog.ChangePassword(ctx, user.ID, user.Version, proofTestPassword)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, versionProof)
	require.ErrorIs(t, err, ErrUnauthenticated, "the same password with new credentials requires a new verification")
	_, proof := issuePasswordProof(t, catalog, "reader", 1)
	copyProof := *proof
	var savedHash []byte
	require.NoError(t, catalog.db.QueryRow(`SELECT hash FROM password_credentials WHERE user_id=?`, user.ID).Scan(&savedHash))
	defer clear(savedHash)
	changedHash := append([]byte{}, savedHash...)
	changedHash[0] ^= 1
	_, err = catalog.db.Exec(`UPDATE password_credentials SET hash=? WHERE user_id=?`, changedHash, user.ID)
	clear(changedHash)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, &copyProof)
	require.ErrorIs(t, err, ErrUnauthenticated, "credential content is checked even if version is unchanged")
	_, err = catalog.db.Exec(`UPDATE password_credentials SET hash=? WHERE user_id=?`, savedHash, user.ID)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.ErrorIs(t, err, ErrUnauthenticated, "observed mismatch irreversibly burns all copies even after exact rollback")
	_, fresh := issuePasswordProof(t, catalog, "reader", 1)
	_, err = catalog.RevalidatePasswordProof(ctx, fresh)
	require.NoError(t, err, "a new real password verification can issue a new proof")
}

func TestPasswordProofOriginalAliasAndDisabledStateCannotRevive(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	_, aliasProof := issuePasswordProof(t, catalog, "reader@example.com", 1)
	_, loginProof := issuePasswordProof(t, catalog, "reader", 1)
	email := "new@example.com"
	user, err := catalog.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Email: &email})
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, loginProof)
	require.NoError(t, err, "an unrelated profile alias change preserves the verified original login alias")
	_, err = catalog.RevalidatePasswordProof(ctx, aliasProof)
	require.ErrorIs(t, err, ErrUnauthenticated)
	email = "reader@example.com"
	user, err = catalog.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Email: &email})
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, aliasProof)
	require.ErrorIs(t, err, ErrUnauthenticated)
	// A current alias that resolves to another user never borrows the old proof.
	_, reassigned := issuePasswordProof(t, catalog, "reader@example.com", 1)
	_, err = catalog.db.Exec(`UPDATE user_identifiers SET user_id=1 WHERE identifier='reader@example.com'`)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, reassigned)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.db.Exec(`UPDATE user_identifiers SET user_id=? WHERE identifier='reader@example.com'`, user.ID)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, reassigned)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, disabledProof := issuePasswordProof(t, catalog, "reader", 1)
	user, err = catalog.SetUserDisabled(ctx, user.ID, user.Version, true)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, disabledProof)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.SetUserDisabled(ctx, user.ID, user.Version, false)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, disabledProof)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestPasswordProofSelectedOrganizationAndMembershipIncarnation(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	org, err := catalog.BeginOrganization(ctx, "Proof business")
	require.NoError(t, err)
	org, err = catalog.ActivateOrganization(ctx, org.ID, org.Version)
	require.NoError(t, err)
	membership, err := catalog.AddOrganizationUser(ctx, org.ID, user.ID, RoleEditor)
	require.NoError(t, err)
	p, proof := issuePasswordProof(t, catalog, "reader", org.ID)
	require.Equal(t, org.ID, p.OrgID)
	_, err = catalog.db.Exec(`UPDATE users SET current_org_id=1 WHERE id=?`, user.ID)
	require.NoError(t, err)
	p, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err)
	require.Equal(t, org.ID, p.OrgID, "the selected organization is fixed independently of current-org changes")
	membership, err = catalog.RemoveOrganizationUser(ctx, org.ID, user.ID, membership.Version)
	require.NoError(t, err)
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, org.ID, user.ID, membership.Version))
	membership, err = catalog.AddOrganizationUser(ctx, org.ID, user.ID, RoleViewer)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.ErrorIs(t, err, ErrUnauthenticated, "rejoin invalidates an old proof even if no check observed the removal")
	_, orgProof := issuePasswordProof(t, catalog, "reader", org.ID)
	org, err = catalog.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, orgProof)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.SetOrganizationState(ctx, org.ID, org.Version, OrganizationActive)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, orgProof)
	require.ErrorIs(t, err, ErrUnauthenticated, "observed inactive organization cannot revive the same proof")
}

func TestPasswordProofCancellationAndDatabaseErrorsDoNotBurnValidProof(t *testing.T) {
	catalog, _ := passwordProofFixture(t)
	ctx := context.Background()
	_, proof := issuePasswordProof(t, catalog, "reader", 1)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := catalog.RevalidatePasswordProof(canceled, proof)
	require.ErrorIs(t, err, context.Canceled)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err)
	// Exhausting the sole connection makes BeginTx wait and then fail because
	// the request expires, rather than observing a credential mismatch.
	conn, err := catalog.db.Conn(ctx)
	require.NoError(t, err)
	timed, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	_, err = catalog.RevalidatePasswordProof(timed, proof)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, conn.Close())
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err)
	_, err = catalog.db.Exec(`ALTER TABLE password_credentials RENAME TO temporarily_missing_credentials`)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.db.Exec(`ALTER TABLE temporarily_missing_credentials RENAME TO password_credentials`)
	require.NoError(t, err)
	_, err = catalog.RevalidatePasswordProof(ctx, proof)
	require.NoError(t, err, "a repaired temporary DB failure must not burn the proof")
	passwordWork <- struct{}{}
	defer func() { <-passwordWork }()
	timed, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
	_, rejected, err := catalog.AuthenticatePasswordWithProof(timed, "reader", proofTestPassword, 1)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, rejected, "proof issuance still requires the queued real password verification")
}

func TestPasswordCandidateRejectsPostHashAliasAndCredentialTOCTOU(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	for _, mutation := range []string{"alias-owner", "credential-content", "user-version", "credential-version"} {
		t.Run(mutation, func(t *testing.T) {
			candidate, err := catalog.passwordCandidate(ctx, "reader", proofTestPassword)
			require.NoError(t, err)
			require.Empty(t, candidate.hash)
			require.Empty(t, candidate.salt)
			tx, err := catalog.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer tx.Rollback()
			switch mutation {
			case "alias-owner":
				_, err = tx.ExecContext(ctx, `UPDATE user_identifiers SET user_id=1 WHERE identifier='reader'`)
			case "credential-content":
				_, err = tx.ExecContext(ctx, `UPDATE password_credentials SET hash=zeroblob(32) WHERE user_id=?`, user.ID)
			case "user-version":
				_, err = tx.ExecContext(ctx, `UPDATE users SET version=version+1 WHERE id=?`, user.ID)
			case "credential-version":
				_, err = tx.ExecContext(ctx, `UPDATE password_credentials SET version=version+1 WHERE user_id=?`, user.ID)
			}
			require.NoError(t, err)
			_, err = candidate.principal(ctx, tx, 1)
			require.ErrorIs(t, err, ErrUnauthenticated)
		})
	}
}

func TestPasswordProofConcurrentCopiesShareIrreversibleInvalidation(t *testing.T) {
	catalog, user := passwordProofFixture(t)
	ctx := context.Background()
	_, proof := issuePasswordProof(t, catalog, "reader", 1)
	copyProof := *proof
	var hash []byte
	require.NoError(t, catalog.db.QueryRow(`SELECT hash FROM password_credentials WHERE user_id=?`, user.ID).Scan(&hash))
	defer clear(hash)
	_, err := catalog.db.Exec(`UPDATE password_credentials SET hash=zeroblob(32) WHERE user_id=?`, user.ID)
	require.NoError(t, err)
	const checks = 16
	start := make(chan struct{})
	results := make(chan error, checks)
	var workers sync.WaitGroup
	for i := range checks {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			selected := proof
			if i%2 == 0 {
				selected = &copyProof
			}
			_, err := catalog.RevalidatePasswordProof(ctx, selected)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
	_, err = catalog.db.Exec(`UPDATE password_credentials SET hash=? WHERE user_id=?`, hash, user.ID)
	require.NoError(t, err)
	for _, selected := range []*PasswordProof{proof, &copyProof} {
		_, err = catalog.RevalidatePasswordProof(ctx, selected)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
}
