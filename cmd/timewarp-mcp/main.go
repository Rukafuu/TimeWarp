package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timewarp-dev/timewarp/internal/agentmcp"
	"github.com/timewarp-dev/timewarp/internal/storage"
)

func main() {
	dbPath := flag.String("db", env("TIMEWARP_DB", "timewarp.db"), "Timewarp SQLite database")
	scopes := flag.String("scopes", os.Getenv("TIMEWARP_MCP_SCOPES"), "comma-separated operator-approved scopes")
	actor := flag.String("actor", env("TIMEWARP_MCP_ACTOR", "local-user"), "audit actor name")
	flag.Parse()

	store, err := storage.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	server := agentmcp.NewServer(store, agentmcp.Config{
		Actor:           *actor,
		Scopes:          agentmcp.ParseScopes(*scopes),
		GrantStore:      store,
		CheckpointStore: store,
	})
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
