package agentmcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timewarp-dev/timewarp/internal/graph"
	"github.com/timewarp-dev/timewarp/internal/replay"
	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
	"github.com/timewarp-dev/timewarp/pkg/trust"
)

const (
	ScopeTraceRead      = "trace:read"
	ScopePayloadRead    = "payload:read"
	ScopeReplayBuild    = "replay:build"
	ScopeCheckpointRead = "checkpoint:read"
)

var (
	supportedScopes = map[string]struct{}{
		ScopeTraceRead: {}, ScopePayloadRead: {}, ScopeReplayBuild: {}, ScopeCheckpointRead: {},
	}
	sessionPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

type Config struct {
	Actor           string
	Scopes          map[string]bool
	GrantStore      consent.Store
	CheckpointStore checkpoint.Store
	TrustStore      trust.Store
	DeviceID        string
	Workspace       string
	Now             func() time.Time
}

type gateway struct {
	store protocol.EventStore
	cfg   Config
	seq   atomic.Uint64
}

type SessionInput struct {
	SessionID string `json:"session_id" jsonschema:"stable non-secret identifier used to audit this agent investigation"`
}

type CapabilitiesInput struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"optional session identifier used to resolve temporary grants"`
}

type CapabilitiesOutput struct {
	Actor            string   `json:"actor"`
	ApprovedScopes   []string `json:"approved_scopes"`
	SupportedScopes  []string `json:"supported_scopes"`
	ConsentMode      string   `json:"consent_mode"`
	MutationTools    bool     `json:"mutation_tools"`
	AuditTracePrefix string   `json:"audit_trace_prefix"`
	ActiveGrantIDs   []string `json:"active_grant_ids,omitempty"`
	ExpiresAt        int64    `json:"expires_at,omitempty"`
	TrustedDeviceID  string   `json:"trusted_device_id,omitempty"`
	TrustedWorkspace string   `json:"trusted_workspace,omitempty"`
	TrustIDs         []string `json:"trust_ids,omitempty"`
}

type ConsentInput struct {
	SessionID       string   `json:"session_id" jsonschema:"stable non-secret identifier for the proposed investigation"`
	RequestedScopes []string `json:"requested_scopes" jsonschema:"capability scopes the agent wants the operator to approve"`
	Reason          string   `json:"reason" jsonschema:"short user-facing explanation of why the scopes are needed"`
}

type ConsentOutput struct {
	GrantID         string   `json:"grant_id"`
	Status          string   `json:"status"`
	Approved        bool     `json:"approved"`
	RequestedScopes []string `json:"requested_scopes"`
	Reason          string   `json:"reason"`
	OperatorAction  string   `json:"operator_action"`
	AuditTraceID    string   `json:"audit_trace_id"`
}

type SearchInput struct {
	SessionInput
	Service string `json:"service,omitempty" jsonschema:"optional exact service name"`
	SinceMS int64  `json:"since_ms,omitempty" jsonschema:"optional Unix millisecond lower bound"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum traces from 1 to 1000"`
}

type SearchOutput struct {
	Traces []protocol.Trace `json:"traces"`
}

type TraceInput struct {
	SessionInput
	TraceID         string `json:"trace_id" jsonschema:"trace identifier to retrieve"`
	IncludePayloads bool   `json:"include_payloads,omitempty" jsonschema:"include captured headers and bodies; additionally requires payload:read"`
}

type TraceOutput struct {
	TraceID  string       `json:"trace_id"`
	Events   []TraceEvent `json:"events"`
	Redacted bool         `json:"redacted"`
}

type TraceEvent struct {
	EventID    string         `json:"event_id"`
	TraceID    string         `json:"trace_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Service    string         `json:"service"`
	Instance   string         `json:"instance,omitempty"`
	Type       string         `json:"type"`
	Timestamp  int64          `json:"timestamp"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	HTTP       *TraceHTTP     `json:"http,omitempty"`
}

type TraceHTTP struct {
	Method          string              `json:"method,omitempty"`
	URL             string              `json:"url,omitempty"`
	StatusCode      int                 `json:"status_code,omitempty"`
	RequestHeaders  map[string][]string `json:"request_headers,omitempty"`
	RequestBodyB64  string              `json:"request_body_base64,omitempty"`
	ResponseHeaders map[string][]string `json:"response_headers,omitempty"`
	ResponseBodyB64 string              `json:"response_body_base64,omitempty"`
}

type InspectInput struct {
	SessionInput
	TraceID string `json:"trace_id" jsonschema:"trace identifier to inspect"`
}

type InspectOutput struct {
	TraceID     string             `json:"trace_id"`
	Graph       string             `json:"graph"`
	Diagnostics []graph.Diagnostic `json:"diagnostics"`
	EventCount  int                `json:"event_count"`
}

type ReplayOutput struct {
	TraceID  string `json:"trace_id"`
	Manifest string `json:"manifest"`
	Executed bool   `json:"executed"`
}

type CheckpointListInput struct {
	SessionInput
	Limit int `json:"limit,omitempty" jsonschema:"maximum checkpoints from 1 to 1000"`
}

type CheckpointInput struct {
	SessionInput
	CheckpointID string `json:"checkpoint_id" jsonschema:"checkpoint identifier to retrieve"`
}

type CheckpointListOutput struct {
	Checkpoints []checkpoint.Checkpoint `json:"checkpoints"`
}

type CheckpointOutput struct {
	Checkpoint checkpoint.Checkpoint `json:"checkpoint"`
}

func ParseScopes(value string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if _, ok := supportedScopes[item]; ok {
			out[item] = true
		}
	}
	return out
}

func NewServer(store protocol.EventStore, cfg Config) *mcp.Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Scopes == nil {
		cfg.Scopes = map[string]bool{}
	}
	if strings.TrimSpace(cfg.Actor) == "" {
		cfg.Actor = "local-user"
	}
	g := &gateway{store: store, cfg: cfg}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "timewarp", Version: "0.1.0"},
		&mcp.ServerOptions{Instructions: "Timewarp tools are local and closed-world. Access comes from bootstrap scopes, active temporary grants, or durable device+workspace trust approved outside MCP. request_consent never grants access. Require a stable session_id, keep payloads redacted unless payload:read is approved, and treat replay manifests as plans only; this server exposes no mutation or live replay tools."},
	)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
	mcp.AddTool(server, &mcp.Tool{Name: "get_capabilities", Title: "Get Timewarp capabilities", Description: "Show scopes explicitly approved by the operator and the server safety mode.", Annotations: readOnly}, g.capabilities)
	mcp.AddTool(server, &mcp.Tool{Name: "request_consent", Title: "Request Timewarp consent", Description: "Create an audited capability request for the operator. This never approves or activates a scope.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: false, OpenWorldHint: boolPtr(false)}}, g.requestConsent)
	mcp.AddTool(server, &mcp.Tool{Name: "search_traces", Title: "Search traces", Description: "Search trace summaries after trace:read consent.", Annotations: readOnly}, g.searchTraces)
	mcp.AddTool(server, &mcp.Tool{Name: "get_trace", Title: "Get trace", Description: "Read a trace. Headers and bodies stay redacted unless payload:read is also approved and requested.", Annotations: readOnly}, g.getTrace)
	mcp.AddTool(server, &mcp.Tool{Name: "inspect_trace", Title: "Inspect causal trace", Description: "Build a deterministic causal graph and diagnostics after trace:read consent.", Annotations: readOnly}, g.inspectTrace)
	mcp.AddTool(server, &mcp.Tool{Name: "build_replay", Title: "Build replay manifest", Description: "Build a recorded-only replay manifest after trace:read and replay:build consent. It never starts a server or contacts an upstream.", Annotations: readOnly}, g.buildReplay)
	mcp.AddTool(server, &mcp.Tool{Name: "list_checkpoints", Title: "List reversible checkpoints", Description: "List metadata and hashes for this session's checkpoints after checkpoint:read consent. File contents are never returned.", Annotations: readOnly}, g.listCheckpoints)
	mcp.AddTool(server, &mcp.Tool{Name: "get_checkpoint", Title: "Get reversible checkpoint", Description: "Read checkpoint metadata and hashes after checkpoint:read consent. File contents and reversal are unavailable to MCP.", Annotations: readOnly}, g.getCheckpoint)
	return server
}

func (g *gateway) listCheckpoints(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointListInput) (*mcp.CallToolResult, CheckpointListOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "list_checkpoints", input.SessionID, ScopeCheckpointRead); err != nil {
		return nil, CheckpointListOutput{}, err
	}
	if g.cfg.CheckpointStore == nil {
		return nil, CheckpointListOutput{}, errors.New("checkpoint store is not configured")
	}
	items, err := g.cfg.CheckpointStore.ListCheckpoints(ctx, checkpoint.ListFilter{SessionID: input.SessionID, Limit: input.Limit})
	if err != nil {
		return nil, CheckpointListOutput{}, err
	}
	if err := g.audit(ctx, input.SessionID, "list_checkpoints", input.SessionID, "allowed"); err != nil {
		return nil, CheckpointListOutput{}, err
	}
	return nil, CheckpointListOutput{Checkpoints: items}, nil
}

func (g *gateway) getCheckpoint(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointInput) (*mcp.CallToolResult, CheckpointOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "get_checkpoint", input.CheckpointID, ScopeCheckpointRead); err != nil {
		return nil, CheckpointOutput{}, err
	}
	if g.cfg.CheckpointStore == nil {
		return nil, CheckpointOutput{}, errors.New("checkpoint store is not configured")
	}
	item, err := g.cfg.CheckpointStore.GetCheckpoint(ctx, strings.TrimSpace(input.CheckpointID))
	if err != nil {
		return nil, CheckpointOutput{}, err
	}
	if item.SessionID != input.SessionID {
		_ = g.audit(ctx, input.SessionID, "get_checkpoint", input.CheckpointID, "denied:session")
		return nil, CheckpointOutput{}, errors.New("checkpoint does not belong to this session")
	}
	if err := g.audit(ctx, input.SessionID, "get_checkpoint", input.CheckpointID, "allowed"); err != nil {
		return nil, CheckpointOutput{}, err
	}
	return nil, CheckpointOutput{Checkpoint: item}, nil
}

func (g *gateway) capabilities(ctx context.Context, _ *mcp.CallToolRequest, input CapabilitiesInput) (*mcp.CallToolResult, CapabilitiesOutput, error) {
	if input.SessionID != "" {
		if err := validateSession(input.SessionID); err != nil {
			return nil, CapabilitiesOutput{}, err
		}
	}
	auth, err := g.effectiveAuthorization(ctx, input.SessionID)
	if err != nil {
		return nil, CapabilitiesOutput{}, fmt.Errorf("resolve grants: %w", err)
	}
	mode := "operator-approved-temporary-grants"
	if g.cfg.TrustStore != nil && g.cfg.DeviceID != "" && g.cfg.Workspace != "" {
		mode = "bootstrap-grants-and-workspace-trust"
	}
	return nil, CapabilitiesOutput{
		Actor: g.cfg.Actor, ApprovedScopes: sortedEnabled(auth.scopes), SupportedScopes: sortedSupported(),
		ConsentMode: mode, MutationTools: false, AuditTracePrefix: "agent-session:",
		ActiveGrantIDs: auth.grantIDs, ExpiresAt: auth.expiresAt,
		TrustedDeviceID: auth.deviceID, TrustedWorkspace: auth.workspace, TrustIDs: auth.trustIDs,
	}, nil
}

func (g *gateway) requestConsent(ctx context.Context, _ *mcp.CallToolRequest, input ConsentInput) (*mcp.CallToolResult, ConsentOutput, error) {
	if err := validateSession(input.SessionID); err != nil {
		return nil, ConsentOutput{}, err
	}
	requested, err := validateRequestedScopes(input.RequestedScopes)
	if err != nil {
		return nil, ConsentOutput{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 500 || strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return nil, ConsentOutput{}, errors.New("reason must contain 1 to 500 characters")
	}
	if g.cfg.GrantStore == nil {
		return nil, ConsentOutput{}, errors.New("dynamic consent store is not configured")
	}
	grantID, err := consent.NewID()
	if err != nil {
		return nil, ConsentOutput{}, fmt.Errorf("create grant id: %w", err)
	}
	grant := consent.Grant{
		ID: grantID, SessionID: input.SessionID, Actor: g.cfg.Actor, Scopes: requested,
		Reason: reason, Status: consent.Pending, RequestedAt: g.cfg.Now().UnixMilli(),
	}
	if err := g.cfg.GrantStore.CreateGrant(ctx, grant); err != nil {
		return nil, ConsentOutput{}, fmt.Errorf("persist consent request: %w", err)
	}
	if err := g.audit(ctx, input.SessionID, "request_consent", strings.Join(requested, ","), "requested"); err != nil {
		return nil, ConsentOutput{}, fmt.Errorf("audit consent request: %w", err)
	}
	return nil, ConsentOutput{
		GrantID: grantID, Status: string(consent.Pending), Approved: false, RequestedScopes: requested, Reason: reason,
		OperatorAction: "Run `timewarp consent approve " + grantID + " --ttl 15m` and type the grant ID when prompted after reviewing the reason and scopes.",
		AuditTraceID:   auditTraceID(input.SessionID),
	}, nil
}

func (g *gateway) searchTraces(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "search_traces", input.Service, ScopeTraceRead); err != nil {
		return nil, SearchOutput{}, err
	}
	filter := protocol.TraceFilter{Limit: input.Limit, Service: strings.TrimSpace(input.Service)}
	if input.SinceMS > 0 {
		filter.Since = time.UnixMilli(input.SinceMS)
	}
	traces, err := g.store.Search(ctx, filter)
	if err != nil {
		return nil, SearchOutput{}, err
	}
	if err := g.audit(ctx, input.SessionID, "search_traces", input.Service, "allowed"); err != nil {
		return nil, SearchOutput{}, fmt.Errorf("audit search: %w", err)
	}
	return nil, SearchOutput{Traces: traces}, nil
}

func (g *gateway) getTrace(ctx context.Context, _ *mcp.CallToolRequest, input TraceInput) (*mcp.CallToolResult, TraceOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "get_trace", input.TraceID, ScopeTraceRead); err != nil {
		return nil, TraceOutput{}, err
	}
	if input.IncludePayloads {
		if err := g.authorize(ctx, input.SessionID, "get_trace", input.TraceID, ScopePayloadRead); err != nil {
			return nil, TraceOutput{}, err
		}
	}
	events, err := g.loadTrace(ctx, input.TraceID)
	if err != nil {
		return nil, TraceOutput{}, err
	}
	redacted := !input.IncludePayloads
	if redacted {
		events = redactPayloads(events)
	}
	if err := g.audit(ctx, input.SessionID, "get_trace", input.TraceID, "allowed"); err != nil {
		return nil, TraceOutput{}, fmt.Errorf("audit trace read: %w", err)
	}
	return nil, TraceOutput{TraceID: input.TraceID, Events: traceEvents(events), Redacted: redacted}, nil
}

func (g *gateway) inspectTrace(ctx context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, InspectOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "inspect_trace", input.TraceID, ScopeTraceRead); err != nil {
		return nil, InspectOutput{}, err
	}
	events, err := g.loadTrace(ctx, input.TraceID)
	if err != nil {
		return nil, InspectOutput{}, err
	}
	causal := graph.Build(events)
	if err := g.audit(ctx, input.SessionID, "inspect_trace", input.TraceID, "allowed"); err != nil {
		return nil, InspectOutput{}, fmt.Errorf("audit trace inspection: %w", err)
	}
	return nil, InspectOutput{TraceID: input.TraceID, Graph: graph.ASCII(causal), Diagnostics: causal.Diagnostics, EventCount: len(events)}, nil
}

func (g *gateway) buildReplay(ctx context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, ReplayOutput, error) {
	if err := g.authorize(ctx, input.SessionID, "build_replay", input.TraceID, ScopeTraceRead, ScopeReplayBuild); err != nil {
		return nil, ReplayOutput{}, err
	}
	events, err := g.loadTrace(ctx, input.TraceID)
	if err != nil {
		return nil, ReplayOutput{}, err
	}
	manifest := replay.BuildManifest(input.TraceID, events).YAML()
	if err := g.audit(ctx, input.SessionID, "build_replay", input.TraceID, "allowed"); err != nil {
		return nil, ReplayOutput{}, fmt.Errorf("audit replay build: %w", err)
	}
	return nil, ReplayOutput{TraceID: input.TraceID, Manifest: manifest, Executed: false}, nil
}

func (g *gateway) authorize(ctx context.Context, sessionID, action, target string, scopes ...string) error {
	if err := validateSession(sessionID); err != nil {
		return err
	}
	auth, err := g.effectiveAuthorization(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("resolve grants: %w", err)
	}
	for _, scope := range scopes {
		if !auth.scopes[scope] {
			_ = g.audit(ctx, sessionID, action, target, "denied:"+scope)
			return fmt.Errorf("scope %s was not approved by the operator", scope)
		}
	}
	return nil
}

type authorization struct {
	scopes    map[string]bool
	grantIDs  []string
	trustIDs  []string
	expiresAt int64
	deviceID   string
	workspace string
}

func (g *gateway) effectiveAuthorization(ctx context.Context, sessionID string) (authorization, error) {
	auth := authorization{scopes: map[string]bool{}}
	for scope, enabled := range g.cfg.Scopes {
		if enabled {
			auth.scopes[scope] = true
		}
	}
	if g.cfg.TrustStore != nil && g.cfg.DeviceID != "" && g.cfg.Workspace != "" && g.cfg.Actor != "" {
		resolved, err := g.cfg.TrustStore.Resolve(ctx, g.cfg.DeviceID, g.cfg.Workspace, g.cfg.Actor)
		if err == nil {
			auth.deviceID = resolved.DeviceID
			auth.workspace = resolved.Workspace
			auth.trustIDs = []string{resolved.DeviceID, resolved.WorkspaceID}
			for _, scope := range resolved.Scopes {
				if _, supported := supportedScopes[scope]; supported {
					auth.scopes[scope] = true
				}
			}
		} else if !errors.Is(err, trust.ErrNotFound) {
			return authorization{}, err
		}
	}
	if sessionID == "" || g.cfg.GrantStore == nil {
		sort.Strings(auth.trustIDs)
		return auth, nil
	}
	grants, err := g.cfg.GrantStore.ActiveGrants(ctx, sessionID, g.cfg.Now().UnixMilli())
	if err != nil {
		return authorization{}, err
	}
	for _, grant := range grants {
		auth.grantIDs = append(auth.grantIDs, grant.ID)
		if auth.expiresAt == 0 || grant.ExpiresAt < auth.expiresAt {
			auth.expiresAt = grant.ExpiresAt
		}
		for _, scope := range grant.Scopes {
			if _, supported := supportedScopes[scope]; supported {
				auth.scopes[scope] = true
			}
		}
	}
	sort.Strings(auth.grantIDs)
	sort.Strings(auth.trustIDs)
	return auth, nil
}

func (g *gateway) loadTrace(ctx context.Context, traceID string) ([]protocol.Event, error) {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" || len(traceID) > 256 {
		return nil, errors.New("trace_id must contain 1 to 256 characters")
	}
	events, err := g.store.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, errors.New("trace not found")
	}
	return events, nil
}

func (g *gateway) audit(ctx context.Context, sessionID, action, target, outcome string) error {
	now := g.cfg.Now()
	auth, err := g.effectiveAuthorization(ctx, sessionID)
	if err != nil {
		return err
	}
	target = strings.TrimSpace(target)
	if len(target) > 256 {
		target = target[:256]
	}
	event := protocol.Event{
		EventID: fmt.Sprintf("tool:%d:%d", now.UnixMilli(), g.seq.Add(1)),
		TraceID: auditTraceID(sessionID), Service: "timewarp-mcp", Type: protocol.Custom,
		Timestamp: now.UnixMilli(),
		Metadata: map[string]any{
			"actor": g.cfg.Actor, "action": action, "target": target, "outcome": outcome,
			"approved_scopes": sortedEnabled(auth.scopes), "grant_ids": auth.grantIDs, "trust_ids": auth.trustIDs,
		},
	}
	return g.store.Save(ctx, event)
}

func redactPayloads(events []protocol.Event) []protocol.Event {
	out := make([]protocol.Event, len(events))
	for i, event := range events {
		out[i] = event
		out[i].Metadata = redactSensitiveMetadata(event.Metadata)
		if event.HTTP != nil {
			httpCopy := *event.HTTP
			httpCopy.RequestHeaders = nil
			httpCopy.RequestBody = nil
			httpCopy.ResponseHeaders = nil
			httpCopy.ResponseBody = nil
			out[i].HTTP = &httpCopy
		}
	}
	return out
}

func traceEvents(events []protocol.Event) []TraceEvent {
	out := make([]TraceEvent, len(events))
	for index, event := range events {
		out[index] = TraceEvent{
			EventID: event.EventID, TraceID: event.TraceID, ParentID: event.ParentID,
			Service: event.Service, Instance: event.Instance, Type: string(event.Type),
			Timestamp: event.Timestamp, DurationMS: event.DurationMS, Metadata: event.Metadata,
		}
		if event.HTTP != nil {
			out[index].HTTP = &TraceHTTP{
				Method: event.HTTP.Method, URL: event.HTTP.URL, StatusCode: event.HTTP.StatusCode,
				RequestHeaders: event.HTTP.RequestHeaders, ResponseHeaders: event.HTTP.ResponseHeaders,
				RequestBodyB64:  base64.StdEncoding.EncodeToString(event.HTTP.RequestBody),
				ResponseBodyB64: base64.StdEncoding.EncodeToString(event.HTTP.ResponseBody),
			}
		}
	}
	return out
}

func redactSensitiveMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	redacted := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if sensitiveMetadataKey(key) {
			redacted[key] = "[redacted:payload-read-required]"
			continue
		}
		redacted[key] = redactMetadataValue(value)
	}
	return redacted
}

func redactMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactSensitiveMetadata(typed)
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactMetadataValue(item)
		}
		return redacted
	default:
		return value
	}
}

func sensitiveMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch normalized {
	case "dom", "dom_snapshot", "html", "inner_html", "outer_html", "screenshot", "visual", "form_values":
		return true
	default:
		return false
	}
}

func validateSession(sessionID string) error {
	if !sessionPattern.MatchString(sessionID) {
		return errors.New("session_id must match [A-Za-z0-9._:-] and contain 1 to 128 characters")
	}
	return nil
}

func validateRequestedScopes(items []string) ([]string, error) {
	set := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if _, ok := supportedScopes[item]; !ok {
			return nil, fmt.Errorf("unsupported scope %q", item)
		}
		set[item] = true
	}
	out := sortedEnabled(set)
	if len(out) == 0 {
		return nil, errors.New("at least one supported scope is required")
	}
	return out, nil
}

func sortedSupported() []string {
	out := make([]string, 0, len(supportedScopes))
	for scope := range supportedScopes {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func sortedEnabled(scopes map[string]bool) []string {
	out := make([]string, 0, len(scopes))
	for scope, enabled := range scopes {
		if enabled {
			out = append(out, scope)
		}
	}
	sort.Strings(out)
	return out
}

func auditTraceID(sessionID string) string { return "agent-session:" + sessionID }
func boolPtr(value bool) *bool             { return &value }
