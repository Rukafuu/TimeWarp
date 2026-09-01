//go:build windows

package protocolhandler

import (
	"fmt"
	"os/exec"
)

const windowsRegistryKey = `HKCU\Software\Classes\timewarp`

func install(executable, databasePath string) (Status, error) {
	commands := [][]string{
		{"ADD", windowsRegistryKey, "/ve", "/d", "URL:Timewarp Protocol", "/f"},
		{"ADD", windowsRegistryKey, "/v", "URL Protocol", "/d", "", "/f"},
		{"ADD", windowsRegistryKey + `\shell\open\command`, "/ve", "/d", fmt.Sprintf(`"%s" handle-url --db "%s" "%%1"`, executable, databasePath), "/f"},
	}
	for _, args := range commands {
		if output, err := exec.Command("reg.exe", args...).CombinedOutput(); err != nil {
			return Status{}, fmt.Errorf("register timewarp URL protocol: %w: %s", err, output)
		}
	}
	return Status{Installed: true, Location: windowsRegistryKey, Detail: executable}, nil
}

func uninstall() error {
	if output, err := exec.Command("reg.exe", "DELETE", windowsRegistryKey, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("remove timewarp URL protocol: %w: %s", err, output)
	}
	return nil
}

func currentStatus() (Status, error) {
	output, err := exec.Command("reg.exe", "QUERY", windowsRegistryKey+`\shell\open\command`, "/ve").CombinedOutput()
	if err != nil {
		return Status{Installed: false}, nil
	}
	return Status{Installed: true, Location: windowsRegistryKey, Detail: string(output)}, nil
}
