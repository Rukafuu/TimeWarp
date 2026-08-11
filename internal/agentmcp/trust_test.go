package agentmcp

import (
	"strings"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/trust"
)

func TestWorkspaceTrustGrantsTraceReadWithoutGrant(t *testing.T) {
	store := &memoryStore{}
	if err := store.CreateDevice(t.Context(), trust.Device{
		ID: "device_1", DeviceLabel: "test", Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateWorkspace(t.Context(), trust.Workspace{
		ID: "ws_1", DeviceID: "device_1", Workspace: `C:\proj`, Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}

	client := connectClient(t, NewServer(store, Config{
		Actor: "tester", GrantStore: store, TrustStore: store,
		DeviceID: "device_1", Workspace: `C:\proj`, Now: fixedNow,
	}))

	caps := callTool(t, client, "get_capabilities", map[string]any{"session_id": "session-trust"})
	if caps.IsError {
		t.Fatalf("get_capabilities failed: %s", toolText(caps))
	}
	var output CapabilitiesOutput
	decodeStructured(t, caps, &output)
	if !containsScope(output.ApprovedScopes, ScopeTraceRead) || !containsScope(output.ApprovedScopes, ScopeCheckpointRead) {
		t.Fatalf("approved scopes = %#v", output.ApprovedScopes)
	}
	if output.TrustedDeviceID != "device_1" || output.TrustedWorkspace != `C:\proj` {
		t.Fatalf("trust metadata = %#v", output)
	}
	if containsScope(output.ApprovedScopes, ScopePayloadRead) {
		t.Fatal("payload:read must not come from default trust")
	}

	allowed := callTool(t, client, "search_traces", map[string]any{"session_id": "session-trust"})
	if allowed.IsError {
		t.Fatalf("search with workspace trust failed: %s", toolText(allowed))
	}
}

func TestWorkspaceTrustRequiresDeviceAndMatchingWorkspace(t *testing.T) {
	store := &memoryStore{}
	_ = store.CreateDevice(t.Context(), trust.Device{
		ID: "device_1", DeviceLabel: "test", Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 1,
	})
	_ = store.CreateWorkspace(t.Context(), trust.Workspace{
		ID: "ws_1", DeviceID: "device_1", Workspace: `C:\proj`, Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 2,
	})

	wrongWorkspace := connectClient(t, NewServer(store, Config{
		Actor: "tester", GrantStore: store, TrustStore: store,
		DeviceID: "device_1", Workspace: `C:\other`, Now: fixedNow,
	}))
	denied := callTool(t, wrongWorkspace, "search_traces", map[string]any{"session_id": "session-trust"})
	if !denied.IsError || !strings.Contains(toolText(denied), ScopeTraceRead) {
		t.Fatalf("wrong workspace = error %v text %q", denied.IsError, toolText(denied))
	}

	deviceOnly := connectClient(t, NewServer(store, Config{
		Actor: "tester", GrantStore: store, TrustStore: store,
		DeviceID: "device_1", Workspace: "", Now: fixedNow,
	}))
	deniedDeviceOnly := callTool(t, deviceOnly, "search_traces", map[string]any{"session_id": "session-trust"})
	if !deniedDeviceOnly.IsError {
		t.Fatal("device-only trust must not authorize")
	}
}

func TestWorkspaceTrustRevokedBlocksAccess(t *testing.T) {
	store := &memoryStore{}
	_ = store.CreateDevice(t.Context(), trust.Device{
		ID: "device_1", DeviceLabel: "test", Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 1,
	})
	_ = store.CreateWorkspace(t.Context(), trust.Workspace{
		ID: "ws_1", DeviceID: "device_1", Workspace: `C:\proj`, Actor: "tester",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 2,
	})
	client := connectClient(t, NewServer(store, Config{
		Actor: "tester", GrantStore: store, TrustStore: store,
		DeviceID: "device_1", Workspace: `C:\proj`, Now: fixedNow,
	}))
	if callTool(t, client, "search_traces", map[string]any{"session_id": "session-trust"}).IsError {
		t.Fatal("expected trusted access before revoke")
	}
	if _, err := store.RevokeDevice(t.Context(), "device_1", 99); err != nil {
		t.Fatal(err)
	}
	denied := callTool(t, client, "search_traces", map[string]any{"session_id": "session-trust"})
	if !denied.IsError {
		t.Fatal("revoked trust must deny without MCP restart")
	}
}

func containsScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}
