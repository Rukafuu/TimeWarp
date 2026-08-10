package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/timewarp-dev/timewarp/internal/checkpointfs"
	"github.com/timewarp-dev/timewarp/internal/collector"
	"github.com/timewarp-dev/timewarp/internal/graph"
	"github.com/timewarp-dev/timewarp/internal/replay"
	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
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
	fmt.Fprintln(os.Stderr, "usage: timewarp <serve|traces|inspect|graph|replay|consent|checkpoint> [arguments]")
}
