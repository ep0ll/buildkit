package secrets

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// getSecretMethod tells the generic scope package how to read/rewrite the
// secret ID on a GetSecret request. This is the only secrets-specific piece
// of the whole scoping mechanism — everything else (allow-lists, aliases,
// intersection, transport-level enforcement) comes from package
// session/scope.
var getSecretMethod = scope.MethodScope{
	FullMethod: Secrets_GetSecret_FullMethodName,
	GetKey: func(req any) string {
		return req.(*GetSecretRequest).ID
	},
	WithKey: func(req any, key string) any {
		r := req.(*GetSecretRequest)
		return &GetSecretRequest{ID: key, Annotations: r.Annotations}
	},
}

// FilteredCaller wraps a session.Caller and restricts its secret access to
// the given Scope at the gRPC transport level. The scope is embedded in the
// object — it is NOT carried in any context value — so the restriction
// cannot be bypassed by using a fresh context (e.g. context.Background()).
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller. childScope is the scope
// declared by this caller's build op. parentScope (may be nil) is the scope
// inherited from the parent build; the effective scope is
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope *Scope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCaller(inner, childScope, parentScope, getSecretMethod)}
}

// SecretsClient returns a SecretsClient backed by the scope-enforcing
// connection so that GetSecret calls go through scope enforcement at the
// transport level. Always use this instead of NewSecretsClient(fc.Conn())
// when fetching secrets.
func (fc *FilteredCaller) SecretsClient() SecretsClient {
	return NewSecretsClient(fc.FilteredConn())
}

// GetSecretFromCaller fetches a secret using the provided caller.
//
//   - If caller is a *FilteredCaller, scope enforcement happens at the gRPC
//     transport level via the scope-enforcing connection — entirely
//     independent of ctx. Even context.Background() cannot bypass it.
//   - For any other session.Caller (top-level build path): no scope is applied;
//     the secret is fetched directly from the session.
func GetSecretFromCaller(ctx context.Context, c session.Caller, id string) ([]byte, error) {
    var client SecretsClient

    if fc, ok := c.(*FilteredCaller); ok {
        ctx = fc.Inner().Context(ctx)
        client = fc.SecretsClient()
    } else {
        ctx = c.Context(ctx)
        client = NewSecretsClient(c.Conn())
    }

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
