//go:build windows

package operatorprompt

import (
	"fmt"
	"os/exec"
	"strings"
)

func Open(executable, grantID, databasePath string) error {
	command := fmt.Sprintf(
		"$env:TIMEWARP_DB='%s'; Start-Process -FilePath '%s' -ArgumentList @('consent','approve','%s','--ttl','15m')",
		strings.ReplaceAll(databasePath, "'", "''"), strings.ReplaceAll(executable, "'", "''"), strings.ReplaceAll(grantID, "'", "''"),
	)
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).Start()
}
