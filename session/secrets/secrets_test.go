package secrets

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Scope.Resolve
// ---------------------------------------------------------------------------

func TestScopeResolve_Nil(t *testing.T) {
	// A nil scope allows everything and resolves to itself (legacy behaviour).
	var s *Scope
	id, ok := s.Resolve("any-secret")
	require.True(t, ok)
	require.Equal(t, "any-secret", id)
}

func TestScopeResolve_Full(t *testing.T) {
	// Full scope resolves to itself regardless of Allowed contents.
	s := NewScope(nil, true, nil)
	id, ok := s.Resolve("any-secret")
	require.True(t, ok)
	require.Equal(t, "any-secret", id)
}

func TestScopeResolve_DirectAllow(t *testing.T) {
	s := NewScope([]string{"token", "db"}, false, nil)

	id, ok := s.Resolve("token")
	require.True(t, ok)
	require.Equal(t, "token", id)

	_, ok = s.Resolve("missing")
	require.False(t, ok)
}

func TestScopeResolve_Alias(t *testing.T) {
	// "db" is an alias for "prod_db"; "prod_db" is in Allowed.
	s := NewScope([]string{"prod_db", "ci_token"}, false, map[string]string{
		"db":    "prod_db",
		"token": "ci_token",
	})

	parentID, ok := s.Resolve("db")
	require.True(t, ok)
	require.Equal(t, "prod_db", parentID, "alias should resolve to parent name")

	parentID, ok = s.Resolve("token")
	require.True(t, ok)
	require.Equal(t, "ci_token", parentID)
}

func TestScopeResolve_AliasToDisallowedParent(t *testing.T) {
	// "db" aliases "prod_db" but "prod_db" is NOT in Allowed.
	s := NewScope([]string{"other_secret"}, false, map[string]string{
		"db": "prod_db",
	})

	_, ok := s.Resolve("db")
	require.False(t, ok, "alias to non-allowed parent must be denied")
}

func TestScopeResolve_AliasOnFullScope(t *testing.T) {
	// On a Full scope aliases resolve to their parent name (no filtering).
	s := NewScope(nil, true, map[string]string{"db": "prod_db"})
	parentID, ok := s.Resolve("db")
	require.True(t, ok)
	require.Equal(t, "prod_db", parentID)
}

// ---------------------------------------------------------------------------
// Allows (delegates to Resolve)
// ---------------------------------------------------------------------------

func TestScopeAllows(t *testing.T) {
	s := NewScope([]string{"secret-a"}, false, map[string]string{"alias-a": "secret-a"})
	require.True(t, s.Allows("secret-a"))
	require.True(t, s.Allows("alias-a"))
	require.False(t, s.Allows("secret-b"))
}

// ---------------------------------------------------------------------------
// Intersect
// ---------------------------------------------------------------------------

func TestIntersect_BothFull(t *testing.T) {
	a := NewScope(nil, true, map[string]string{"x": "y"})
	b := NewScope(nil, true, map[string]string{"p": "q"})
	out := Intersect(a, b)
	require.True(t, out.Full)
	// All aliases from both sides are preserved.
	require.Equal(t, "y", out.Aliases["x"])
	require.Equal(t, "q", out.Aliases["p"])
}

func TestIntersect_OneFullOneRestricted(t *testing.T) {
	full := NewScope(nil, true, map[string]string{"x": "prod"})
	restricted := NewScope([]string{"prod", "ci"}, false, map[string]string{"db": "prod"})
	out := Intersect(full, restricted)
	require.False(t, out.Full)
	// Allowed set = restricted side.
	_, ok := out.Allowed["prod"]
	require.True(t, ok)
	// Alias "db"→"prod" survives because "prod" is in result.
	require.Equal(t, "prod", out.Aliases["db"])
	// Alias "x"→"prod" from full side also survives.
	require.Equal(t, "prod", out.Aliases["x"])
}

func TestIntersect_TwoRestricted_Overlap(t *testing.T) {
	a := NewScope([]string{"common", "only-a"}, false, map[string]string{"alias-a": "common"})
	b := NewScope([]string{"common", "only-b"}, false, map[string]string{"alias-b": "common"})
	out := Intersect(a, b)
	require.False(t, out.Full)
	// Only "common" survives.
	_, hasCommon := out.Allowed["common"]
	require.True(t, hasCommon)
	_, hasOnlyA := out.Allowed["only-a"]
	require.False(t, hasOnlyA)
	// Aliases from both sides whose parent is "common" survive.
	require.Equal(t, "common", out.Aliases["alias-a"])
	require.Equal(t, "common", out.Aliases["alias-b"])
}

func TestIntersect_TwoRestricted_NoOverlap(t *testing.T) {
	a := NewScope([]string{"secret-a"}, false, nil)
	b := NewScope([]string{"secret-b"}, false, nil)
	out := Intersect(a, b)
	require.False(t, out.Full)
	require.Empty(t, out.Allowed)
}

func TestIntersect_AliasDroppedWhenParentNotInResult(t *testing.T) {
	// "alias-a" → "secret-a" is in scope A, but "secret-a" is not in scope B.
	a := NewScope([]string{"secret-a"}, false, map[string]string{"alias-a": "secret-a"})
	b := NewScope([]string{"secret-b"}, false, nil)
	out := Intersect(a, b)
	require.Empty(t, out.Allowed, "intersection should be empty")
	require.Empty(t, out.Aliases, "alias should be pruned when parent not in result")
}

// ---------------------------------------------------------------------------
// Intersect — nesting cannot escalate
// ---------------------------------------------------------------------------

func TestIntersect_NestingCannotGrow(t *testing.T) {
	// Parent allows only "a".
	parent := NewScope([]string{"a"}, false, nil)

	// Child tries to grant "a" and "b".
	child := NewScope([]string{"a", "b"}, false, nil)

	// Nesting is modeled as Intersect(parent, child).
	effective := Intersect(parent, child)
	require.True(t, effective.Allows("a"))
	require.False(t, effective.Allows("b"), "child cannot escalate beyond parent's allow-list")
}

func TestIntersect_FullChildDoesNotEscalateRestrictedParent(t *testing.T) {
	parent := NewScope([]string{"limited"}, false, nil)

	// Child claims Full — must be intersected down to parent's restriction.
	child := NewScope(nil, true, nil)

	effective := Intersect(parent, child)
	require.False(t, effective.Full, "Full child must not override restricted parent")
	require.True(t, effective.Allows("limited"))
	require.False(t, effective.Allows("other"))
}

// ---------------------------------------------------------------------------
// parseSecretAliases is tested indirectly via NewScope — verified by Resolve.
// ---------------------------------------------------------------------------
