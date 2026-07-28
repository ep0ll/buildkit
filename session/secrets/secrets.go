package secrets

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
	"github.com/pkg/errors"
)

type SecretStore interface {
	GetSecret(context.Context, string) ([]byte, error)
}

var ErrNotFound = errors.Errorf("not found")

// Scope restricts which secret IDs are reachable through a given session.
//
// This is used to control which secrets a nested build (llb.Build /
// solver/llbsolver/ops.BuildOp) is allowed to see: by default a nested build
// gets no secrets at all. The outer build can either allow-list a subset of
// secret IDs to be forwarded, or explicitly opt in to forwarding every
// secret available on the session (Full).
//
// Note: Scope is enforced at the gRPC transport layer inside FilteredCaller,
// not via context values. This means the restriction cannot be bypassed by
// using context.Background() or any other context manipulation.
//
// The underlying implementation lives in package session/scope, which is
// shared by every scoped session type (secrets, auth, session groups, ...)
// so the allow-list/alias/intersection semantics only need to be
// implemented and tested once.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed secret IDs and an optional
// alias map. aliases maps child-visible names to parent-visible secret IDs;
// pass nil when no aliasing is needed.
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes: the result allows only
// secrets that both a and b allow. Aliases from both scopes are preserved
// when their resolved parent name survives in the intersection.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// GetSecret fetches a secret using the provided caller. The caller may be a
// *FilteredCaller (transport-level scope enforcement, independent of context)
// or a plain session.Caller (no scope enforcement — top-level build path).
//
// Callers that need scope enforcement must supply a *FilteredCaller obtained
// from a FilteredManager — do not rely on context values for this.
func GetSecret(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	return GetSecretFromCaller(ctx, c, id)
}
