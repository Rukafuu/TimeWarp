package checkpointfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
)

func TestCapturePlanAndRevert(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "existing.txt")
	created := filepath.Join(root, "created.txt")
	if err := os.WriteFile(original, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := Service{Store: store, Now: func() time.Time { return time.UnixMilli(1000) }}
	ctx := context.Background()
	item, err := service.Capture(ctx, CaptureRequest{
		Workspace: root, SessionID: "session-1", Actor: "local-user", Label: "before edit",
		Files: []string{"existing.txt", "created.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, item.ID, root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Entries[0].Action != "delete" || plan.Entries[1].Action != "restore" {
		t.Fatalf("unexpected plan: %+v", plan.Entries)
	}
	result, err := service.Revert(ctx, item.ID, root, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(original)
	if err != nil || string(content) != "original" {
		t.Fatalf("restored content=%q err=%v", content, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file still exists: %v", err)
	}
	safety, err := store.GetCheckpoint(ctx, result.SafetyCheckpointID)
	if err != nil || len(safety.Files) != 2 || safety.Status != checkpoint.Active {
		t.Fatalf("safety checkpoint=%+v err=%v", safety, err)
	}
	if _, err := service.Revert(ctx, item.ID, root, "local-user"); !errors.Is(err, checkpoint.ErrInvalidState) {
		t.Fatalf("second revert error=%v", err)
	}
}

func TestCaptureRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := Service{Store: store}
	base := CaptureRequest{Workspace: root, SessionID: "session-1", Actor: "local-user", Label: "test"}
	for _, path := range []string{"../outside.txt", root} {
		request := base
		request.Files = []string{path}
		if _, err := service.Capture(context.Background(), request); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
}
