//go:build !windows && !linux && !darwin

package protocolhandler

import "errors"

var errUnsupported = errors.New("timewarp URL protocol is supported on Windows, Linux, and macOS")

func install(string, string) (Status, error) { return Status{}, errUnsupported }
func uninstall() error               { return errUnsupported }
func currentStatus() (Status, error) { return Status{}, errUnsupported }
