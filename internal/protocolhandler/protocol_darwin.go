//go:build darwin

package protocolhandler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const macAppName = "Timewarp Bridge.app"

func install(executable, databasePath string) (Status, error) {
	appPath, err := macApplicationPath()
	if err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(appPath), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.RemoveAll(appPath); err != nil {
		return Status{}, err
	}
	source, err := os.CreateTemp("", "timewarp-url-*.applescript")
	if err != nil {
		return Status{}, err
	}
	sourcePath := source.Name()
	defer os.Remove(sourcePath)
	script := `on open location theURL
set cliPath to "` + escapeAppleScript(executable) + `"
set databasePath to "` + escapeAppleScript(databasePath) + `"
set launchCommand to quoted form of cliPath & " handle-url --db " & quoted form of databasePath & " " & quoted form of theURL & " >/dev/null 2>&1 &"
do shell script launchCommand
end open location
`
	if _, err := source.WriteString(script); err != nil {
		_ = source.Close()
		return Status{}, err
	}
	if err := source.Close(); err != nil {
		return Status{}, err
	}
	if output, err := exec.Command("osacompile", "-o", appPath, sourcePath).CombinedOutput(); err != nil {
		return Status{}, fmt.Errorf("compile timewarp URL handler: %w: %s", err, output)
	}
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	plistBuddy := "/usr/libexec/PlistBuddy"
	plistCommands := []string{
		"Set :CFBundleIdentifier dev.timewarp.url-handler",
		"Set :CFBundleName Timewarp Bridge",
		"Add :LSUIElement bool true",
		"Add :CFBundleURLTypes array",
		"Add :CFBundleURLTypes:0 dict",
		"Add :CFBundleURLTypes:0:CFBundleURLName string Timewarp Protocol",
		"Add :CFBundleURLTypes:0:CFBundleURLSchemes array",
		"Add :CFBundleURLTypes:0:CFBundleURLSchemes:0 string timewarp",
	}
	for _, command := range plistCommands {
		if output, err := exec.Command(plistBuddy, "-c", command, plist).CombinedOutput(); err != nil {
			return Status{}, fmt.Errorf("configure timewarp URL handler: %w: %s", err, output)
		}
	}
	lsregister := "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
	if output, err := exec.Command(lsregister, "-f", appPath).CombinedOutput(); err != nil {
		return Status{}, fmt.Errorf("register timewarp URL protocol: %w: %s", err, output)
	}
	return Status{Installed: true, Location: appPath, Detail: executable}, nil
}

func uninstall() error {
	appPath, err := macApplicationPath()
	if err != nil {
		return err
	}
	return os.RemoveAll(appPath)
}

func currentStatus() (Status, error) {
	appPath, err := macApplicationPath()
	if err != nil {
		return Status{}, err
	}
	_, statErr := os.Stat(filepath.Join(appPath, "Contents", "Info.plist"))
	if statErr != nil && !os.IsNotExist(statErr) {
		return Status{}, statErr
	}
	return Status{Installed: statErr == nil, Location: appPath}, nil
}

func macApplicationPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", macAppName), nil
}

func escapeAppleScript(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
