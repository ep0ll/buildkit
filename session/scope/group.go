package scope

import "github.com/moby/buildkit/session"

// FilteredGroup wraps a session.Group and restricts which session IDs it
// yields to those allowed by scope. This lets a caller scope down *which
// sessions* a nested operation may reach, independent of (and composable
// with) scoping *which resources within a session* are reachable via
// FilteredCaller/FilteredManager.
type FilteredGroup struct {
	inner session.Group
	scope *Scope[string]
}

// NewFilteredGroup wraps inner, only exposing session IDs allowed by scope.
// A nil scope allows every session ID through unchanged.
func NewFilteredGroup(inner session.Group, scope *Scope[string]) *FilteredGroup {
	return &FilteredGroup{inner: inner, scope: scope}
}

func (g *FilteredGroup) SessionIterator() session.Iterator {
	if g.inner == nil {
		return nil
	}
	it := g.inner.SessionIterator()
	if it == nil {
		return nil
	}
	return &filteredIterator{inner: it, scope: g.scope}
}

var _ session.Group = (*FilteredGroup)(nil)

type filteredIterator struct {
	inner session.Iterator
	scope *Scope[string]
}

func (it *filteredIterator) NextSession() string {
	for {
		id := it.inner.NextSession()
		if id == "" {
			return ""
		}
		if it.scope.Allows(id) {
			return id
		}
	}
}

var _ session.Iterator = (*filteredIterator)(nil)
