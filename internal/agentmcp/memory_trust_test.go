package agentmcp

import (
	"context"

	"github.com/timewarp-dev/timewarp/pkg/trust"
)

func (s *memoryStore) CreateDevice(_ context.Context, device trust.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.devices == nil {
		s.devices = map[string]trust.Device{}
	}
	if existing, ok := s.devices[device.ID]; ok && existing.Status == trust.Active {
		return trust.ErrInvalidState
	}
	device.Scopes = trust.NormalizeScopes(device.Scopes)
	device.Status = trust.Active
	s.devices[device.ID] = device
	return nil
}

func (s *memoryStore) GetDevice(_ context.Context, id string) (trust.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[id]
	if !ok {
		return trust.Device{}, trust.ErrNotFound
	}
	return device, nil
}

func (s *memoryStore) ListDevices(_ context.Context, filter trust.ListFilter) ([]trust.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []trust.Device
	for _, device := range s.devices {
		if filter.Status == "" || device.Status == filter.Status {
			out = append(out, device)
		}
	}
	return out, nil
}

func (s *memoryStore) RevokeDevice(_ context.Context, id string, revokedAt int64) (trust.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[id]
	if !ok {
		return trust.Device{}, trust.ErrNotFound
	}
	if device.Status == trust.Revoked {
		return trust.Device{}, trust.ErrInvalidState
	}
	device.Status = trust.Revoked
	device.RevokedAt = revokedAt
	s.devices[id] = device
	for key, ws := range s.workspaces {
		if ws.DeviceID == id && ws.Status == trust.Active {
			ws.Status = trust.Revoked
			ws.RevokedAt = revokedAt
			s.workspaces[key] = ws
		}
	}
	return device, nil
}

func (s *memoryStore) ActiveDeviceForActor(_ context.Context, actor string) (trust.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, device := range s.devices {
		if device.Actor == actor && device.Status == trust.Active {
			return device, nil
		}
	}
	return trust.Device{}, trust.ErrNotFound
}

func (s *memoryStore) CreateWorkspace(_ context.Context, item trust.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaces == nil {
		s.workspaces = map[string]trust.Workspace{}
	}
	item.Scopes = trust.NormalizeScopes(item.Scopes)
	item.Status = trust.Active
	s.workspaces[item.ID] = item
	return nil
}

func (s *memoryStore) GetWorkspace(_ context.Context, id string) (trust.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workspaces[id]
	if !ok {
		return trust.Workspace{}, trust.ErrNotFound
	}
	return item, nil
}

func (s *memoryStore) ListWorkspaces(_ context.Context, filter trust.ListFilter) ([]trust.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []trust.Workspace
	for _, item := range s.workspaces {
		if filter.Status == "" || item.Status == filter.Status {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *memoryStore) RevokeWorkspace(_ context.Context, id string, revokedAt int64) (trust.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workspaces[id]
	if !ok {
		return trust.Workspace{}, trust.ErrNotFound
	}
	if item.Status == trust.Revoked {
		return trust.Workspace{}, trust.ErrInvalidState
	}
	item.Status = trust.Revoked
	item.RevokedAt = revokedAt
	s.workspaces[id] = item
	return item, nil
}

func (s *memoryStore) Resolve(_ context.Context, deviceID, workspace, actor string) (trust.Resolved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[deviceID]
	if !ok || device.Status != trust.Active || device.Actor != actor {
		return trust.Resolved{}, trust.ErrNotFound
	}
	for _, item := range s.workspaces {
		if item.DeviceID == deviceID && item.Workspace == workspace && item.Actor == actor && item.Status == trust.Active {
			scopes := trust.IntersectScopes(item.Scopes, device.Scopes)
			if len(scopes) == 0 {
				return trust.Resolved{}, trust.ErrNotFound
			}
			return trust.Resolved{
				DeviceID: device.ID, WorkspaceID: item.ID, Workspace: item.Workspace, Actor: actor, Scopes: scopes,
			}, nil
		}
	}
	return trust.Resolved{}, trust.ErrNotFound
}
