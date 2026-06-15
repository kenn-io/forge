package server

import (
	"context"
	"errors"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/procutil"
)

const (
	fleetTmuxProbeTimeout      = 750 * time.Millisecond
	fleetTmuxStaleThreshold    = 30 * time.Second
	fleetTmuxActivityCPUThresh = 0.1
	// tmux 3.5a renders literal tabs in -F output as underscores, so use a
	// printable separator the real command preserves.
	fleetTmuxFieldSeparator = "|"
)

type fleetTmuxMonitor struct {
	mu                      sync.RWMutex
	tmuxCmd                 []string
	includeUnmanagedDetails bool
	clock                   func() time.Time

	currentInventory     fleetTmuxInventorySample
	previousInventory    fleetTmuxInventorySample
	hasCurrentInventory  bool
	hasPreviousInventory bool
	inventoryError       string

	metrics fleetTmuxMetricsSample
}

type fleetTmuxMonitorSnapshot struct {
	CurrentInventory       *fleetTmuxInventorySample
	PreviousInventory      *fleetTmuxInventorySample
	InventoryError         string
	Metrics                *fleetTmuxMetricsSample
	IncludeUnmanagedDetail bool
}

type fleetTmuxInventorySample struct {
	PolledAt  time.Time
	Sessions  map[string]fleetTmuxLiveSession
	Error     string
	Succeeded bool
}

type fleetTmuxLiveSession struct {
	Name        string
	CreatedAt   *time.Time
	Windows     []fleet.TmuxWindowInfo
	WindowCount int
}

type fleetTmuxMetricsSample struct {
	SampledAt time.Time
	Sessions  map[string]fleetTmuxSessionMetrics
	Error     string
}

type fleetTmuxSessionMetrics struct {
	CPUPercent     float64
	ResidentMB     int
	ProcessCount   int
	LastOutputAt   *time.Time
	LastActiveAt   *time.Time
	ExecutableName string
}

type fleetTmuxPaneInfo struct {
	Session        string
	Activity       *time.Time
	PID            int
	CurrentCommand string
}

type fleetProcessInfo struct {
	PID        int
	PPID       int
	CPUPercent float64
	RSSKB      int
	Command    string
}

func newFleetTmuxMonitor(
	tmuxCmd []string,
	includeUnmanagedDetails bool,
	clock func() time.Time,
) *fleetTmuxMonitor {
	if clock == nil {
		clock = time.Now
	}
	if len(tmuxCmd) == 0 {
		tmuxCmd = []string{"tmux"}
	}
	return &fleetTmuxMonitor{
		tmuxCmd:                 slices.Clone(tmuxCmd),
		includeUnmanagedDetails: includeUnmanagedDetails,
		clock:                   clock,
	}
}

func (m *fleetTmuxMonitor) snapshot() fleetTmuxMonitorSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var current *fleetTmuxInventorySample
	if m.hasCurrentInventory {
		c := cloneFleetTmuxInventorySample(m.currentInventory)
		current = &c
	}
	var previous *fleetTmuxInventorySample
	if m.hasPreviousInventory {
		p := cloneFleetTmuxInventorySample(m.previousInventory)
		previous = &p
	}
	var metrics *fleetTmuxMetricsSample
	if !m.metrics.SampledAt.IsZero() || m.metrics.Error != "" || len(m.metrics.Sessions) > 0 {
		mt := cloneFleetTmuxMetricsSample(m.metrics)
		metrics = &mt
	}
	return fleetTmuxMonitorSnapshot{
		CurrentInventory:       current,
		PreviousInventory:      previous,
		InventoryError:         m.inventoryError,
		Metrics:                metrics,
		IncludeUnmanagedDetail: m.includeUnmanagedDetails,
	}
}

func (m *fleetTmuxMonitor) recordInventorySample(sample fleetTmuxInventorySample) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sample.PolledAt.IsZero() {
		sample.PolledAt = m.clock().UTC()
	}
	if sample.Sessions == nil {
		sample.Sessions = map[string]fleetTmuxLiveSession{}
	}
	if sample.Error == "" {
		sample.Succeeded = true
	}
	if !sample.Succeeded {
		m.inventoryError = sample.Error
		return
	}
	if sample.Succeeded {
		if m.hasCurrentInventory {
			m.previousInventory = m.currentInventory
			m.hasPreviousInventory = true
		}
		m.inventoryError = ""
		m.currentInventory = cloneFleetTmuxInventorySample(sample)
		m.hasCurrentInventory = true
		return
	}
}

func (m *fleetTmuxMonitor) recordMetricsSample(sample fleetTmuxMetricsSample) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sample.SampledAt.IsZero() {
		sample.SampledAt = m.clock().UTC()
	}
	if sample.Sessions == nil {
		sample.Sessions = map[string]fleetTmuxSessionMetrics{}
	}
	m.metrics = cloneFleetTmuxMetricsSample(sample)
}

func (m *fleetTmuxMonitor) refreshInventory(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, fleetTmuxProbeTimeout)
	defer cancel()

	// Fixed-format fields come first and the free-text session name last, so
	// a name containing the field separator still parses (the name is the
	// unsplit remainder).
	sessionsOut, err := m.tmuxOutput(cctx,
		"list-sessions", "-F",
		"#{session_created}"+fleetTmuxFieldSeparator+
			"#{session_windows}"+fleetTmuxFieldSeparator+
			"#{session_name}",
	)
	if err != nil {
		if tmuxEmptyServerError(err) {
			m.recordInventorySample(fleetTmuxInventorySample{
				PolledAt:  m.clock().UTC(),
				Sessions:  map[string]fleetTmuxLiveSession{},
				Succeeded: true,
			})
			return
		}
		m.recordInventorySample(fleetTmuxInventorySample{
			PolledAt:  m.clock().UTC(),
			Error:     err.Error(),
			Succeeded: false,
		})
		return
	}
	windowsOut, err := m.tmuxOutput(cctx,
		"list-windows", "-a", "-F",
		"#{session_name}"+fleetTmuxFieldSeparator+
			"#{window_id}"+fleetTmuxFieldSeparator+
			"#{window_index}"+fleetTmuxFieldSeparator+
			"#{window_name}"+fleetTmuxFieldSeparator+
			"#{window_activity}",
	)
	if err != nil {
		if tmuxEmptyServerError(err) {
			m.recordInventorySample(fleetTmuxInventorySample{
				PolledAt:  m.clock().UTC(),
				Sessions:  map[string]fleetTmuxLiveSession{},
				Succeeded: true,
			})
			return
		}
		m.recordInventorySample(fleetTmuxInventorySample{
			PolledAt:  m.clock().UTC(),
			Error:     err.Error(),
			Succeeded: false,
		})
		return
	}
	m.recordInventorySample(fleetTmuxInventorySample{
		PolledAt:  m.clock().UTC(),
		Sessions:  parseFleetTmuxInventory(string(sessionsOut), string(windowsOut)),
		Succeeded: true,
	})
}

func (m *fleetTmuxMonitor) refreshMetrics(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, fleetTmuxProbeTimeout)
	defer cancel()

	snap := m.snapshot()
	if snap.CurrentInventory == nil {
		return
	}
	if len(snap.CurrentInventory.Sessions) == 0 {
		m.recordMetricsSample(fleetTmuxMetricsSample{
			SampledAt: m.clock().UTC(),
			Sessions:  map[string]fleetTmuxSessionMetrics{},
		})
		return
	}
	managed := make(map[string]struct{}, len(snap.CurrentInventory.Sessions))
	for name := range snap.CurrentInventory.Sessions {
		managed[name] = struct{}{}
	}
	panesOut, err := m.tmuxOutput(cctx,
		"list-panes", "-a", "-F",
		"#{session_name}"+fleetTmuxFieldSeparator+
			"#{session_activity}"+fleetTmuxFieldSeparator+
			"#{pane_pid}"+fleetTmuxFieldSeparator+
			"#{pane_current_command}",
	)
	if err != nil {
		m.recordMetricsSample(fleetTmuxMetricsSample{
			SampledAt: m.clock().UTC(),
			Error:     err.Error(),
		})
		return
	}
	psCmd := procutil.CommandContext(
		cctx, "ps", "-ax", "-o", "pid=", "-o", "ppid=", "-o",
		"%cpu=", "-o", "rss=", "-o", "comm=",
	)
	processOut, err := procutil.Output(cctx, psCmd, "fleet process probe")
	if err != nil {
		m.recordMetricsSample(fleetTmuxMetricsSample{
			SampledAt: m.clock().UTC(),
			Error:     err.Error(),
		})
		return
	}
	sampledAt := m.clock().UTC()
	m.recordMetricsSample(fleetTmuxMetricsSample{
		SampledAt: sampledAt,
		Sessions: parseFleetTmuxMetrics(
			string(panesOut), string(processOut), managed, sampledAt,
		),
	})
}

func (m *fleetTmuxMonitor) run(ctx context.Context) {
	m.refreshInventory(ctx)
	m.refreshMetrics(ctx)

	inventoryTicker := time.NewTicker(4 * time.Second)
	defer inventoryTicker.Stop()
	metricsTicker := time.NewTicker(15 * time.Second)
	defer metricsTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-inventoryTicker.C:
			m.refreshInventory(ctx)
		case <-metricsTicker.C:
			m.refreshMetrics(ctx)
		}
	}
}

func (m *fleetTmuxMonitor) tmuxOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := m.tmuxCommand(ctx, args...)
	return procutil.Output(ctx, cmd, "fleet tmux probe")
}

func tmuxEmptyServerError(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	stderr := strings.ToLower(string(exitErr.Stderr))
	return strings.Contains(stderr, "no server running") ||
		strings.Contains(stderr, "no sessions") ||
		(strings.Contains(stderr, "error connecting to") &&
			strings.Contains(stderr, "no such file or directory"))
}

func (m *fleetTmuxMonitor) tmuxCommand(ctx context.Context, args ...string) *exec.Cmd {
	if len(m.tmuxCmd) == 0 {
		return procutil.CommandContext(ctx, "tmux", args...)
	}
	cmdArgs := make([]string, 0, len(m.tmuxCmd)-1+len(args))
	cmdArgs = append(cmdArgs, m.tmuxCmd[1:]...)
	cmdArgs = append(cmdArgs, args...)
	return procutil.CommandContext(ctx, m.tmuxCmd[0], cmdArgs...)
}

func parseFleetTmuxInventory(
	sessionOutput string,
	windowOutput string,
) map[string]fleetTmuxLiveSession {
	out := map[string]fleetTmuxLiveSession{}
	for _, line := range nonEmptyLines(sessionOutput) {
		fields := splitFleetTmuxFields(line, 3)
		if len(fields) != 3 || strings.TrimSpace(fields[2]) == "" {
			continue
		}
		// created|windows|name: the name is the unsplit remainder, so a
		// session name containing the separator parses intact.
		name := fields[2]
		createdAt, ok := parseTmuxEpoch(fields[0])
		if !ok {
			continue
		}
		windowCount, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil || windowCount < 0 {
			continue
		}
		out[name] = fleetTmuxLiveSession{
			Name:        name,
			CreatedAt:   &createdAt,
			Windows:     []fleet.TmuxWindowInfo{},
			WindowCount: windowCount,
		}
	}

	for _, line := range nonEmptyLines(windowOutput) {
		sessionName, window, ok := parseFleetTmuxWindowLine(line)
		if !ok {
			continue
		}
		session, ok := out[sessionName]
		if !ok {
			continue
		}
		session.Windows = append(session.Windows, window)
		if len(session.Windows) > session.WindowCount {
			session.WindowCount = len(session.Windows)
		}
		out[sessionName] = session
	}

	return out
}

// fleetTmuxWindowIDRe matches tmux window ids ("@12"), the fixed-format
// anchor that pins where a free-text session name ends in a window line.
var fleetTmuxWindowIDRe = regexp.MustCompile(`^@\d+$`)

// parseFleetTmuxWindowLine parses one list-windows line of the form
// session_name|window_id|window_index|window_name|activity. Session and
// window names are free text that may contain the field separator, so the
// fixed fields anchor the parse: the window id and index pin the left side
// and the trailing activity epoch pins the right; the name fields keep any
// separators they contain.
func parseFleetTmuxWindowLine(
	line string,
) (sessionName string, window fleet.TmuxWindowInfo, ok bool) {
	for _, sep := range []string{fleetTmuxFieldSeparator, "\t"} {
		parts := strings.Split(line, sep)
		if len(parts) < 5 {
			continue
		}
		activity, epochOK := parseTmuxEpoch(parts[len(parts)-1])
		if !epochOK {
			continue
		}
		for i := 1; i+3 < len(parts); i++ {
			id := strings.TrimSpace(parts[i])
			if !fleetTmuxWindowIDRe.MatchString(id) {
				continue
			}
			index, err := strconv.Atoi(strings.TrimSpace(parts[i+1]))
			if err != nil {
				continue
			}
			return strings.Join(parts[:i], sep), fleet.TmuxWindowInfo{
				ID:       id,
				Index:    index,
				Name:     strings.Join(parts[i+2:len(parts)-1], sep),
				Activity: activity.UTC().Format(time.RFC3339),
			}, true
		}
	}
	return "", fleet.TmuxWindowInfo{}, false
}

func parseFleetTmuxMetrics(
	paneOutput string,
	processOutput string,
	managedSessions map[string]struct{},
	sampledAt time.Time,
) map[string]fleetTmuxSessionMetrics {
	panes := parseFleetTmuxPanes(paneOutput, managedSessions)
	processes := parseFleetProcessTable(processOutput)
	children := map[int][]int{}
	for pid, proc := range processes {
		children[proc.PPID] = append(children[proc.PPID], pid)
	}

	out := map[string]fleetTmuxSessionMetrics{}
	rssKBBySession := map[string]int{}
	for _, pane := range panes {
		procIDs := collectProcessTree(pane.PID, children)
		metric := out[pane.Session]
		if pane.Activity != nil &&
			(metric.LastOutputAt == nil || pane.Activity.After(*metric.LastOutputAt)) {
			activity := pane.Activity.UTC()
			metric.LastOutputAt = &activity
		}
		if metric.ExecutableName == "" && pane.CurrentCommand != "" {
			metric.ExecutableName = pane.CurrentCommand
		}
		var paneCPU float64
		for _, pid := range procIDs {
			proc, ok := processes[pid]
			if !ok {
				continue
			}
			paneCPU += proc.CPUPercent
			rssKBBySession[pane.Session] += proc.RSSKB
			metric.ProcessCount++
			if metric.ExecutableName == "" && proc.Command != "" {
				metric.ExecutableName = proc.Command
			}
		}
		metric.CPUPercent += paneCPU
		if pane.Activity != nil {
			activeAt := pane.Activity.UTC()
			if metric.LastActiveAt == nil || activeAt.After(*metric.LastActiveAt) {
				metric.LastActiveAt = &activeAt
			}
		}
		out[pane.Session] = metric
	}
	for name, metric := range out {
		metric.ResidentMB = rssKBBySession[name] / 1024
		if metric.CPUPercent > fleetTmuxActivityCPUThresh {
			activeAt := sampledAt.UTC()
			metric.LastActiveAt = &activeAt
		}
		out[name] = metric
	}
	return out
}

func parseFleetTmuxPanes(
	output string,
	managedSessions map[string]struct{},
) []fleetTmuxPaneInfo {
	var panes []fleetTmuxPaneInfo
	for _, line := range nonEmptyLines(output) {
		pane, ok := parseFleetTmuxPaneLine(line)
		if !ok {
			continue
		}
		if _, ok := managedSessions[pane.Session]; !ok {
			continue
		}
		panes = append(panes, pane)
	}
	return panes
}

// parseFleetTmuxPaneLine parses one list-panes line of the form
// session_name|activity|pid|command. The session name is free text that may
// contain the field separator, so the activity epoch and pid anchor the
// parse and the name keeps any separators it contains.
func parseFleetTmuxPaneLine(line string) (fleetTmuxPaneInfo, bool) {
	for _, sep := range []string{fleetTmuxFieldSeparator, "\t"} {
		parts := strings.Split(line, sep)
		if len(parts) < 4 {
			continue
		}
		for i := 1; i+2 < len(parts); i++ {
			activity, epochOK := parseTmuxEpoch(parts[i])
			if !epochOK {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(parts[i+1]))
			if err != nil || pid <= 0 {
				continue
			}
			return fleetTmuxPaneInfo{
				Session:        strings.Join(parts[:i], sep),
				Activity:       &activity,
				PID:            pid,
				CurrentCommand: strings.TrimSpace(strings.Join(parts[i+2:], sep)),
			}, true
		}
	}
	return fleetTmuxPaneInfo{}, false
}

func splitFleetTmuxFields(line string, n int) []string {
	fields := strings.SplitN(line, fleetTmuxFieldSeparator, n)
	if len(fields) == n {
		return fields
	}
	return strings.SplitN(line, "\t", n)
}

func parseFleetProcessTable(output string) map[int]fleetProcessInfo {
	out := map[int]fleetProcessInfo{}
	for _, line := range nonEmptyLines(output) {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		rss, err := strconv.Atoi(fields[3])
		if err != nil || rss < 0 {
			continue
		}
		out[pid] = fleetProcessInfo{
			PID:        pid,
			PPID:       ppid,
			CPUPercent: cpu,
			RSSKB:      rss,
			Command:    strings.Join(fields[4:], " "),
		}
	}
	return out
}

func collectProcessTree(root int, children map[int][]int) []int {
	var out []int
	stack := []int{root}
	seen := map[int]struct{}{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
		stack = append(stack, children[pid]...)
	}
	return out
}

func parseTmuxEpoch(raw string) (time.Time, bool) {
	sec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0).UTC(), true
}

func nonEmptyLines(output string) []string {
	rawLines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func cloneFleetTmuxInventorySample(
	in fleetTmuxInventorySample,
) fleetTmuxInventorySample {
	out := in
	out.Sessions = make(map[string]fleetTmuxLiveSession, len(in.Sessions))
	for k, v := range in.Sessions {
		windows := slices.Clone(v.Windows)
		v.Windows = windows
		out.Sessions[k] = v
	}
	return out
}

func cloneFleetTmuxMetricsSample(
	in fleetTmuxMetricsSample,
) fleetTmuxMetricsSample {
	out := in
	out.Sessions = make(map[string]fleetTmuxSessionMetrics, len(in.Sessions))
	maps.Copy(out.Sessions, in.Sessions)
	return out
}
