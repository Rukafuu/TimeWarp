//go:build linux

package operatorprompt

import (
	"errors"
	"os"
	"os/exec"
)

func Open(executable, grantID, databasePath string) error {
	args := []string{executable, "consent", "approve", grantID, "--ttl", "15m"}
	candidates := []struct {
		name string
		args []string
	}{
		{"x-terminal-emulator", append([]string{"-e"}, args...)},
		{"gnome-terminal", append([]string{"--"}, args...)},
		{"konsole", append([]string{"-e"}, args...)},
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		command := exec.Command(candidate.name, candidate.args...)
		command.Env = append(os.Environ(), "TIMEWARP_DB="+databasePath)
		return command.Start()
	}
	return errors.New("no supported terminal emulator was found")
}
