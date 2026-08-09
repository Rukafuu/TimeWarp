package graph

import (
	"strings"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

func TestBuildOrdersAndDiagnoses(t *testing.T) {
	events := []protocol.Event{{EventID: "child", TraceID: "t", ParentID: "root", Service: "payment", Type: protocol.HTTPClient, Timestamp: 20}, {EventID: "root", TraceID: "t", Service: "gateway", Type: protocol.HTTPServer, Timestamp: 10}, {EventID: "lost", TraceID: "t", ParentID: "missing", Service: "worker", Type: protocol.Custom, Timestamp: 30}, {EventID: "root", TraceID: "t", Service: "duplicate", Type: protocol.Custom, Timestamp: 40}}
	g := Build(events)
	if len(g.Roots) != 2 {
		t.Fatalf("roots=%d", len(g.Roots))
	}
	codes := map[string]bool{}
	for _, d := range g.Diagnostics {
		codes[d.Code] = true
	}
	if !codes["DUPLICATE"] || !codes["ORPHAN"] {
		t.Fatalf("diagnostics=%v", g.Diagnostics)
	}
	ascii := ASCII(g)
	if !strings.Contains(ascii, "gateway") || !strings.Contains(ascii, "payment") {
		t.Fatalf("ASCII=%q", ascii)
	}
}
func TestBuildDetectsCycle(t *testing.T) {
	g := Build([]protocol.Event{{EventID: "a", TraceID: "t", ParentID: "b", Service: "a", Type: protocol.Custom, Timestamp: 1}, {EventID: "b", TraceID: "t", ParentID: "a", Service: "b", Type: protocol.Custom, Timestamp: 2}})
	for _, d := range g.Diagnostics {
		if d.Code == "CYCLE" {
			return
		}
	}
	t.Fatal("cycle not diagnosed")
}
