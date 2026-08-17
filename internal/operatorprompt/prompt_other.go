//go:build !windows && !linux && !darwin

package operatorprompt

import "errors"

func Open(string, string, string) error {
	return errors.New("operator approval prompts are supported on Windows, Linux, and macOS")
}
