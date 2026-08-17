package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTargetDir(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}

	tests := []struct {
		target   Target
		expected string
	}{
		{TargetCodex, filepath.Join(homeDir, ".codex", "skills")},
		{TargetClaude, filepath.Join(homeDir, ".claude", "skills")},
		{TargetCursor, filepath.Join(homeDir, ".cursor", "skills")},
	}

	for _, tt := range tests {
		t.Run(string(tt.target), func(t *testing.T) {
			result, err := resolveTargetDir(tt.target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestResolveTargetDirInvalid(t *testing.T) {
	_, err := resolveTargetDir("invalid")
	if err != ErrInvalidTarget {
		t.Errorf("expected ErrInvalidTarget, got %v", err)
	}
}

func TestGetTargetDirMultiPlatform(t *testing.T) {
	// Testa que GetTargetDir funciona corretamente com diferentes home dirs
	testHomeDirs := []string{
		"/home/user",
		"/Users/user",
		"C:\\Users\\user",
	}

	targets := []Target{TargetCodex, TargetClaude, TargetCursor}

	for _, homeDir := range testHomeDirs {
		for _, target := range targets {
			t.Run(filepath.Join(homeDir, string(target)), func(t *testing.T) {
				result, err := GetTargetDir(target, homeDir)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				var expected string
				switch target {
				case TargetCodex:
					expected = filepath.Join(homeDir, ".codex", "skills")
				case TargetClaude:
					expected = filepath.Join(homeDir, ".claude", "skills")
				case TargetCursor:
					expected = filepath.Join(homeDir, ".cursor", "skills")
				}

				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			})
		}
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		input    string
		expected Target
		hasError bool
	}{
		{"codex", TargetCodex, false},
		{"CODEX", TargetCodex, false},
		{"CoDeX", TargetCodex, false},
		{"claude", TargetClaude, false},
		{"CLAUDE", TargetClaude, false},
		{"cursor", TargetCursor, false},
		{"CURSOR", TargetCursor, false},
		{"all", TargetAll, false},
		{"ALL", TargetAll, false},
		{"invalid", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseTarget(tt.input)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for input %q", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for input %q: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("expected %s, got %s", tt.expected, result)
				}
			}
		})
	}
}

func TestDetectOS(t *testing.T) {
	os := DetectOS()
	if os == "" {
		t.Error("expected non-empty OS string")
	}
	// Verifica que é um dos valores esperados
	validOS := map[string]bool{
		"linux":   true,
		"darwin":  true,
		"windows": true,
		"freebsd": true,
	}
	if !validOS[os] {
		t.Errorf("unexpected OS value: %s", os)
	}
}

func TestGetAllTargets(t *testing.T) {
	targets := GetAllTargets()
	expected := []Target{TargetCodex, TargetClaude, TargetCursor}

	if len(targets) != len(expected) {
		t.Fatalf("expected %d targets, got %d", len(expected), len(targets))
	}

	for i, target := range targets {
		if target != expected[i] {
			t.Errorf("target %d: expected %s, got %s", i, expected[i], target)
		}
	}
}
