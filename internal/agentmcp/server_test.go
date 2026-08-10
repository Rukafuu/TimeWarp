package agentmcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type memoryStore struct {
	mu          sync.Mutex
	events      []protocol.Event
	grants      map[string]consent.Grant
	checkpoints map[string]checkpoint.Checkpoint
}

func (s *memoryStore) CreateCheckpoint(_ context.Context, item checkpoint.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = map[string]checkpoint.Checkpoint{}
	}
	s.checkpoints[item.ID] = item
	return nil
}

func (s *memoryStore) GetCheckpoint(_ context.Context, id string) (checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.checkpoints[id]
	if !ok {
		return checkpoint.Checkpoint{}, checkpoint.ErrNotFound
	}
	return item, nil
}

func (s *memoryStore) ListCheckpoints(_ context.Context, filter checkpoint.ListFilter) ([]checkpoint.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []checkpoint.Checkpoint
	for _, item := range s.checkpoints {
		if filter.SessionID == "" || item.SessionID == filter.SessionID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *memoryStore) MarkCheckpointReverted(_ context.Context, id string, revertedAt int64, safetyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.checkpoints[id]
	if !ok {
		return checkpoint.ErrNotFound
	}
	item.Status, item.RevertedAt, item.SafetyCheckpointID = checkpoint.Reverted, revertedAt, safetyID
	s.checkpoints[id] = item
	return nil
}

func (s *memoryStore) CreateGrant(_ context.Context, grant consent.Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = map[string]consent.Grant{}
	}
	grant.Status = consent.Pending
	s.grants[grant.ID] = grant
	return nil
}

func (s *memoryStore) GetGrant(_ context.Context, id string) (consent.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[id]
	if !ok {
		return consent.Grant{}, consent.ErrNotFound
	}
	return grant, nil
}

func (s *memoryStore) ListGrants(_ context.Context, filter consent.ListFilter) ([]consent.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []consent.Grant
	for _, grant := range s.grants {
		grant.Status = grant.EffectiveStatus(filter.NowMS)
		if filter.Status == "" || filter.Status == grant.Status {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (s *memoryStore) ApproveGrant(_ context.Context, id string, approvedAt, expiresAt int64) (consent.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[id]
	if !ok {
		return consent.Grant{}, consent.ErrNotFound
	}
	if grant.Status != consent.Pending {
		return consent.Grant{}, consent.ErrInvalidState
	}
	grant.Status, grant.ApprovedAt, grant.ExpiresAt = consent.Active, approvedAt, expiresAt
	s.grants[id] = grant
	return grant, nil
}

func (s *memoryStore) RevokeGrant(_ context.Context, id string, revokedAt int64) (consent.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[id]
	if !ok {
		return consent.Grant{}, consent.ErrNotFound
	}
	grant.Status, grant.RevokedAt = consent.Revoked, revokedAt
	s.grants[id] = grant
	return grant, nil
}

func (s *memoryStore) ActiveGrants(_ context.Context, sessionID string, nowMS int64) ([]consent.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []consent.Grant
	for _, grant := range s.grants {
		if grant.SessionID == sessionID && grant.IsActive(nowMS) {
			out = append(out, grant)
		}
	}
	return out, nil
}

func (s *memoryStore) Save(ctx context.Context, event protocol.Event) error {
	return s.SaveBatch(ctx, []protocol.Event{event})
}

func (s *memoryStore) SaveBatch(_ context.Context, events []protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return err
		}
		s.events = append(s.events, event)
	}
	return nil
}

func (s *memoryStore) GetTrace(_ context.Context, traceID string) ([]protocol.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.Event
	for _, event := range s.events {
		if event.TraceID == traceID {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *memoryStore) Search(_ context.Context, filter protocol.TraceFilter) ([]protocol.Trace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []protocol.Trace
	for _, event := range s.events {
		if filter.Service != "" && event.Service != filter.Service {
			continue
		}
		if seen[event.TraceID] || strings.HasPrefix(event.TraceID, "agent-session:") {
			continue
		}
		seen[event.TraceID] = true
		out = append(out, protocol.Trace{TraceID: event.TraceID, RootService: event.Service, Status: "OK", StartedAt: event.Timestamp, EventCount: 1})
	}
	return out, nil
}

func TestConsentRequestDoesNotGrantScope(t *testing.T) {
	store := &memoryStore{}
	client := connectClient(t, NewServer(store, Config{Actor: "tester", GrantStore: store, Now: fixedNow}))

	denied := callTool(t, client, "search_traces", map[string]any{"session_id": "session-1"})
	if !denied.IsError || !strings.Contains(toolText(denied), ScopeTraceRead) {
		t.Fatalf("search without consent = error %v, text %q", denied.IsError, toolText(denied))
	}

	requested := callTool(t, client, "request_consent", map[string]any{
		"session_id": "session-1", "requested_scopes": []string{ScopeTraceRead}, "reason": "Investigate a failed request",
	})
	if requested.IsError {
		t.Fatalf("request_consent failed: %s", toolText(requested))
	}
	var output ConsentOutput
	decodeStructured(t, requested, &output)
	if output.Approved {
		t.Fatal("request_consent must never self-approve")
	}

	deniedBeforeApproval := callTool(t, client, "search_traces", map[string]any{"session_id": "session-1"})
	if !deniedBeforeApproval.IsError {
		t.Fatal("requesting consent unexpectedly activated the scope")
	}
	if _, err := store.ApproveGrant(context.Background(), output.GrantID, fixedNow().UnixMilli(), fixedNow().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	allowed := callTool(t, client, "search_traces", map[string]any{"session_id": "session-1"})
	if allowed.IsError {
		t.Fatalf("approved dynamic grant was not applied: %s", toolText(allowed))
	}
	if _, err := store.RevokeGrant(context.Background(), output.GrantID, fixedNow().Add(time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	deniedAfterRevocation := callTool(t, client, "search_traces", map[string]any{"session_id": "session-1"})
	if !deniedAfterRevocation.IsError {
		t.Fatal("revoked grant still authorized a protected call")
	}
	audit, _ := store.GetTrace(context.Background(), "agent-session:session-1")
	if len(audit) != 5 {
		t.Fatalf("audit event count = %d, want 5", len(audit))
	}
}

func TestConsentReasonRejectsTerminalControlCharacters(t *testing.T) {
	store := &memoryStore{}
	client := connectClient(t, NewServer(store, Config{Actor: "tester", GrantStore: store, Now: fixedNow}))
	result := callTool(t, client, "request_consent", map[string]any{
		"session_id": "session-control", "requested_scopes": []string{ScopeTraceRead}, "reason": "inspect\x1b[2Jhidden",
	})
	if !result.IsError {
		t.Fatal("control characters in operator-facing reason were accepted")
	}
}

func TestDynamicGrantExpiresWithoutRestart(t *testing.T) {
	nowMS := int64(1_800_000_000_000)
	store := &memoryStore{}
	grant := consent.Grant{ID: "grant_expiring", SessionID: "session-expiry", Actor: "tester", Scopes: []string{ScopeTraceRead}, Reason: "test", RequestedAt: nowMS}
	if err := store.CreateGrant(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveGrant(context.Background(), grant.ID, nowMS, nowMS+100); err != nil {
		t.Fatal(err)
	}
	client := connectClient(t, NewServer(store, Config{Actor: "tester", GrantStore: store, Now: func() time.Time { return time.UnixMilli(nowMS) }}))
	if result := callTool(t, client, "search_traces", map[string]any{"session_id": grant.SessionID}); result.IsError {
		t.Fatalf("active grant denied: %s", toolText(result))
	}
	nowMS += 100
	if result := callTool(t, client, "search_traces", map[string]any{"session_id": grant.SessionID}); !result.IsError {
		t.Fatal("expired grant still authorized a protected call")
	}
}

func TestTracePayloadsAreRedactedAndReadsAreAudited(t *testing.T) {
	store := &memoryStore{events: []protocol.Event{{
		EventID: "event-1", TraceID: "trace-1", Service: "example-api", Type: protocol.HTTPClient, Timestamp: 1,
		Metadata: map[string]any{
			"name": "browser.render_issue", "dom": map[string]any{"text": "private"},
			"diagnostic": map[string]any{"html": "private nested html"},
		},
		HTTP: &protocol.HTTPRecord{
			Method: "POST", URL: "https://example.test/action", StatusCode: 200,
			RequestHeaders: map[string][]string{"Authorization": {"secret"}}, RequestBody: []byte("private request"),
			ResponseHeaders: map[string][]string{"Set-Cookie": {"secret"}}, ResponseBody: []byte("private response"),
		},
	}}}
	client := connectClient(t, NewServer(store, Config{Actor: "tester", Scopes: ParseScopes(ScopeTraceRead), Now: fixedNow}))

	result := callTool(t, client, "get_trace", map[string]any{"session_id": "session-2", "trace_id": "trace-1"})
	if result.IsError {
		t.Fatalf("get_trace failed: %s", toolText(result))
	}
	var output TraceOutput
	decodeStructured(t, result, &output)
	if !output.Redacted || len(output.Events) != 1 {
		t.Fatalf("unexpected trace output: %+v", output)
	}
	httpRecord := output.Events[0].HTTP
	if httpRecord == nil || httpRecord.RequestBodyB64 != "" || httpRecord.ResponseBodyB64 != "" || httpRecord.RequestHeaders != nil || httpRecord.ResponseHeaders != nil {
		t.Fatalf("sensitive HTTP fields were not redacted: %+v", httpRecord)
	}
	if output.Events[0].Metadata["name"] != "browser.render_issue" || output.Events[0].Metadata["dom"] != "[redacted:payload-read-required]" {
		t.Fatalf("sensitive diagnostic metadata was not selectively redacted: %+v", output.Events[0].Metadata)
	}
	nested, ok := output.Events[0].Metadata["diagnostic"].(map[string]any)
	if !ok || nested["html"] != "[redacted:payload-read-required]" {
		t.Fatalf("nested sensitive metadata was not redacted: %+v", output.Events[0].Metadata)
	}

	deniedPayload := callTool(t, client, "get_trace", map[string]any{"session_id": "session-2", "trace_id": "trace-1", "include_payloads": true})
	if !deniedPayload.IsError || !strings.Contains(toolText(deniedPayload), ScopePayloadRead) {
		t.Fatalf("payload read should be denied: %q", toolText(deniedPayload))
	}
	audit, _ := store.GetTrace(context.Background(), "agent-session:session-2")
	if len(audit) != 2 {
		t.Fatalf("audit event count = %d, want 2", len(audit))
	}
	activeScopes, ok := audit[0].Metadata["approved_scopes"].([]string)
	if !ok || len(activeScopes) != 1 || activeScopes[0] != ScopeTraceRead {
		t.Fatalf("audited scopes = %#v, want [%s]", audit[0].Metadata["approved_scopes"], ScopeTraceRead)
	}

	payloadClient := connectClient(t, NewServer(store, Config{
		Actor: "tester", Scopes: ParseScopes(ScopeTraceRead + "," + ScopePayloadRead), Now: fixedNow,
	}))
	withPayload := callTool(t, payloadClient, "get_trace", map[string]any{
		"session_id": "session-payload", "trace_id": "trace-1", "include_payloads": true,
	})
	if withPayload.IsError {
		t.Fatalf("payload-authorized trace failed: %s", toolText(withPayload))
	}
	var payloadOutput TraceOutput
	decodeStructured(t, withPayload, &payloadOutput)
	if _, ok := payloadOutput.Events[0].Metadata["dom"].(map[string]any); !ok {
		t.Fatalf("authorized diagnostic metadata missing: %+v", payloadOutput.Events[0].Metadata)
	}
	if payloadOutput.Events[0].HTTP.RequestBodyB64 != "cHJpdmF0ZSByZXF1ZXN0" || payloadOutput.Events[0].HTTP.ResponseBodyB64 != "cHJpdmF0ZSByZXNwb25zZQ==" {
		t.Fatalf("authorized HTTP bodies were not returned as explicit base64: %+v", payloadOutput.Events[0].HTTP)
	}
}

func TestBuildReplayReturnsPlanWithoutExecution(t *testing.T) {
	store := &memoryStore{events: []protocol.Event{{
		EventID: "event-1", TraceID: "trace-2", Service: "checkout", Type: protocol.HTTPClient, Timestamp: 1,
		HTTP: &protocol.HTTPRecord{Method: "POST", URL: "https://example.test/pay", StatusCode: 503},
	}}}
	scopes := ParseScopes(ScopeTraceRead + "," + ScopeReplayBuild)
	client := connectClient(t, NewServer(store, Config{Actor: "tester", Scopes: scopes, Now: fixedNow}))

	result := callTool(t, client, "build_replay", map[string]any{"session_id": "session-3", "trace_id": "trace-2"})
	if result.IsError {
		t.Fatalf("build_replay failed: %s", toolText(result))
	}
	var output ReplayOutput
	decodeStructured(t, result, &output)
	if output.Executed || !strings.Contains(output.Manifest, "mode: recorded") {
		t.Fatalf("unexpected replay output: %+v", output)
	}
}

func TestCheckpointToolsAreReadOnlyAndSessionScoped(t *testing.T) {
	store := &memoryStore{checkpoints: map[string]checkpoint.Checkpoint{
		"checkpoint_one": {
			ID: "checkpoint_one", SessionID: "session-checkpoint", Actor: "local-user", Workspace: `C:\workspace`,
			Label: "before edit", Status: checkpoint.Active, CreatedAt: 1000,
			Files: []checkpoint.File{{Path: "secret.txt", Existed: true, SHA256: "abc", Size: 6, Content: []byte("secret")}},
		},
	}}
	client := connectClient(t, NewServer(store, Config{
		Actor: "tester", Scopes: ParseScopes(ScopeCheckpointRead), CheckpointStore: store, Now: fixedNow,
	}))
	listed := callTool(t, client, "list_checkpoints", map[string]any{"session_id": "session-checkpoint"})
	if listed.IsError {
		t.Fatalf("list checkpoints failed: %s", toolText(listed))
	}
	encoded, err := json.Marshal(listed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "c2VjcmV0") || strings.Contains(string(encoded), `"Content"`) {
		t.Fatalf("checkpoint content leaked: %s", encoded)
	}
	denied := callTool(t, client, "get_checkpoint", map[string]any{"session_id": "other-session", "checkpoint_id": "checkpoint_one"})
	if !denied.IsError || !strings.Contains(toolText(denied), "does not belong") {
		t.Fatalf("cross-session checkpoint read should be denied: %s", toolText(denied))
	}
}

func connectClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "timewarp-test", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		serverSession.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientSession.Close()
		serverSession.Close()
	})
	return clientSession
}

func callTool(t *testing.T, client *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func toolText(result *mcp.CallToolResult) string {
	var text string
	for _, content := range result.Content {
		if item, ok := content.(*mcp.TextContent); ok {
			text += item.Text
		}
	}
	return text
}

func fixedNow() time.Time { return time.UnixMilli(1_800_000_000_000) }
