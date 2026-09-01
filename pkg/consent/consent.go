package consent

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
	Pending Status = "PENDING"
	Active  Status = "ACTIVE"
	Revoked Status = "REVOKED"
	Expired Status = "EXPIRED"
)

var (
	ErrNotFound     = errors.New("consent grant not found")
	ErrInvalidState = errors.New("consent grant is not in the required state")
)

type Grant struct {
	ID          string   `json:"id"`
	SessionID   string   `json:"session_id"`
	Actor       string   `json:"actor"`
	Scopes      []string `json:"scopes"`
	Reason      string   `json:"reason"`
	Status      Status   `json:"status"`
	RequestedAt int64    `json:"requested_at"`
	ApprovedAt  int64    `json:"approved_at,omitempty"`
	ExpiresAt   int64    `json:"expires_at,omitempty"`
	RevokedAt   int64    `json:"revoked_at,omitempty"`
}

type ListFilter struct {
	Status Status
	NowMS  int64
	Limit  int
}

type Store interface {
	CreateGrant(context.Context, Grant) error
	GetGrant(context.Context, string) (Grant, error)
	ListGrants(context.Context, ListFilter) ([]Grant, error)
	ApproveGrant(context.Context, string, int64, int64) (Grant, error)
	RevokeGrant(context.Context, string, int64) (Grant, error)
	ActiveGrants(context.Context, string, int64) ([]Grant, error)
}

func NewID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "grant_" + hex.EncodeToString(data), nil
}

func (g Grant) EffectiveStatus(nowMS int64) Status {
	if g.Status == Active && g.ExpiresAt > 0 && g.ExpiresAt <= nowMS {
		return Expired
	}
	return g.Status
}

func (g Grant) IsActive(nowMS int64) bool {
	return g.EffectiveStatus(nowMS) == Active
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
