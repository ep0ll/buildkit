package secrets

import (
	"context"
	"strings"

	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// filteredSecretsConn
// ─────────────────────────────────────────────────────────────────────────────

// filteredSecretsConn implements grpc.ClientConnInterface and intercepts
// GetSecret RPCs at the transport level. The scope check and alias resolution
// happen inside Invoke — independent of the context object used by the caller.
// Even context.Background() cannot bypass the restriction.
type filteredSecretsConn struct {
	inner *grpc.ClientConn
	scope *Scope
}

func (fc *filteredSecretsConn) Invoke(ctx context.Context, method string, args, reply interface{}, opts ...grpc.CallOption) error {
	if strings.HasSuffix(method, "/GetSecret") {
		req, ok := args.(*GetSecretRequest)
		if !ok {
			return status.Errorf(codes.Internal, "unexpected request type for GetSecret: %T", args)
		}
		resolved, allowed := fc.scope.Resolve(req.ID)
		if !allowed {
			return status.Errorf(codes.NotFound, "secret %s: not found", req.ID)
		}
		// Replace with the resolved (parent) ID so alias is transparent.
		args = &GetSecretRequest{ID: resolved, Annotations: req.Annotations}
	}
	return fc.inner.Invoke(ctx, method, args, reply, opts...)
}

func (fc *filteredSecretsConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return fc.inner.NewStream(ctx, desc, method, opts...)
}

// ─────────────────────────────────────────────────────────────────────────────
// FilteredCaller
// ─────────────────────────────────────────────────────────────────────────────

// FilteredCaller wraps a session.Caller and restricts its secret access to
// the given Scope at the gRPC transport level. The scope is embedded in the
// object — it is NOT carried in any context value — so the restriction
// cannot be bypassed by using a fresh context (e.g. context.Background()).
type FilteredCaller struct {
	inner session.Caller
	scope *Scope
	conn  *filteredSecretsConn
}

// NewFilteredCaller creates a FilteredCaller. childScope is the scope
// declared by this caller's build op. parentScope (may be nil) is the scope
// inherited from the parent build; the effective scope is
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope *Scope, parentScope *Scope) *FilteredCaller {
	effective := childScope
	if parentScope != nil {
		effective = Intersect(parentScope, childScope)
	}
	return &FilteredCaller{
		inner: inner,
		scope: effective,
		conn:  &filteredSecretsConn{inner: inner.Conn(), scope: effective},
	}
}

// Context implements session.Caller.
func (fc *FilteredCaller) Context(ctx context.Context) context.Context {
	return fc.inner.Context(ctx)
}

// Supports implements session.Caller.
func (fc *FilteredCaller) Supports(method string) bool {
	return fc.inner.Supports(method)
}

// Conn implements session.Caller. For secret operations use SecretsClient()
// instead; Conn() is provided so FilteredCaller satisfies session.Caller for
// other services (SSH, uploads, etc.) that DO NOT need scope filtering.
func (fc *FilteredCaller) Conn() *grpc.ClientConn {
	return fc.inner.Conn()
}

// SharedKey implements session.Caller.
func (fc *FilteredCaller) SharedKey() string {
	return fc.inner.SharedKey()
}

// SecretsClient returns a SecretsClient backed by filteredSecretsConn so that
// GetSecret calls go through scope enforcement at the transport level.
// Always use this instead of NewSecretsClient(fc.Conn()) when fetching secrets.
func (fc *FilteredCaller) SecretsClient() SecretsClient {
	return NewSecretsClient(fc.conn)
}

// Scope returns the effective scope of this caller.
func (fc *FilteredCaller) Scope() *Scope {
	return fc.scope
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSecretFromCaller — dispatch to transport enforcement for FilteredCaller
// ─────────────────────────────────────────────────────────────────────────────

// GetSecretFromCaller fetches a secret using the provided caller.
//
//   - If caller is a *FilteredCaller, scope enforcement happens at the gRPC
//     transport level via filteredSecretsConn — entirely independent of ctx.
//     Even context.Background() cannot bypass the restriction.
//   - For any other session.Caller (top-level build path): no scope is applied;
//     the secret is fetched directly from the session.
func GetSecretFromCaller(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	if fc, ok := c.(*FilteredCaller); ok {
		ctx = fc.inner.Context(ctx)
		client := fc.SecretsClient()
		resp, err := client.GetSecret(ctx, &GetSecretRequest{ID: id})
		if err != nil {
			if code := codeOf(err); code == codes.Unimplemented || code == codes.NotFound {
				return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
			}
			return nil, err
		}
		return resp.Data, nil
	}
	// Top-level (unrestricted) path — plain session caller.
	ctx = c.Context(ctx)
	client := NewSecretsClient(c.Conn())
	resp, err := client.GetSecret(ctx, &GetSecretRequest{ID: id})
	if err != nil {
		if code := codeOf(err); code == codes.Unimplemented || code == codes.NotFound {
			return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
		}
		return nil, err
	}
	return resp.Data, nil
}

func codeOf(err error) codes.Code {
	if s, ok := status.FromError(err); ok {
		return s.Code()
	}
	return codes.Unknown
}

// ─────────────────────────────────────────────────────────────────────────────
// FilteredManager
// ─────────────────────────────────────────────────────────────────────────────

// FilteredManager wraps a *session.Manager and implements session.CallerManager.
// Every session.Caller returned through Any is wrapped in a *FilteredCaller so
// that secret access is scope-restricted at the transport level — regardless
// of what context the consumer passes to the Any callback.
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope
	parentScope *Scope
}

// NewFilteredManager creates a FilteredManager.
// parentScope is the scope inherited from the parent build (may be nil).
// childScope is the scope declared by this subbuild's BuildOp attributes.
// The effective scope for every caller is Intersect(parentScope, childScope).
func NewFilteredManager(inner *session.Manager, childScope *Scope, parentScope *Scope) *FilteredManager {
	return &FilteredManager{
		inner:       inner,
		scope:       childScope,
		parentScope: parentScope,
	}
}

// Inner returns the underlying *session.Manager (needed by nested builds to
// create a new FilteredManager wrapping the real, unrestricted manager).
func (fm *FilteredManager) Inner() *session.Manager {
	return fm.inner
}

// Scope returns the child scope this FilteredManager was constructed with.
// Use this to compute the effective scope for a further-nested build via
// Intersect(fm.Scope(), childBuildScope).
func (fm *FilteredManager) Scope() *Scope {
	return fm.scope
}

// Any implements session.CallerManager. Every caller yielded to f is a
// *FilteredCaller with effective scope Intersect(parentScope, childScope).
func (fm *FilteredManager) Any(ctx context.Context, g session.Group, f func(context.Context, string, session.Caller) error) error {
	return fm.inner.Any(ctx, g, func(ctx context.Context, id string, c session.Caller) error {
		filtered := NewFilteredCaller(c, fm.scope, fm.parentScope)
		return f(ctx, id, filtered)
	})
}

// EffectiveScope returns the computed effective scope for this manager
// (the intersection of parent and child scopes).
func (fm *FilteredManager) EffectiveScope() *Scope {
	if fm.parentScope != nil {
		return Intersect(fm.parentScope, fm.scope)
	}
	return fm.scope
}
