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
	assert := assert.New(t)
	const seal = "private-preparation-seal"
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{
		ReadyLaunchSpecs: 3, ReadyToActivate: true, PreparationSeal: seal,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner,
	})
	cmd.SetArgs([]string{"prepare-spoke", "--config", "/tmp/forge config.toml"})

	require.NoError(t, cmd.Execute())
	assert.Contains(stdout.String(), "preparation is complete")
	assert.Contains(stdout.String(), "3 launch specifications")
	assert.Contains(stdout.String(), "kenn-forge daemon restart --config '/tmp/forge config.toml'")
	assert.NotContains(stdout.String(), seal)
}

func TestFleetPrepareSpokeReportsEveryBlockerCount(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{
		Unprepared:             []db.UnpreparedWorkspace{{Workspace: db.Workspace{ID: "ws-1", RepoOwner: "team", RepoName: "project", ItemType: "issue", ItemNumber: 42}, Reason: "missing branch"}, {Workspace: db.Workspace{ID: "ws-2", RepoOwner: "team", RepoName: "other", ItemType: "pull_request", ItemNumber: 7}, Reason: "unavailable"}},
		HandoffConflicts:       []db.ProviderStateConflict{{Kind: "review_draft", SourceKey: "draft-1"}},
		HandoffErrors:          []string{"offline"},
		InFlightProviderWrites: 4, ActiveDeferredMerges: 5, UndrainedAcks: 6,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner,
	})
	cmd.SetArgs([]string{"prepare-spoke", "--config", "/tmp/forge config.toml", "--timeout", "2m"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "not ready")
	for _, want := range []string{
		"2 unprepared", "1 handoff conflict", "1 handoff error",
		"4 provider write", "5 deferred merge", "6 notification acknowledgement",
		"Workspace ws-1: team/project issue 42: missing branch",
		"Workspace ws-2: team/other pull_request 7: unavailable",
		"Inspect handoff conflict review_draft: draft-1", "Handoff error: offline",
		"Pending writes, merges, and acknowledgements can finish draining.",
		"kenn-forge fleet prepare-spoke --config '/tmp/forge config.toml' --timeout 2m0s",
	} {
		assert.Contains(t, stdout.String(), want)
	}
}

func TestFleetPrepareSpokeDefaultCommand(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{Unprepared: []db.UnpreparedWorkspace{{Workspace: db.Workspace{ID: "ws-1", ItemType: "adhoc", ItemKey: "branch:main"}, Reason: "missing"}}}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.Error(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "adhoc branch:main: missing")
	assert.Contains(t, stdout.String(), "Rerun:\n  kenn-forge fleet prepare-spoke\n")
}
