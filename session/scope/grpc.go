package scope

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MethodScope describes how to enforce a Scope[string] for a single unary
// gRPC method: how to pull the requested key out of the request message,
// and how to rewrite the request with the resolved (parent) key before
// forwarding it — this is what makes aliasing transparent to the callee.
//
// Every scoped session type (secrets, auth, ...) only needs to supply one of
// these per method; all allow-list/alias/intersection semantics come from
// Scope itself.
type MethodScope struct {
	// FullMethod is the gRPC method name this rule applies to, e.g.
	// "/moby.buildkit.secrets.v1.Secrets/GetSecret".
	FullMethod string
	// GetKey extracts the requested key from the request message.
	GetKey func(req any) string
	// WithKey returns a copy of the request message with the resolved key
	// substituted for the originally requested key.
	WithKey func(req any, key string) any
}

// FilteredConn wraps a grpc.ClientConnInterface and enforces a Scope[string]
// against a set of MethodScope rules. Invocations of methods with no
// matching rule pass through unfiltered. Streaming methods are not filtered
// here (their keys typically travel via context metadata rather than the
// request message); wrap the context/metadata separately if needed.
type FilteredConn struct {
	inner   grpc.ClientConnInterface
	scope   *Scope[string]
	methods map[string]MethodScope
}

// NewFilteredConn builds a FilteredConn enforcing scope over inner,
// according to the given method rules.
func NewFilteredConn(inner grpc.ClientConnInterface, scope *Scope[string], methods ...MethodScope) *FilteredConn {
	m := make(map[string]MethodScope, len(methods))
	for _, ms := range methods {
		m[ms.FullMethod] = ms
	}
	return &FilteredConn{inner: inner, scope: scope, methods: m}
}

func (fc *FilteredConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	if ms, ok := fc.methods[method]; ok {
		key := ms.GetKey(args)
		resolved, allowed := fc.scope.Resolve(key)
		if !allowed {
			return status.Errorf(codes.NotFound, "%s: not found", key)
		}
		args = ms.WithKey(args, resolved)
	}
	return fc.inner.Invoke(ctx, method, args, reply, opts...)
}

func (fc *FilteredConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return fc.inner.NewStream(ctx, desc, method, opts...)
}

var _ grpc.ClientConnInterface = (*FilteredConn)(nil)
