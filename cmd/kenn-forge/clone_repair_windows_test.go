package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/safefileio"
	"golang.org/x/sys/windows"
)

func TestDaemonStartDefersCloneRepairUntilPublicationWindowsE2E(t *testing.T) {
	root := filepath.Join(t.TempDir(), "with spaces")
	require.NoError(t, os.Mkdir(root, 0o700))
	gatePath := filepath.Join(root, "publish-gate")
	require.NoError(t, os.WriteFile(gatePath, nil, 0o600))
	bin := buildForgeWithLDFlags(t, fmt.Sprintf(
		`-X main.version=v-clone-repair -X "main.runtimePublishGatePath=%s"`, gatePath,
	))
	fixture := newDaemonLifecycleFixture(t, bin)
	dataDir := fixture.dataDir("data")
	fixture.writeConfig(dataDir)
	require.NoError(t, safefileio.EnsurePrivateDir(fixture.store.Dir))
	store, err := daemonruntime.Store()
	require.NoError(t, err)
	store.ValidateFiles = true

	// Move an inherited broad-access clone into an already private home.
	source := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(source, 0o700))
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + tokenUser.User.Sid.String() + ")(A;OICI;GR;;;WD)")
	require.NoError(t, err)
	acl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(source, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	moved := filepath.Join(source, "clone")
	require.NoError(t, os.Mkdir(moved, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(moved, "file"), nil, 0o600))
	cloneDir := filepath.Join(fixture.store.Dir, "clone")
	require.NoError(t, os.Rename(moved, cloneDir))
	cloneFile := filepath.Join(cloneDir, "file")
	before := cloneFileDACL(t, cloneFile)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := procutil.CommandContext(ctx, bin, "daemon", "start", "--config", fixture.configPath)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				assert.Fail(t, "start caller did not exit during cleanup")
			}
		}
		_ = os.Remove(gatePath)
		status, err := runtimelock.Read(dataDir)
		if err == nil && status.Running && status.Metadata != nil {
			if process, err := os.FindProcess(status.Metadata.PID); err == nil {
				_ = process.Kill()
				_, _ = process.Wait()
				_ = process.Release()
			}
		}
	})
	waitForFile(t, gatePath+".ready", 10*time.Second)
	require.Equal(t, before, cloneFileDACL(t, cloneFile))
	status, err := runtimelock.Read(dataDir)
	require.NoError(t, err)
	require.True(t, status.Running)
	require.NotNil(t, status.Metadata)
	pid := status.Metadata.PID
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+status.Metadata.ListenAddr+"/healthz", nil)
	require.NoError(t, err)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	records, err := store.List()
	require.NoError(t, err)
	require.Empty(t, records)

	select {
	case err := <-done:
		waited = true
		require.Error(t, err, output.String())
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	case <-time.After(20 * time.Second):
		require.FailNow(t, "start caller did not stop at its deadline")
	}
	stdout, stderr, err := fixture.run("status", "--json")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, `"running": true`)
	require.Contains(t, stdout, fmt.Sprintf(`"pid": %d`, pid))
	require.Equal(t, before, cloneFileDACL(t, cloneFile))

	require.NoError(t, os.Remove(gatePath))
	require.Eventually(t, func() bool {
		record, _, found, err := daemonruntime.FindVerified(t.Context(), store, dataDir)
		if err != nil || !found {
			return false
		}
		require.Equal(t, pid, record.PID)
		require.Equal(t, "v-clone-repair", record.Version)
		ready, err := daemonruntime.IsVerifiedReady(t.Context(), record, dataDir)
		return err == nil && ready
	}, 15*time.Second, 50*time.Millisecond)
	require.NotEqual(t, before, cloneFileDACL(t, cloneFile))
	_, stderr, err = fixture.run("stop")
	require.NoError(t, err, stderr)

	// Idle status and stop must leave newly moved-in files for the next serve.
	fresh := filepath.Join(source, "fresh")
	require.NoError(t, os.WriteFile(fresh, nil, 0o600))
	cloneFile = filepath.Join(cloneDir, "fresh")
	require.NoError(t, os.Rename(fresh, cloneFile))
	before = cloneFileDACL(t, cloneFile)
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"stop"}} {
		_, stderr, err := fixture.run(args...)
		require.NoError(t, err, stderr)
		require.Equal(t, before, cloneFileDACL(t, cloneFile))
	}
}

func cloneFileDACL(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	return sd.String()
}
