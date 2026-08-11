package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/timewarp-dev/timewarp/internal/checkpointfs"
	"github.com/timewarp-dev/timewarp/internal/collector"
	"github.com/timewarp-dev/timewarp/internal/deviceid"
	"github.com/timewarp-dev/timewarp/internal/graph"
	"github.com/timewarp-dev/timewarp/internal/replay"
	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/internal/workspacepath"
	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
	"github.com/timewarp-dev/timewarp/pkg/trust"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	dbPath := env("TIMEWARP_DB", "timewarp.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	switch os.Args[1] {
	case "serve":
		serve(store)
	case "traces":
		traces(ctx, store)
	case "inspect", "graph":
		needTrace()
		events, err := store.GetTrace(ctx, os.Args[2])
		fatal(err)
		if len(events) == 0 {
			log.Fatal("trace not found")
		}
		g := graph.Build(events)
		fmt.Print(graph.ASCII(g))
		for _, d := range g.Diagnostics {
			fmt.Printf("! %s %s: %s\n", d.Code, d.EventID, d.Detail)
		}
	case "replay":
		replayCmd(ctx, store)
	case "consent":
		consentCmd(ctx, store)
	case "trust":
		trustCmd(ctx, store, dbPath)
	case "checkpoint":
		checkpointCmd(ctx, store)
	default:
		usage()
		os.Exit(2)
	}
}

type repeatedFiles []string

func (values *repeatedFiles) String() string { return strings.Join(*values, ",") }
func (values *repeatedFiles) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func checkpointCmd(ctx context.Context, store *storage.SQLite) {
	if len(os.Args) < 3 {
		checkpointUsage()
		os.Exit(2)
	}
	service := checkpointfs.Service{Store: store}
	switch os.Args[2] {
	case "create":
		fs := flag.NewFlagSet("checkpoint create", flag.ExitOnError)
		workspace := fs.String("workspace", "", "existing workspace directory")
		sessionID := fs.String("session", "", "agent session identifier")
		actor := fs.String("actor", env("TIMEWARP_CONSENT_OPERATOR", "local-user"), "operator creating the checkpoint")
		label := fs.String("label", "before agent edit", "checkpoint label")
		var files repeatedFiles
		fs.Var(&files, "file", "explicit workspace-relative file; repeat for multiple files")
		fs.Parse(os.Args[3:])
		fmt.Printf("Workspace: %s\nSession: %s\nFiles:\n", *workspace, *sessionID)
		for _, path := range files {
			fmt.Printf("- %s\n", path)
		}
		fmt.Printf("Type %s to capture these file contents locally: ", *sessionID)
		if !typedExactly(*sessionID) {
			log.Fatal("checkpoint creation cancelled")
		}
		item, err := service.Capture(ctx, checkpointfs.CaptureRequest{
			Workspace: *workspace, SessionID: *sessionID, Actor: *actor, Label: *label, Files: files,
		})
		fatal(err)
		if err := recordCheckpointAudit(ctx, store, item, "checkpoint_created", ""); err != nil {
			log.Fatalf("checkpoint %s was created, but its audit event failed: %v", item.ID, err)
		}
		fmt.Printf("created %s with %d files\n", item.ID, len(item.Files))
	case "list":
		fs := flag.NewFlagSet("checkpoint list", flag.ExitOnError)
		sessionID := fs.String("session", "", "optional agent session identifier")
		limit := fs.Int("limit", 50, "maximum checkpoints")
		fs.Parse(os.Args[3:])
		items, err := store.ListCheckpoints(ctx, checkpoint.ListFilter{SessionID: *sessionID, Limit: *limit})
		fatal(err)
		printJSON(items)
	case "inspect":
		if len(os.Args) != 4 {
			log.Fatal("usage: timewarp checkpoint inspect <checkpoint-id>")
		}
		item, err := store.GetCheckpoint(ctx, os.Args[3])
		fatal(err)
		printJSON(item)
	case "revert":
		if len(os.Args) < 4 {
			log.Fatal("checkpoint id is required")
		}
		checkpointID := os.Args[3]
		fs := flag.NewFlagSet("checkpoint revert", flag.ExitOnError)
		workspace := fs.String("workspace", "", "existing workspace directory")
		actor := fs.String("actor", env("TIMEWARP_CONSENT_OPERATOR", "local-user"), "operator reverting the checkpoint")
		fs.Parse(os.Args[4:])
		plan, err := service.Plan(ctx, checkpointID, *workspace)
		fatal(err)
		fmt.Printf("Workspace: %s\nRevert plan:\n", plan.Workspace)
		for _, entry := range plan.Entries {
			fmt.Printf("- %-9s %s\n", entry.Action, entry.Path)
		}
		fmt.Printf("Type %s to revert: ", checkpointID)
		if !typedExactly(checkpointID) {
			log.Fatal("checkpoint revert cancelled")
		}
		result, err := service.Revert(ctx, checkpointID, *workspace, *actor)
		fatal(err)
		if err := recordCheckpointAudit(ctx, store, result.Checkpoint, "checkpoint_reverted", result.SafetyCheckpointID); err != nil {
			log.Fatalf("checkpoint %s was reverted and safety checkpoint %s was created, but the audit event failed: %v", checkpointID, result.SafetyCheckpointID, err)
		}
		fmt.Printf("reverted %s; safety checkpoint %s preserves the pre-revert state\n", checkpointID, result.SafetyCheckpointID)
	default:
		checkpointUsage()
		os.Exit(2)
	}
}

func typedExactly(expected string) bool {
	scanner := bufio.NewScanner(os.Stdin)
	return scanner.Scan() && strings.TrimSpace(scanner.Text()) == expected && expected != ""
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	fatal(encoder.Encode(value))
}

func recordCheckpointAudit(ctx context.Context, store protocol.EventStore, item checkpoint.Checkpoint, action, safetyID string) error {
	paths := make([]string, len(item.Files))
	for index, file := range item.Files {
		paths[index] = file.Path
	}
	now := time.Now()
	return store.Save(ctx, protocol.Event{
		EventID: "checkpoint:" + item.ID + ":" + action + ":" + fmt.Sprint(now.UnixMilli()),
		TraceID: "agent-session:" + item.SessionID, Service: "timewarp-checkpoint", Type: protocol.Custom,
		Timestamp: now.UnixMilli(), Metadata: map[string]any{
			"action": action, "checkpoint_id": item.ID, "workspace": item.Workspace,
			"paths": paths, "safety_checkpoint_id": safetyID,
		},
	})
}

func checkpointUsage() {
	fmt.Fprintln(os.Stderr, "usage: timewarp checkpoint <create|list|inspect|revert> [arguments]")
}

func consentCmd(ctx context.Context, store *storage.SQLite) {
	if len(os.Args) < 3 {
		consentUsage()
		os.Exit(2)
	}
	switch os.Args[2] {
	case "list":
		fs := flag.NewFlagSet("consent list", flag.ExitOnError)
		statusValue := fs.String("status", "", "PENDING, ACTIVE, EXPIRED, or REVOKED")
		limit := fs.Int("limit", 50, "maximum grants")
		fs.Parse(os.Args[3:])
		status := consent.Status(strings.ToUpper(strings.TrimSpace(*statusValue)))
		if status != "" && status != consent.Pending && status != consent.Active && status != consent.Expired && status != consent.Revoked {
			log.Fatal("invalid consent status")
		}
		grants, err := store.ListGrants(ctx, consent.ListFilter{Status: status, NowMS: time.Now().UnixMilli(), Limit: *limit})
		fatal(err)
		printJSON(grants)
	case "approve":
		if len(os.Args) < 4 {
			log.Fatal("grant id is required")
		}
		grantID := os.Args[3]
		fs := flag.NewFlagSet("consent approve", flag.ExitOnError)
		ttl := fs.Duration("ttl", 15*time.Minute, "grant lifetime between 1m and 24h")
		fs.Parse(os.Args[4:])
		if *ttl < time.Minute || *ttl > 24*time.Hour {
			log.Fatal("ttl must be between 1m and 24h")
		}
		grant, err := store.GetGrant(ctx, grantID)
		fatal(err)
		if grant.Status != consent.Pending {
			log.Fatalf("grant is %s, not PENDING", grant.Status)
		}
		fmt.Printf("Session: %s\nActor: %s\nScopes: %s\nReason: %s\nTTL: %s\n", grant.SessionID, grant.Actor, strings.Join(grant.Scopes, ","), grant.Reason, ttl.String())
		fmt.Printf("Type %s to approve: ", grantID)
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != grantID {
			log.Fatal("approval cancelled")
		}
		now := time.Now()
		grant, err = store.ApproveGrant(ctx, grantID, now.UnixMilli(), now.Add(*ttl).UnixMilli())
		fatal(err)
		if err := recordConsentAudit(ctx, store, grant, "grant_approved", now); err != nil {
			_, revokeErr := store.RevokeGrant(ctx, grantID, time.Now().UnixMilli())
			log.Fatalf("audit approval failed; grant revoked (revoke error=%v): %v", revokeErr, err)
		}
		fmt.Printf("approved %s until %s\n", grant.ID, time.UnixMilli(grant.ExpiresAt).Format(time.RFC3339))
	case "revoke":
		if len(os.Args) != 4 {
			log.Fatal("usage: timewarp consent revoke <grant-id>")
		}
		now := time.Now()
		grant, err := store.RevokeGrant(ctx, os.Args[3], now.UnixMilli())
		fatal(err)
		fatal(recordConsentAudit(ctx, store, grant, "grant_revoked", now))
		fmt.Printf("revoked %s\n", grant.ID)
	default:
		consentUsage()
		os.Exit(2)
	}
}

func recordConsentAudit(ctx context.Context, store protocol.EventStore, grant consent.Grant, action string, now time.Time) error {
	return store.Save(ctx, protocol.Event{
		EventID: "consent:" + grant.ID + ":" + action + ":" + fmt.Sprint(now.UnixMilli()),
		TraceID: "agent-session:" + grant.SessionID, Service: "timewarp-consent", Type: protocol.Custom,
		Timestamp: now.UnixMilli(),
		Metadata: map[string]any{
			"action": action, "grant_id": grant.ID, "scopes": grant.Scopes,
			"status": grant.Status, "expires_at": grant.ExpiresAt,
			"operator": env("TIMEWARP_CONSENT_OPERATOR", "local-user"),
		},
	})
}

func consentUsage() {
	fmt.Fprintln(os.Stderr, "usage: timewarp consent <list|approve|revoke> [arguments]")
}

func trustCmd(ctx context.Context, store *storage.SQLite, dbPath string) {
	if len(os.Args) < 3 {
		trustUsage()
		os.Exit(2)
	}
	switch os.Args[2] {
	case "device":
		fs := flag.NewFlagSet("trust device", flag.ExitOnError)
		actor := fs.String("actor", env("TIMEWARP_CONSENT_OPERATOR", "cursor"), "operator actor that MCP will use")
		label := fs.String("label", hostnameOr("local-device"), "human-readable device label")
		scopesFlag := fs.String("scopes", strings.Join(trust.DefaultScopes, ","), "permanent scopes for this device")
		deviceIDFile := fs.String("device-id-file", env("TIMEWARP_DEVICE_ID_FILE", deviceid.DefaultPath(dbPath)), "stable device id file")
		fs.Parse(os.Args[3:])
		scopes := parseTrustScopes(*scopesFlag)
		deviceID, err := deviceid.ResolveOrCreate(*deviceIDFile)
		fatal(err)
		fmt.Printf("Device ID: %s\nLabel: %s\nActor: %s\nScopes: %s\nFile: %s\n", deviceID, *label, *actor, strings.Join(scopes, ","), *deviceIDFile)
		fmt.Printf("Type %s to trust this device permanently: ", deviceID)
		if !typedExactly(deviceID) {
			log.Fatal("device trust cancelled")
		}
		now := time.Now()
		device := trust.Device{
			ID: deviceID, DeviceLabel: strings.TrimSpace(*label), Actor: strings.TrimSpace(*actor),
			Scopes: scopes, Status: trust.Active, CreatedAt: now.UnixMilli(),
		}
		if existing, err := store.GetDevice(ctx, deviceID); err == nil && existing.Status == trust.Active {
			log.Fatalf("device %s is already trusted; revoke it first to change scopes", deviceID)
		} else if err != nil && !errors.Is(err, trust.ErrNotFound) {
			fatal(err)
		}
		if err := store.CreateDevice(ctx, device); err != nil {
			if errors.Is(err, trust.ErrInvalidState) {
				log.Fatalf("device %s is already trusted; revoke it first to change scopes", deviceID)
			}
			fatal(err)
		}
		if err := recordTrustAudit(ctx, store, "device_trusted", deviceID, "", scopes, now); err != nil {
			_, _ = store.RevokeDevice(ctx, deviceID, time.Now().UnixMilli())
			log.Fatalf("audit device trust failed; trust revoked: %v", err)
		}
		fmt.Printf("trusted device %s (%s)\n", deviceID, *label)
	case "workspace":
		fs := flag.NewFlagSet("trust workspace", flag.ExitOnError)
		workspace := fs.String("workspace", "", "existing workspace directory")
		actor := fs.String("actor", env("TIMEWARP_CONSENT_OPERATOR", "cursor"), "operator actor that MCP will use")
		scopesFlag := fs.String("scopes", strings.Join(trust.DefaultScopes, ","), "permanent scopes for this workspace (intersected with device)")
		deviceIDFile := fs.String("device-id-file", env("TIMEWARP_DEVICE_ID_FILE", deviceid.DefaultPath(dbPath)), "stable device id file")
		fs.Parse(os.Args[3:])
		if strings.TrimSpace(*workspace) == "" {
			log.Fatal("--workspace is required")
		}
		canonical, err := workspacepath.Canonical(*workspace)
		fatal(err)
		deviceID, err := deviceid.ResolveOrCreate(*deviceIDFile)
		fatal(err)
		device, err := store.GetDevice(ctx, deviceID)
		if errors.Is(err, trust.ErrNotFound) || (err == nil && device.Status != trust.Active) {
			log.Fatal(trust.ErrNoDevice)
		}
		fatal(err)
		if device.Actor != strings.TrimSpace(*actor) {
			log.Fatalf("device actor is %q, but --actor is %q", device.Actor, *actor)
		}
		scopes := trust.IntersectScopes(parseTrustScopes(*scopesFlag), device.Scopes)
		if len(scopes) == 0 {
			log.Fatal("no overlapping scopes with the trusted device")
		}
		fmt.Printf("Device ID: %s\nWorkspace: %s\nActor: %s\nScopes: %s\n", deviceID, canonical, *actor, strings.Join(scopes, ","))
		fmt.Printf("Type %s to trust this workspace permanently: ", canonical)
		if !typedExactly(canonical) {
			log.Fatal("workspace trust cancelled")
		}
		id, err := trust.NewWorkspaceID()
		fatal(err)
		now := time.Now()
		item := trust.Workspace{
			ID: id, DeviceID: deviceID, Workspace: canonical, Actor: strings.TrimSpace(*actor),
			Scopes: scopes, Status: trust.Active, CreatedAt: now.UnixMilli(),
		}
		fatal(store.CreateWorkspace(ctx, item))
		if err := recordTrustAudit(ctx, store, "workspace_trusted", deviceID, id, scopes, now); err != nil {
			_, _ = store.RevokeWorkspace(ctx, id, time.Now().UnixMilli())
			log.Fatalf("audit workspace trust failed; trust revoked: %v", err)
		}
		fmt.Printf("trusted workspace %s (%s)\n", id, canonical)
	case "list":
		fs := flag.NewFlagSet("trust list", flag.ExitOnError)
		statusValue := fs.String("status", "ACTIVE", "ACTIVE or REVOKED (empty for all)")
		limit := fs.Int("limit", 50, "maximum records per list")
		fs.Parse(os.Args[3:])
		status := trust.Status(strings.ToUpper(strings.TrimSpace(*statusValue)))
		if status != "" && status != trust.Active && status != trust.Revoked {
			log.Fatal("invalid trust status")
		}
		devices, err := store.ListDevices(ctx, trust.ListFilter{Status: status, Limit: *limit})
		fatal(err)
		workspaces, err := store.ListWorkspaces(ctx, trust.ListFilter{Status: status, Limit: *limit})
		fatal(err)
		printJSON(map[string]any{"devices": devices, "workspaces": workspaces})
	case "revoke":
		if len(os.Args) < 5 {
			log.Fatal("usage: timewarp trust revoke <device|workspace> <id>")
		}
		now := time.Now()
		switch os.Args[3] {
		case "device":
			device, err := store.RevokeDevice(ctx, os.Args[4], now.UnixMilli())
			fatal(err)
			fatal(recordTrustAudit(ctx, store, "device_revoked", device.ID, "", device.Scopes, now))
			fmt.Printf("revoked device %s (workspace trusts cascaded)\n", device.ID)
		case "workspace":
			item, err := store.RevokeWorkspace(ctx, os.Args[4], now.UnixMilli())
			fatal(err)
			fatal(recordTrustAudit(ctx, store, "workspace_revoked", item.DeviceID, item.ID, item.Scopes, now))
			fmt.Printf("revoked workspace %s\n", item.ID)
		default:
			log.Fatal("usage: timewarp trust revoke <device|workspace> <id>")
		}
	default:
		trustUsage()
		os.Exit(2)
	}
}

func parseTrustScopes(raw string) []string {
	parts := strings.Split(raw, ",")
	scopes := trust.NormalizeScopes(parts)
	if len(scopes) == 0 {
		scopes = append([]string{}, trust.DefaultScopes...)
	}
	allowed := map[string]bool{
		"trace:read": true, "checkpoint:read": true, "replay:build": true, "payload:read": true,
	}
	filtered := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !allowed[scope] {
			log.Fatalf("unsupported trust scope %q", scope)
		}
		filtered = append(filtered, scope)
	}
	return filtered
}

func recordTrustAudit(ctx context.Context, store protocol.EventStore, action, deviceID, workspaceID string, scopes []string, now time.Time) error {
	return store.Save(ctx, protocol.Event{
		EventID: "trust:" + action + ":" + deviceID + ":" + workspaceID + ":" + fmt.Sprint(now.UnixMilli()),
		TraceID: "agent-session:trust", Service: "timewarp-trust", Type: protocol.Custom,
		Timestamp: now.UnixMilli(),
		Metadata: map[string]any{
			"action": action, "device_id": deviceID, "workspace_id": workspaceID,
			"scopes": scopes, "operator": env("TIMEWARP_CONSENT_OPERATOR", "local-user"),
		},
	})
}

func hostnameOr(fallback string) string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func trustUsage() {
	fmt.Fprintln(os.Stderr, "usage: timewarp trust <device|workspace|list|revoke> [arguments]")
}

func serve(store protocol.EventStore) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":7777", "listen address")
	bufferEvents := fs.Int("buffer-events", 1024, "maximum queued events")
	flushInterval := fs.Duration("flush-interval", 25*time.Millisecond, "maximum batching delay")
	fs.Parse(os.Args[2:])
	c := collector.New(store, *bufferEvents, *flushInterval)
	defer c.Close()
	log.Printf("timewarp listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, c.Routes()))
}
func traces(ctx context.Context, store protocol.EventStore) {
	items, err := store.Search(ctx, protocol.TraceFilter{Limit: 50})
	fatal(err)
	fmt.Printf("%-22s %-22s %-8s %s\n", "TRACE", "SERVICE", "STATUS", "DURATION")
	for _, t := range items {
		fmt.Printf("%-22s %-22s %-8s %dms\n", t.TraceID, t.RootService, t.Status, t.DurationMS)
	}
}
func replayCmd(ctx context.Context, store protocol.EventStore) {
	needTrace()
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	addr := fs.String("addr", ":7778", "mock listen address")
	manifestOnly := fs.Bool("manifest", false, "print manifest and exit")
	fs.Parse(os.Args[3:])
	events, err := store.GetTrace(ctx, os.Args[2])
	fatal(err)
	if len(events) == 0 {
		log.Fatal("trace not found")
	}
	m := replay.BuildManifest(os.Args[2], events)
	fmt.Print(m.YAML())
	if *manifestOnly {
		return
	}
	log.Printf("recorded-only replay mock listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, replay.NewMockServer(events, m.PreserveLatency)))
}
func needTrace() {
	if len(os.Args) < 3 {
		log.Fatal("trace id is required")
	}
}
func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: timewarp <serve|traces|inspect|graph|replay|consent|trust|checkpoint> [arguments]")
}
