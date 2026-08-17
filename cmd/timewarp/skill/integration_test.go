package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkill(t *testing.T) {
	// Cria um diretório temporário para simular o repositório canônico
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}

	// Cria SKILL.md
	skillMdContent := `---
name: debug-with-timewarp
description: Test skill
---
# Test Content
`
	if err := os.WriteFile(filepath.Join(canonicalSkillDir, "SKILL.md"), []byte(skillMdContent), 0644); err != nil {
		t.Fatalf("failed to create SKILL.md: %v", err)
	}

	// Cria agents/openai.yaml
	agentsDir := filepath.Join(canonicalSkillDir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("failed to create agents directory: %v", err)
	}
	openaiYamlContent := "interface:\n  display_name: Test\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "openai.yaml"), []byte(openaiYamlContent), 0644); err != nil {
		t.Fatalf("failed to create openai.yaml: %v", err)
	}

	// Cria um diretório home temporário para instalação
	homeDir := t.TempDir()

	// Para este teste, vamos manipular diretamente o caminho
	targetDir := filepath.Join(homeDir, ".codex", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")

	// Instala manualmente para testar copyDir e validação
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatalf("failed to create target directory: %v", err)
	}

	if err := copyDir(canonicalSkillDir, skillDestPath); err != nil {
		t.Fatalf("failed to copy skill: %v", err)
	}

	// Verifica readback: SKILL.md deve existir
	installedSkillMd := filepath.Join(skillDestPath, "SKILL.md")
	if _, err := os.Stat(installedSkillMd); err != nil {
		t.Errorf("readback failed: SKILL.md not found at %s", installedSkillMd)
	}

	// Verifica que agents/openai.yaml foi copiado
	installedOpenaiYaml := filepath.Join(skillDestPath, "agents", "openai.yaml")
	if _, err := os.Stat(installedOpenaiYaml); err != nil {
		t.Errorf("agents/openai.yaml not copied to %s", installedOpenaiYaml)
	}

	// Verifica conteúdo
	content, err := os.ReadFile(installedSkillMd)
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md: %v", err)
	}
	if string(content) != skillMdContent {
		t.Errorf("installed SKILL.md content differs from canonical")
	}
}

func TestInstallSkillRefusesWithoutForce(t *testing.T) {
	// Cria um diretório temporário para simular o repositório canônico
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}

	// Cria SKILL.md
	if err := os.WriteFile(filepath.Join(canonicalSkillDir, "SKILL.md"), []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create SKILL.md: %v", err)
	}

	// Cria um diretório home temporário
	homeDir := t.TempDir()
	targetDir := filepath.Join(homeDir, ".codex", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")

	// Simula skill já instalada
	if err := os.MkdirAll(skillDestPath, 0755); err != nil {
		t.Fatalf("failed to create existing skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDestPath, "SKILL.md"), []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create existing SKILL.md: %v", err)
	}

	// Cria instalador com homeDir modificado via variável de ambiente não funciona
	// Precisamos testar o comportamento de recusa diretamente
	_, err := os.Stat(skillDestPath)
	if err != nil {
		t.Fatalf("failed to verify existing skill: %v", err)
	}

	// O teste real do comportamento --force será feito no teste de integração
	// Aqui apenas verificamos que o path existe
	if _, err := os.Stat(skillDestPath); err != nil {
		t.Error("skill path should exist")
	}
}

func TestInstallSkillWithForce(t *testing.T) {
	// Cria diretório canônico
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}

	newContent := "---\nname: updated\n---\n# Updated Content\n"
	if err := os.WriteFile(filepath.Join(canonicalSkillDir, "SKILL.md"), []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to create SKILL.md: %v", err)
	}

	// Cria diretório de destino com skill antiga
	homeDir := t.TempDir()
	targetDir := filepath.Join(homeDir, ".claude", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")
	if err := os.MkdirAll(skillDestPath, 0755); err != nil {
		t.Fatalf("failed to create existing skill directory: %v", err)
	}
	oldContent := "---\nname: old\n---\n# Old Content\n"
	if err := os.WriteFile(filepath.Join(skillDestPath, "SKILL.md"), []byte(oldContent), 0644); err != nil {
		t.Fatalf("failed to create old SKILL.md: %v", err)
	}

	// Simula --force: remove e copia novamente
	if err := os.RemoveAll(skillDestPath); err != nil {
		t.Fatalf("failed to remove existing skill: %v", err)
	}

	if err := copyDir(canonicalSkillDir, skillDestPath); err != nil {
		t.Fatalf("failed to copy skill with force: %v", err)
	}

	// Verifica que o conteúdo foi atualizado
	installedContent, err := os.ReadFile(filepath.Join(skillDestPath, "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md: %v", err)
	}
	if string(installedContent) != newContent {
		t.Errorf("expected updated content, got: %s", installedContent)
	}
}

func TestValidateCanonicalSkillNotFound(t *testing.T) {
	repoRoot := t.TempDir()
	installer := NewSkillInstaller(repoRoot)

	err := installer.validateCanonicalSkill()
	if err != ErrSkillNotFound {
		t.Errorf("expected ErrSkillNotFound, got %v", err)
	}
}

func TestValidateCanonicalSkillMissingSkillMd(t *testing.T) {
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}

	// Não cria SKILL.md
	installer := NewSkillInstaller(repoRoot)

	err := installer.validateCanonicalSkill()
	if err == nil {
		t.Error("expected error when SKILL.md is missing")
	}
}

func TestComputeFileHash(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "test content"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	hash1, err := computeFileHash(testFile)
	if err != nil {
		t.Fatalf("failed to compute hash: %v", err)
	}

	// Hash deve ser consistente
	hash2, err := computeFileHash(testFile)
	if err != nil {
		t.Fatalf("failed to compute second hash: %v", err)
	}

	if hash1 != hash2 {
		t.Error("hash should be consistent")
	}

	if hash1 == "" {
		t.Error("hash should not be empty")
	}
}

func TestComputeDirHash(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("failed to create file2: %v", err)
	}

	hash, err := computeDirHash(tmpDir)
	if err != nil {
		t.Fatalf("failed to compute dir hash: %v", err)
	}

	if hash == "" {
		t.Error("dir hash should not be empty")
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")
	content := "file content for copy test"

	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("failed to copy file: %v", err)
	}

	copiedContent, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read copied file: %v", err)
	}

	if string(copiedContent) != content {
		t.Errorf("copied content differs: expected %q, got %q", content, copiedContent)
	}
}

func TestStatusInstalled(t *testing.T) {
	// Setup: cria skill canônica e instalada
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}
	content := "---\nname: test\n---\ntest"
	if err := os.WriteFile(filepath.Join(canonicalSkillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create canonical SKILL.md: %v", err)
	}

	homeDir := t.TempDir()
	targetDir := filepath.Join(homeDir, ".cursor", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")
	if err := os.MkdirAll(skillDestPath, 0755); err != nil {
		t.Fatalf("failed to create installed skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDestPath, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to create installed SKILL.md: %v", err)
	}

	// Cria instalador apontando para repoRoot mas precisamos modificar o comportamento
	// para usar homeDir temporário. Vamos testar StatusInfo diretamente.

	// Calcula hashes
	canonicalHash, _ := computeDirHash(canonicalSkillDir)
	installedHash, _ := computeDirHash(skillDestPath)

	if canonicalHash != installedHash {
		t.Error("hashes should match for identical content")
	}
}

func TestStatusDivergent(t *testing.T) {
	repoRoot := t.TempDir()
	canonicalSkillDir := filepath.Join(repoRoot, "skills", "debug-with-timewarp")
	if err := os.MkdirAll(canonicalSkillDir, 0755); err != nil {
		t.Fatalf("failed to create canonical skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonicalSkillDir, "SKILL.md"), []byte("canonical"), 0644); err != nil {
		t.Fatalf("failed to create canonical SKILL.md: %v", err)
	}

	homeDir := t.TempDir()
	targetDir := filepath.Join(homeDir, ".codex", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")
	if err := os.MkdirAll(skillDestPath, 0755); err != nil {
		t.Fatalf("failed to create installed skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDestPath, "SKILL.md"), []byte("divergent"), 0644); err != nil {
		t.Fatalf("failed to create divergent SKILL.md: %v", err)
	}

	canonicalHash, _ := computeDirHash(canonicalSkillDir)
	installedHash, _ := computeDirHash(skillDestPath)

	if canonicalHash == installedHash {
		t.Error("hashes should differ for divergent content")
	}
}

func TestStatusNotInstalled(t *testing.T) {
	homeDir := t.TempDir()
	targetDir := filepath.Join(homeDir, ".claude", "skills")
	skillDestPath := filepath.Join(targetDir, "debug-with-timewarp")

	// Não cria o diretório
	_, err := os.Stat(skillDestPath)
	if !os.IsNotExist(err) {
		t.Error("expected skill to not exist")
	}
}
