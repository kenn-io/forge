package panebootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/procutil"
)

const execHelperMarker = "panebootstrap-exec-helper"

func TestMain(m *testing.M) {
	// When this binary is invoked as the pane bootstrap, exec the handoff
	// command and never return — this is what the exec e2e exercises. In a
	// normal `go test` invocation os.Args[1] is a -test flag, so this is a
	// no-op and the suite runs.
	ExecIfRequested()
	os.Exit(m.Run())
}

func TestWriteReadHandoffRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		env  []string
		argv []string
	}{
		{"simple", []string{"PATH=/usr/bin", "TERM=xterm"}, []string{"/bin/sh", "-c", "echo hi"}},
		{"empty env", nil, []string{"/bin/true"}},
		{"value with newline", []string{"X=line1\nline2"}, []string{"/bin/echo", ""}},
		{"value with quotes and equals", []string{`Q=a"b'c=d`}, []string{"/bin/echo", "a=b", ""}},
		{"trailing empty argv args", []string{"A=1"}, []string{"/bin/echo", "", "x", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			path, err := WriteHandoff(dir, tc.env, tc.argv)
			require.NoError(t, err)

			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(os.FileMode(0o600), info.Mode().Perm())

			gotEnv, gotArgv, err := ReadHandoff(path)
			require.NoError(t, err)
			// slices.Equal treats nil and empty as equal, sidestepping the
			// nil-vs-[]string{} reflect.DeepEqual trap.
			assert.True(slices.Equal(tc.env, gotEnv), "env: want %q got %q", tc.env, gotEnv)
			assert.True(slices.Equal(tc.argv, gotArgv), "argv: want %q got %q", tc.argv, gotArgv)
		})
	}
}

func TestReadHandoffRejectsMalformed(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(os.WriteFile(p, []byte(content), 0o600))
		return p
	}

	_, _, err := ReadHandoff(write("garbage", "not a handoff at all"))
	require.Error(err)
	_, _, err = ReadHandoff(write("badheader", "wrong/9\x002\x000\x00a\x00b\x00"))
	require.Error(err)
	_, _, err = ReadHandoff(write("badcount", handoffHeader+"\x00notnum\x000\x00"))
	require.Error(err)
	_, _, err = ReadHandoff(write("truncated", handoffHeader+"\x005\x000\x00only\x00"))
	require.Error(err)
	_, _, err = ReadHandoff(filepath.Join(dir, "does-not-exist"))
	require.Error(err)
}

// TestExecIfRequestedExecsHandoffCommand drives the full bootstrap: spawn
// this test binary as `<exe> __tmux-pane-bootstrap <handoff>`, which reads
// the handoff, removes it, and execs the recorded argv (the test binary's
// exec helper) under exactly the handoff env.
func TestExecIfRequestedExecsHandoffCommand(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	exe, err := os.Executable()
	require.NoError(t, err)

	handoffEnv := []string{
		"BOOTSTRAP_TEST_KEY=bootstrap-test-value",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	helperArgv := []string{exe, "-test.run=TestExecHelper", "--", execHelperMarker, "extra-arg"}
	path, err := WriteHandoff(dir, handoffEnv, helperArgv)
	require.NoError(t, err)

	cmd := procutil.Command(exe, Subcommand, path)
	// Pollute the bootstrap process env to prove env -i wipes it before exec.
	cmd.Env = append(os.Environ(), "SHOULD_NOT_LEAK=leaked")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	got := string(out)

	// argv delivered to the exec'd command.
	assert.Contains(got, "ARG:"+execHelperMarker)
	assert.Contains(got, "ARG:extra-arg")
	// The handoff env is present...
	assert.Contains(got, "ENV:BOOTSTRAP_TEST_KEY=bootstrap-test-value")
	// ...and the bootstrap process's own env did not leak through (env -i).
	assert.NotContains(got, "SHOULD_NOT_LEAK")
	// The handoff file is removed before exec.
	_, statErr := os.Stat(path)
	assert.True(os.IsNotExist(statErr), "handoff file must be removed, stat err: %v", statErr)
}

func TestExecIfRequestedExits127OnBadHandoff(t *testing.T) {
	assert := Assert.New(t)
	exe, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	bad := filepath.Join(dir, "garbage")
	require.NoError(t, os.WriteFile(bad, []byte("not a handoff"), 0o600))

	for _, path := range []string{bad, filepath.Join(dir, "missing")} {
		cmd := procutil.Command(exe, Subcommand, path)
		runErr := cmd.Run()
		var exitErr *exec.ExitError
		require.ErrorAs(t, runErr, &exitErr)
		assert.Equal(127, exitErr.ExitCode(), "handoff %q", path)
	}
}

// TestExecHelper is the exec target for TestExecIfRequestedExecsHandoffCommand.
// It is a no-op unless invoked with the marker after a `--` separator.
func TestExecHelper(t *testing.T) {
	args := os.Args
	i := slices.Index(args, "--")
	if i < 0 {
		return
	}
	args = args[i+1:]
	if len(args) == 0 || args[0] != execHelperMarker {
		return
	}
	var b strings.Builder
	for _, e := range os.Environ() {
		b.WriteString("ENV:" + e + "\n")
	}
	for _, a := range args {
		b.WriteString("ARG:" + a + "\n")
	}
	fmt.Print(b.String())
	os.Exit(0)
}
