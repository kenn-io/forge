package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/fleetsetup"
)

type fakeFleetSetupRunner struct {
	plan        fleetsetup.Plan
	result      fleetsetup.Result
	planOptions fleetsetup.Options
	appliedPlan fleetsetup.Plan
	planErr     error
	applyErr    error
}

func (r *fakeFleetSetupRunner) Plan(
	_ context.Context,
	options fleetsetup.Options,
) (fleetsetup.Plan, error) {
	r.planOptions = options
	return r.plan, r.planErr
}

func (r *fakeFleetSetupRunner) Apply(
	_ context.Context,
	plan fleetsetup.Plan,
) (fleetsetup.Result, error) {
	r.appliedPlan = plan
	return r.result, r.applyErr
}

func setupCommandFixture(role fleetsetup.Role) (*fakeFleetSetupRunner, fleetCLIOptions, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	runner := &fakeFleetSetupRunner{
		plan: fleetsetup.Plan{
			Role: role, User: "operator", ConfigPath: "/home/operator/config.toml",
			DataDir: "/home/operator/data", ServiceKind: "systemd user service",
			ServicePath: "/home/operator/kenn-forge.service", Host: "127.0.0.1",
			Port: 8091, Origin: "https://forge.internal.example",
			Publication: "external",
		},
		result: fleetsetup.Result{
			Role: role, Origin: "https://forge.internal.example",
			NodeID: "11111111111111111111111111111111",
		},
	}
	return runner, fleetCLIOptions{
		Stdin: strings.NewReader("yes\n"), Stdout: stdout, Stderr: stderr,
		SetupRunner: runner, Runner: &fakeFleetCommandRunner{}, StdinIsTerminal: true,
	}, stdout, stderr
}

func TestFleetSetupHubDisplaysAndAppliesResolvedPlan(t *testing.T) {
	assert := assert.New(t)
	runner, options, stdout, stderr := setupCommandFixture(fleetsetup.RoleHub)
	cmd := newFleetCommand(options)
	cmd.SetArgs([]string{
		"setup", "hub", "--origin", "https://forge.internal.example",
		"--config", "/tmp/config.toml", "--port", "8092",
	})

	require.NoError(t, cmd.Execute())
	assert.Equal(fleetsetup.RoleHub, runner.planOptions.Role)
	assert.Equal("https://forge.internal.example", runner.planOptions.Origin)
	assert.Equal("/tmp/config.toml", runner.planOptions.ConfigPath)
	assert.Equal(8092, runner.planOptions.Port)
	assert.Equal(runner.plan, runner.appliedPlan)
	assert.Contains(stdout.String(), "Forge fleet setup plan")
	assert.Contains(stdout.String(), "Forge is ready")
	assert.Contains(stdout.String(), "fleet enrollment-token")
	assert.Contains(stderr.String(), "Apply this setup?")
}

func TestFleetSetupNodeTailscaleAutomation(t *testing.T) {
	runner, options, stdout, _ := setupCommandFixture(fleetsetup.RoleSpoke)
	options.StdinIsTerminal = false
	options.Stdin = strings.NewReader("")
	cmd := newFleetCommand(options)
	cmd.SetArgs([]string{"setup", "spoke", "--tailscale", "--yes"})

	require.NoError(t, cmd.Execute())
	assert.True(t, runner.planOptions.Tailscale)
	assert.Contains(t, stdout.String(), "fleet join HUB_URL")
}

func TestFleetSetupDryRunDoesNotApply(t *testing.T) {
	runner, options, stdout, _ := setupCommandFixture(fleetsetup.RoleHub)
	cmd := newFleetCommand(options)
	cmd.SetArgs([]string{"setup", "hub", "--tailscale", "--dry-run"})

	require.NoError(t, cmd.Execute())
	assert.Zero(t, runner.appliedPlan)
	assert.Contains(t, stdout.String(), "no changes made")
}

func TestFleetSetupNonInteractiveRequiresYes(t *testing.T) {
	runner, options, _, _ := setupCommandFixture(fleetsetup.RoleHub)
	options.StdinIsTerminal = false
	options.Stdin = strings.NewReader("")
	cmd := newFleetCommand(options)
	cmd.SetArgs([]string{"setup", "hub", "--tailscale"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "requires --yes")
	assert.Zero(t, runner.appliedPlan)
}

func TestFleetSetupReportsPlanningFailureBeforeApply(t *testing.T) {
	runner, options, _, _ := setupCommandFixture(fleetsetup.RoleHub)
	runner.planErr = errors.New("origin is ambiguous")
	cmd := newFleetCommand(options)
	cmd.SetArgs([]string{"setup", "hub", "--tailscale", "--yes"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "origin is ambiguous")
	assert.Zero(t, runner.appliedPlan)
}
