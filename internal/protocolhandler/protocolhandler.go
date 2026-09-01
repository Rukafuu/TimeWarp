package protocolhandler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const Scheme = "timewarp"

type Status struct {
	Installed bool   `json:"installed"`
	Location  string `json:"location,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func Install(databasePath string) (Status, error) {
	executable, err := executablePath()
	if err != nil {
		return Status{}, err
	}
	databasePath, err = filepath.Abs(databasePath)
	if err != nil {
		return Status{}, err
	}
	if strings.ContainsAny(databasePath, "\r\n") {
		return Status{}, errors.New("database path contains an invalid newline")
	}
	return install(executable, databasePath)
}

func Uninstall() error {
	return uninstall()
}

func CurrentStatus() (Status, error) {
	return currentStatus()
}

func executablePath() (string, error) {
	value, err := os.Executable()
	if err != nil {
		return "", err
	}
	value, err = filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("executable path contains an invalid newline")
	}
	return value, nil
}
