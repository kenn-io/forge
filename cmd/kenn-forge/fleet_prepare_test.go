package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/spokeapi"
)

func TestFleetPrepareSpokeReportsReadyWithoutPrintingSeal(t *testing.T) {
	const seal = "private-preparation-seal"
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
		HandoffConflicts:       []spokeapi.SpokePreparationConflict{{Description: "review draft for team/project #42 on github.com (github)"}},
		HandoffErrors:          []string{"handoff review draft for team/project #42 on github.com (github): offline"},
		InFlightProviderWrites: 4, ActiveDeferredMerges: 5, UndrainedAcks: 6,
	}}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"flags", []string{"prepare-spoke", "--config", "/tmp/forge.toml", "--timeout", "2m"}, "kenn-forge fleet prepare-spoke --config /tmp/forge.toml --timeout 2m0s\n"},
		{"defaults", []string{"prepare-spoke"}, "kenn-forge fleet prepare-spoke\n"},
		{"explicit timeout", []string{"prepare-spoke", "--timeout", "60s"}, "kenn-forge fleet prepare-spoke --timeout 1m0s\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
			cmd.SetArgs(tc.args)
			require.ErrorContains(t, cmd.Execute(), "not ready")
			for _, want := range []string{
				"2 unprepared", "1 handoff conflict", "1 handoff error",
				"4 provider write", "5 deferred merge", "6 notification acknowledgement",
				"Workspace ws-1: team/project issue 42: launchSpecMissing",
				"Workspace ws-2 issue 7: sourceVisibilityExpired",
				"Handoff conflict: review draft for team/project #42 on github.com (github)", "Handoff error: handoff review draft for team/project #42 on github.com (github): offline",
				"Rerun:", tc.want,
			} {
				assert.Contains(t, stdout.String(), want)
			}
		})
	}
}

func TestFleetPrepareSpokeRerunWithOnlyInFlightCounts(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{InFlightProviderWrites: 1}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.ErrorContains(t, cmd.Execute(), "not ready")
	assert.Contains(t, stdout.String(), "1 provider write")
	assert.Contains(t, stdout.String(), "Rerun:\n  kenn-forge fleet prepare-spoke\n")
}
