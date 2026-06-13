//go:build unix

package panebootstrap

import "syscall"

// execProcess replaces the current process image with argv under env,
// inheriting the tmux pane's fds and working directory. On success it does
// not return.
func execProcess(argv0 string, argv, env []string) error {
	return syscall.Exec(argv0, argv, env)
}
