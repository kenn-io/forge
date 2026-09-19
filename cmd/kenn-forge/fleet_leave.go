package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/runtimelock"
)

func newFleetLeaveCommand(options fleetCLIOptions) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use: "leave", Short: "Return a revoked, stopped spoke to standalone operation",
		Long: "First revoke this spoke on its hub while both are reachable. " +
			"Stop the spoke daemon and retain a matching backup of its data, config, and binary. " +
			"Leave preserves current local workspaces and disables federation; it does not " +
			"copy provider data or credentials from the hub. Configure independent provider access before restarting.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := leaveFleet(cmd.Context(), configPath); err != nil {
				return err
			}
			_, err := fmt.Fprintln(options.Stdout, "Fleet disabled. Configure independent provider access, then start Forge.")
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	return cmd
}

func leaveFleet(ctx context.Context, configPath string) (err error) {
	store, err := daemonruntime.Store()
	if err != nil {
		return err
	}
	lifecycle := &daemonLifecycle{deps: defaultDaemonLifecycleDeps()}
	lifecycle.deps.loadConfig = config.Load
	path, cfg, configLock, err := lifecycle.lockMutationConfig(ctx, "fleet leave", store, configPath, daemonMutationOptions{})
	if err != nil {
		return err
	}
	defer joinLifecycleLockRelease("fleet leave", configLock, &err)
	lock, err := runtimelock.Acquire(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("stop the Forge daemon before leaving its fleet: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	enrollments, err := federation.Open(federation.DefaultStorePath(cfg.DataDir), federation.StoreOptions{})
	if err != nil {
		return err
	}
	local, ok := enrollments.Local()
	if !ok || local.State != federation.EnrollmentRevoked || local.HubID == "" {
		return errors.New("revoke this spoke's enrollment on its hub before leaving; use abort-preparation for a pending enrollment")
	}
	if len(cfg.Fleet.Members) != 0 {
		return errors.New("revoke this hub's members before leaving")
	}
	for _, enrollment := range enrollments.List() {
		if enrollment.State != federation.EnrollmentRevoked {
			return errors.New("revoke this hub's enrollments before leaving")
		}
	}
	if cfg.Fleet.Hub == nil {
		if !cfg.Fleet.Enabled && cfg.Fleet.RoleOrDefault() == config.FleetRoleHub {
			// Already standalone. Do not consume a forced-abort cleanup credential.
			return nil
		}
		return errors.New("no configured hub binding; after a forced abort, finish pending revocation on the hub instead")
	}
	if cfg.Fleet.Hub.NodeID != local.HubID || cfg.Fleet.Hub.BaseURL != local.HubURL {
		return errors.New("fleet hub does not match the revoked enrollment")
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return fmt.Errorf("inspect existing Forge database: %w", err)
	}
	credentials, err := federationauth.Open(federationauth.DefaultStorePath(cfg.DataDir))
	if err != nil {
		return err
	}
	if err := credentials.RevokeOutbound(local.HubID); err != nil {
		return err
	}
	if err := credentials.RevokeInboundNode(local.HubID); err != nil {
		return err
	}
	database, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	// Finish local revocation before changing role, including after an interrupted leave.
	// Keep the revoked enrollment as history; never restore the pre-enrollment database.
	err = errors.Join(database.AbortSpokePreparation(ctx), database.Close())
	if err != nil {
		return err
	}
	cfg.Fleet.Enabled = false
	cfg.Fleet.Role = config.FleetRoleHub
	cfg.Fleet.Hub = nil
	return cfg.Save(path)
}
