package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
)

func newFleetPrepareSpokeCommand(options fleetCLIOptions) *cobra.Command {
	flags := fleetPrepareOptions{}
	cmd := &cobra.Command{
		Use:   "prepare-spoke",
		Short: "Quiesce provider state and prepare this daemon as a fleet spoke",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := options.Runner.PrepareSpoke(cmd.Context(), flags)
			if err != nil {
				return err
			}
			if report.ReadyToActivate {
				_, err = fmt.Fprintf(
					options.Stdout,
					"Spoke preparation is complete (%d launch specifications ready); restart kenn-forge to activate the spoke role.\n",
					report.ReadyLaunchSpecs,
				)
				return err
			}
			_, writeErr := fmt.Fprintf(
				options.Stdout,
				"Spoke preparation is blocked: %d unprepared workspace(s), %d handoff conflict(s), %d handoff error(s), %d provider write(s), %d deferred merge(s), and %d notification acknowledgement(s) remain.\n",
				len(report.Unprepared), len(report.HandoffConflicts), len(report.HandoffErrors),
				report.InFlightProviderWrites, report.ActiveDeferredMerges, report.UndrainedAcks,
			)
			if writeErr != nil {
				return writeErr
			}
			return errors.New("spoke preparation is not ready to activate")
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", 60*time.Second, "spoke preparation request timeout")
	return cmd
}

func newFleetAbortPreparationCommand(options fleetCLIOptions) *cobra.Command {
	flags := fleetAbortPreparationOptions{}
	cmd := &cobra.Command{
		Use:   "abort-preparation",
		Short: "Abandon a pending enrollment and restore standalone provider writes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := options.Runner.AbortPreparation(cmd.Context(), flags)
			if err != nil {
				return err
			}
			message := "Spoke preparation was aborted; standalone provider writes are open."
			if report.RestartRequired {
				message = "Spoke preparation was aborted; restart the daemon to restore standalone provider writes."
			}
			if !report.HubRevoked {
				message += " The hub could not be reached; when it is available, run `kenn-forge fleet revoke " + report.EnrollmentID + "` there."
			}
			_, err = fmt.Fprintln(options.Stdout, message)
			return err
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "abort request timeout")
	cmd.Flags().BoolVar(&flags.Force, "force", false, "restore locally even when the hub is unavailable")
	return cmd
}

func newFleetRevokeCommand(options fleetCLIOptions) *cobra.Command {
	flags := fleetRevokeOptions{}
	cmd := &cobra.Command{
		Use:   "revoke ENROLLMENT_ID",
		Short: "Revoke a fleet spoke and its credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.EnrollmentID = strings.TrimSpace(args[0])
			if flags.EnrollmentID == "" {
				return errors.New("enrollment ID is required")
			}
			if err := options.Runner.Revoke(cmd.Context(), flags); err != nil {
				return err
			}
			_, err := fmt.Fprintln(options.Stdout, "Fleet enrollment revoked.")
			return err
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "revocation request timeout")
	return cmd
}
