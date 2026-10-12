package instance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

var ErrUnavailable = errors.New("organization is not active and registered")
var ErrClosed = errors.New("instance registry is closed")

type Config struct {
	PrimaryServer      *server.Server
	OrganizationsRoot  string
	OrganizationLookup func(context.Context, int64) (identity.Organization, error)
	OrganizationList   func(context.Context) ([]identity.Organization, error)
	StorageFactory     func(context.Context, identity.Organization, string) (store.MetricBackend, error)
	ConfigureServer    func(context.Context, *server.Server, identity.Organization) error
	ManagePrimary      bool
}

type organizationServer struct {
	server       *server.Server
	cancel       context.CancelFunc
	done         chan struct{}
	stopping     bool
	shutdownDone chan struct{}
	shutdownErr  error
}

type Registry struct {
	mu           sync.Mutex
	config       Config
	root         string
	servers      map[int64]*organizationServer
	background   context.Context
	cancel       context.CancelFunc
	closed       bool
	shutdownDone chan struct{}
	shutdownErr  error
}

func New(config Config) (*Registry, error) {
	if config.PrimaryServer == nil || config.PrimaryServer.Store == nil || config.PrimaryServer.Store.OrganizationID() != 1 || config.OrganizationLookup == nil {
		return nil, errors.New("a primary organization 1 server and trusted organization lookup are required")
	}
	root := config.OrganizationsRoot
	if root == "" {
		primaryPath, err := filepath.Abs(config.PrimaryServer.Store.Path)
		if err != nil {
			return nil, err
		}
		root = filepath.Join(filepath.Dir(primaryPath), "organizations")
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	return &Registry{config: config, root: root, servers: map[int64]*organizationServer{1: {server: config.PrimaryServer}}}, nil
}

func (r *Registry) Primary() *server.Server { return r.config.PrimaryServer }
func (r *Registry) Root() string            { return r.root }

// Resolve is an internal dispatch operation. A header/path alone must never be
// passed here as an authenticated organization identity by an HTTP adapter.
func (r *Registry) Resolve(ctx context.Context, id int64) (*server.Server, error) {
	org, err := r.config.OrganizationLookup(ctx, id)
	if err != nil || org.ID != id || org.State != identity.OrganizationActive {
		return nil, ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	entry, err := r.openLocked(ctx, org)
	if err != nil {
		return nil, err
	}
	if r.background != nil && (id != 1 || r.config.ManagePrimary) {
		r.startLocked(entry)
	}
	return entry.server, nil
}

// ResolvePrincipal dispatches only a validated, internally supplied principal.
func (r *Registry) ResolvePrincipal(ctx context.Context) (*server.Server, error) {
	p, ok := identity.FromContext(ctx)
	if !ok {
		return nil, identity.ErrUnauthenticated
	}
	if !p.Role.AtLeast(identity.RoleViewer) {
		return nil, identity.ErrForbidden
	}
	return r.Resolve(ctx, p.OrgID)
}

// Initialize prepares a directory for a durable creating organization. The
// identity store must activate it after success; this method cannot publish it.
func (r *Registry) Initialize(ctx context.Context, id int64) (*server.Server, error) {
	org, err := r.config.OrganizationLookup(ctx, id)
	if err != nil || org.ID != id || org.State != identity.OrganizationCreating || id <= 1 {
		return nil, ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	entry, err := r.openLocked(ctx, org)
	if err != nil {
		return nil, err
	}
	return entry.server, nil
}

func (r *Registry) openLocked(ctx context.Context, org identity.Organization) (*organizationServer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entry := r.servers[org.ID]; entry != nil {
		if entry.stopping {
			return nil, ErrUnavailable
		}
		return entry, nil
	}
	path, err := organizationPath(r.root, org.ID)
	if err != nil {
		return nil, err
	}
	control, err := store.OpenForOrganization(path, org.ID)
	if err != nil {
		return nil, err
	}
	if err := control.BindPluginRegistry(r.config.PrimaryServer.Store); err != nil {
		control.DB.Close()
		return nil, err
	}
	if r.config.StorageFactory != nil {
		control.Backend, err = r.config.StorageFactory(ctx, org, path)
	} else if primary, ok := r.config.PrimaryServer.Store.Backend.(*store.ClickHouse); ok {
		var database string
		database, err = ClickHouseDatabase(primary.Database, org.ID)
		if err == nil {
			control.Backend, err = store.OpenClickHouse(ctx, primary.URL, database, primary.User, primary.Password, primary.RetentionDays)
		}
	} else if r.config.PrimaryServer.Store.Backend != nil {
		err = errors.New("organization storage requires a configured backend factory")
	}
	if err != nil {
		control.DB.Close()
		return nil, err
	}
	if err := r.validateBackendLocked(control.Backend, org.ID); err != nil {
		control.DB.Close()
		return nil, err
	}
	primary := r.config.PrimaryServer
	child := server.New(control, primary.Token, primary.RetentionDays)
	child.Plugins.Close()
	allowed := []string{}
	for id, enabled := range primary.Plugins.AllowUnsigned {
		if enabled {
			allowed = append(allowed, id)
		}
	}
	child.Plugins = plugins.New(control, primary.Plugins.Root, allowed)
	child.Plugins.RootURL = primary.Plugins.RootURL
	if r.config.ConfigureServer != nil {
		err = r.config.ConfigureServer(ctx, child, org)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (child.Store != control || child.Plugins == nil || child.Plugins.Store != control) {
		err = errors.New("organization configuration cannot replace its store or plugin manager")
	}
	if err != nil {
		closeServer(child)
		return nil, err
	}
	entry := &organizationServer{server: child}
	r.servers[org.ID] = entry
	return entry, nil
}

func (r *Registry) startLocked(entry *organizationServer) {
	if entry.done != nil || entry.stopping || r.background.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.background)
	entry.cancel = cancel
	entry.done = make(chan struct{})
	ctx = identity.WithPrincipal(ctx, identity.BackgroundPrincipal(entry.server.Store.OrganizationID()))
	go func() { defer close(entry.done); entry.server.RunBackground(ctx) }()
}

// RunBackground starts active registered organizations and owns their shutdown.
// ManagePrimary is explicit so an existing CLI primary worker is never doubled.
func (r *Registry) RunBackground(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if r.background != nil {
		r.mu.Unlock()
		return errors.New("instance background lifecycle already started")
	}
	r.background, r.cancel = context.WithCancel(ctx)
	if r.config.ManagePrimary {
		r.startLocked(r.servers[1])
	}
	r.mu.Unlock()
	// Failed startup owns the same cleanup as a normal canceled lifecycle.
	defer r.Close(context.Background())
	if r.config.OrganizationList != nil {
		orgs, err := r.config.OrganizationList(ctx)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if org.State == identity.OrganizationActive && org.ID != 1 {
				if _, err := r.Resolve(ctx, org.ID); err != nil {
					return fmt.Errorf("start organization %d: %w", org.ID, err)
				}
			}
		}
	}
	<-r.background.Done()
	return r.Close(context.Background())
}

// Stop requires the durable directory to be disabled/deleting first, so a
// concurrent resolver cannot silently recreate an active server being stopped.
func (r *Registry) Stop(ctx context.Context, id int64) error {
	if id == 1 {
		return errors.New("the primary organization has instance-owned resources")
	}
	org, err := r.config.OrganizationLookup(ctx, id)
	if err != nil || org.ID != id || (org.State != identity.OrganizationDisabled && org.State != identity.OrganizationDeleting) {
		return ErrUnavailable
	}
	r.mu.Lock()
	entry := r.servers[id]
	if entry == nil {
		r.mu.Unlock()
		return nil
	}
	r.beginStopLocked(id, entry)
	r.mu.Unlock()
	return waitShutdown(ctx, entry)
}

func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.cancel != nil {
			r.cancel()
		}
		entries := []*organizationServer{}
		var primary *organizationServer
		for id, entry := range r.servers {
			if id != 1 {
				r.beginStopLocked(id, entry)
				entries = append(entries, entry)
			} else if r.config.ManagePrimary {
				primary = entry
				if entry.cancel != nil {
					entry.cancel()
				}
			}
		}
		r.shutdownDone = make(chan struct{})
		go func() {
			var result error
			for _, entry := range entries {
				result = errors.Join(result, waitShutdown(context.Background(), entry))
			}
			// Child stores read instance plugin metadata from the primary store.
			// Keep that store available until every child worker has exited.
			if primary != nil {
				r.mu.Lock()
				r.beginStopLocked(1, primary)
				r.mu.Unlock()
				result = errors.Join(result, waitShutdown(context.Background(), primary))
			}
			r.shutdownErr = result
			close(r.shutdownDone)
		}()
	}
	done := r.shutdownDone
	r.mu.Unlock()
	select {
	case <-done:
		return r.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancellation of a caller only bounds its wait: cleanup continues and future
// callers can wait for the same completion. Keep the stopping entry registered
// until its workers and connections close, so reactivation cannot double-open it.
func (r *Registry) beginStopLocked(id int64, entry *organizationServer) {
	if entry.stopping {
		return
	}
	entry.stopping = true
	entry.shutdownDone = make(chan struct{})
	if entry.cancel != nil {
		entry.cancel()
	}
	go func() {
		if entry.done != nil {
			<-entry.done
		}
		entry.shutdownErr = closeServer(entry.server)
		r.mu.Lock()
		if r.servers[id] == entry {
			delete(r.servers, id)
		}
		r.mu.Unlock()
		close(entry.shutdownDone)
	}()
}

func waitShutdown(ctx context.Context, entry *organizationServer) error {
	select {
	case <-entry.shutdownDone:
		return entry.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) validateBackendLocked(backend store.MetricBackend, id int64) error {
	if backend == nil {
		return nil
	}
	value := reflect.ValueOf(backend)
	for _, entry := range r.servers {
		other := entry.server.Store.Backend
		if other == nil {
			continue
		}
		otherValue := reflect.ValueOf(other)
		if value.Kind() == reflect.Pointer && otherValue.Kind() == reflect.Pointer && value.Type() == otherValue.Type() && value.Pointer() == otherValue.Pointer() {
			return errors.New("organization storage must not share a backend instance")
		}
		if ch, ok := backend.(*store.ClickHouse); ok {
			if existing, ok := other.(*store.ClickHouse); ok && ch.Database == existing.Database {
				return errors.New("organizations require distinct ClickHouse databases")
			}
		}
	}
	if ch, ok := backend.(*store.ClickHouse); ok {
		if _, err := ClickHouseDatabase(ch.Database, 1); err != nil {
			return err
		}
		if ch.Database == "" {
			return fmt.Errorf("organization %d has no ClickHouse database", id)
		}
	}
	return nil
}

func closeServer(s *server.Server) error {
	s.Live.Close()
	s.Plugins.Close()
	s.CloseSQLSources()
	return s.Store.DB.Close()
}
