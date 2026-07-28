package scope

import (
	"testing"
)

// TestScopeNeverEscalates verifies that scope intersection never allows
// more than what either input scope allows - it only narrows or stays the same.
func TestScopeNeverEscalates(t *testing.T) {
	tests := []struct {
		name        string
		scopeA      *Scope[string]
		scopeB      *Scope[string]
		description string
	}{
		{
			name:        "both nil",
			scopeA:      nil,
			scopeB:      nil,
			description: "nil scopes should remain nil",
		},
		{
			name:        "one nil",
			scopeA:      New([]string{"a", "b"}, false, nil),
			scopeB:      nil,
			description: "nil + restricted should yield restricted",
		},
		{
			name:        "both full",
			scopeA:      &Scope[string]{Full: true},
			scopeB:      &Scope[string]{Full: true},
			description: "full + full should remain full",
		},
		{
			name:        "full + restricted",
			scopeA:      &Scope[string]{Full: true},
			scopeB:      New([]string{"a", "b"}, false, nil),
			description: "full + restricted should yield restricted",
		},
		{
			name:        "restricted + restricted (overlap)",
			scopeA:      New([]string{"a", "b"}, false, nil),
			scopeB:      New([]string{"b", "c"}, false, nil),
			description: "should only allow intersection (b)",
		},
		{
			name:        "restricted + restricted (no overlap)",
			scopeA:      New([]string{"a", "b"}, false, nil),
			scopeB:      New([]string{"c", "d"}, false, nil),
			description: "should allow nothing",
		},
		{
			name: "with aliases (both full)",
			scopeA: &Scope[string]{
				Full:    true,
				Aliases: map[string]string{"x": "a", "y": "b"},
			},
			scopeB: &Scope[string]{
				Full:    true,
				Aliases: map[string]string{"z": "c"},
			},
			description: "should merge all aliases when both full",
		},
		{
			name: "with aliases (full + restricted)",
			scopeA: &Scope[string]{
				Full:    true,
				Aliases: map[string]string{"x": "a", "y": "b"},
			},
			scopeB: New([]string{"a", "c"}, false, nil),
			description: "should only keep aliases pointing to allowed keys",
		},
		{
			name: "with aliases (both restricted)",
			scopeA: New([]string{"a", "b"}, false, map[string]string{"x": "a", "y": "c"}),
			scopeB: New([]string{"a", "c"}, false, map[string]string{"z": "a", "w": "d"}),
			description: "should only keep aliases pointing to intersection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Intersect(tt.scopeA, tt.scopeB)

			// Verify that result never allows more than either input
			if tt.scopeA != nil && !tt.scopeA.Full {
				for key := range result.Allowed {
					if !tt.scopeA.Allows(key) {
						t.Errorf("intersection allows %s which scopeA does not allow", key)
					}
				}
			}
			if tt.scopeB != nil && !tt.scopeB.Full {
				for key := range result.Allowed {
					if !tt.scopeB.Allows(key) {
						t.Errorf("intersection allows %s which scopeB does not allow", key)
					}
				}
			}

			// Verify that result is a subset of both
			if tt.scopeA != nil && !tt.scopeA.Full && tt.scopeB != nil && !tt.scopeB.Full {
				for key := range tt.scopeA.Allowed {
					if result.Allows(key) && !tt.scopeB.Allows(key) {
						t.Errorf("intersection allows %s which scopeB does not allow", key)
					}
				}
				for key := range tt.scopeB.Allowed {
					if result.Allows(key) && !tt.scopeA.Allows(key) {
						t.Errorf("intersection allows %s which scopeA does not allow", key)
					}
				}
			}
		})
	}
}

// TestScopeResolveSecurity verifies that Resolve never allows access
// to keys outside the scope, even with aliases.
func TestScopeResolveSecurity(t *testing.T) {
	tests := []struct {
		name        string
		scope       *Scope[string]
		input       string
		shouldAllow bool
		description string
	}{
		{
			name:        "nil scope allows everything",
			scope:       nil,
			input:       "any-key",
			shouldAllow: true,
			description: "nil scope should allow any key",
		},
		{
			name:        "full scope allows everything",
			scope:       &Scope[string]{Full: true},
			input:       "any-key",
			shouldAllow: true,
			description: "full scope should allow any key",
		},
		{
			name:        "restricted scope allows only allowed keys",
			scope:       New([]string{"a", "b"}, false, nil),
			input:       "a",
			shouldAllow: true,
			description: "should allow key in allowed set",
		},
		{
			name:        "restricted scope rejects non-allowed keys",
			scope:       New([]string{"a", "b"}, false, nil),
			input:       "c",
			shouldAllow: false,
			description: "should reject key not in allowed set",
		},
		{
			name:        "alias to allowed key is allowed",
			scope:       New([]string{"a", "b"}, false, map[string]string{"x": "a"}),
			input:       "x",
			shouldAllow: true,
			description: "alias pointing to allowed key should be allowed",
		},
		{
			name:        "alias to non-allowed key is rejected",
			scope:       New([]string{"a", "b"}, false, map[string]string{"x": "c"}),
			input:       "x",
			shouldAllow: false,
			description: "alias pointing to non-allowed key should be rejected",
		},
		{
			name:        "full scope with alias allows any alias",
			scope:       &Scope[string]{Full: true, Aliases: map[string]string{"x": "y"}},
			input:       "x",
			shouldAllow: true,
			description: "full scope should allow any alias",
		},
		{
			name:        "empty string is ignored",
			scope:       New([]string{"a", "b"}, false, nil),
			input:       "",
			shouldAllow: false,
			description: "empty string should not be allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, allowed := tt.scope.Resolve(tt.input)
			if allowed != tt.shouldAllow {
				t.Errorf("%s: expected allow=%v, got allow=%v", tt.description, tt.shouldAllow, allowed)
			}
		})
	}
}

// TestScopeIntersectionIdempotent verifies that intersecting a scope with
// itself yields the same scope (idempotent property).
func TestScopeIntersectionIdempotent(t *testing.T) {
	scopes := []*Scope[string]{
		nil,
		&Scope[string]{Full: true},
		New([]string{"a", "b"}, false, nil),
		New([]string{"a", "b"}, false, map[string]string{"x": "a"}),
		&Scope[string]{Full: true, Aliases: map[string]string{"x": "a"}},
	}

	for i, s := range scopes {
		t.Run("scope_"+string(rune('a'+i)), func(t *testing.T) {
			result := Intersect(s, s)
			if s == nil {
				if result != nil {
					t.Errorf("expected nil, got non-nil result")
				}
			} else {
				if result == nil {
					t.Errorf("expected non-nil, got nil result")
				} else if result.Full != s.Full {
					t.Errorf("Full mismatch: expected %v, got %v", s.Full, result.Full)
				}
			}
		})
	}
}

// TestScopeIntersectionCommutative verifies that Intersect(a, b) == Intersect(b, a).
func TestScopeIntersectionCommutative(t *testing.T) {
	scopes := []*Scope[string]{
		nil,
		&Scope[string]{Full: true},
		New([]string{"a", "b"}, false, nil),
		New([]string{"b", "c"}, false, nil),
		New([]string{"a", "b"}, false, map[string]string{"x": "a"}),
	}

	for i, s1 := range scopes {
		for j, s2 := range scopes {
			t.Run(string(rune('a'+i))+"_"+string(rune('a'+j)), func(t *testing.T) {
				result1 := Intersect(s1, s2)
				result2 := Intersect(s2, s1)

				// Compare Full
				if (result1 == nil || result2 == nil) && result1 != result2 {
					t.Errorf("nil mismatch: result1=%v, result2=%v", result1, result2)
					return
				}
				if result1 != nil && result2 != nil && result1.Full != result2.Full {
					t.Errorf("Full mismatch: result1=%v, result2=%v", result1.Full, result2.Full)
				}

				// Compare Allowed sets
				if result1 != nil && result2 != nil && !result1.Full && !result2.Full {
					if len(result1.Allowed) != len(result2.Allowed) {
						t.Errorf("Allowed size mismatch: %d vs %d", len(result1.Allowed), len(result2.Allowed))
					}
					for k := range result1.Allowed {
						if !result2.Allows(k) {
							t.Errorf("result1 allows %s but result2 does not", k)
						}
					}
					for k := range result2.Allowed {
						if !result1.Allows(k) {
							t.Errorf("result2 allows %s but result1 does not", k)
						}
					}
				}
			})
		}
	}
}

// TestScopeIntersectionAssociative verifies that Intersect(Intersect(a, b), c) == Intersect(a, Intersect(b, c)).
func TestScopeIntersectionAssociative(t *testing.T) {
	scopes := []*Scope[string]{
		nil,
		&Scope[string]{Full: true},
		New([]string{"a", "b", "c"}, false, nil),
		New([]string{"b", "c", "d"}, false, nil),
		New([]string{"c", "d", "e"}, false, nil),
	}

	for _, a := range scopes {
		for _, b := range scopes {
			for _, c := range scopes {
				left := Intersect(Intersect(a, b), c)
				right := Intersect(a, Intersect(b, c))

				// Both should be nil or both non-nil
				if (left == nil) != (right == nil) {
					t.Errorf("nil mismatch: left=%v, right=%v", left, right)
					continue
				}

				if left != nil && right != nil {
					if left.Full != right.Full {
						t.Errorf("Full mismatch: left=%v, right=%v", left.Full, right.Full)
					}

					if !left.Full && !right.Full {
						if len(left.Allowed) != len(right.Allowed) {
							t.Errorf("Allowed size mismatch: %d vs %d", len(left.Allowed), len(right.Allowed))
						}
						for k := range left.Allowed {
							if !right.Allows(k) {
								t.Errorf("left allows %s but right does not", k)
							}
						}
						for k := range right.Allowed {
							if !left.Allows(k) {
								t.Errorf("right allows %s but left does not", k)
							}
						}
					}
				}
			}
		}
	}
}

// TestScopeNeverGrows verifies that repeated intersections never grow the allowed set.
func TestScopeNeverGrows(t *testing.T) {
	initial := New([]string{"a", "b", "c", "d", "e"}, false, nil)
	
	// Intersect with progressively smaller scopes
	scopes := []*Scope[string]{
		New([]string{"a", "b", "c"}, false, nil),
		New([]string{"a", "b"}, false, nil),
		New([]string{"a"}, false, nil),
	}

	current := initial
	prevSize := len(current.Allowed)

	for _, s := range scopes {
		current = Intersect(current, s)
		newSize := len(current.Allowed)
		
		if newSize > prevSize {
			t.Errorf("intersection grew from %d to %d", prevSize, newSize)
		}
		prevSize = newSize
	}

	// Final should be just "a"
	if len(current.Allowed) != 1 || !current.Allows("a") {
		t.Errorf("expected only 'a' to remain, got %v", current.Allowed)
	}
}
