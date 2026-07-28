package upload

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
)

// Scope restricts which upload URL paths (as registered by
// uploadprovider.Uploader.Add) are reachable through a session's Upload
// service. It shares its implementation with session/secrets.Scope and
// friends via the generic session/scope package.
//
// Note: uploadprovider IDs are random, single-use tokens generated per
// Add() call rather than stable names, so an allow-list scope is most
// useful when the set of permitted upload paths is computed/propagated
// alongside the nested build request (e.g. only forwarding the specific
// upload URLs referenced by that build).
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed URL paths and an optional
// alias map (child-visible path -> parent-visible path).
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// pathMeta enforces scope on Upload.Pull, a bidi-streaming RPC that carries
// the requested URL path via outgoing metadata (keyPath) rather than a
// request field.
var pathMeta = scope.MetadataScope{Key: keyPath}

// FilteredCaller wraps a session.Caller and restricts which upload URL
// paths may be pulled, enforced at the gRPC transport level.
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCallerWithMeta(inner, childScope, parentScope, pathMeta)}
}

// UploadClient returns an UploadClient backed by the scope-enforcing
// connection. Always use this instead of NewUploadClient(fc.Conn()) so
// path restrictions are actually enforced.
func (fc *FilteredCaller) UploadClient() UploadClient {
	return NewUploadClient(fc.FilteredConn())
}

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding *FilteredCaller instances scoped to
// Intersect(parentScope, childScope) for every underlying caller.
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope
	parentScope *Scope
}

// NewFilteredManager creates a FilteredManager. parentScope (may be nil) is
// the path scope inherited from the parent build; childScope is declared
// by this subbuild.
func NewFilteredManager(inner *session.Manager, childScope, parentScope *Scope) *FilteredManager {
	return &FilteredManager{inner: inner, scope: childScope, parentScope: parentScope}
}

// Inner returns the underlying, unrestricted *session.Manager.
func (fm *FilteredManager) Inner() *session.Manager { return fm.inner }

// Scope returns the child scope this FilteredManager was constructed with.
func (fm *FilteredManager) Scope() *Scope { return fm.scope }

// Any implements session.CallerManager.
func (fm *FilteredManager) Any(ctx context.Context, g session.Group, f func(context.Context, string, session.Caller) error) error {
	return fm.inner.Any(ctx, g, func(ctx context.Context, id string, c session.Caller) error {
		return f(ctx, id, NewFilteredCaller(c, fm.scope, fm.parentScope))
	})
}

// EffectiveScope returns the computed effective scope for this manager.
func (fm *FilteredManager) EffectiveScope() *Scope {
	if fm.parentScope != nil {
		return Intersect(fm.parentScope, fm.scope)
	}
	return fm.scope
}

var _ session.CallerManager = (*FilteredManager)(nil)
