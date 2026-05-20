//go:build windows

package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

func fakeGHCLIPath(dir string) string {
	return filepath.Join(dir, "gh.cmd")
}

func fakeGHCLIScript(t *testing.T, opts fakeGHCLIOptions) string {
	t.Helper()
	script := "@echo off\r\n"
	script += "echo %*>>\"%FAKE_GH_ARGV%\"\r\n"
	if opts.SleepSeconds > 0 {
		script += ":loop\r\ngoto loop\r\n"
	}
	if opts.Stderr != "" {
		script += "echo " + opts.Stderr + " 1>&2\r\n"
	}
	if opts.Stdout != "" {
		script += "echo " + opts.Stdout + "\r\n"
	}
	script += fmt.Sprintf("exit /b %d\r\n", opts.ExitCode)
	return script
}

func fakeGHCLIRejectHostnameThenBareScript() string {
	return "@echo off\r\n" +
		"set \"ARGS=%*\"\r\n" +
		"echo %*>>\"%FAKE_GH_ARGV%\"\r\n" +
		"if not \"%ARGS:--hostname=%\"==\"%ARGS%\" (\r\n" +
		"  echo unknown flag: --hostname 1>&2\r\n" +
		"  exit /b 2\r\n" +
		")\r\n" +
		"echo gh-secret-bare\r\n" +
		"exit /b 0\r\n"
}

func fakeGHCLIRejectHostnameScript() string {
	return "@echo off\r\n" +
		"echo %*>>\"%FAKE_GH_ARGV%\"\r\n" +
		"echo unknown flag: --hostname 1>&2\r\n" +
		"exit /b 2\r\n"
}
