package sshfleet

import (
	"context"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/kit/openssh"
)

// Package sshfleet owns Forge's remote CLI relay over SSH. Persistent
// ControlMaster policy and lifecycle belong to kit/openssh; this package binds
// Forge commands to one generation returned by that shared manager.

// Connection is the generation-bound capability for one fleet peer.
type Connection struct {
	Identity   string
	Generation openssh.Generation
	Target     openssh.Target
	// Persistent distinguishes a kit-managed generation from the explicit
	// masterless mode used when multiplexing is unsupported.
	Persistent bool
}

// NewPersistentManager builds the shared kit manager with Forge's bounded
// subprocess runner. Callers may still override RunSSH for tests.
func NewPersistentManager(
	socketDir string,
	config openssh.PersistentConfig,
) (*openssh.PersistentManager, error) {
	if config.RunSSH == nil {
		config.RunSSH = runSSH
	}
	return openssh.NewPersistentManager(socketDir, config)
}

func runSSH(ctx context.Context, arguments []string) (int, error) {
	cmd := procutil.CommandContext(ctx, "ssh", arguments...)
	err := procutil.Run(ctx, cmd, "ssh fleet control")
	if err == nil {
		return 0, nil
	}
	if cmd.ProcessState == nil {
		return -1, err
	}
	return cmd.ProcessState.ExitCode(), err
}
