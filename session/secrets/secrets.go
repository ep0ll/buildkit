package secrets

import (
	"context"

	"github.com/moby/buildkit/session"
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
type Scope struct {
	// Allowed is the set of secret IDs that may be fetched. Ignored when Full is true.
	Allowed map[string]struct{}
	// Full, when true, disables filtering entirely.
	Full bool
	// Aliases maps a child-visible name to the parent-visible secret ID.
	// For example, given Aliases["db"] = "prod_db", a nested build may
	// request secret "db" and receive the value of "prod_db" from the parent
	// session. The resolved parent name must be in Allowed (or Full must be
	// true) for the lookup to succeed.
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
// A nil scope allows everything (top-level build / unrestricted path).
func (s *Scope) Allows(id string) bool {
	_, ok := s.Resolve(id)
	return ok
}

// Resolve returns the actual secret ID that should be fetched for the given
// request ID, together with an ok flag.
//
//   - Aliases are always checked first, regardless of Full: if id is an alias
//     key, the parent name is returned (on Full scopes unconditionally; on
//     restricted scopes only when the parent is in Allowed).
//   - If s is nil or Full (and id is not an alias), every id is allowed and
//     resolves to itself.
//   - Otherwise id must be directly present in Allowed.
//
// Returns ("", false) when the id is not accessible under this scope.
func (s *Scope) Resolve(id string) (parentID string, ok bool) {
	// Check alias table first — applies even on Full scopes because an alias
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

// Intersect returns the intersection of two scopes: the result allows only
// secrets that both a and b allow. Aliases from both scopes are preserved
// when their resolved parent name survives in the intersection.
func Intersect(a, b *Scope) *Scope {
	return intersect(a, b)
}

func intersect(a, b *Scope) *Scope {
	switch {
	case a.Full && b.Full:
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
		out := &Scope{Allowed: make(map[string]struct{}, len(b.Allowed))}
		for id := range b.Allowed {
			out.Allowed[id] = struct{}{}
		}
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	case b.Full:
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
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	}
}

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

// GetSecret fetches a secret using the provided caller. The caller may be a
// *FilteredCaller (transport-level scope enforcement, independent of context)
// or a plain session.Caller (no scope enforcement — top-level build path).
//
// Callers that need scope enforcement must supply a *FilteredCaller obtained
// from a FilteredManager — do not rely on context values for this.
func GetSecret(ctx context.Context, c session.Caller, id string) ([]byte, error) {
	return GetSecretFromCaller(ctx, c, id)
}
