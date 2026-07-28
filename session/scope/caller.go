package scope

import (
	"context"

	"github.com/moby/buildkit/session"
	"google.golang.org/grpc"
)

// FilteredCaller wraps a session.Caller and restricts access to a set of
// gRPC methods according to Scope, enforced at the transport level — this
// means the restriction cannot be bypassed by swapping the context (e.g.
// context.Background()) the way a context-value-based check could be.
//
// Service packages (secrets, auth, ...) embed *FilteredCaller in their own
// FilteredCaller type to add a service-specific typed client accessor (e.g.
// SecretsClient(), AuthClient()) built on top of FilteredConn().
type FilteredCaller struct {
	inner session.Caller
	scope *Scope[string]
	conn  *FilteredConn
}

// NewFilteredCaller wraps inner with effective scope
// Intersect(parentScope, childScope) (or just childScope if parentScope is
// nil), enforced for the given method rules.
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope[string], methods ...MethodScope) *FilteredCaller {
	effective := childScope
	if parentScope != nil {
		effective = Intersect(parentScope, childScope)
	}
	return &FilteredCaller{
		inner: inner,
		scope: effective,
		conn:  NewFilteredConn(inner.Conn(), effective, methods...),
	}
}

// Context implements session.Caller.
func (fc *FilteredCaller) Context(ctx context.Context) context.Context { return fc.inner.Context(ctx) }

// Supports implements session.Caller.
func (fc *FilteredCaller) Supports(method string) bool { return fc.inner.Supports(method) }

// Conn implements session.Caller. It returns the underlying, unfiltered
// connection so FilteredCaller still satisfies session.Caller for services
// that don't need scope enforcement. Use FilteredConn() to build clients
// for the scoped service.
func (fc *FilteredCaller) Conn() *grpc.ClientConn { return fc.inner.Conn() }

// SharedKey implements session.Caller.
func (fc *FilteredCaller) SharedKey() string { return fc.inner.SharedKey() }

// Scope returns the effective scope of this caller.
func (fc *FilteredCaller) Scope() *Scope[string] { return fc.scope }

// Inner returns the wrapped, unrestricted session.Caller.
func (fc *FilteredCaller) Inner() session.Caller { return fc.inner }

// FilteredConn returns a grpc.ClientConnInterface enforcing the scope; use
// it to construct service-specific clients instead of Conn().
func (fc *FilteredCaller) FilteredConn() grpc.ClientConnInterface { return fc.conn }

var _ session.Caller = (*FilteredCaller)(nil)

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding a *FilteredCaller (scoped to
// Intersect(parentScope, childScope)) for every underlying caller. This
// keeps nested-build scoping safe: a child can never escalate beyond what
// its parent already allowed.
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope[string]
	parentScope *Scope[string]
	methods     []MethodScope
}

// NewFilteredManager creates a FilteredManager. parentScope (may be nil) is
// the scope inherited from the parent; childScope is declared by this
// caller. methods are the MethodScope rules to enforce for every yielded
// caller.
func NewFilteredManager(inner *session.Manager, childScope, parentScope *Scope[string], methods ...MethodScope) *FilteredManager {
	return &FilteredManager{inner: inner, scope: childScope, parentScope: parentScope, methods: methods}
}

// Inner returns the underlying, unrestricted *session.Manager.
func (fm *FilteredManager) Inner() *session.Manager { return fm.inner }

// Scope returns the child scope this FilteredManager was constructed with.
func (fm *FilteredManager) Scope() *Scope[string] { return fm.scope }

// Any implements session.CallerManager.
func (fm *FilteredManager) Any(ctx context.Context, g session.Group, f func(context.Context, string, session.Caller) error) error {
	return fm.inner.Any(ctx, g, func(ctx context.Context, id string, c session.Caller) error {
		filtered := NewFilteredCaller(c, fm.scope, fm.parentScope, fm.methods...)
		return f(ctx, id, filtered)
	})
}

// EffectiveScope returns the computed effective scope for this manager.
func (fm *FilteredManager) EffectiveScope() *Scope[string] {
	if fm.parentScope != nil {
		return Intersect(fm.parentScope, fm.scope)
	}
	return fm.scope
}

var _ session.CallerManager = (*FilteredManager)(nil)
