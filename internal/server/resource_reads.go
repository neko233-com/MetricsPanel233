package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

// This deliberately admits only the implemented current-resource read surface.
// History requires Write; query execution, imports and mutations are separate
// policies and are not made available by this read adapter.
var resourceReadRoutes = map[string]bool{
	"GET /api/v1/dashboards":             true,
	"GET /api/dashboards/uid/{uid}":      true,
	"GET /api/search":                    true,
	"GET /api/v1/folders":                true,
	"GET /api/folders":                   true,
	"GET /api/folders/{uid}":             true,
	"GET /api/folders/{uid}/parents":     true,
	"GET /api/v1/dashboard-exports/{id}": true,
}

func ResourceReadRoute(route ProtectedRoute) bool {
	return route.Surface == RouteAPI && resourceReadRoutes[route.Pattern]
}

type resourceReadContextKey struct{}

type resourceReadContext struct {
	owner     *Server
	route     ProtectedRoute
	principal identity.Principal
	resolver  store.PermissionIdentityResolver
	mu        sync.Mutex
	fences    []store.PermissionMutationFence
}

func sameReadIdentity(a, b identity.Principal) bool {
	return a.OrgID == b.OrgID && a.Type == b.Type && a.ID == b.ID
}

// BindResourceReadAuthorization is called by a credential-backed route policy
// after mux selection. Its state is private, scoped to this exact server and
// registration, and cannot be supplied by JSON, a role or a legacy workspace key.
// It does not authorize any row: handlers check actual resources before expanding
// libraries and before filtering/pagination. Configure before building Handler.
func (s *Server) BindResourceReadAuthorization(r *http.Request, route ProtectedRoute, resolver store.PermissionIdentityResolver) error {
	if s == nil || s.Store == nil || r == nil || !ResourceReadRoute(route) || route.Pattern != r.Pattern || resolver.CurrentPrincipal == nil || resolver.ValidateSubject == nil || resolver.TeamsForPrincipal == nil {
		return identity.ErrForbidden
	}
	p, ok := identity.FromContext(r.Context())
	if !ok || p.Validate() != nil || p.Type != identity.TypeUser && p.Type != identity.TypeServiceAccount {
		return identity.ErrUnauthenticated
	}
	if err := p.RequireOrganization(s.OrganizationID()); err != nil {
		return identity.ErrForbidden
	}
	fresh, err := resolver.CurrentPrincipal(r.Context(), p)
	if err != nil {
		return err
	}
	if fresh.Validate() != nil || !sameReadIdentity(p, fresh) {
		return identity.ErrUnauthenticated
	}
	state := &resourceReadContext{owner: s, route: route, principal: fresh, resolver: resolver}
	*r = *r.WithContext(context.WithValue(r.Context(), resourceReadContextKey{}, state))
	return nil
}

func (s *Server) resourceReadAccess(ctx context.Context) (*resourceReadContext, error) {
	if s.Authorizer == nil {
		return nil, nil
	}
	state, ok := ctx.Value(resourceReadContextKey{}).(*resourceReadContext)
	p, present := identity.FromContext(ctx)
	if !ok || state == nil || state.owner != s || !ResourceReadRoute(state.route) || !present || !sameReadIdentity(p, state.principal) {
		return nil, identity.ErrForbidden
	}
	return state, nil
}

func (state *resourceReadContext) remember(fence store.PermissionMutationFence) {
	state.mu.Lock()
	state.fences = append(state.fences, fence)
	state.mu.Unlock()
}

// Recursively expanded models can reference libraries not recorded as direct
// dashboard connections. Check each actual lookup, including nested models.
func (s *Server) authorizeLibraryRead(ctx context.Context, uid string) error {
	state, err := s.resourceReadAccess(ctx)
	if err != nil || state == nil {
		return err
	}
	fence, err := s.Store.CapturePermissionMutation(ctx, nil, state.resolver, uid)
	if err != nil {
		return err
	}
	owners := fence.LibraryFolders()
	if len(owners) == 0 {
		return sql.ErrNoRows
	}
	for _, folder := range owners {
		if folder == "" {
			if !state.principal.Role.AtLeast(identity.RoleViewer) {
				return identity.ErrForbidden
			}
			continue
		}
		effective, err := fence.Effective(model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: folder})
		if err != nil {
			return err
		}
		if !effective.Permission.Includes(model.PermissionView) {
			return identity.ErrForbidden
		}
	}
	state.remember(fence)
	return nil
}

func (s *Server) readableDashboard(ctx context.Context, candidate model.Dashboard, expandLibraries bool) (model.Dashboard, bool, error) {
	state, err := s.resourceReadAccess(ctx)
	if err != nil || state == nil {
		return candidate, err == nil, err
	}
	ref := model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionDashboard, UID: candidate.ID}
	fence, err := s.Store.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, state.resolver)
	if err != nil {
		return candidate, false, err
	}
	effective, err := fence.Effective(ref)
	if err != nil || !effective.Permission.Includes(model.PermissionView) {
		return candidate, false, err
	}
	if expandLibraries {
		for _, folder := range fence.LibraryFolders() {
			if folder == "" {
				// The virtual General owner has its own basic-role library Read.
				// A direct dashboard grant is not a grant on that library.
				if !state.principal.Role.AtLeast(identity.RoleViewer) {
					return candidate, false, nil
				}
				continue
			}
			owner, err := fence.Effective(model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: folder})
			if err != nil || !owner.Permission.Includes(model.PermissionView) {
				return candidate, false, err
			}
		}
	}
	// Re-read after capture so an old candidate cannot contribute stale content
	// when a same-ID resource was recreated during catalog callbacks.
	dashboard, err := s.Store.Dashboard(ctx, candidate.ID)
	if err != nil {
		return candidate, false, err
	}
	if effective.Versions["resource:dashboard:"+dashboard.ID] != dashboard.UpdatedAt {
		return candidate, false, store.ErrPermissionFence
	}
	state.remember(fence)
	return dashboard, true, nil
}

func (s *Server) readableFolder(ctx context.Context, candidate model.Folder) (model.Folder, bool, error) {
	state, err := s.resourceReadAccess(ctx)
	if err != nil || state == nil {
		return candidate, err == nil, err
	}
	if model.RootFolderUID(candidate.UID) == "" {
		candidate.CanSave, candidate.CanEdit, candidate.CanAdmin, candidate.CanDelete = false, false, false, false
		return candidate, true, nil // Virtual metadata, never a fabricated ACL row.
	}
	ref := model.PermissionResource{OrgID: s.OrganizationID(), Kind: model.PermissionFolder, UID: candidate.UID}
	fence, err := s.Store.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, state.resolver)
	if err != nil {
		return candidate, false, err
	}
	effective, err := fence.Effective(ref)
	if err != nil || !effective.Permission.Includes(model.PermissionView) {
		return candidate, false, err
	}
	folder, err := s.Store.Folder(ctx, candidate.UID)
	if err != nil {
		return candidate, false, err
	}
	if effective.Versions["resource:folder:"+folder.UID] != folder.Version {
		return candidate, false, store.ErrPermissionFence
	}
	folder.CanSave, folder.CanEdit, folder.CanDelete = effective.Permission.Includes(model.PermissionEdit), effective.Permission.Includes(model.PermissionEdit), effective.Permission.Includes(model.PermissionEdit)
	folder.CanAdmin = effective.Permission.Includes(model.PermissionAdmin)
	state.remember(fence)
	return folder, true, nil
}

func (s *Server) filterReadableFolders(ctx context.Context, folders []model.Folder) ([]model.Folder, error) {
	out := make([]model.Folder, 0, len(folders))
	for _, candidate := range folders {
		folder, allowed, err := s.readableFolder(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, folder)
		}
	}
	return out, nil
}

func writeResourceReadError(w http.ResponseWriter, err error) {
	var api *grafanaAPIError
	if errors.As(err, &api) {
		writeGrafanaError(w, api)
		return
	}
	status, message := http.StatusServiceUnavailable, "Resource authorization is unavailable"
	switch {
	case errors.Is(err, identity.ErrSessionNeedsRotation), errors.Is(err, identity.ErrUnauthenticated):
		status, message = http.StatusUnauthorized, "Authentication required"
	case errors.Is(err, identity.ErrForbidden):
		status, message = http.StatusForbidden, "Permission denied"
	case errors.Is(err, sql.ErrNoRows):
		status, message = http.StatusNotFound, "Resource not found"
	case errors.Is(err, store.ErrPermissionFence):
		status, message = http.StatusConflict, "Resource permissions changed; reload and retry"
	}
	writeJSON(w, status, map[string]string{"message": message})
}

// These admitted routes return finite JSON, never streams or WebSockets. Buffer
// their responses so revocation/fence failure cannot follow already exposed data.
const resourceReadResponseBudget = 32 << 20

type resourceReadResponse struct {
	header http.Header
	status int
	data   bytes.Buffer
	err    error
}

func (w *resourceReadResponse) Header() http.Header { return w.header }
func (w *resourceReadResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *resourceReadResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.err != nil {
		return 0, w.err
	}
	if len(data) > resourceReadResponseBudget-w.data.Len() {
		w.err = errors.New("resource response exceeds its budget")
		return 0, w.err
	}
	return w.data.Write(data)
}

func (s *Server) serveResourceRead(w http.ResponseWriter, r *http.Request, route ProtectedRoute, handler http.Handler) {
	state, err := s.resourceReadAccess(r.Context())
	if err != nil || state == nil || state.route != route {
		writeResourceReadError(w, identity.ErrForbidden)
		return
	}
	response := &resourceReadResponse{header: w.Header().Clone()}
	handler.ServeHTTP(response, r)
	if response.err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"message": "Resource response exceeds the 32 MiB budget"})
		return
	}
	if response.status == 0 {
		response.status = http.StatusOK
	}
	if response.status >= 200 && response.status < 300 {
		fresh, err := state.resolver.CurrentPrincipal(r.Context(), state.principal)
		if err == nil && (fresh.Validate() != nil || !sameReadIdentity(fresh, state.principal) || fresh.Role != state.principal.Role || fresh.IsServerAdmin != state.principal.IsServerAdmin) {
			err = store.ErrPermissionFence
		}
		state.mu.Lock()
		fences := append([]store.PermissionMutationFence{}, state.fences...)
		state.mu.Unlock()
		if err == nil {
			err = s.Store.RevalidatePermissionReadFences(r.Context(), fences)
		}
		if err != nil {
			writeResourceReadError(w, err)
			return
		}
	}
	for key := range w.Header() {
		delete(w.Header(), key)
	}
	for key, values := range response.header {
		w.Header()[key] = append([]string{}, values...)
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.data.Bytes())
}
