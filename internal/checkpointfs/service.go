package checkpointfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/timewarp-dev/timewarp/pkg/checkpoint"
)

const (
	maxFiles      = 100
	maxFileBytes  = 4 << 20
	maxTotalBytes = 20 << 20
)

type Service struct {
	Store checkpoint.Store
	Now   func() time.Time
}

type CaptureRequest struct {
	Workspace string
	SessionID string
	Actor     string
	Label     string
	Files     []string
}

type PlanEntry struct {
	Path             string `json:"path"`
	Action           string `json:"action"`
	CheckpointSHA256 string `json:"checkpoint_sha256,omitempty"`
	CurrentSHA256    string `json:"current_sha256,omitempty"`
}

type RevertPlan struct {
	CheckpointID string      `json:"checkpoint_id"`
	Workspace    string      `json:"workspace"`
	Entries      []PlanEntry `json:"entries"`
}

type RevertResult struct {
	Checkpoint         checkpoint.Checkpoint `json:"checkpoint"`
	SafetyCheckpointID string                `json:"safety_checkpoint_id"`
}

func (s Service) Capture(ctx context.Context, request CaptureRequest) (checkpoint.Checkpoint, error) {
	if s.Store == nil {
		return checkpoint.Checkpoint{}, errors.New("checkpoint store is required")
	}
	workspace, err := canonicalWorkspace(request.Workspace)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	if !validSingleLine(request.SessionID, 128) {
		return checkpoint.Checkpoint{}, errors.New("session id must contain 1 to 128 characters")
	}
	if !validSingleLine(request.Actor, 128) {
		return checkpoint.Checkpoint{}, errors.New("actor must contain 1 to 128 characters")
	}
	label := strings.TrimSpace(request.Label)
	if !validSingleLine(label, 200) {
		return checkpoint.Checkpoint{}, errors.New("label must contain 1 to 200 characters on one line")
	}
	files, err := captureFiles(workspace, request.Files)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	id, err := checkpoint.NewID()
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	item := checkpoint.Checkpoint{
		ID: id, SessionID: request.SessionID, Actor: request.Actor, Workspace: workspace,
		Label: label, Status: checkpoint.Active, CreatedAt: now().UnixMilli(), Files: files,
	}
	if err := s.Store.CreateCheckpoint(ctx, item); err != nil {
		return checkpoint.Checkpoint{}, err
	}
	return item, nil
}

func (s Service) Plan(ctx context.Context, checkpointID, workspace string) (RevertPlan, error) {
	item, err := s.Store.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		return RevertPlan{}, err
	}
	root, err := canonicalWorkspace(workspace)
	if err != nil {
		return RevertPlan{}, err
	}
	if !samePath(root, item.Workspace) {
		return RevertPlan{}, errors.New("workspace does not match checkpoint")
	}
	plan := RevertPlan{CheckpointID: item.ID, Workspace: item.Workspace}
	for _, saved := range item.Files {
		current, err := captureOne(root, saved.Path)
		if err != nil {
			return RevertPlan{}, err
		}
		entry := PlanEntry{Path: saved.Path, CheckpointSHA256: saved.SHA256, CurrentSHA256: current.SHA256}
		switch {
		case saved.Existed && (!current.Existed || saved.SHA256 != current.SHA256 || saved.Mode != current.Mode):
			entry.Action = "restore"
		case !saved.Existed && current.Existed:
			entry.Action = "delete"
		default:
			entry.Action = "unchanged"
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func (s Service) Revert(ctx context.Context, checkpointID, workspace, actor string) (RevertResult, error) {
	item, err := s.Store.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		return RevertResult{}, err
	}
	if item.Status != checkpoint.Active {
		return RevertResult{}, checkpoint.ErrInvalidState
	}
	root, err := canonicalWorkspace(workspace)
	if err != nil {
		return RevertResult{}, err
	}
	if !samePath(root, item.Workspace) {
		return RevertResult{}, errors.New("workspace does not match checkpoint")
	}
	paths := make([]string, len(item.Files))
	for index, file := range item.Files {
		paths[index] = file.Path
	}
	safety, err := s.Capture(ctx, CaptureRequest{
		Workspace: root, SessionID: item.SessionID, Actor: actor,
		Label: "pre-revert safety for " + item.ID, Files: paths,
	})
	if err != nil {
		return RevertResult{}, fmt.Errorf("create safety checkpoint: %w", err)
	}
	if err := applyFiles(root, item.Files); err != nil {
		rollbackErr := applyFiles(root, safety.Files)
		return RevertResult{}, fmt.Errorf("revert failed: %w; safety rollback error: %v", err, rollbackErr)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	revertedAt := now().UnixMilli()
	if err := s.Store.MarkCheckpointReverted(ctx, item.ID, revertedAt, safety.ID); err != nil {
		rollbackErr := applyFiles(root, safety.Files)
		return RevertResult{}, fmt.Errorf("record revert failed: %w; safety rollback error: %v", err, rollbackErr)
	}
	item.Status, item.RevertedAt, item.SafetyCheckpointID = checkpoint.Reverted, revertedAt, safety.ID
	return RevertResult{Checkpoint: item, SafetyCheckpointID: safety.ID}, nil
}

func captureFiles(workspace string, paths []string) ([]checkpoint.File, error) {
	if len(paths) == 0 || len(paths) > maxFiles {
		return nil, fmt.Errorf("checkpoint requires 1 to %d explicit files", maxFiles)
	}
	seen := map[string]bool{}
	files := make([]checkpoint.File, 0, len(paths))
	var total int64
	for _, path := range paths {
		file, err := captureOne(workspace, path)
		if err != nil {
			return nil, err
		}
		if seen[file.Path] {
			return nil, fmt.Errorf("duplicate checkpoint path %q", file.Path)
		}
		seen[file.Path] = true
		total += file.Size
		if total > maxTotalBytes {
			return nil, fmt.Errorf("checkpoint content exceeds %d bytes", maxTotalBytes)
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func captureOne(workspace, relativePath string) (checkpoint.File, error) {
	target, normalized, err := safeTarget(workspace, relativePath)
	if err != nil {
		return checkpoint.File{}, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return checkpoint.File{Path: normalized}, nil
	}
	if err != nil {
		return checkpoint.File{}, err
	}
	if !info.Mode().IsRegular() {
		return checkpoint.File{}, fmt.Errorf("checkpoint path %q is not a regular file", normalized)
	}
	if info.Size() > maxFileBytes {
		return checkpoint.File{}, fmt.Errorf("checkpoint file %q exceeds %d bytes", normalized, maxFileBytes)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return checkpoint.File{}, err
	}
	after, err := os.Lstat(target)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return checkpoint.File{}, fmt.Errorf("checkpoint file %q changed while being read", normalized)
	}
	digest := sha256.Sum256(content)
	return checkpoint.File{
		Path: normalized, Existed: true, Mode: uint32(info.Mode().Perm()),
		SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)), Content: content,
	}, nil
}

func applyFiles(workspace string, files []checkpoint.File) error {
	for _, file := range files {
		target, _, err := safeTarget(workspace, file.Path)
		if err != nil {
			return err
		}
		if file.Existed {
			if err := replaceRegularFile(target, file.Content, os.FileMode(file.Mode)); err != nil {
				return fmt.Errorf("restore %s: %w", file.Path, err)
			}
			continue
		}
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to delete non-regular path %q", file.Path)
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("delete %s: %w", file.Path, err)
		}
	}
	return nil
}

func replaceRegularFile(target string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return errors.New("target is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".timewarp-restore-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	backup := temporaryPath + ".backup"
	hadTarget := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
		hadTarget = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if hadTarget {
		_ = os.Remove(backup)
	}
	return nil
}

func canonicalWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace must be an existing directory")
	}
	return filepath.Clean(resolved), nil
}

func safeTarget(workspace, relativePath string) (string, string, error) {
	if filepath.IsAbs(relativePath) {
		return "", "", errors.New("checkpoint paths must be relative")
	}
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relativePath)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("checkpoint path %q escapes the workspace", relativePath)
	}
	target := filepath.Join(workspace, clean)
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", "", fmt.Errorf("resolve parent for %q: %w", relativePath, err)
	}
	if !within(workspace, parent) {
		return "", "", fmt.Errorf("checkpoint path %q escapes through a symlink", relativePath)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("checkpoint path %q is a symlink", relativePath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return target, filepath.ToSlash(clean), nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func validSingleLine(value string, limit int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
