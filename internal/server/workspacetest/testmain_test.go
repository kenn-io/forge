package workspacetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/testutil/gitsafe"
	"go.kenn.io/middleman/internal/testutil/processjob"
)

var workspaceTestTmuxCommand []string

func TestMain(m *testing.M) {
	if slices.Contains(os.Args, workspaceRuntimeHelperMarker) {
		os.Exit(m.Run())
	}
	if err := processjob.ContainCurrentProcessTree(); err != nil {
		fmt.Fprintf(os.Stderr, "contain workspace test process tree: %v\n", err)
		os.Exit(1)
	}
	cleanupTmux, err := configureWorkspaceTestTmux()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure isolated workspace test tmux: %v\n", err)
		os.Exit(1)
	}
	code := gitsafe.RunIsolatedMain(m)
	if err := cleanupTmux(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup isolated workspace test tmux: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func configureWorkspaceTestTmux() (func() error, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if errors.Is(err, exec.ErrNotFound) {
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find tmux: %w", err)
	}

	tmuxDir, err := os.MkdirTemp("/tmp", "middleman-workspacetest-tmux-*")
	if err != nil {
		return nil, fmt.Errorf("create tmux socket directory: %w", err)
	}
	socket := filepath.Join(tmuxDir, "tmux.sock")
	workspaceTestTmuxCommand = []string{
		tmuxPath, "-f", "/dev/null", "-S", socket,
	}

	return func() error {
		var errs []error
		if _, statErr := os.Stat(socket); statErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, killErr := procutil.CombinedOutput(
				ctx,
				procutil.CommandContext(
					ctx, tmuxPath, "-f", "/dev/null", "-S", socket,
					"kill-server",
				),
				"workspace test tmux cleanup",
			)
			cancel()
			msg := strings.TrimSpace(string(out))
			if killErr != nil && !workspaceTestTmuxServerAbsent(msg) {
				errs = append(errs, fmt.Errorf(
					"kill private tmux server: %w: %s",
					killErr, msg,
				))
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("inspect tmux socket: %w", statErr))
		}
		if removeErr := os.RemoveAll(tmuxDir); removeErr != nil {
			errs = append(errs, fmt.Errorf(
				"remove tmux socket directory: %w", removeErr,
			))
		}
		return errors.Join(errs...)
	}, nil
}

func workspaceTestTmuxServerAbsent(msg string) bool {
	return strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}
