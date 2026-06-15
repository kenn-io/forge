package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFleetTmuxInventory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	sessions := "1717150000|2|managed-1\n1717150300|1|unmanaged\n"
	windows := "" +
		"managed-1|@1|0|main|1717150100\n" +
		"managed-1|@2|1|review|1717150200\n" +
		"unmanaged|@3|0|secret title|1717150400\n"

	got := parseFleetTmuxInventory(sessions, windows)

	require.Len(got, 2)
	managed := got["managed-1"]
	require.NotNil(managed.CreatedAt)
	assert.Equal("2024-05-31T10:06:40Z", managed.CreatedAt.UTC().Format(time.RFC3339))
	assert.Equal(2, managed.WindowCount)
	require.Len(managed.Windows, 2)
	assert.Equal("@1", managed.Windows[0].ID)
	assert.Equal(0, managed.Windows[0].Index)
	assert.Equal("main", managed.Windows[0].Name)
	assert.Equal("2024-05-31T10:08:20Z", managed.Windows[0].Activity)

	unmanaged := got["unmanaged"]
	require.NotNil(unmanaged.CreatedAt)
	assert.Equal(1, unmanaged.WindowCount)
	require.Len(unmanaged.Windows, 1)
	assert.Equal("secret title", unmanaged.Windows[0].Name)
}

func TestParseFleetTmuxInventorySkipsMalformedRows(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	sessions := "bad\n1717150000\t1\tok\n"
	windows := "ok\t@1\t0\tmain\t1717150100\nbad-window\nok\t@2\tnope\tbroken\t1717150200\n"

	got := parseFleetTmuxInventory(sessions, windows)

	require.Len(got, 1)
	ok := got["ok"]
	assert.Equal(1, ok.WindowCount)
	require.Len(ok.Windows, 1)
	assert.Equal("@1", ok.Windows[0].ID)
}

// TestParseFleetTmuxInventoryNamesContainingSeparator covers session and
// window names that contain the field separator: the fixed-format fields
// anchor the parse so the names survive intact instead of being misparsed
// or skipped.
func TestParseFleetTmuxInventoryNamesContainingSeparator(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	sessions := "1717150000|2|feat|x\n"
	windows := "feat|x|@1|0|win|title|1717150100\n"

	got := parseFleetTmuxInventory(sessions, windows)

	require.Len(got, 1)
	session, ok := got["feat|x"]
	require.True(ok, "session name containing the separator must parse intact")
	require.Len(session.Windows, 1)
	assert.Equal("@1", session.Windows[0].ID)
	assert.Equal("win|title", session.Windows[0].Name,
		"window name containing the separator must parse intact")
}

// TestParseFleetTmuxPaneLineNameContainingSeparator covers the pane probe's
// twin of the separator-in-name case.
func TestParseFleetTmuxPaneLineNameContainingSeparator(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	pane, ok := parseFleetTmuxPaneLine("feat|x|1717150400|123|nvim")
	require.True(ok)
	assert.Equal("feat|x", pane.Session)
	assert.Equal(123, pane.PID)
	assert.Equal("nvim", pane.CurrentCommand)
}

func TestFleetTmuxMonitorKeepsPreviousInventorySample(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	firstAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(4 * time.Second)
	mon := newFleetTmuxMonitor([]string{"tmux"}, false, func() time.Time {
		return secondAt
	})
	mon.recordInventorySample(fleetTmuxInventorySample{
		PolledAt: firstAt,
		Sessions: map[string]fleetTmuxLiveSession{
			"managed-1": {Name: "managed-1", WindowCount: 1},
		},
	})

	mon.recordInventorySample(fleetTmuxInventorySample{
		PolledAt:  secondAt,
		Sessions:  map[string]fleetTmuxLiveSession{},
		Succeeded: true,
	})

	snap := mon.snapshot()
	require.NotNil(snap.PreviousInventory)
	assert.Contains(snap.PreviousInventory.Sessions, "managed-1")
	require.NotNil(snap.CurrentInventory)
	assert.Empty(snap.CurrentInventory.Sessions)
}

func TestFleetTmuxMonitorTreatsNoServerAsEmptyInventory(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{
			name: "list sessions no server",
			script: `#!/bin/sh
case "$1" in
  list-sessions)
    echo "no server running on /tmp/tmux-1000/default" >&2
    exit 1
    ;;
esac
exit 0
`,
		},
		{
			name: "list windows race no server",
			script: `#!/bin/sh
case "$1" in
  list-sessions)
    printf 'gone\t1717150000\t1\n'
    ;;
  list-windows)
    echo "error connecting to /tmp/tmux-1000/default (No such file or directory)" >&2
    exit 1
    ;;
esac
exit 0
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			mon := newFleetTmuxMonitor(
				[]string{writeFleetTmuxMonitorScript(t, tc.script)}, false, nil,
			)

			mon.refreshInventory(context.Background())

			snap := mon.snapshot()
			require.NotNil(snap.CurrentInventory)
			assert.True(snap.CurrentInventory.Succeeded)
			assert.Empty(snap.CurrentInventory.Sessions)
			assert.Empty(snap.InventoryError)
		})
	}
}

func TestFleetTmuxMonitorReportsFirstInventoryError(t *testing.T) {
	mon := newFleetTmuxMonitor([]string{"tmux"}, false, nil)

	mon.recordInventorySample(fleetTmuxInventorySample{
		Error:     "tmux exploded",
		Succeeded: false,
	})

	snap := mon.snapshot()
	assert.Nil(t, snap.CurrentInventory)
	assert.Equal(t, "tmux exploded", snap.InventoryError)
}

func TestParseFleetTmuxMetricsAggregatesPaneDescendants(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	sampledAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	panes := "" +
		"managed-1\t1717150200\t100\tcodex\n" +
		"managed-1\t1717150260\t300\thelper\n" +
		"unmanaged\t1717150300\t200\tvim\n"
	processes := "" +
		"100 1 0.0 4000 tmux\n" +
		"101 100 12.5 64000 codex\n" +
		"102 101 1.5 2048 sh\n" +
		"300 1 1.0 1024 helper\n" +
		"301 300 2.0 2048 sh\n" +
		"200 1 99.0 99999 vim\n"

	got := parseFleetTmuxMetrics(panes, processes, map[string]struct{}{
		"managed-1": {},
	}, sampledAt)

	require.Len(got, 1)
	metric := got["managed-1"]
	require.NotNil(metric.LastOutputAt)
	assert.Equal("2024-05-31T10:11:00Z", metric.LastOutputAt.UTC().Format(time.RFC3339))
	require.NotNil(metric.LastActiveAt)
	assert.Equal(sampledAt, *metric.LastActiveAt)
	assert.InDelta(17.0, metric.CPUPercent, 0.0001)
	assert.Equal(71, metric.ResidentMB)
	assert.Equal(5, metric.ProcessCount)
	assert.Equal("codex", metric.ExecutableName)
}

func TestParseFleetTmuxMetricsUsesAggregateCPUForActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	sampledAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	panes := "" +
		"managed-aggregate\t1717150200\t100\tcodex\n" +
		"managed-aggregate\t1717150260\t200\thelper\n"
	processes := "" +
		"100 1 0.06 1024 codex\n" +
		"200 1 0.06 1024 helper\n"

	got := parseFleetTmuxMetrics(panes, processes, map[string]struct{}{
		"managed-aggregate": {},
	}, sampledAt)

	require.Len(got, 1)
	metric := got["managed-aggregate"]
	require.NotNil(metric.LastActiveAt)
	assert.Equal(sampledAt, *metric.LastActiveAt)
	assert.InDelta(0.12, metric.CPUPercent, 0.0001)
}

func writeFleetTmuxMonitorScript(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}
