package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
	"github.com/timewarp-dev/timewarp/pkg/consent"
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

func TestSQLiteCheckpointLifecycle(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	item := checkpoint.Checkpoint{
		ID: "checkpoint_test", SessionID: "session-test", Actor: "local-user",
		Workspace: `C:\workspace`, Label: "before edit", Status: checkpoint.Active, CreatedAt: 1000,
		Files: []checkpoint.File{{Path: "a.txt", Existed: true, Mode: 0644, SHA256: "abc", Size: 3, Content: []byte("old")}, {Path: "new.txt"}},
	}
	if err := store.CreateCheckpoint(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCheckpoint(ctx, item.ID)
	if err != nil || len(got.Files) != 2 || string(got.Files[0].Content) != "old" {
		t.Fatalf("checkpoint = %+v, err=%v", got, err)
	}
	listed, err := store.ListCheckpoints(ctx, checkpoint.ListFilter{SessionID: item.SessionID})
	if err != nil || len(listed) != 1 || len(listed[0].Files) != 2 {
		t.Fatalf("listed = %+v, err=%v", listed, err)
	}
	if err := store.MarkCheckpointReverted(ctx, item.ID, 2000, "checkpoint_safety"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetCheckpoint(ctx, item.ID)
	if err != nil || got.Status != checkpoint.Reverted || got.RevertedAt != 2000 || got.SafetyCheckpointID != "checkpoint_safety" {
		t.Fatalf("reverted = %+v, err=%v", got, err)
	}
	if err := store.MarkCheckpointReverted(ctx, item.ID, 3000, "other"); !errors.Is(err, checkpoint.ErrInvalidState) {
		t.Fatalf("second revert error = %v", err)
	}
}

func TestSQLiteConsentGrantLifecycle(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "consent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	grant := consent.Grant{
		ID: "grant_test", SessionID: "session-test", Actor: "agent", Scopes: []string{"trace:read", "trace:read"},
		Reason: "inspect a failed trace", Status: consent.Pending, RequestedAt: 1000,
	}
	if err := s.CreateGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	pending, err := s.GetGrant(ctx, grant.ID)
	if err != nil || pending.Status != consent.Pending || len(pending.Scopes) != 1 {
		t.Fatalf("pending grant = %+v, err=%v", pending, err)
	}
	approved, err := s.ApproveGrant(ctx, grant.ID, 2000, 3000)
	if err != nil || approved.Status != consent.Active {
		t.Fatalf("approved grant = %+v, err=%v", approved, err)
	}
	active, err := s.ActiveGrants(ctx, grant.SessionID, 2500)
	if err != nil || len(active) != 1 {
		t.Fatalf("active grants = %+v, err=%v", active, err)
	}
	expired, err := s.ActiveGrants(ctx, grant.SessionID, 3000)
	if err != nil || len(expired) != 0 {
		t.Fatalf("expired active grants = %+v, err=%v", expired, err)
	}
	listed, err := s.ListGrants(ctx, consent.ListFilter{Status: consent.Expired, NowMS: 3000})
	if err != nil || len(listed) != 1 || listed[0].Status != consent.Expired {
		t.Fatalf("listed expired grants = %+v, err=%v", listed, err)
	}
	revoked, err := s.RevokeGrant(ctx, grant.ID, 3100)
	if err != nil || revoked.Status != consent.Revoked {
		t.Fatalf("revoked grant = %+v, err=%v", revoked, err)
	}
	if _, err := s.ApproveGrant(ctx, grant.ID, 3200, 4000); !errors.Is(err, consent.ErrInvalidState) {
		t.Fatalf("reapprove error = %v, want ErrInvalidState", err)
	}
}

func TestSQLiteBatchRollsBackWhenAnyEventIsInvalid(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	valid := protocol.Event{EventID: "valid", TraceID: "atomic", Service: "gateway", Type: protocol.Custom, Timestamp: 1}
	invalid := protocol.Event{EventID: "invalid", TraceID: "atomic", Service: "gateway", Type: protocol.Custom}
	if err := s.SaveBatch(context.Background(), []protocol.Event{valid, invalid}); err == nil {
		t.Fatal("SaveBatch accepted an invalid event")
	}
	events, err := s.GetTrace(context.Background(), "atomic")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("batch was partially persisted: %#v", events)
	}
}
