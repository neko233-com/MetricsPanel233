package store

import (
	"context"
	"database/sql"
	"sort"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
)

// LibraryFolders returns the distinct authoritative owners recorded in a fence.
// An empty folder denotes a General library; an empty slice denotes no libraries.
// These owners still need service-level library actions, independently of the
// consuming dashboard's permissions.
func (f PermissionMutationFence) LibraryFolders() []string {
	owners := map[string]bool{}
	for _, library := range f.libraries {
		if library.id > 0 {
			owners[library.folder] = true
		}
	}
	out := make([]string, 0, len(owners))
	for owner := range owners {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// RevalidatePermissionMutationFence also supports a read response that was
// assembled after capture. It checks the actual credential, membership and
// resource snapshot before those bytes are sent. Identity and control databases
// do not share an atomic transaction; this does not hold catalog callbacks under
// an organization database transaction or replace mutation-time checks.
func (s *Store) RevalidatePermissionMutationFence(ctx context.Context, f PermissionMutationFence) error {
	return s.RevalidatePermissionReadFences(ctx, []PermissionMutationFence{f})
}

// RevalidatePermissionReadFences completes all catalog callbacks first, then
// checks every observed control snapshot in one final read transaction. A later
// fence's actor callback must not be able to change an earlier checked ACL.
// No identity callback runs after this control check.
func (s *Store) RevalidatePermissionReadFences(ctx context.Context, fences []PermissionMutationFence) error {
	for _, f := range fences {
		if err := s.validatePermissionFence(ctx, f); err != nil {
			return err
		}
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT organization_id FROM store_identity WHERE id=1`).Scan(&owner); err != nil {
		return err
	}
	if owner != s.OrganizationID() {
		return identity.ErrForbidden
	}
	for _, f := range fences {
		if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
			return err
		}
	}
	return nil
}
