package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/spokeapi"
)

func TestFleetPrepareSpokeReportsReadyWithoutPrintingSeal(t *testing.T) {
	const seal = "private-preparation-seal"
	run := "Otherwise run:"
	if runtime.GOOS == "windows" {
		run = "Otherwise run in Command Prompt:"
	}
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{
		ReadyLaunchSpecs: 3, ReadyToActivate: true, PreparationSeal: seal,
	}}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"config", []string{"prepare-spoke", "--config", "/tmp/forge.toml"}, "kenn-forge daemon restart --config /tmp/forge.toml\n"},
		{"default", []string{"prepare-spoke"}, "kenn-forge daemon restart\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
			cmd.SetArgs(tc.args)
			require.NoError(t, cmd.Execute())
			assert.Contains(t, stdout.String(), "preparation is complete")
			assert.Contains(t, stdout.String(), "3 launch specifications")
			assert.Contains(t, stdout.String(), run+"\n  ")
			assert.Contains(t, stdout.String(), tc.want)
			assert.NotContains(t, stdout.String(), seal)
		})
	}
}

func TestFleetPrepareSpokeReportsEveryBlockerCount(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{
		Unprepared: []db.UnpreparedWorkspace{
			{Workspace: db.Workspace{ID: "ws-1", RepoOwner: "team", RepoName: "project", ItemType: "issue", ItemNumber: 42}, Reason: "launchSpecMissing"},
			{Workspace: db.Workspace{ID: "ws-2", ItemType: "issue", ItemNumber: 7}, Reason: "sourceVisibilityExpired"},
		},
		HandoffConflicts:       []spokeapi.SpokePreparationConflict{{Kind: "review_draft", SourceKey: "github\x00github.com\x00123\x00pr\x0042", Description: "review draft for team/project #42 on github.com (github)"}},
		HandoffErrors:          []string{"handoff review draft for team/project #42 on github.com (github): offline"},
		InFlightProviderWrites: 4, ActiveDeferredMerges: 5, UndrainedAcks: 6,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.ErrorContains(t, cmd.Execute(), "not ready")
	assert.NotContains(t, stdout.String(), "daemon restart")
	for _, want := range []string{
		"2 unprepared", "1 handoff conflict", "1 handoff error",
		"4 provider write", "5 deferred merge", "6 notification acknowledgement",
		"Workspace ws-1: team/project issue 42:",
		"Workspace ws-2 issue 7:",
		`Handoff conflict: review draft for team/project #42 on github.com (github) (review_draft "github\x00github.com\x00123\x00pr\x0042")`, "Handoff error: handoff review draft for team/project #42 on github.com (github): offline",
	} {
		assert.Contains(t, stdout.String(), want)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"flags", []string{"prepare-spoke", "--config", "/tmp/forge.toml", "--timeout", "2m"}, "kenn-forge fleet prepare-spoke --config /tmp/forge.toml --timeout 2m0s\n"},
		{"defaults", []string{"prepare-spoke"}, "kenn-forge fleet prepare-spoke\n"},
		{"explicit timeout", []string{"prepare-spoke", "--timeout", "60s"}, "kenn-forge fleet prepare-spoke --timeout 1m0s\n"},
		{"explicit config", []string{"prepare-spoke", "--config", config.DefaultConfigPath()}, "kenn-forge fleet prepare-spoke --config " + quoteCommand([]string{config.DefaultConfigPath()}) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
			cmd.SetArgs(tc.args)
			require.ErrorContains(t, cmd.Execute(), "not ready")
			assert.Contains(t, stdout.String(), tc.want)
		})
	}
	t.Run("pending work only", func(t *testing.T) {
		runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{InFlightProviderWrites: 1}}
		var stdout bytes.Buffer
		cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
		cmd.SetArgs([]string{"prepare-spoke"})
		require.ErrorContains(t, cmd.Execute(), "not ready")
		assert.Contains(t, stdout.String(), "Wait for pending work to finish")
	})
}

func TestFleetPrepareSpokeOldDaemonConflict(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{
		HandoffConflicts: []spokeapi.SpokePreparationConflict{{Kind: "review_draft", SourceKey: "github\x00github.com\x00123\x00pr\x0042"}},
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke", "--config", "/tmp/forge.toml"})
	require.ErrorContains(t, cmd.Execute(), "not ready")
	output := stdout.String()
	assert.NotContains(t, output, "Handoff conflict:  (")
	assert.Contains(t, output, `Handoff conflict: review_draft "github\x00github.com\x00123\x00pr\x0042"`)
	assert.Contains(t, output, "kenn-forge daemon restart --config /tmp/forge.toml")
	assert.Contains(t, output, "kenn-forge fleet prepare-spoke --config /tmp/forge.toml")
	assert.Less(t, strings.Index(output, "kenn-forge daemon restart"), strings.Index(output, "kenn-forge fleet prepare-spoke"))
}

func TestFleetPrepareSpokeWorkspaceReasons(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{Unprepared: []db.UnpreparedWorkspace{
		{Workspace: db.Workspace{ID: "one"}, Reason: "launchSpecMissing"},
		{Workspace: db.Workspace{ID: "two"}, Reason: "sourceNotVisible"},
		{Workspace: db.Workspace{ID: "three"}, Reason: "sourceVisibilityExpired"},
		{Workspace: db.Workspace{ID: "four"}, Reason: "launchSpecMismatch"},
		{Workspace: db.Workspace{ID: "five"}, Reason: "diagnostic detail"},
	}}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.ErrorContains(t, cmd.Execute(), "not ready")
	for _, want := range []string{"one: launch specification is missing", "two: source is not visible", "three: source visibility has expired", "four: launch specification does not match", "five: diagnostic detail"} {
		assert.Contains(t, stdout.String(), want)
	}
}
