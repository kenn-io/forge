package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/runtimelock"
)

func newFleetMigrateProtocolCommand(options fleetCLIOptions) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use: "migrate-protocol", Short: "Migrate stopped fleet enrollment to the current protocol",
		Long: "Migrate durable federation protocol 3 to 4. Stop the daemon and retain a matching " +
			"backup of its database, enrollment, credentials, config, and binary first. " +
			"Existing protocol-4 and unenrolled installations need no changes. " +
			"An interrupted migration can be resumed with the same command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := migrateFleetProtocol(cmd.Context(), configPath); err != nil {
				return err
			}
			_, err := fmt.Fprintln(options.Stdout, "Fleet enrollment is ready for protocol 4.")
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	return cmd
}

func migrateFleetProtocol(ctx context.Context, configPath string) (err error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	lock, err := runtimelock.Acquire(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("stop the Forge daemon before migrating its fleet protocol: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	return federation.MigrateProtocol3To4(federation.DefaultStorePath(cfg.DataDir),
		func(local *federation.LocalEnrollment) (digest string, err error) {
			database, err := db.Open(cfg.DBPath())
			if err != nil {
				return "", err
			}
			defer func() { err = errors.Join(err, database.Close()) }()
			var binding *db.SpokePreparationBinding
			seal := ""
			if local != nil {
				binding = &db.SpokePreparationBinding{
					EnrollmentID: local.EnrollmentID, LocalNodeID: local.NodeID,
					HubNodeID: local.HubID, ProtocolVersion: local.ProtocolVersion,
				}
				if local.Preparation != nil {
					digest, seal = local.Preparation.PreparationDigest, local.Preparation.Seal
				}
			}
			return database.MigrateFleetProtocol3To4(ctx, binding, digest, seal)
		})
}
