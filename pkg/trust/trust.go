package trust

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

type Status string

const (
	Active  Status = "ACTIVE"
	Revoked Status = "REVOKED"
)

var (
	ErrNotFound     = errors.New("trust record not found")
	ErrInvalidState = errors.New("trust record is not in the required state")
	ErrNoDevice     = errors.New("no active device trust; run `timewarp trust device` first")
)

// DefaultScopes are the permanent scopes granted by trust when --scopes is omitted.
// payload:read and replay:build stay opt-in only.
var DefaultScopes = []string{"trace:read", "checkpoint:read"}

type Device struct {
	ID          string   `json:"id"`
	DeviceLabel string   `json:"device_label"`
	Actor       string   `json:"actor"`
	Scopes      []string `json:"scopes"`
	Status      Status   `json:"status"`
	CreatedAt   int64    `json:"created_at"`
	RevokedAt   int64    `json:"revoked_at,omitempty"`
}

type Workspace struct {
	ID        string   `json:"id"`
	DeviceID  string   `json:"device_id"`
	Workspace string   `json:"workspace"`
	Actor     string   `json:"actor"`
	Scopes    []string `json:"scopes"`
	Status    Status   `json:"status"`
	CreatedAt int64    `json:"created_at"`
	RevokedAt int64    `json:"revoked_at,omitempty"`
}

// Resolved is the effective trust for a device+workspace+actor pair.
type Resolved struct {
	DeviceID    string   `json:"device_id"`
	WorkspaceID string   `json:"workspace_id"`
	Workspace   string   `json:"workspace"`
	Actor       string   `json:"actor"`
	Scopes      []string `json:"scopes"`
}

type ListFilter struct {
	Status Status
	Limit  int
}

type Store interface {
	CreateDevice(context.Context, Device) error
	GetDevice(context.Context, string) (Device, error)
	ListDevices(context.Context, ListFilter) ([]Device, error)
	RevokeDevice(context.Context, string, int64) (Device, error)
	ActiveDeviceForActor(context.Context, string) (Device, error)

	CreateWorkspace(context.Context, Workspace) error
	GetWorkspace(context.Context, string) (Workspace, error)
	ListWorkspaces(context.Context, ListFilter) ([]Workspace, error)
	RevokeWorkspace(context.Context, string, int64) (Workspace, error)
	Resolve(ctx context.Context, deviceID, workspace, actor string) (Resolved, error)
}

func NewDeviceID() (string, error) {
	return newID("trust_dev_")
}

func NewWorkspaceID() (string, error) {
	return newID("trust_ws_")
}

func newID(prefix string) (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data), nil
}

func NormalizeScopes(scopes []string) []string {
	set := map[string]bool{}
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			set[scope] = true
		}
	}
	out := make([]string, 0, len(set))
	for scope := range set {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// IntersectScopes returns scopes present in both lists (order from allowed).
func IntersectScopes(requested, allowed []string) []string {
	allow := map[string]bool{}
	for _, scope := range allowed {
		allow[scope] = true
	}
	out := make([]string, 0, len(requested))
	for _, scope := range NormalizeScopes(requested) {
		if allow[scope] {
			out = append(out, scope)
		}
	}
	return out
}
