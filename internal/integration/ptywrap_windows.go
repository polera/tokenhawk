//go:build windows

package integration

import "errors"

// wrapPTY is unavailable on Windows, where the wrapper requires tmux (for
// example under WSL, Cygwin, or MSYS2).
func wrapPTY(client, provider string, providerArgs []string) error {
	return errors.New("tokenhawk wrap without tmux is not supported on Windows; install tmux or use the native status-line integrations")
}
