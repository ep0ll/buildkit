package session

import (
	"context"
	"sync"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/pkg/errors"
)

// ScopedManager wraps a Manager and provides scoped session ID registration
// and resolution. When a session is scoped, a new session ID is generated.
// When that scoped session ID is used, it resolves to a restricted version
// of the original session.
//
// *Manager is embedded (not held as a private field) so ScopedManager is a
// true superset of *Manager: every exported method of *Manager
// (HandleHTTPRequest, HandleConn, ID, ...) is promoted and usable directly
// on *ScopedManager. Get and Any are overridden below to add scoped-ID
// resolution; Go's method resolution prefers the outer type's own methods
// over promoted ones, so this is a clean override, not a naming collision.
type ScopedManager struct {
	*Manager
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
		Manager:        inner,
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
// it delegates to the embedded Manager.
func (sm *ScopedManager) Get(ctx context.Context, id string, noWait bool) (Caller, error) {
	sm.mu.Lock()
	scoped, ok := sm.scopedSessions[id]
	sm.mu.Unlock()

	if ok {
		return scoped.caller, nil
	}

	return sm.Manager.Get(ctx, id, noWait)
}

// getForAny resolves a single session ID the same way Get does, but applies
// the same 5s wait-timeout that Manager.Any uses for plain (non-scoped) IDs,
// so that a not-yet-registered ID fails over to the next ID in the group
// instead of blocking forever. Scoped IDs resolve immediately from the map
// and never need the timeout.
func (sm *ScopedManager) getForAny(ctx context.Context, id string) (Caller, error) {
	sm.mu.Lock()
	scoped, ok := sm.scopedSessions[id]
	sm.mu.Unlock()

	if ok {
		return scoped.caller, nil
	}

	timeoutCtx, cancel := context.WithCancelCause(ctx)
	timeoutCtx, _ = context.WithTimeoutCause(timeoutCtx, 5*time.Second, errors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer cancel(errors.WithStack(context.Canceled))

	return sm.Manager.Get(timeoutCtx, id, false)
}

// Any implements CallerManager by iterating over session IDs in the group.
// For each session ID, it resolves it (handling both scoped and regular IDs,
// with the same wait-timeout behavior as Manager.Any) and calls the provided
// function.
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

		c, err := sm.getForAny(ctx, id)
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

// Inner returns the underlying, unrestricted *Manager. Kept for backward
// compatibility with callers that used sm.Inner() before *Manager was
// embedded; sm.Manager is equivalent.
func (sm *ScopedManager) Inner() *Manager {
	return sm.Manager
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
