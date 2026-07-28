// Package scope provides a single, generic implementation of "scoped access"
// that every buildkit session type can reuse: secrets, ssh agents, auth
// hosts, session groups, etc. Instead of re-implementing allow-lists,
// aliasing and intersection logic per-service (as session/secrets used to),
// callers instantiate Scope[T] with whatever key type identifies their
// resource (usually string) and get identical, tested semantics for free.
package scope

// Scope restricts which keys of type T are reachable through a session.
//
//   - Allowed is the set of keys that may be used. Ignored when Full is true.
//   - Full, when true, disables filtering entirely.
//   - Aliases maps a caller-visible key to the underlying (parent) key. This
//     lets a restricted scope expose a resource under a different name than
//     the one used by the parent/unrestricted side, e.g. a nested build
//     asking for secret "db" transparently receiving "prod_db".
type Scope[T comparable] struct {
	Allowed map[T]struct{}
	Full    bool
	Aliases map[T]T
}

// New builds a Scope[T] from an allow-list and an optional alias map.
// aliases maps caller-visible keys to parent-visible keys; pass nil when no
// aliasing is needed. Zero-valued keys (e.g. "") are ignored.
func New[T comparable](allowed []T, full bool, aliases map[T]T) *Scope[T] {
	var zero T
	s := &Scope[T]{Full: full}
	if !full {
		s.Allowed = make(map[T]struct{}, len(allowed))
		for _, id := range allowed {
			if id == zero {
				continue
			}
			s.Allowed[id] = struct{}{}
		}
	}
	if len(aliases) > 0 {
		s.Aliases = make(map[T]T, len(aliases))
		for k, v := range aliases {
			if k != zero && v != zero {
				s.Aliases[k] = v
			}
		}
	}
	return s
}

// Resolve returns the actual key that should be used for the given
// caller-supplied key, together with an ok flag.
//
//   - Aliases are always checked first, regardless of Full: if id is an
//     alias key, the parent key is returned (on Full scopes unconditionally;
//     on restricted scopes only when the parent is in Allowed).
//   - If s is nil or Full (and id is not an alias), every id is allowed and
//     resolves to itself.
//   - Otherwise id must be directly present in Allowed.
//
// Returns (zero, false) when the id is not accessible under this scope.
func (s *Scope[T]) Resolve(id T) (parent T, ok bool) {
	if s != nil {
		if p, isAlias := s.Aliases[id]; isAlias {
			if s.Full {
				return p, true
			}
			if _, allowed := s.Allowed[p]; allowed {
				return p, true
			}
			var zero T
			return zero, false
		}
	}
	if s == nil || s.Full {
		return id, true
	}
	if _, allowed := s.Allowed[id]; allowed {
		return id, true
	}
	var zero T
	return zero, false
}

// Allows reports whether the given key is reachable under this scope.
// A nil scope allows everything (top-level / unrestricted path).
func (s *Scope[T]) Allows(id T) bool {
	_, ok := s.Resolve(id)
	return ok
}

// Intersect returns the intersection of two scopes: the result allows only
// keys that both a and b allow. Aliases from both scopes are preserved when
// their resolved parent key survives in the intersection. This is what makes
// nested scoping (e.g. nested builds) safe: a child can never escalate
// beyond what its parent already allowed.
func Intersect[T comparable](a, b *Scope[T]) *Scope[T] {
	switch {
	case a.Full && b.Full:
		out := &Scope[T]{Full: true}
		if len(a.Aliases)+len(b.Aliases) > 0 {
			out.Aliases = make(map[T]T, len(a.Aliases)+len(b.Aliases))
			for k, v := range a.Aliases {
				out.Aliases[k] = v
			}
			for k, v := range b.Aliases {
				out.Aliases[k] = v
			}
		}
		return out
	case a.Full:
		out := &Scope[T]{Allowed: make(map[T]struct{}, len(b.Allowed))}
		for id := range b.Allowed {
			out.Allowed[id] = struct{}{}
		}
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	case b.Full:
		out := &Scope[T]{Allowed: make(map[T]struct{}, len(a.Allowed))}
		for id := range a.Allowed {
			out.Allowed[id] = struct{}{}
		}
		out.Aliases = mergeAliases(b.Aliases, a.Aliases, out.Allowed)
		return out
	default:
		out := &Scope[T]{Allowed: map[T]struct{}{}}
		for id := range a.Allowed {
			if _, ok := b.Allowed[id]; ok {
				out.Allowed[id] = struct{}{}
			}
		}
		out.Aliases = mergeAliases(a.Aliases, b.Aliases, out.Allowed)
		return out
	}
}

func mergeAliases[T comparable](primary, secondary map[T]T, allowed map[T]struct{}) map[T]T {
	if len(primary)+len(secondary) == 0 {
		return nil
	}
	out := make(map[T]T)
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
