//go:build android || ios

package testtmux

// Supported reports whether private real-tmux test servers are available.
func Supported() bool {
	return false
}
