package store

import (
	"context"
	"encoding/json"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// SaveLibraryElementWithPermissions commits an explicitly captured library
// create or update. The service must authorize library actions on its actual
// owner and, when creating or moving, its destination. The fence validates the
// actor and resource snapshot; it does not grant those actions itself.
//
// The service generates a new UID before CapturePermissionMutation so creation
// is fenced by a captured absent identity. Libraries inherit folder/General
// authority and receive no independent dashboard/folder ACL.
// An optional input ID must match the captured incarnation on update. A draft
// containing only UID/version does not gain a cross-request incarnation token.
func (s *Store) SaveLibraryElementWithPermissions(ctx context.Context, e model.LibraryElement, create bool, f PermissionMutationFence) (model.LibraryElement, error) {
	// RawMessage is caller-owned memory; freeze it before any resolver callback.
	e.Model = append(json.RawMessage(nil), e.Model...)
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return e, err
	}
	if e.OrgID != 0 && e.OrgID != s.OrganizationID() {
		return e, identity.ErrForbidden
	}
	e.FolderUID = model.RootFolderUID(e.FolderUID)
	if err := e.Validate(); err != nil {
		return e, err
	}
	old, captured := f.libraries[e.UID]
	if e.UID == "" || !captured {
		return e, ErrPermissionFence
	}
	if create {
		if old.id != 0 {
			return e, ErrResourceExists
		}
	} else {
		if old.id == 0 {
			return e, ErrPermissionFence
		}
		if e.Version <= 0 || e.Version != old.version || e.ID != 0 && e.ID != old.id {
			return e, ErrResourceConflict
		}
		if err := s.requirePermissionRef(f, model.PermissionFolder, old.folder); err != nil {
			return e, err
		}
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, e.FolderUID); err != nil {
		return e, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return e, err
	}
	if e.FolderUID != "" {
		if err = validatePermissionFolderParent(ctx, tx, "", e.FolderUID); err != nil {
			return e, err
		}
	}
	e, err = saveLibraryTx(ctx, tx, e, create)
	if err != nil {
		return e, err
	}
	e.OrgID = s.OrganizationID()
	return e, tx.Commit()
}

// DeleteLibraryElementWithPermissions fences the actual library incarnation,
// owner ancestry and caller before deleting. Connections are checked inside the
// same transaction; even a connection added after capture prevents deletion.
// This protects capture-to-write races, not an old version-only client draft
// authorized after a UID has already been deleted and recreated.
func (s *Store) DeleteLibraryElementWithPermissions(ctx context.Context, uid string, version int64, f PermissionMutationFence) error {
	if err := s.validatePermissionFence(ctx, f); err != nil {
		return err
	}
	old, captured := f.libraries[uid]
	if uid == "" || !captured || old.id == 0 {
		return ErrPermissionFence
	}
	if version <= 0 || version != old.version {
		return ErrResourceConflict
	}
	if err := s.requirePermissionRef(f, model.PermissionFolder, old.folder); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkPermissionFenceTx(ctx, tx, f); err != nil {
		return err
	}
	e, err := libraryByUID(ctx, tx, uid)
	if err != nil {
		return err
	}
	if e.Meta.ConnectedDashboards > 0 {
		return ErrLibraryConnected
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM library_elements WHERE id=? AND uid=? AND version=?`, old.id, uid, version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrResourceConflict
	}
	return tx.Commit()
}
