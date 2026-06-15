package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/runtimelock"
)

// TestAPIVerbE2E pins the thin-client contract end to end against a
// real daemon with [api].require_auth enabled: the verb discovers the
// daemon via runtime metadata, authenticates with the minted token,
// relays bodies verbatim, and exits 0 / 1 / 2 for success / HTTP
// error / no-request — the exit semantics fleet transports script
// against. It also proves auth is actually enforced end to end: a
// raw credential-less request is rejected.
func TestAPIVerbE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)
	appendConfig(t, cfgPath, "\n[api]\nrequire_auth = true\n")

	daemon := procutil.Command(bin, "--config", cfgPath)
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	daemon.Env = append(os.Environ(), "MIDDLEMAN_LOG_LEVEL=warn")
	require.NoError(daemon.Start())
	t.Cleanup(func() {
		if daemon.Process != nil {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	})
	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)
	waitForFile(t, runtimelock.AuthTokenPath(dataDir), 10*time.Second)

	// Enforcement proof: a credential-less request is rejected.
	require.Eventually(func() bool {
		resp, err := http.Get(fmt.Sprintf(
			"http://127.0.0.1:%d/api/v1/snapshot", port,
		))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	}, 10*time.Second, 100*time.Millisecond,
		"credential-less API request must 401")

	run := func(args ...string) (string, string, int) {
		cmd := procutil.Command(bin,
			append([]string{"api", "--config", cfgPath}, args...)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		var exitErr *exec.ExitError
		if err != nil {
			require.ErrorAs(err, &exitErr)
			code = exitErr.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}

	// Success: exit 0, response body on stdout.
	out, _, code := run("GET", "/api/v1/snapshot")
	assert.Equal(0, code)
	assert.Contains(out, "\"hosts\"",
		"snapshot body relayed verbatim")

	// -i mode: the exact status line precedes the body so relays can
	// recover the code; still exit 1 on HTTP errors.
	out, _, code = run("-i", "GET", "/api/v1/projects/prj_missing")
	assert.Equal(1, code)
	assert.True(strings.HasPrefix(out, "HTTP/1.1 404"),
		"status line first: %q", out)
	assert.Contains(out, "\r\n\r\n{",
		"blank line separates status from the body")

	// Zero-body mutation: the verb must send application/json so the
	// CSRF content-type guard does not reject it.
	_, _, code = run("POST", "/api/v1/sync")
	assert.Equal(0, code,
		"zero-body POST must clear the CSRF content-type guard")

	// HTTP error: exit 1, the problem document still on stdout.
	out, errOut, code := run("GET", "/api/v1/projects/prj_missing")
	assert.Equal(1, code)
	assert.Contains(out, "\"code\"",
		"problem document relayed on stdout")
	assert.Contains(errOut, "returned",
		"status summary on stderr")

	// Stop the daemon: exit 2, nothing on stdout.
	require.NoError(daemon.Process.Signal(syscall.SIGTERM))
	_ = daemon.Wait()
	require.Eventually(func() bool {
		st, err := runtimelock.Read(dataDir)
		return err == nil && !st.Running
	}, 10*time.Second, 100*time.Millisecond)

	out, errOut, code = run("GET", "/api/v1/snapshot")
	assert.Equal(2, code)
	assert.Empty(out)
	assert.Contains(errOut, "no middleman daemon is running")
}

func appendConfig(t *testing.T, path, extra string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(extra)
	require.NoError(t, err)
}

// TestAPIVerbE2EWithBasePath pins that the api verb honors a
// non-root base_path: API routes live under the prefix, so the verb
// must join it from config rather than hitting root-mounted paths.
// The runtime metadata also publishes base_path for thin clients
// that discover the daemon from the lock file alone.
func TestAPIVerbE2EWithBasePath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)
	// base_path is a top-level key; prepend so it does not land
	// inside the last TOML table of the minimal config.
	existing, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.NoError(os.WriteFile(
		cfgPath,
		append([]byte("base_path = \"/mm\"\n"), existing...),
		0o600,
	))

	daemon := procutil.Command(bin, "--config", cfgPath)
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	daemon.Env = append(os.Environ(), "MIDDLEMAN_LOG_LEVEL=warn")
	require.NoError(daemon.Start())
	t.Cleanup(func() {
		if daemon.Process != nil {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	})
	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)

	metadata, err := os.ReadFile(runtimelock.MetadataPath(dataDir))
	require.NoError(err)
	assert.Contains(string(metadata), `"base_path": "/mm"`)

	callSnapshot := func() bool {
		cmd := procutil.Command(bin,
			"api", "--config", cfgPath, "GET", "/api/v1/snapshot")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard
		err := cmd.Run()
		return err == nil &&
			strings.Contains(stdout.String(), `"hosts"`)
	}
	require.Eventually(callSnapshot, 10*time.Second, 200*time.Millisecond,
		"api verb must reach the snapshot under the base path")

	// base_path is startup-bound: editing the config while the old
	// daemon still serves /mm must not repoint the verb — the runtime
	// metadata's base_path is authoritative.
	edited, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.NoError(os.WriteFile(cfgPath, []byte(strings.Replace(
		string(edited), `base_path = "/mm"`, `base_path = "/other"`, 1,
	)), 0o600))
	assert.True(callSnapshot(),
		"verb must keep using the running daemon's base path")
}
