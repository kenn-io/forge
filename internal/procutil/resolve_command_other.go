//go:build !windows

package procutil

func resolveCommand(name string, arg []string) (string, []string) {
	return ResolveBinary(name), arg
}
