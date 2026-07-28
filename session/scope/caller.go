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
// Service packages (secrets, auth, sshforward, filesync, upload, content,
// ...) embed *FilteredCaller in their own FilteredCaller type to add a
// service-specific typed client accessor (e.g. SecretsClient(), AuthClient(),
// SSHClient()) built on top of FilteredConn().
type FilteredCaller struct {
	inner session.Caller
	scope *Scope[string]
	conn  *FilteredConn
}

// NewFilteredCaller wraps inner with effective scope
// Intersect(parentScope, childScope) (or just childScope if parentScope is
// nil), enforced for the given message-based method rules.
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

// NewFilteredCallerWithMeta is like NewFilteredCaller but also (or instead)
// enforces a MetadataScope fallback, for services that key their target
// resource via outgoing gRPC metadata rather than a request field — this
// covers unary calls keyed by metadata as well as streaming RPCs.
func NewFilteredCallerWithMeta(inner session.Caller, childScope, parentScope *Scope[string], meta MetadataScope, methods ...MethodScope) *FilteredCaller {
	effective := childScope
	if parentScope != nil {
		effective = Intersect(parentScope, childScope)
	}
	return &FilteredCaller{
		inner: inner,
		scope: effective,
		conn:  NewFilteredMetaConn(inner.Conn(), effective, meta, methods...),
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
