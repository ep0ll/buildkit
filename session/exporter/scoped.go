package exporter

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
	"google.golang.org/grpc"
)

// Scope restricts which exporter Types a session's Exporter service may
// reveal via FindExporters. It shares its implementation with
// session/secrets.Scope and friends via the generic session/scope package.
//
// Unlike secrets/auth/ssh/filesync/upload/content — which are scoped by a
// key on the *request* (a message field or metadata value) — exporter
// discovery has no such per-call key: FindExporters returns a *set* of
// candidate exporters for the caller to choose from. So instead of rewriting
// the request, FilteredCaller filters the *response*, keeping only
// ExporterRequest entries whose Type is allowed by Scope.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed exporter Types and an
// optional alias map (child-visible Type -> parent-visible Type).
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// FilteredCaller wraps a session.Caller and restricts which exporter Types
// FindExporters may return to Scope, enforced by filtering the response
// after every call.
type FilteredCaller struct {
	inner session.Caller
	scope *Scope
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	effective := childScope
	if parentScope != nil {
		effective = scope.Intersect(parentScope, childScope)
	}
	return &FilteredCaller{inner: inner, scope: effective}
}

// Context implements session.Caller.
func (fc *FilteredCaller) Context(ctx context.Context) context.Context { return fc.inner.Context(ctx) }

// Supports implements session.Caller.
func (fc *FilteredCaller) Supports(method string) bool { return fc.inner.Supports(method) }

// Conn implements session.Caller. It returns the underlying, unfiltered
// connection; use FindExporters below rather than calling the Exporter
// service directly if Type filtering should be enforced.
func (fc *FilteredCaller) Conn() *grpc.ClientConn { return fc.inner.Conn() }

// SharedKey implements session.Caller.
func (fc *FilteredCaller) SharedKey() string { return fc.inner.SharedKey() }

// Scope returns the effective scope of this caller.
func (fc *FilteredCaller) Scope() *Scope { return fc.scope }

// Inner returns the wrapped, unrestricted session.Caller.
func (fc *FilteredCaller) Inner() session.Caller { return fc.inner }

// FindExporters calls the underlying Exporter service and filters the
// returned Exporters down to Types allowed by Scope.
func (fc *FilteredCaller) FindExporters(ctx context.Context, md map[string][]byte, refs []string) (*FindExportersResponse, error) {
	client := NewExporterClient(fc.inner.Conn())
	resp, err := client.FindExporters(fc.inner.Context(ctx), &FindExportersRequest{Metadata: md, Refs: refs})
	if err != nil {
		return nil, err
	}
	filtered := make([]*ExporterRequest, 0, len(resp.Exporters))
	for _, e := range resp.Exporters {
		if fc.scope.Allows(e.Type) {
			filtered = append(filtered, e)
		}
	}
	return &FindExportersResponse{Exporters: filtered}, nil
}

// FinalizeExport passes through to the underlying Exporter service
// unfiltered: finalization operates on an exporter response already chosen
// via a (Type-filtered) FindExporters call, so there is no additional
// per-Type restriction to apply here.
func (fc *FilteredCaller) FinalizeExport(ctx context.Context, exporterResponse map[string]string) error {
	client := NewExporterClient(fc.inner.Conn())
	_, err := client.FinalizeExport(fc.inner.Context(ctx), &FinalizeExportRequest{ExporterResponse: exporterResponse})
	return err
}

var _ session.Caller = (*FilteredCaller)(nil)

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding *FilteredCaller instances scoped to
// Intersect(parentScope, childScope) for every underlying caller.
// This is a type-safe wrapper for exporter-specific scopes.
// Note: exporter uses response filtering rather than transport-level enforcement,
// so it doesn't use scope.FilteredManager directly.
type FilteredManager struct {
	inner       *session.Manager
	scope       *Scope
	parentScope *Scope
}

// NewFilteredManager creates a FilteredManager. parentScope (may be nil) is
// the Type scope inherited from the parent build; childScope is declared
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
