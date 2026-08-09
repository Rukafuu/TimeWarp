package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/timewarp-dev/timewarp/internal/collector"
	"github.com/timewarp-dev/timewarp/internal/graph"
	"github.com/timewarp-dev/timewarp/internal/replay"
	"github.com/timewarp-dev/timewarp/internal/storage"
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
	default:
		usage()
		os.Exit(2)
	}
}
func serve(store protocol.EventStore) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":7777", "listen address")
	fs.Parse(os.Args[2:])
	c := collector.New(store, 256, 0)
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
	fmt.Fprintln(os.Stderr, "usage: timewarp <serve|traces|inspect|graph|replay> [arguments]")
}
