//go:build !windows

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
)

func TestServerPtyOwnerHelperStopsWhenParentKilled(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	session := "test-parent-loss"
	paths, err := ptyowner.NewSessionPaths(root, session)
	require.NoError(err)

	cmd := procutil.Command(
		os.Args[0],
		"-test.run=TestServerPtyOwnerParentHelperProcess",
		"--",
		serverPtyOwnerParentHelperMarker,
		"-root", root,
		"-session", session,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(cmd.Start())
	waited := false
	t.Cleanup(func() {
		if !waited && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = ptyowner.NewClient(root, []string{"/bin/sh"}).Stop(stopCtx, session)
	})

	type ownerProcessState struct {
		PID int `json:"pid"`
	}
	var state ownerProcessState
	require.Eventually(func() bool {
		data, readErr := os.ReadFile(paths.StatePath)
		if readErr != nil {
			return false
		}
		return json.Unmarshal(data, &state) == nil && state.PID > 0
	}, 5*time.Second, 20*time.Millisecond, "owner did not start: %s", output.String())

	require.NoError(syscall.Kill(cmd.Process.Pid, syscall.SIGKILL))
	require.Error(cmd.Wait())
	waited = true

	require.Eventually(func() bool {
		processErr := syscall.Kill(state.PID, 0)
		_, stateErr := os.Stat(paths.Dir)
		_, socketErr := os.Stat(paths.Socket)
		return errors.Is(processErr, syscall.ESRCH) &&
			os.IsNotExist(stateErr) &&
			os.IsNotExist(socketErr)
	}, 5*time.Second, 20*time.Millisecond, "owner survived parent loss: %s", output.String())
}

func TestServerPtyOwnerParentHelperProcess(t *testing.T) {
	args := os.Args
	sep := slices.Index(args, "--")
	if sep < 0 || len(args) <= sep+1 ||
		args[sep+1] != serverPtyOwnerParentHelperMarker {
		return
	}
	args = args[sep+2:]
	fs := flag.NewFlagSet("test pty-owner parent", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	root := fs.String("root", "", "pty owner state root")
	session := fs.String("session", "", "session name")
	require.NoError(t, fs.Parse(args))

	client := &ptyowner.Client{
		Root:    *root,
		ExePath: os.Args[0],
		ExeArgs: ptyOwnerHelperExeArgs(os.Getpid()),
		Command: []string{"/bin/sh"},
	}
	require.NoError(t, client.Ensure(context.Background(), *session, os.TempDir()))
	blockServerRuntimeHelper()
}

func testPtyOwnerParentContext(parentPID int) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if parentPID <= 0 {
		return ctx, cancel
	}
	if os.Getppid() != parentPID {
		cancel()
		return ctx, cancel
	}

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if os.Getppid() != parentPID {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
