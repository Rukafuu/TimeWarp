//go:build linux

package protocolhandler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const linuxDesktopFile = "timewarp-url.desktop"

func install(executable, databasePath string) (Status, error) {
	directory, err := linuxApplicationsDirectory()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return Status{}, err
	}
	path := filepath.Join(directory, linuxDesktopFile)
	content := "[Desktop Entry]\n" +
		"Name=Timewarp URL Handler\n" +
		"Type=Application\n" +
		"NoDisplay=true\n" +
		"Terminal=false\n" +
		"Exec=\"" + escapeDesktopValue(executable) + "\" handle-url --db \"" + escapeDesktopValue(databasePath) + "\" %u\n" +
		"MimeType=x-scheme-handler/timewarp;\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return Status{}, err
	}
	if output, err := exec.Command("xdg-mime", "default", linuxDesktopFile, "x-scheme-handler/timewarp").CombinedOutput(); err != nil {
		return Status{}, fmt.Errorf("register timewarp URL protocol: %w: %s", err, output)
	}
	return Status{Installed: true, Location: path, Detail: executable}, nil
}

func uninstall() error {
	directory, err := linuxApplicationsDirectory()
	if err != nil {
		return err
	}
	path := filepath.Join(directory, linuxDesktopFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func currentStatus() (Status, error) {
	directory, err := linuxApplicationsDirectory()
	if err != nil {
		return Status{}, err
	}
	path := filepath.Join(directory, linuxDesktopFile)
	output, commandErr := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/timewarp").CombinedOutput()
	installed := commandErr == nil && strings.TrimSpace(string(output)) == linuxDesktopFile
	return Status{Installed: installed, Location: path, Detail: strings.TrimSpace(string(output))}, nil
}

func linuxApplicationsDirectory() (string, error) {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "applications"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "applications"), nil
}

func escapeDesktopValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "%", "%%").Replace(value)
}
