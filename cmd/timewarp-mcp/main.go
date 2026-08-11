package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timewarp-dev/timewarp/internal/agentmcp"
	"github.com/timewarp-dev/timewarp/internal/deviceid"
	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/internal/workspacepath"
)

func main() {
	dbPath := flag.String("db", env("TIMEWARP_DB", "timewarp.db"), "Timewarp SQLite database")
	scopes := flag.String("scopes", os.Getenv("TIMEWARP_MCP_SCOPES"), "comma-separated operator-approved bootstrap scopes")
	actor := flag.String("actor", env("TIMEWARP_MCP_ACTOR", "local-user"), "audit actor name")
	workspace := flag.String("workspace", os.Getenv("TIMEWARP_MCP_WORKSPACE"), "canonical workspace directory for durable trust")
	deviceIDFile := flag.String("device-id-file", os.Getenv("TIMEWARP_DEVICE_ID_FILE"), "stable device id file (default: beside the DB)")
	flag.Parse()

	store, err := storage.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	deviceFile := strings.TrimSpace(*deviceIDFile)
	if deviceFile == "" {
		deviceFile = deviceid.DefaultPath(*dbPath)
	}
	deviceID, err := deviceid.ResolveOrCreate(deviceFile)
	if err != nil {
		log.Fatal(err)
	}

	cfg := agentmcp.Config{
		Actor:           *actor,
		Scopes:          agentmcp.ParseScopes(*scopes),
		GrantStore:      store,
		CheckpointStore: store,
		TrustStore:      store,
		DeviceID:        deviceID,
	}
	if ws := strings.TrimSpace(*workspace); ws != "" {
		canonical, err := workspacepath.Canonical(ws)
		if err != nil {
			log.Fatalf("workspace: %v", err)
		}
		cfg.Workspace = canonical
	} else if abs, err := filepath.Abs("."); err == nil {
		// Leave empty unless explicitly configured — trust requires an exact match.
		_ = abs
	}

	server := agentmcp.NewServer(store, cfg)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
