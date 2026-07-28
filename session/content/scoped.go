package content

import (
	"context"

	api "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/content/proxy"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
)

// Scope restricts which attachable content store IDs (as registered via
// NewAttachable's stores map) are reachable through a session's Content
// service. It shares its implementation with session/secrets.Scope and
// friends via the generic session/scope package.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed store IDs and an optional
// alias map (child-visible store ID -> parent-visible store ID).
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// storeMeta enforces scope on every Content RPC (Info, Update, Walk, Delete,
// ListStatuses, Status, Abort, Writer, ReaderAt — a mix of unary and
// streaming calls), all of which carry the target store ID via outgoing
// metadata (GRPCHeaderID) rather than a request field.
var storeMeta = scope.MetadataScope{Key: GRPCHeaderID}

// FilteredCaller wraps a session.Caller and restricts which content store
// IDs are reachable, enforced at the gRPC transport level.
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCallerWithMeta(inner, childScope, parentScope, storeMeta)}
}

// NewCallerStore builds a content.Store for storeID, backed by the
// scope-enforcing connection. Always use this instead of NewCallerStore(...)
// from caller.go so store-ID restrictions are actually enforced.
func (fc *FilteredCaller) NewCallerStore(storeID string) content.Store {
	client := api.NewContentClient(fc.FilteredConn())
	return &callerContentStore{
		store:   proxy.NewContentStore(client),
		storeID: storeID,
		caller:  fc,
	}
}

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding *FilteredCaller instances scoped to
// Intersect(parentScope, childScope) for every underlying caller.
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope
	parentScope *Scope
}

// NewFilteredManager creates a FilteredManager. parentScope (may be nil) is
// the store-ID scope inherited from the parent build; childScope is
// declared by this subbuild.
func NewFilteredManager(inner *session.Manager, childScope, parentScope *Scope) *FilteredManager {
	return &FilteredManager{inner: inner, scope: childScope, parentScope: parentScope}
}

// Inner returns the underlying, unrestricted *session.Manager.
func (fm *FilteredManager) Inner() *session.Manager { return fm.inner }

// Scope returns the child scope this FilteredManager was constructed with.
func (fm *FilteredManager) Scope() *Scope { return fm.scope }

// Any implements session.CallerManager.
func (fm *FilteredManager) Any(ctx context.Context, g session.Group, f func(context.Context, string, session.Caller) error) error {
	return fm.inner.Any(ctx, g, func(ctx context.Context, id string, c session.Caller) error {
		return f(ctx, id, NewFilteredCaller(c, fm.scope, fm.parentScope))
	})
}

// EffectiveScope returns the computed effective scope for this manager.
func (fm *FilteredManager) EffectiveScope() *Scope {
	if fm.parentScope != nil {
		return Intersect(fm.parentScope, fm.scope)
	}
	return fm.scope
}

var _ session.CallerManager = (*FilteredManager)(nil)
