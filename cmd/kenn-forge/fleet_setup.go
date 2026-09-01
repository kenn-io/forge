package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleetsetup"
)

type fleetSetupCommandRunner interface {
	Plan(context.Context, fleetsetup.Options) (fleetsetup.Plan, error)
	Apply(context.Context, fleetsetup.Plan) (fleetsetup.Result, error)
}

func newFleetSetupCommand(options fleetCLIOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use: "setup", Short: "Install and publish this Forge for federation",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newFleetSetupRoleCommand(options, fleetsetup.RoleHub),
		newFleetSetupRoleCommand(options, fleetsetup.RoleSpoke),
	)
	return cmd
}

func newFleetSetupRoleCommand(options fleetCLIOptions, role fleetsetup.Role) *cobra.Command {
	flags := fleetsetup.Options{Role: role}
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   string(role),
		Short: fmt.Sprintf("Configure this Forge as a federation %s", role),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			plan, err := options.SetupRunner.Plan(cmd.Context(), flags)
			if err != nil {
				return err
			}
			if err := writeFleetSetupPlan(options.Stdout, plan); err != nil {
				return err
			}
			if dryRun {
				_, err := fmt.Fprintln(options.Stdout, "Dry run complete; no changes made.")
				return err
			}
			if !yes {
				if !options.StdinIsTerminal {
					return errors.New("non-interactive setup requires --yes after reviewing the plan")
				}
				confirmed, err := confirmFleetSetup(options.Stdin, options.Stderr)
				if err != nil {
					return err
				}
				if !confirmed {
					return errors.New("setup canceled")
				}
			}
			result, err := options.SetupRunner.Apply(cmd.Context(), plan)
			if err != nil {
				return err
			}
			return writeFleetSetupResult(options.Stdout, result)
		},
	}
	cmd.Flags().BoolVar(&flags.Tailscale, "tailscale", false, "publish through Tailscale Serve")
	cmd.Flags().StringVar(&flags.Origin, "origin", "", "canonical HTTPS origin managed by an external LAN or reverse proxy")
	cmd.Flags().StringVar(&flags.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVar(&flags.DataDir, "data-dir", "", "Forge data directory (defaults to the existing config or standard path)")
	cmd.Flags().StringVar(&flags.BinaryPath, "binary", "", "kenn-forge binary installed in the service")
	cmd.Flags().StringVar(&flags.User, "user", "", "operating-system user that runs Forge")
	cmd.Flags().StringVar(&flags.TailscaleLogin, "tailscale-login", "", "allowed Tailscale user login")
	cmd.Flags().StringVar(&flags.TailscaleDNS, "tailscale-dns-name", "", "canonical Tailscale device DNS name")
	cmd.Flags().IntVar(&flags.Port, "port", 0, "loopback Forge port (defaults to existing config or 8091)")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply the displayed plan without an interactive prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "display the resolved plan without changing anything")
	return cmd
}

func writeFleetSetupPlan(writer io.Writer, plan fleetsetup.Plan) error {
	lines := [][2]string{
		{"Role", string(plan.Role)},
		{"User", plan.User},
		{"Config", plan.ConfigPath},
		{"Data", plan.DataDir},
		{"Service", plan.ServiceKind + " at " + plan.ServicePath},
		{"Listener", fmt.Sprintf("%s:%d", plan.Host, plan.Port)},
		{"HTTPS origin", plan.Origin},
		{"Publication", plan.Publication},
	}
	if plan.TailscaleDNS != "" {
		lines = append(lines,
			[2]string{"Tailscale DNS", plan.TailscaleDNS},
			[2]string{"Allowed login", plan.TailscaleLogin},
		)
	}
	if _, err := fmt.Fprintln(writer, "Forge fleet setup plan:"); err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(writer, "  %-14s %s\n", line[0]+":", line[1]); err != nil {
			return err
		}
	}
	return nil
}

func confirmFleetSetup(stdin io.Reader, stderr io.Writer) (bool, error) {
	if _, err := fmt.Fprint(stderr, "Apply this setup? [y/N] "); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read setup confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func writeFleetSetupResult(writer io.Writer, result fleetsetup.Result) error {
	if _, err := fmt.Fprintf(
		writer,
		"Forge is ready at %s (%s %s).\n",
		result.Origin,
		result.Role,
		result.NodeID,
	); err != nil {
		return err
	}
	if result.Role == fleetsetup.RoleHub {
		_, err := fmt.Fprintf(
			writer,
			"Next: kenn-forge fleet enrollment-token --base-url %s\n",
			result.Origin,
		)
		return err
	}
	_, err := fmt.Fprintf(
		writer,
		"Next: kenn-forge fleet join HUB_URL --base-url %s\n",
		result.Origin,
	)
	return err
}
