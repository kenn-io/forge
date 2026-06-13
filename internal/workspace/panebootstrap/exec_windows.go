//go:build windows

package panebootstrap

import "errors"

// execProcess is unreachable on Windows: tmux panes are a unix-only
// concept and the launcher never emits the bootstrap token there. The stub
// exists so panebootstrap and its importers cross-compile; if the
// subcommand is somehow requested, ExecIfRequested surfaces this error and
// exits 127.
func execProcess(argv0 string, argv, env []string) error {
	return errors.New("pane bootstrap is not supported on windows")
}
