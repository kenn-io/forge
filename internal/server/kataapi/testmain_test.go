package kataapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/testsignal"
)

var kataAPITestTmuxCommand []string

func TestMain(m *testing.M) {
	cleanupTmux, err := configureKataAPITestTmux()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure isolated Kata API test tmux: %v\n", err)
		os.Exit(1)
	}
	runCleanup, stopSignalCleanup := testsignal.Install(cleanupTmux, func(err error) {
		fmt.Fprintf(os.Stderr, "cleanup isolated Kata API test tmux: %v\n", err)
	})
	code := gitsafe.RunIsolatedMain(m)
	stopSignalCleanup()
	if err := runCleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup isolated Kata API test tmux: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func configureKataAPITestTmux() (func() error, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if errors.Is(err, exec.ErrNotFound) {
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find tmux: %w", err)
	}

	tmuxDir, err := os.MkdirTemp("/tmp", "forge-kataapi-test-tmux-*")
	if err != nil {
		return nil, fmt.Errorf("create tmux socket directory: %w", err)
	}
	socket := filepath.Join(tmuxDir, "tmux.sock")
	kataAPITestTmuxCommand = []string{
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
				"Kata API test tmux cleanup",
			)
			cancel()
			msg := strings.TrimSpace(string(out))
			if killErr != nil && !kataAPITestTmuxServerAbsent(msg) {
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

func kataAPITestTmuxServerAbsent(msg string) bool {
	return strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}
