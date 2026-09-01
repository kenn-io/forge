package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
)

func TestFleetPrepareSpokeReportsReadyWithoutPrintingSeal(t *testing.T) {
	assert := assert.New(t)
	const seal = "private-preparation-seal"
	runner := &fakeFleetCommandRunner{prepareResult: server.SpokePreparationReport{
		ReadyLaunchSpecs: 3, ReadyToActivate: true, PreparationSeal: seal,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner,
	})
	cmd.SetArgs([]string{"prepare-spoke", "--config", "/tmp/forge.toml"})

	require.NoError(t, cmd.Execute())
	assert.Contains(stdout.String(), "preparation is complete")
	assert.Contains(stdout.String(), "3 launch specifications")
	assert.Contains(stdout.String(), "restart kenn-forge")
	assert.NotContains(stdout.String(), seal)
}

func TestFleetPrepareSpokeReportsEveryBlockerCount(t *testing.T) {
	runner := &fakeFleetCommandRunner{prepareResult: server.SpokePreparationReport{
		Unprepared:             []db.UnpreparedWorkspace{{}, {}},
		HandoffConflicts:       []db.ProviderStateConflict{{}},
		HandoffErrors:          []string{"offline"},
		InFlightProviderWrites: 4, ActiveDeferredMerges: 5, UndrainedAcks: 6,
	}}
	var stdout bytes.Buffer
	cmd := newFleetCommand(fleetCLIOptions{
		Stdin: strings.NewReader(""), Stdout: &stdout, Runner: runner,
	})
	cmd.SetArgs([]string{"prepare-spoke"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "not ready")
	for _, want := range []string{
		"2 unprepared", "1 handoff conflict", "1 handoff error",
		"4 provider write", "5 deferred merge", "6 notification acknowledgement",
	} {
		assert.Contains(t, stdout.String(), want)
	}
}
