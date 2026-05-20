//go:build windows

package procutil

import (
	"os"
	"path/filepath"
)

func resolveCommand(name string, arg []string) (string, []string) {
	resolved := ResolveBinary(name)
	if shouldRunShebangScriptWithShell(resolved) {
		if shell := ResolveBinary("sh"); shell != "sh" {
			return shell, append(
				[]string{"-c", `exec sh "$0" "$@"`, resolved},
				arg...,
			)
		}
	}
	return resolved, arg
}

func shouldRunShebangScriptWithShell(path string) bool {
	if filepath.Ext(path) != "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 2 {
		return false
	}
	return data[0] == '#' && data[1] == '!'
}
