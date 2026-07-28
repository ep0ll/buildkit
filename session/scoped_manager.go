package session

import (
	"context"
	"sync"

	"github.com/moby/buildkit/identity"
	"github.com/pkg/errors"
)

// ScopedManager wraps a Manager and provides scoped session ID registration
// and resolution. When a session is scoped, a new session ID is generated.
// When that scoped session ID is used, it resolves to a restricted version
// of the original session.
type ScopedManager struct {
	inner          *Manager
	scopedSessions map[string]*scopedSession // scoped session ID -> scoped session info
	mu             sync.Mutex
}

// scopedSession holds information about a scoped session
type scopedSession struct {
	parentID string
	caller   Caller
}

// NewScopedManager creates a new ScopedManager wrapping the given Manager
func NewScopedManager(inner *Manager) *ScopedManager {
	return &ScopedManager{
		inner:          inner,
		scopedSessions: make(map[string]*scopedSession),
	}
}

// RegisterScopedSession registers a new scoped session ID for the given parent session ID
// and returns the new scoped session ID. The caller is the restricted version of the
// parent session that should be used when the scoped session ID is resolved.
func (sm *ScopedManager) RegisterScopedSession(parentID string, caller Caller) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	scopedID := identity.NewID()
	sm.scopedSessions[scopedID] = &scopedSession{
		parentID: parentID,
		caller:   caller,
	}
	return scopedID
}

// Get resolves a session ID. If the ID is a scoped session ID, it returns the
// restricted caller that was registered with it. If it's a regular session ID,
// it delegates to the inner Manager.
func (sm *ScopedManager) Get(ctx context.Context, id string, noWait bool) (Caller, error) {
	// Check if this is a scoped session ID
	sm.mu.Lock()
	scoped, ok := sm.scopedSessions[id]
	sm.mu.Unlock()

	if ok {
		// Return the restricted caller for this scoped session
		return scoped.caller, nil
	}

	// Otherwise, delegate to the inner manager
	return sm.inner.Get(ctx, id, noWait)
}

// Any implements CallerManager by iterating over session IDs in the group.
// For each session ID, it resolves it (handling both scoped and regular IDs)
// and calls the provided function.
func (sm *ScopedManager) Any(ctx context.Context, g Group, f func(context.Context, string, Caller) error) error {
	if g == nil {
		return nil
	}

	iter := g.SessionIterator()
	if iter == nil {
		return nil
	}

	var lastErr error
	for {
		id := iter.NextSession()
		if id == "" {
			if lastErr != nil {
				return lastErr
			}
			return errors.WithStack(ErrNoActiveSessions)
		}

		// Resolve the session ID (handles both scoped and regular IDs)
		c, err := sm.Get(ctx, id, false)
		if err != nil {
			lastErr = err
			continue
		}
		if err := f(c.Context(ctx), id, c); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
}

// DeleteScopedSession removes a scoped session ID from the registry.
// This is typically called when the scoped session is no longer needed.
func (sm *ScopedManager) DeleteScopedSession(scopedID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.scopedSessions, scopedID)
}

// Inner returns the underlying Manager
func (sm *ScopedManager) Inner() *Manager {
	return sm.inner
}

// IsScopedID returns true if the given session ID is a scoped session ID
func (sm *ScopedManager) IsScopedID(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	_, ok := sm.scopedSessions[id]
	return ok
}

// ResolveParentID returns the parent session ID for a scoped session ID.
// If the ID is not a scoped session ID, it returns the ID unchanged.
func (sm *ScopedManager) ResolveParentID(id string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if scoped, ok := sm.scopedSessions[id]; ok {
		return scoped.parentID
	}
	return id
}

var _ CallerManager = (*ScopedManager)(nil)
