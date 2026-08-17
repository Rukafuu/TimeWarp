package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Target representa uma plataforma de agente suportada.
type Target string

const (
	TargetCodex   Target = "codex"
	TargetClaude  Target = "claude"
	TargetCursor  Target = "cursor"
	TargetAll     Target = "all"
)

// SkillInstaller gerencia a instalação de skills em diretórios de agentes.
type SkillInstaller struct {
	CanonicalRoot string // caminho raiz onde as skills canônicas estão localizadas
}

// InstallOptions configurações para instalação de skill.
type InstallOptions struct {
	Target Target
	Force  bool
}

// StatusInfo informações sobre o status de uma skill instalada.
type StatusInfo struct {
	Target           Target
	ExpectedPath     string
	Installed        bool
	CanonicalHash    string
	InstalledHash    string
	Divergent        bool
}

// ErrSkillExists indica que a skill já existe e --force é necessário.
var ErrSkillExists = errors.New("skill already exists; use --force to overwrite")

// ErrSkillNotFound indica que a skill canônica não foi encontrada.
var ErrSkillNotFound = errors.New("canonical skill not found")

// ErrInvalidTarget indica um target inválido.
var ErrInvalidTarget = errors.New("invalid target")

// NewSkillInstaller cria um novo instalador de skills.
func NewSkillInstaller(canonicalRoot string) *SkillInstaller {
	return &SkillInstaller{CanonicalRoot: canonicalRoot}
}

// resolveTargetDir retorna o diretório base para um target específico.
func resolveTargetDir(target Target) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	switch target {
	case TargetCodex:
		return filepath.Join(homeDir, ".codex", "skills"), nil
	case TargetClaude:
		return filepath.Join(homeDir, ".claude", "skills"), nil
	case TargetCursor:
		return filepath.Join(homeDir, ".cursor", "skills"), nil
	default:
		return "", ErrInvalidTarget
	}
}

// getCanonicalSkillPath retorna o caminho da skill canônica debug-with-timewarp.
func (si *SkillInstaller) getCanonicalSkillPath() string {
	return filepath.Join(si.CanonicalRoot, "skills", "debug-with-timewarp")
}

// validateCanonicalSkill valida que a skill canônica existe e tem SKILL.md.
func (si *SkillInstaller) validateCanonicalSkill() error {
	canonicalPath := si.getCanonicalSkillPath()
	skillMdPath := filepath.Join(canonicalPath, "SKILL.md")

	// Verifica se o diretório existe
	info, err := os.Stat(canonicalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrSkillNotFound
		}
		return fmt.Errorf("failed to stat canonical skill directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("canonical skill path is not a directory: %s", canonicalPath)
	}

	// Verifica se SKILL.md existe
	_, err = os.Stat(skillMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SKILL.md not found in canonical skill directory: %s", canonicalPath)
		}
		return fmt.Errorf("failed to stat SKILL.md: %w", err)
	}

	return nil
}

// computeFileHash calcula o hash SHA256 de um arquivo.
func computeFileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// computeDirHash calcula o hash combinado de todos os arquivos em um diretório recursivamente.
func computeDirHash(dirPath string) (string, error) {
	var combinedHash string

	err := filepath.Walk(dirPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			hash, err := computeFileHash(path)
			if err != nil {
				return err
			}
			combinedHash += hash
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	// Hash do conteúdo combinado
	finalHasher := sha256.New()
	finalHasher.Write([]byte(combinedHash))
	return hex.EncodeToString(finalHasher.Sum(nil)), nil
}

// copyDir copia recursivamente um diretório para outro destino.
func copyDir(src, dst string) error {
	// Cria o diretório de destino
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copia um único arquivo preservando permissões.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Obtém permissões do arquivo original
	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	return nil
}

// Install instala uma skill no target especificado.
func (si *SkillInstaller) Install(opts InstallOptions) error {
	// Valida a skill canônica antes de qualquer operação
	if err := si.validateCanonicalSkill(); err != nil {
		return err
	}

	canonicalPath := si.getCanonicalSkillPath()
	skillName := "debug-with-timewarp"

	// Determina quais targets instalar
	targets := []Target{opts.Target}
	if opts.Target == TargetAll {
		targets = []Target{TargetCodex, TargetClaude, TargetCursor}
	}

	for _, target := range targets {
		targetDir, err := resolveTargetDir(target)
		if err != nil {
			return err
		}

		skillDestPath := filepath.Join(targetDir, skillName)

		// Verifica se já existe
		_, err = os.Stat(skillDestPath)
		if err == nil {
			if !opts.Force {
				return fmt.Errorf("%w for target %s; use timewarp skill install --target %s --force", ErrSkillExists, target, target)
			}
			// Com --force, remove apenas a pasta da skill alvo
			if err := os.RemoveAll(skillDestPath); err != nil {
				return fmt.Errorf("failed to remove existing skill at %s: %w", skillDestPath, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check existing skill: %w", err)
		}

		// Garante que o diretório pai existe
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("failed to create target directory: %w", err)
		}

		// Copia a skill
		if err := copyDir(canonicalPath, skillDestPath); err != nil {
			return fmt.Errorf("failed to copy skill to %s: %w", skillDestPath, err)
		}

		// Readback: verifica que SKILL.md foi instalado corretamente
		installedSkillMd := filepath.Join(skillDestPath, "SKILL.md")
		if _, err := os.Stat(installedSkillMd); err != nil {
			return fmt.Errorf("readback failed: SKILL.md not found at %s after installation", installedSkillMd)
		}
	}

	return nil
}

// Status retorna o status das skills instaladas para os targets especificados.
func (si *SkillInstaller) Status(targets []Target) ([]StatusInfo, error) {
	var results []StatusInfo

	skillName := "debug-with-timewarp"
	canonicalPath := si.getCanonicalSkillPath()

	// Calcula hash canônico
	canonicalHash, err := computeDirHash(canonicalPath)
	if err != nil {
		canonicalHash = ""
	}

	for _, target := range targets {
		targetDir, err := resolveTargetDir(target)
		if err != nil {
			continue
		}

		skillDestPath := filepath.Join(targetDir, skillName)
		info := StatusInfo{
			Target:        target,
			ExpectedPath:  skillDestPath,
			CanonicalHash: canonicalHash,
		}

		// Verifica se está instalado
		_, err = os.Stat(skillDestPath)
		if err == nil {
			info.Installed = true

			// Calcula hash instalado
			installedHash, err := computeDirHash(skillDestPath)
			if err == nil {
				info.InstalledHash = installedHash
				info.Divergent = installedHash != canonicalHash
			}
		}

		results = append(results, info)
	}

	return results, nil
}

// GetAllTargets retorna todos os targets suportados.
func GetAllTargets() []Target {
	return []Target{TargetCodex, TargetClaude, TargetCursor}
}

// ParseTarget converte uma string em Target.
func ParseTarget(s string) (Target, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "codex":
		return TargetCodex, nil
	case "claude":
		return TargetClaude, nil
	case "cursor":
		return TargetCursor, nil
	case "all":
		return TargetAll, nil
	default:
		return "", ErrInvalidTarget
	}
}

// GetHomeDir retorna o diretório home do usuário de forma compatível multiplataforma.
func GetHomeDir() (string, error) {
	return os.UserHomeDir()
}

// GetTargetDir retorna o diretório base para um target em um SO específico.
// Esta função é útil para testes que precisam simular diferentes ambientes.
func GetTargetDir(target Target, homeDir string) (string, error) {
	switch target {
	case TargetCodex:
		return filepath.Join(homeDir, ".codex", "skills"), nil
	case TargetClaude:
		return filepath.Join(homeDir, ".claude", "skills"), nil
	case TargetCursor:
		return filepath.Join(homeDir, ".cursor", "skills"), nil
	default:
		return "", ErrInvalidTarget
	}
}

// DetectOS retorna o sistema operacional atual.
func DetectOS() string {
	return runtime.GOOS
}
