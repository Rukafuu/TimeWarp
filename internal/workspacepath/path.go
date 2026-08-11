package workspacepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Canonical returns an absolute, symlink-resolved, cleaned directory path.
// Used by checkpoints and workspace trust so aliases cannot bypass trust.
func Canonical(path string) (string, error) {
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
