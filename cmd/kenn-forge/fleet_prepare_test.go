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
		Unprepared: []db.UnpreparedWorkspace{
			{Workspace: db.Workspace{ID: "ws-1", RepoOwner: "team", RepoName: "project", ItemType: "issue", ItemNumber: 42}, Reason: "launchSpecMissing"},
			{Workspace: db.Workspace{ID: "ws-2", RepoOwner: "team", RepoName: "other", ItemType: "pull_request", ItemNumber: 7}, Reason: "sourceNotVisible"},
			{Workspace: db.Workspace{ID: "ws-3"}, Reason: "sourceVisibilityExpired"},
			{Workspace: db.Workspace{ID: "ws-4"}, Reason: "launchSpecMismatch"},
		},
		HandoffConflicts:       []db.ProviderStateConflict{{Kind: "review_draft", SourceKey: "github\x00github.com\x00123\x00pr\x0042"}},
		HandoffErrors:          []string{"handoff review_draft github\x00github.com\x00123\x00pr\x0042: offline"},
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
		"4 unprepared", "1 handoff conflict", "1 handoff error",
		"4 provider write", "5 deferred merge", "6 notification acknowledgement",
		"Workspace ws-1: team/project issue 42: launch specification missing",
		"Workspace ws-2: team/other pull_request 7: source is not visible",
		"source visibility expired", "launch specification does not match the workspace",
		"Handoff conflict review_draft: github / github.com / 123 / pr / 42", "Handoff error: handoff review_draft github / github.com / 123 / pr / 42: offline",
		"Pending writes, merges, and acknowledgements can finish draining.",
		"Rerun after resolving the items above:",
		"kenn-forge fleet prepare-spoke --config '/tmp/forge config.toml' --timeout 2m0s",
	} {
		assert.Contains(t, stdout.String(), want)
	}
}

func TestFleetPrepareSpokeDefaultCommand(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{Unprepared: []db.UnpreparedWorkspace{{Workspace: db.Workspace{ID: "ws-1", RepoOwner: "team", RepoName: "project", ItemType: "issue", ItemNumber: 42}, Reason: "database error"}}}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.Error(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "team/project issue 42: database error")
	assert.Contains(t, stdout.String(), "Rerun after resolving the items above:\n  kenn-forge fleet prepare-spoke\n")
}

func TestFleetPrepareSpokeDefaultRestart(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: spokeapi.SpokePreparationReport{ReadyToActivate: true}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner})
	cmd.SetArgs([]string{"prepare-spoke"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "kenn-forge daemon restart\n")
}
