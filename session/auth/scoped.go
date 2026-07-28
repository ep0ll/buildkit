package auth

import (
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/scope"
)

// Scope restricts which registry hosts are reachable through a session's
// Auth service (Credentials, FetchToken, GetTokenAuthority,
// VerifyTokenAuthority). It shares its implementation with
// session/secrets.Scope via the generic session/scope package, so a nested
// build's registry-auth access can be scoped down exactly the same way its
// secret access is.
type Scope = scope.Scope[string]

// NewScope builds a Scope from a set of allowed hosts and an optional alias
// map (child-visible host -> parent-visible host). Pass aliases as nil when
// no aliasing is needed.
func NewScope(allowed []string, full bool, aliases map[string]string) *Scope {
	return scope.New(allowed, full, aliases)
}

// Intersect returns the intersection of two scopes: the result allows only
// hosts that both a and b allow.
func Intersect(a, b *Scope) *Scope {
	return scope.Intersect(a, b)
}

// hoster is implemented by every Auth request message (they all have a
// Host field with a generated getter), letting a single GetKey/WithKey pair
// per method reuse the same accessor.
type hoster interface {
	GetHost() string
}

func hostMethod(fullMethod string, withHost func(req any, host string) any) scope.MethodScope {
	return scope.MethodScope{
		FullMethod: fullMethod,
		GetKey: func(req any) string {
			return req.(hoster).GetHost()
		},
		WithKey: withHost,
	}
}

var authMethods = []scope.MethodScope{
	hostMethod(Auth_Credentials_FullMethodName, func(req any, host string) any {
		return &CredentialsRequest{Host: host}
	}),
	hostMethod(Auth_FetchToken_FullMethodName, func(req any, host string) any {
		r := req.(*FetchTokenRequest)
		return &FetchTokenRequest{ClientID: r.ClientID, Host: host, Realm: r.Realm, Service: r.Service, Scopes: r.Scopes}
	}),
	hostMethod(Auth_GetTokenAuthority_FullMethodName, func(req any, host string) any {
		r := req.(*GetTokenAuthorityRequest)
		return &GetTokenAuthorityRequest{Host: host, Salt: r.Salt}
	}),
	hostMethod(Auth_VerifyTokenAuthority_FullMethodName, func(req any, host string) any {
		r := req.(*VerifyTokenAuthorityRequest)
		return &VerifyTokenAuthorityRequest{Host: host, Payload: r.Payload, Salt: r.Salt}
	}),
}

// FilteredCaller wraps a session.Caller and restricts Auth-service access to
// a Scope of registry hosts, enforced at the gRPC transport level so the
// restriction can't be bypassed via context manipulation.
type FilteredCaller struct {
	*scope.FilteredCaller
}

// NewFilteredCaller creates a FilteredCaller with effective scope
// Intersect(parentScope, childScope).
func NewFilteredCaller(inner session.Caller, childScope, parentScope *Scope) *FilteredCaller {
	return &FilteredCaller{scope.NewFilteredCaller(inner, childScope, parentScope, authMethods...)}
}

// AuthClient returns an AuthClient backed by the scope-enforcing connection.
// Always use this instead of NewAuthClient(fc.Conn()) so host restrictions
// are actually enforced.
func (fc *FilteredCaller) AuthClient() AuthClient {
	return NewAuthClient(fc.FilteredConn())
}
