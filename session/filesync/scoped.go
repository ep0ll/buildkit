package filesync

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
)

// Scope restricts which named sync directories/exporters are reachable
// through a session's FileSync/FileSend services. It shares its
// implementation with session/secrets.Scope and friends via the generic
// session/scope package.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed names and an optional alias
// map (child-visible name -> parent-visible name).
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// sourceMeta enforces scope on FileSync's DiffCopy/TarStream (both
// bidi-streaming), which carry the requested source directory name via
// outgoing metadata (keyDirName) rather than a request field.
var sourceMeta = scope.MetadataScope{Key: keyDirName}

// FilteredCaller wraps a session.Caller and restricts which named
// directories may be pulled from the client via FSSync, enforced at the
// gRPC transport level.
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope), restricting which "dir-name" values
// FSSync may request.
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCallerWithMeta(inner, childScope, parentScope, sourceMeta)}
}

// FileSyncClient returns a FileSyncClient backed by the scope-enforcing
// connection. Always use this instead of NewFileSyncClient(fc.Conn()) so
// directory-name restrictions are actually enforced.
func (fc *FilteredCaller) FileSyncClient() FileSyncClient {
	return NewFileSyncClient(fc.FilteredConn())
}

// exporterMeta enforces scope on FileSend's DiffCopy (streaming), which
// carries the target exporter ID via outgoing metadata (keyExporterID)
// rather than a request field. This restricts which exporter IDs the server
// may push files into on behalf of the caller (see CopyToCaller /
// CopyFileWriter).
var exporterMeta = scope.MetadataScope{Key: keyExporterID}

// ExporterFilteredCaller wraps a session.Caller and restricts which
// exporter IDs the server may target via FileSend, enforced at the gRPC
// transport level.
type ExporterFilteredCaller struct {
	*scope.FilteredCaller
}

// NewExporterFilteredCaller creates an ExporterFilteredCaller with effective
// scope Intersect(parentScope, childScope), restricting which
// "exporter-id" values FileSend calls may target.
func NewExporterFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *ExporterFilteredCaller {
	return &ExporterFilteredCaller{scope.NewFilteredCallerWithMeta(inner, childScope, parentScope, exporterMeta)}
}

// FileSendClient returns a FileSendClient backed by the scope-enforcing
// connection. Always use this instead of NewFileSendClient(fc.Conn()) so
// exporter-ID restrictions are actually enforced.
func (fc *ExporterFilteredCaller) FileSendClient() FileSendClient {
	return NewFileSendClient(fc.FilteredConn())
}

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding *FilteredCaller instances scoped to
// Intersect(parentScope, childScope) for every underlying caller. Use
// NewExporterFilteredCaller directly (there is no manager variant for it,
// since server-side exporter pushes are not typically dispatched via
// session.CallerManager.Any).
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope
	parentScope *Scope
}

// NewFilteredManager creates a FilteredManager restricting which "dir-name"
// values nested FSSync calls may request. parentScope (may be nil) is the
// scope inherited from the parent build; childScope is declared by this
// subbuild.
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

// RegisterScopedSession creates a scoped session ID for the given parent session ID
// with the specified filesync scope. It registers the scoped session with the
// ScopedManager and returns the new session ID.
func RegisterScopedSession(sm *session.ScopedManager, parentID string, childScope, parentScope *Scope) (string, error) {
	// Get the parent caller from the inner manager
	parentCaller, err := sm.Inner().Get(context.Background(), parentID, false)
	if err != nil {
		return "", err
	}

	// Create the restricted caller
	filteredCaller := NewFilteredCaller(parentCaller, childScope, parentScope)

	// Register the scoped session
	scopedID := sm.RegisterScopedSession(parentID, filteredCaller)

	return scopedID, nil
}

var _ session.CallerManager = (*FilteredManager)(nil)
