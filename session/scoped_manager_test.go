package session

import (
	"context"
	"testing"

	"google.golang.org/grpc"
)

func TestScopedManager(t *testing.T) {
	// Create a base manager
	baseMgr, err := NewManager()
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create a scoped manager wrapping the base manager
	scopedMgr := NewScopedManager(baseMgr)

	// Test that scoped session IDs can be registered and resolved
	parentID := "parent-session-id"
	
	// Create a mock caller for testing
	mockCaller := &mockCaller{id: parentID}
	
	// Register a scoped session
	scopedID := scopedMgr.RegisterScopedSession(parentID, mockCaller)
	if scopedID == "" {
		t.Fatal("Expected non-empty scoped session ID")
	}

	// Verify the scoped ID is different from parent
	if scopedID == parentID {
		t.Fatal("Scoped ID should be different from parent ID")
	}

	// Verify IsScopedID works
	if !scopedMgr.IsScopedID(scopedID) {
		t.Fatal("Expected scoped ID to be recognized as scoped")
	}
	if scopedMgr.IsScopedID(parentID) {
		t.Fatal("Expected parent ID not to be recognized as scoped")
	}

	// Verify ResolveParentID works
	resolvedParent := scopedMgr.ResolveParentID(scopedID)
	if resolvedParent != parentID {
		t.Fatalf("Expected parent ID %s, got %s", parentID, resolvedParent)
	}

	// Verify Get resolves to the restricted caller
	caller, err := scopedMgr.Get(context.Background(), scopedID, false)
	if err != nil {
		t.Fatalf("Failed to get scoped session: %v", err)
	}
	if caller == nil {
		t.Fatal("Expected to get a caller back")
	}

	// Test DeleteScopedSession
	scopedMgr.DeleteScopedSession(scopedID)
	if scopedMgr.IsScopedID(scopedID) {
		t.Fatal("Expected scoped ID to be deleted")
	}
}

// mockCaller is a simple mock implementation of Caller for testing
type mockCaller struct {
	id string
}

func (m *mockCaller) Context(ctx context.Context) context.Context {
	return ctx
}

func (m *mockCaller) Supports(method string) bool {
	return true
}

func (m *mockCaller) Conn() *grpc.ClientConn {
	return nil
}

func (m *mockCaller) SharedKey() string {
	return "test-shared-key"
}
