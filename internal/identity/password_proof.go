package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"errors"
	"sync"
)

// PasswordProof is a request-local proof of an actual password verification.
// It cannot be serialized, constructed from an identity, or used by another
// Store. Copies share invalidation. HTTP adapters must keep it in their private
// request context and must not cache it across requests.
type PasswordProof struct {
	state *passwordProofState
}

type passwordProofState struct {
	mu                sync.Mutex
	issuer            *Store
	alias             string
	userID            int64
	orgID             int64
	credentialVersion int64
	fingerprint       [32]byte
	generation        int64
	invalidated       bool
}

// Called only while mu is held. An interrupted read cannot revoke a proof.
func (state *passwordProofState) invalidate(ctx context.Context) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	state.invalidated = true
	return Principal{}, ErrUnauthenticated
}

func (PasswordProof) String() string   { return "[redacted password proof]" }
func (PasswordProof) GoString() string { return "[redacted password proof]" }
func (PasswordProof) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted password proof]"`), nil
}
func (*PasswordProof) UnmarshalJSON([]byte) error { return ErrUnauthenticated }

// Only a fingerprint remains after a credential read. Salt and verifier bytes
// are cleared before returning and are never retained in a proof or Principal.
type passwordCredentialState struct {
	userID, userVersion, credentialVersion int64
	disabled                               bool
	fingerprint                            [32]byte
}

func passwordCredentialFingerprint(format string, salt, hash []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("MetricsPanel233.password-proof.v1\x00"))
	var size [8]byte
	for _, field := range [][]byte{[]byte(format), salt, hash} {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		h.Write(size[:])
		h.Write(field)
	}
	var fingerprint [32]byte
	copy(fingerprint[:], h.Sum(nil))
	return fingerprint
}

func samePasswordFingerprint(a, b [32]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func passwordCredentialForAlias(ctx context.Context, tx *sql.Tx, alias string) (passwordCredentialState, error) {
	var current passwordCredentialState
	var format string
	var salt, hash []byte
	defer func() { clear(salt); clear(hash) }()
	err := tx.QueryRowContext(ctx, `SELECT u.id,u.version,c.version,u.disabled,c.format,c.salt,c.hash FROM user_identifiers i JOIN users u ON u.id=i.user_id JOIN password_credentials c ON c.user_id=u.id WHERE i.identifier=?`, alias).Scan(&current.userID, &current.userVersion, &current.credentialVersion, &current.disabled, &format, &salt, &hash)
	if err != nil {
		return passwordCredentialState{}, err
	}
	current.fingerprint = passwordCredentialFingerprint(format, salt, hash)
	return current, nil
}

// AuthenticatePasswordWithProof does the same real password and current-state
// checks as AuthenticatePassword. Expensive Argon2 work runs before the short
// transaction that binds the original alias, credentials and selected org.
func (s *Store) AuthenticatePasswordWithProof(ctx context.Context, login, password string, requestedOrgID int64) (Principal, *PasswordProof, error) {
	if s == nil || ctx == nil {
		return Principal{}, nil, ErrUnauthenticated
	}
	candidate, err := s.passwordCandidate(ctx, login, password)
	if err != nil {
		return Principal{}, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, nil, err
	}
	defer tx.Rollback()
	p, err := candidate.principal(ctx, tx, requestedOrgID)
	if err != nil {
		return Principal{}, nil, err
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM organization_memberships WHERE org_id=? AND user_id=? AND state='active'`, p.OrgID, p.ID).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrUnauthenticated
		}
		return Principal{}, nil, err
	}
	if generation <= 0 {
		return Principal{}, nil, ErrUnauthenticated
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, nil, err
	}
	return p, &PasswordProof{state: &passwordProofState{issuer: s, alias: candidate.alias, userID: p.ID, orgID: p.OrgID, credentialVersion: candidate.credentialVersion, fingerprint: candidate.fingerprint, generation: generation}}, nil
}

// RevalidatePasswordProof reads current alias ownership, exact credentials and
// authorization on every call without Argon2. A definitive mismatch burns the
// shared proof permanently. Canceled operations and DB errors do not burn it.
// A byte-identical raw database rollback completed between reads is inherently
// unobservable; normal password changes advance the durable credential version.
func (s *Store) RevalidatePasswordProof(ctx context.Context, proof *PasswordProof) (Principal, error) {
	if s == nil || ctx == nil || proof == nil || proof.state == nil || proof.state.issuer != s {
		return Principal{}, ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	state := proof.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.invalidated {
		return Principal{}, ErrUnauthenticated
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	current, err := passwordCredentialForAlias(ctx, tx, state.alias)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (current.disabled || current.userID != state.userID || current.credentialVersion != state.credentialVersion || !samePasswordFingerprint(current.fingerprint, state.fingerprint)) {
		return state.invalidate(ctx)
	}
	if err != nil {
		return Principal{}, err
	}
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM organization_memberships WHERE org_id=? AND user_id=? AND state='active'`, state.orgID, state.userID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) || err == nil && generation != state.generation {
		return state.invalidate(ctx)
	}
	if err != nil {
		return Principal{}, err
	}
	p, err := principalForUser(ctx, tx, state.userID, state.orgID)
	if errors.Is(err, ErrUnauthenticated) {
		return state.invalidate(ctx)
	}
	if err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, err
	}
	return p, nil
}
