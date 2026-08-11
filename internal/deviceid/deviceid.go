package deviceid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveOrCreate returns a stable device id stored next to the DB (or at path).
// First call creates the file; later calls reuse it.
func ResolveOrCreate(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("device id file path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", fmt.Errorf("device id file %s is empty", path)
		}
		return id, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := "device_" + hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// DefaultPath places the device id beside the SQLite database.
func DefaultPath(dbPath string) string {
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, ".timewarp-device-id")
}
