package fleetapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCountingTmuxScript returns a fake tmux that appends each subcommand to
// a log file and answers list-sessions from a sessions file, so a test can
// change the session listing between passes and count what was spawned.
func writeCountingTmuxScript(t *testing.T, panePID int) (tmux, logPath, sessionsPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	sessionsPath = filepath.Join(dir, "sessions.txt")
	script := `#!/bin/sh
echo "$1" >> "` + logPath + `"
case "$1" in
  list-sessions)
    cat "` + sessionsPath + `"
    ;;
  list-windows)
    printf 'forge-a|@1|0|shell|1717150000\n'
    ;;
  list-panes)
    printf 'forge-a|1717150000|` + strconv.Itoa(panePID) + `|sh\n'
    ;;
esac
exit 0
`
	tmux = writeFleetTmuxMonitorScript(t, script)
	return tmux, logPath, sessionsPath
}

func countCalls(t *testing.T, logPath, sub string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), sub+"\n")
}

func TestFleetTmuxMonitorReusesWindowsWhileSessionsUnchanged(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	tmux, logPath, sessionsPath := writeCountingTmuxScript(t, os.Getpid())
	require.NoError(os.WriteFile(sessionsPath, []byte("1717150000|1|forge-a\n"), 0o644))

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mon := newFleetTmuxMonitor([]string{tmux}, false, func() time.Time { return now }, nil)
	mon.probeTimeout = 5 * time.Second

	mon.refreshInventory(context.Background())
	require.Equal(1, countCalls(t, logPath, "list-windows"), "the first pass lists windows")
	snap := mon.snapshot()
	require.NotNil(snap.CurrentInventory)
	require.Len(snap.CurrentInventory.Sessions["forge-a"].Windows, 1)

	now = now.Add(fleetTmuxPollInterval)
	mon.refreshInventory(context.Background())
	assert.Equal(2, countCalls(t, logPath, "list-sessions"))
	assert.Equal(1, countCalls(t, logPath, "list-windows"),
		"a byte-identical session listing reuses the previous windows")
	snap = mon.snapshot()
	require.NotNil(snap.CurrentInventory)
	assert.Len(snap.CurrentInventory.Sessions["forge-a"].Windows, 1,
		"reused windows stay in the inventory")
	assert.Equal(now, snap.CurrentInventory.PolledAt, "the sample is still refreshed")

	// A changed listing re-lists windows.
	require.NoError(os.WriteFile(sessionsPath, []byte("1717150000|2|forge-a\n"), 0o644))
	now = now.Add(fleetTmuxPollInterval)
	mon.refreshInventory(context.Background())
	assert.Equal(2, countCalls(t, logPath, "list-windows"))

	// An unchanged listing still re-lists windows once the refresh bound
	// elapses.
	now = now.Add(fleetTmuxWindowRefreshInterval)
	mon.refreshInventory(context.Background())
	assert.Equal(3, countCalls(t, logPath, "list-windows"))
}

func TestFleetTmuxMonitorMetricsQueryOnlyPaneProcesses(t *testing.T) {
	for _, tool := range []string{"ps", "pgrep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	require := require.New(t)
	assert := assert.New(t)
	tmux, logPath, sessionsPath := writeCountingTmuxScript(t, os.Getpid())
	require.NoError(os.WriteFile(sessionsPath, []byte("1717150000|1|forge-a\n"), 0o644))
	mon := newFleetTmuxMonitor([]string{tmux}, false, nil, nil)
	mon.probeTimeout = 5 * time.Second

	mon.refresh(context.Background())

	snap := mon.snapshot()
	require.NotNil(snap.Metrics)
	require.Empty(snap.Metrics.Error)
	metric, ok := snap.Metrics.Sessions["forge-a"]
	require.True(ok, "the pane's session gets metrics")
	assert.GreaterOrEqual(metric.ProcessCount, 1, "the test process itself is counted")
	assert.NotEmpty(metric.ExecutableName)
	assert.Equal(1, countCalls(t, logPath, "list-panes"))
}

func TestFleetTmuxMonitorMetricsSpawnNothingWithoutSessions(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	tmux, logPath, sessionsPath := writeCountingTmuxScript(t, os.Getpid())
	require.NoError(os.WriteFile(sessionsPath, []byte(""), 0o644))
	mon := newFleetTmuxMonitor([]string{tmux}, false, nil, nil)
	mon.probeTimeout = 5 * time.Second

	mon.refresh(context.Background())

	snap := mon.snapshot()
	require.NotNil(snap.CurrentInventory)
	assert.Empty(snap.CurrentInventory.Sessions)
	assert.Equal(0, countCalls(t, logPath, "list-panes"),
		"no managed session means no pane or process probe")
	require.NotNil(snap.Metrics)
	assert.Empty(snap.Metrics.Sessions)
}

func TestProbeFleetProcessTreesWithNoRootsSpawnsNothing(t *testing.T) {
	processes, err := probeFleetProcessTrees(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, processes)
}
