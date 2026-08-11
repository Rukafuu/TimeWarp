package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/trust"
)

func TestSQLiteTrustLifecycle(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "trust.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	device := trust.Device{
		ID: "device_test", DeviceLabel: "test-host", Actor: "cursor",
		Scopes: []string{"trace:read", "checkpoint:read", "trace:read"},
		Status: trust.Active, CreatedAt: 1000,
	}
	if err := s.CreateDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDevice(ctx, device.ID)
	if err != nil || got.Status != trust.Active || len(got.Scopes) != 2 {
		t.Fatalf("device = %+v, err=%v", got, err)
	}

	ws := trust.Workspace{
		ID: "trust_ws_test", DeviceID: device.ID, Workspace: `C:\proj`, Actor: "cursor",
		Scopes: []string{"trace:read", "checkpoint:read", "payload:read"},
		Status: trust.Active, CreatedAt: 1100,
	}
	if err := s.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	resolved, err := s.Resolve(ctx, device.ID, `C:\proj`, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	// Intersection with device scopes drops payload:read.
	if len(resolved.Scopes) != 2 || resolved.WorkspaceID != ws.ID {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, err := s.Resolve(ctx, device.ID, `C:\other`, "cursor"); !errors.Is(err, trust.ErrNotFound) {
		t.Fatalf("wrong workspace err = %v", err)
	}
	if _, err := s.Resolve(ctx, device.ID, `C:\proj`, "other"); !errors.Is(err, trust.ErrNotFound) {
		t.Fatalf("wrong actor err = %v", err)
	}

	if _, err := s.RevokeWorkspace(ctx, ws.ID, 2000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, device.ID, `C:\proj`, "cursor"); !errors.Is(err, trust.ErrNotFound) {
		t.Fatalf("revoked workspace still resolved: %v", err)
	}

	ws2 := trust.Workspace{
		ID: "trust_ws_test2", DeviceID: device.ID, Workspace: `C:\proj`, Actor: "cursor",
		Scopes: trust.DefaultScopes, Status: trust.Active, CreatedAt: 2100,
	}
	if err := s.CreateWorkspace(ctx, ws2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, device.ID, `C:\proj`, "cursor"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.RevokeDevice(ctx, device.ID, 3000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, device.ID, `C:\proj`, "cursor"); !errors.Is(err, trust.ErrNotFound) {
		t.Fatalf("revoked device still resolved: %v", err)
	}
	item, err := s.GetWorkspace(ctx, ws2.ID)
	if err != nil || item.Status != trust.Revoked {
		t.Fatalf("cascade revoke workspace = %+v, err=%v", item, err)
	}
}
