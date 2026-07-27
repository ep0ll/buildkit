package secrets

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type SecretStore interface {
	GetSecret(context.Context, string) ([]byte, error)
}

var ErrNotFound = errors.Errorf("not found")

// Scope restricts which secret IDs are reachable through a given context.
//
// This is used to control which secrets a nested build (llb.Build /
// solver/llbsolver/ops.BuildOp) is allowed to see: by default a nested build
// gets no secrets at all. The outer build can either allow-list a subset of
// secret IDs to be forwarded, or explicitly opt in to forwarding every
// secret available on the session (Full).
type Scope struct {
	// Allowed is the set of secret IDs that may be fetched. Ignored when
	// Full is true.
	Allowed map[string]struct{}
	// Full, when true, disables filtering entirely: every secret available
	// on the session can be fetched. This is the "opt in to full access"
	// escape hatch.
	Full bool
}

// NewScope builds a Scope from a set of allowed secret IDs.
func NewScope(allowed []string, full bool) *Scope {
	s := &Scope{Full: full}
	if !full {
		s.Allowed = make(map[string]struct{}, len(allowed))
		for _, id := range allowed {
			if id == "" {
				continue
			}
			s.Allowed[id] = struct{}{}
		}
	}
	return s
}

// Allows reports whether the given secret ID is reachable under this scope.
// A nil scope allows everything, preserving the historical, unrestricted
// behavior for any caller that never establishes a Scope.
func (s *Scope) Allows(id string) bool {
	if s == nil || s.Full {
		return true
	}
	_, ok := s.Allowed[id]
	return ok
}

type scopeContextKeyT struct{}

var scopeContextKey = scopeContextKeyT{}

// WithScope attaches a secrets Scope to ctx. Any GetSecret call made with a
// context derived from the result will be filtered through it. Passing a
// nil scope is a no-op (falls back to unrestricted access), and an already
// present scope in ctx is replaced, so nested calls to WithScope correctly
// re-scope for deeper levels of nested builds.
func WithScope(ctx context.Context, scope *Scope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, scopeContextKey, scope)
}

// ScopeFromContext returns the secrets Scope attached to ctx, if any.
func ScopeFromContext(ctx context.Context) (*Scope, bool) {
	s, ok := ctx.Value(scopeContextKey).(*Scope)
	return s, ok
}

func GetSecret(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	if scope, ok := ScopeFromContext(ctx); ok && !scope.Allows(id) {
		return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
	}

	ctx = c.Context(ctx)
	client := NewSecretsClient(c.Conn())
	resp, err := client.GetSecret(ctx, &GetSecretRequest{
		ID: id,
	})
	if err != nil {
		if code := grpcerrors.Code(err); code == codes.Unimplemented || code == codes.NotFound {
			return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
		}
		return nil, err
	}
	return resp.Data, nil
}
