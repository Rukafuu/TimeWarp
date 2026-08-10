package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

type Status string

const (
	Active   Status = "ACTIVE"
	Reverted Status = "REVERTED"
)

var (
	ErrNotFound     = errors.New("checkpoint not found")
	ErrInvalidState = errors.New("checkpoint is not active")
)

type File struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Content []byte `json:"-"`
}

type Checkpoint struct {
	ID                 string `json:"id"`
	SessionID          string `json:"session_id"`
	Actor              string `json:"actor"`
	Workspace          string `json:"workspace"`
	Label              string `json:"label"`
	Status             Status `json:"status"`
	CreatedAt          int64  `json:"created_at"`
	RevertedAt         int64  `json:"reverted_at,omitempty"`
	SafetyCheckpointID string `json:"safety_checkpoint_id,omitempty"`
	Files              []File `json:"files"`
}

type ListFilter struct {
	SessionID string
	Limit     int
}

type Store interface {
	CreateCheckpoint(context.Context, Checkpoint) error
	GetCheckpoint(context.Context, string) (Checkpoint, error)
	ListCheckpoints(context.Context, ListFilter) ([]Checkpoint, error)
	MarkCheckpointReverted(context.Context, string, int64, string) error
}

func NewID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "checkpoint_" + hex.EncodeToString(data), nil
}
