package scope

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MethodScope describes how to enforce a Scope[string] for a single unary
// gRPC method by inspecting/rewriting the request *message*: how to pull
// the requested key out of it, and how to rewrite it with the resolved
// (parent) key before forwarding — this is what makes aliasing transparent
// to the callee. Use this when the resource key is a field on the request
// (e.g. secret ID, registry host).
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

// MetadataScope describes how to enforce a Scope[string] using a single
// value carried in the outgoing gRPC *metadata* rather than a request field.
// This suits services that identify their target resource out-of-band —
// e.g. content stores (a header), filesync (a "dir-name"/"exporter-id"
// header), ssh agent forwarding (a header, defaulting to "default"), upload
// (a "urlpath" header) — including over streaming RPCs, where there's no
// single "request message" to rewrite per call.
type MetadataScope struct {
	// Key is the outgoing metadata key that carries the resource ID.
	Key string
	// Default is used when Key is absent (or empty) from the outgoing
	// metadata.
	Default string
}

// FilteredConn wraps a grpc.ClientConnInterface and enforces a Scope[string]
// against a set of MethodScope rules and/or a MetadataScope fallback.
//
//   - For a method with a matching MethodScope rule, the key is read from
//     (and, if aliased, rewritten into) the request message.
//   - Otherwise, if a MetadataScope is configured, it is applied to every
//     Invoke and NewStream call (covering unary calls keyed by metadata and
//     all streaming calls).
//   - If neither applies to a given call, it passes through unfiltered.
type FilteredConn struct {
	inner   grpc.ClientConnInterface
	scope   *Scope[string]
	methods map[string]MethodScope
	meta    *MetadataScope
}

// NewFilteredConn builds a FilteredConn enforcing scope over inner using
// only message-based MethodScope rules.
func NewFilteredConn(inner grpc.ClientConnInterface, scope *Scope[string], methods ...MethodScope) *FilteredConn {
	return newFilteredConn(inner, scope, methods, nil)
}

// NewFilteredMetaConn builds a FilteredConn enforcing scope over inner using
// a MetadataScope fallback (applied to every call not covered by methods),
// plus any message-based MethodScope rules.
func NewFilteredMetaConn(inner grpc.ClientConnInterface, scope *Scope[string], meta MetadataScope, methods ...MethodScope) *FilteredConn {
	return newFilteredConn(inner, scope, methods, &meta)
}

func newFilteredConn(inner grpc.ClientConnInterface, scope *Scope[string], methods []MethodScope, meta *MetadataScope) *FilteredConn {
	m := make(map[string]MethodScope, len(methods))
	for _, ms := range methods {
		m[ms.FullMethod] = ms
	}
	return &FilteredConn{inner: inner, scope: scope, methods: m, meta: meta}
}

func (fc *FilteredConn) resolveMeta(ctx context.Context) (context.Context, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	key := fc.meta.Default
	if vals := md.Get(fc.meta.Key); len(vals) > 0 && vals[0] != "" {
		key = vals[0]
	}
	resolved, allowed := fc.scope.Resolve(key)
	if !allowed {
		return ctx, status.Errorf(codes.NotFound, "%s: not found", key)
	}
	if resolved != key {
		md = md.Copy()
		md.Set(fc.meta.Key, resolved)
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return ctx, nil
}

func (fc *FilteredConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	if ms, ok := fc.methods[method]; ok {
		key := ms.GetKey(args)
		resolved, allowed := fc.scope.Resolve(key)
		if !allowed {
			return status.Errorf(codes.NotFound, "%s: not found", key)
		}
		args = ms.WithKey(args, resolved)
	} else if fc.meta != nil {
		var err error
		ctx, err = fc.resolveMeta(ctx)
		if err != nil {
			return err
		}
	}
	return fc.inner.Invoke(ctx, method, args, reply, opts...)
}

func (fc *FilteredConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if fc.meta != nil {
		var err error
		ctx, err = fc.resolveMeta(ctx)
		if err != nil {
			return nil, err
		}
	}
	return fc.inner.NewStream(ctx, desc, method, opts...)
}

var _ grpc.ClientConnInterface = (*FilteredConn)(nil)
