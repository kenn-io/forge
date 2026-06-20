package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"

	"go.kenn.io/middleman/internal/procutil"
)

var ErrRevealUnsupported = errors.New("open workspace folder is not supported on this platform")

// RevealWorktreePath asks the host OS to reveal or open the workspace folder.
// It is intended for local workspaces only; fleet callers proxy to the remote
// daemon only when opening the folder on that remote host is explicitly desired.
func RevealWorktreePath(ctx context.Context, path string) error {
	if path == "" {
		return errors.New("empty workspace path")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat workspace path: %w", err)
	}
	name, args, ok := revealCommandForPlatform(runtime.GOOS, path)
	if !ok {
		return ErrRevealUnsupported
	}
	cmd := procutil.CommandContext(ctx, name, args...)
	if err := procutil.Run(ctx, cmd, "workspace reveal command"); err != nil {
		return fmt.Errorf("open workspace path: %w", err)
	}
	return nil
}

func revealCommandForPlatform(goos, path string) (string, []string, bool) {
	switch goos {
	case "darwin":
		return "open", []string{"-R", path}, true
	case "windows":
		return "explorer.exe", []string{"/select," + path}, true
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{path}, true
	default:
		return "", nil, false
	}
}
