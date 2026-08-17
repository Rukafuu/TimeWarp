//go:build darwin

package operatorprompt

import (
	"os/exec"
	"strings"
)

func Open(executable, grantID, databasePath string) error {
	terminalCommand := "TIMEWARP_DB=" + shellQuote(databasePath) + " " + shellQuote(executable) + " consent approve " + shellQuote(grantID) + " --ttl 15m"
	script := `tell application "Terminal" to do script "` + escapeAppleScript(terminalCommand) + `"`
	return exec.Command("osascript", "-e", script).Start()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func escapeAppleScript(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
