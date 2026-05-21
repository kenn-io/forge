//go:build windows

package procutil

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

func resolveCommand(name string, arg []string) (string, []string) {
	resolved := ResolveBinary(name)
	if shouldRunShebangScriptWithShell(resolved) {
		if shell := ResolveBinary("sh"); shell != "sh" {
			return shell, append([]string{resolved}, arg...)
		}
	}
	return resolved, arg
}

func shouldRunShebangScriptWithShell(path string) bool {
	if !filepath.IsAbs(path) && !strings.ContainsAny(path, `\/`) {
		return false
	}
	if filepath.Ext(path) != "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var header [2]byte
	n, err := io.ReadFull(f, header[:])
	if err != nil || n < len(header) {
		return false
	}
	return header[0] == '#' && header[1] == '!'
}
