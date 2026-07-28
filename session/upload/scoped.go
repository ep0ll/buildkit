package upload

import (
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
