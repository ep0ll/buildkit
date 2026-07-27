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
	// Aliases maps a child-visible name to the parent-visible secret ID.
	// For example, given Aliases["db"] = "prod_db", a nested build may
	// request secret "db" and receive the value of "prod_db" from the
	// parent session. The resolved parent name must be present in Allowed
	// (or Full must be true) for the lookup to succeed.
	Aliases map[string]string
}

// NewScope builds a Scope from a set of allowed secret IDs and an optional
// alias map. aliases maps child-visible names to parent-visible secret IDs;
// pass nil when no aliasing is needed.
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
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
	if len(aliases) > 0 {
		s.Aliases = make(map[string]string, len(aliases))
		for k, v := range aliases {
			if k != "" && v != "" {
				s.Aliases[k] = v
			}
		}
	}
	return s
}

// Allows reports whether the given secret ID is reachable under this scope.
// A nil scope allows everything, preserving the historical, unrestricted
// behavior for any caller that never establishes a Scope.
func (s *Scope) Allows(id string) bool {
	_, ok := s.Resolve(id)
	return ok
}

// Resolve returns the actual secret ID that should be fetched for the given
// request ID, together with an ok flag.
//
//   - Aliases are always checked first, regardless of Full: if id is an alias
//     key, the parent name is returned unconditionally (on Full scopes the
//     parent name is always reachable; on restricted scopes it must be in
//     Allowed).
//   - If s is nil or Full (and id is not an alias), every id is allowed and
//     resolves to itself.
//   - Otherwise id must be directly present in Allowed.
//
// Returns ("", false) when the id is not accessible under this scope.
func (s *Scope) Resolve(id string) (parentID string, ok bool) {
	// Check alias table first — applies even on Full scopes, because an alias
	// declaration means "child calls it X, parent stores it as Y".
	if s != nil {
		if parent, isAlias := s.Aliases[id]; isAlias {
			if s.Full {
				return parent, true
			}
			_, allowed := s.Allowed[parent]
			if allowed {
				return parent, true
			}
			return "", false
		}
	}
	// No alias match: fall back to Full / Allowed check.
	if s == nil || s.Full {
		return id, true
	}
	_, allowed := s.Allowed[id]
	if allowed {
		return id, true
	}
	return "", false
}

type scopeContextKeyT struct{}

var scopeContextKey = scopeContextKeyT{}

// WithScope narrows ctx's secrets Scope to the intersection of the existing
// scope (if any) and the new one. A nested build can only ever see a
// subset of what its ancestor already allowed — it cannot escalate its own
// access by setting Full on its own attributes if an ancestor restricted it.
func WithScope(ctx context.Context, scope *Scope) context.Context {
	if scope == nil {
		return ctx
	}
	if parent, ok := ScopeFromContext(ctx); ok {
		scope = Intersect(parent, scope)
	}
	return context.WithValue(ctx, scopeContextKey, scope)
}

// Intersect returns the intersection of two scopes: the result allows only
// secrets that both a and b allow. Aliases from both scopes are preserved
// when their resolved parent name survives in the intersection.
// Intersect is exported so that build.go (and other callers) can compute the
// effective scope without going through context.
func Intersect(a, b *Scope) *Scope {
	return intersect(a, b)
}

func intersect(a, b *Scope) *Scope {
	switch {
	case a.Full && b.Full:
		// Both are full: merge all aliases from both.
		out := &Scope{Full: true}
		if len(a.Aliases)+len(b.Aliases) > 0 {
			out.Aliases = make(map[string]string, len(a.Aliases)+len(b.Aliases))
			for k, v := range a.Aliases {
				out.Aliases[k] = v
			}
			for k, v := range b.Aliases {
				out.Aliases[k] = v
			}
		}
		return out
	case a.Full:
		// b is at least as strict; keep b's allowed set but add aliases from
		// a whose resolved parent survives in b's Allowed.
		out := &Scope{Allowed: make(map[string]struct{}, len(b.Allowed))}
		for id := range b.Allowed {
			out.Allowed[id] = struct{}{}
		}
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	case b.Full:
		// a is at least as strict; keep a's allowed set but add aliases from
		// b whose resolved parent survives in a's Allowed.
		out := &Scope{Allowed: make(map[string]struct{}, len(a.Allowed))}
		for id := range a.Allowed {
			out.Allowed[id] = struct{}{}
		}
		out.Aliases = mergeAliases(b.Aliases, a.Aliases, out.Allowed)
		return out
	default:
		out := &Scope{Allowed: map[string]struct{}{}}
		for id := range a.Allowed {
			if _, ok := b.Allowed[id]; ok {
				out.Allowed[id] = struct{}{}
			}
		}
		// Keep aliases from both scopes whose parent name is in the result set.
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	}
}

// mergeAliases merges two alias maps, keeping only entries whose resolved
// parent name is present in the surviving Allowed set.
func mergeAliases(primary, secondary map[string]string, allowed map[string]struct{}) map[string]string {
	if len(primary)+len(secondary) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, parent := range primary {
		if _, ok := allowed[parent]; ok {
			out[k] = parent
		}
	}
	for k, parent := range secondary {
		if _, ok := allowed[parent]; ok {
			out[k] = parent
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ScopeFromContext returns the secrets Scope attached to ctx, if any.
func ScopeFromContext(ctx context.Context) (*Scope, bool) {
	s, ok := ctx.Value(scopeContextKey).(*Scope)
	return s, ok
}

func GetSecret(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	// Resolve alias and check allow-list in one step. fetchID is the actual
	// secret ID to request from the session (may differ from id via aliasing).
	fetchID := id
	if scope, ok := ScopeFromContext(ctx); ok {
		resolved, allowed := scope.Resolve(id)
		if !allowed {
			return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
		}
		fetchID = resolved
	}

	ctx = c.Context(ctx)
	client := NewSecretsClient(c.Conn())
	resp, err := client.GetSecret(ctx, &GetSecretRequest{
		ID: fetchID,
	})
	if err != nil {
		if code := grpcerrors.Code(err); code == codes.Unimplemented || code == codes.NotFound {
			return nil, errors.Wrapf(ErrNotFound, "secret %s", id)
		}
		return nil, err
	}
	return resp.Data, nil
}
