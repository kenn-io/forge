//go:build windows

package procutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestRunHidesBackgroundWindows(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	//nolint:forbidigo // This test verifies Run configures an externally-created Cmd.
	cmd := exec.Command("cmd.exe", "/c", "exit 0")

	err := Run(context.Background(), cmd, "hidden subprocess test")

	require.NoError(err)
	require.NotNil(cmd.SysProcAttr)
	assert.True(cmd.SysProcAttr.HideWindow)
	assert.NotZero(cmd.SysProcAttr.CreationFlags & windows.CREATE_NO_WINDOW)
	assert.Zero(cmd.SysProcAttr.CreationFlags & windows.CREATE_NEW_CONSOLE)
}

func TestCommandHidesBackgroundWindows(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cmd := Command("cmd.exe", "/c", "exit 0")

	require.NotNil(cmd.SysProcAttr)
	assert.True(cmd.SysProcAttr.HideWindow)
	assert.NotZero(cmd.SysProcAttr.CreationFlags & windows.CREATE_NO_WINDOW)
	assert.Zero(cmd.SysProcAttr.CreationFlags & windows.CREATE_NEW_CONSOLE)
}

func TestCommandResolvesWindowsPathextBinary(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	toolPath := filepath.Join(dir, "fake-gh.cmd")
	require.NoError(os.WriteFile(toolPath, []byte("@echo off\r\nexit /b 0\r\n"), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")

	cmd := Command("fake-gh")

	assert.Equal(strings.ToLower(toolPath), strings.ToLower(cmd.Path))
}

func TestShouldRunShebangScriptWithShellRejectsBareName(t *testing.T) {
	Assert.False(t, shouldRunShebangScriptWithShell("fake-gh"))
}

func TestResolveCommandRunsShebangScriptWithResolvedShellDirectly(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	shellPath := filepath.Join(dir, "sh.cmd")
	scriptPath := filepath.Join(dir, "fake-gh")
	require.NoError(os.WriteFile(shellPath, []byte("@echo off\r\nexit /b 0\r\n"), 0o755))
	require.NoError(os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")

	name, args := resolveCommand("fake-gh", []string{"arg-one"})

	assert.Equal(strings.ToLower(shellPath), strings.ToLower(name))
	assert.Equal([]string{scriptPath, "arg-one"}, args)
	assert.NotContains(args, "-c")
}
