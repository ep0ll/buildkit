package sshforward

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
)

// Scope restricts which SSH agent/socket IDs (as configured via AgentConfig.ID
// / sshprovider.NewSSHAgentProvider) are reachable through a session's SSH
// service. It shares its implementation with session/secrets.Scope and
// session/auth.Scope via the generic session/scope package.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed agent IDs and an optional
// alias map (child-visible ID -> parent-visible ID).
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// checkAgentMethod enforces scope on CheckAgent, which carries the agent ID
// as a request field (empty means DefaultID, same as the server).
var checkAgentMethod = scope.MethodScope{
	FullMethod: SSH_CheckAgent_FullMethodName,
	GetKey: func(req any) string {
		r := req.(*CheckAgentRequest)
		if r.ID == "" {
			return DefaultID
		}
		return r.ID
	},
	WithKey: func(_ any, key string) any {
		return &CheckAgentRequest{ID: key}
	},
}

// forwardAgentMeta enforces scope on ForwardAgent, a bidi-streaming RPC that
// carries the agent ID via outgoing metadata (KeySSHID) rather than a
// request message field.
var forwardAgentMeta = scope.MetadataScope{
	Key:     KeySSHID,
	Default: DefaultID,
}

// FilteredCaller wraps a session.Caller and restricts SSH agent access to a
// Scope of agent IDs, enforced at the gRPC transport level for both
// CheckAgent (message-based) and ForwardAgent (metadata-based, streaming).
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCallerWithMeta(inner, childScope, parentScope, forwardAgentMeta, checkAgentMethod)}
}

// SSHClient returns an SSHClient backed by the scope-enforcing connection.
// Always use this instead of NewSSHClient(fc.Conn()) so agent-ID
// restrictions are actually enforced.
func (fc *FilteredCaller) SSHClient() SSHClient {
	return NewSSHClient(fc.FilteredConn())
}

// FilteredManager wraps a *session.Manager and implements
// session.CallerManager, yielding *FilteredCaller instances scoped to
// Intersect(parentScope, childScope) for every underlying caller.
// This is a type-safe wrapper around scope.FilteredManager for sshforward-specific scopes.
type FilteredManager struct {
	*scope.FilteredManager
}

// NewFilteredManager creates a FilteredManager. parentScope (may be nil) is
// the agent-ID scope inherited from the parent build; childScope is
// declared by this subbuild.
func NewFilteredManager(inner *session.Manager, childScope, parentScope *Scope) *FilteredManager {
	return &FilteredManager{
		FilteredManager: scope.NewFilteredManagerWithMeta(inner, childScope, parentScope, forwardAgentMeta, checkAgentMethod),
	}
}

// Scope returns the child scope this FilteredManager was constructed with.
func (fm *FilteredManager) Scope() *Scope {
	return fm.FilteredManager.Scope()
}

// EffectiveScope returns the computed effective scope for this manager.
func (fm *FilteredManager) EffectiveScope() *Scope {
	if fm.FilteredManager.EffectiveScope() != nil {
		return fm.FilteredManager.EffectiveScope()
	}
	return nil
}

// Any implements session.CallerManager.
func (fm *FilteredManager) Any(ctx context.Context, g session.Group, f func(context.Context, string, session.Caller) error) error {
	return fm.FilteredManager.Any(ctx, g, func(ctx context.Context, id string, c session.Caller) error {
		// Convert the generic scope.FilteredCaller back to sshforward.FilteredCaller
		if fc, ok := c.(*scope.FilteredCaller); ok {
			filtered := &FilteredCaller{FilteredCaller: fc}
			return f(ctx, id, filtered)
		}
		return f(ctx, id, c)
	})
}

var _ session.CallerManager = (*FilteredManager)(nil)
