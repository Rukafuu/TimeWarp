package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

func TestSQLiteRoundTripAndDuplicate(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := protocol.Event{EventID: "e1", TraceID: "t1", Service: "gateway", Type: protocol.HTTPServer, Timestamp: 10, Metadata: map[string]any{"route": "/"}}
	if err = s.SaveBatch(context.Background(), []protocol.Event{e, e}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTrace(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d", len(got))
	}
	traces, err := s.Search(context.Background(), protocol.TraceFilter{})
	if err != nil || len(traces) != 1 {
		t.Fatalf("traces=%v err=%v", traces, err)
	}
}
