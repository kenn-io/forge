package fleetapi

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

const (
	fleetTmuxProbeTimeout      = 750 * time.Millisecond
	fleetTmuxStaleThreshold    = 30 * time.Second
	fleetTmuxActivityCPUThresh = 0.1
	// fleetTmuxPollInterval paces one monitor pass: a session listing, a
	// window listing when the session listing moved, and process metrics for
	// the panes of managed sessions.
	fleetTmuxPollInterval = 15 * time.Second
	// fleetTmuxWindowRefreshInterval bounds how long a byte-identical session
	// listing may reuse the previous window inventory, so window renames and
	// activity stamps still converge without a per-pass list-windows spawn.
	fleetTmuxWindowRefreshInterval = 60 * time.Second
	// fleetTmuxProcessTreeDepth bounds how many generations below a pane's
	// process the metrics probe follows.
	fleetTmuxProcessTreeDepth = 6
	// tmux 3.5a renders literal tabs in -F output as underscores, so use a
	// printable separator the real command preserves.
	fleetTmuxFieldSeparator = "|"
)

type fleetTmuxMonitor struct {
	mu                      sync.RWMutex
	tmuxCmd                 []string
	includeUnmanagedDetails bool
	clock                   func() time.Time
	probeTimeout            time.Duration
	gate                    *fleetMonitorGate

	// lastSessionsOutput is the raw list-sessions output of the last
	// successful inventory pass and lastWindowsAt when list-windows last ran;
	// together they decide whether a pass may reuse the previous windows.
	lastSessionsOutput []byte
	lastWindowsAt      time.Time

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
	hasSubscribers func() bool,
) *fleetTmuxMonitor {
	if clock == nil {
		clock = time.Now
	}
	if len(tmuxCmd) == 0 {
		tmuxCmd = config.DefaultTmuxCommand()
	}
	return &fleetTmuxMonitor{
		tmuxCmd:                 slices.Clone(tmuxCmd),
		includeUnmanagedDetails: includeUnmanagedDetails,
		clock:                   clock,
		probeTimeout:            fleetTmuxProbeTimeout,
		gate:                    newFleetMonitorGate("tmux", hasSubscribers),
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
	cctx, cancel := context.WithTimeout(ctx, m.probeTimeout)
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
		m.forgetSessionsOutput()
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
	if previousWindows, ok := m.reusableWindows(sessionsOut); ok {
		sessions := parseFleetTmuxInventory(string(sessionsOut), "")
		for name, session := range sessions {
			if prior, ok := previousWindows[name]; ok {
				session.Windows = slices.Clone(prior.Windows)
				session.WindowCount = max(session.WindowCount, len(session.Windows))
				sessions[name] = session
			}
		}
		m.recordInventorySample(fleetTmuxInventorySample{
			PolledAt:  m.clock().UTC(),
			Sessions:  sessions,
			Succeeded: true,
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
		m.forgetSessionsOutput()
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
	m.rememberSessionsOutput(sessionsOut)
	m.recordInventorySample(fleetTmuxInventorySample{
		PolledAt:  m.clock().UTC(),
		Sessions:  parseFleetTmuxInventory(string(sessionsOut), string(windowsOut)),
		Succeeded: true,
	})
}

// reusableWindows reports whether the session listing is byte-identical to the
// one that produced the current inventory and the window listing is recent
// enough to reuse; when so it returns the current inventory's sessions so the
// caller can carry their windows forward without a list-windows spawn.
func (m *fleetTmuxMonitor) reusableWindows(
	sessionsOut []byte,
) (map[string]fleetTmuxLiveSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.hasCurrentInventory || m.lastSessionsOutput == nil ||
		!bytes.Equal(m.lastSessionsOutput, sessionsOut) ||
		m.clock().Sub(m.lastWindowsAt) >= fleetTmuxWindowRefreshInterval {
		return nil, false
	}
	return cloneFleetTmuxInventorySample(m.currentInventory).Sessions, true
}

func (m *fleetTmuxMonitor) rememberSessionsOutput(sessionsOut []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSessionsOutput = bytes.Clone(sessionsOut)
	m.lastWindowsAt = m.clock()
}

func (m *fleetTmuxMonitor) forgetSessionsOutput() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSessionsOutput = nil
	m.lastWindowsAt = time.Time{}
}

func (m *fleetTmuxMonitor) refreshMetrics(ctx context.Context) {
	// The pane listing plus a ps and pgrep per process generation can add up
	// to a dozen short spawns, so the metrics pass gets a wider budget than a
	// single tmux probe.
	cctx, cancel := context.WithTimeout(ctx, m.probeTimeout*4)
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
	panes := parseFleetTmuxPanes(string(panesOut), managed)
	processes, err := probeFleetProcessTrees(cctx, panePIDs(panes))
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
		Sessions:  aggregateFleetTmuxMetrics(panes, processes, sampledAt),
	})
}

// refresh runs one monitor pass: the session inventory, then process metrics
// for managed panes.
func (m *fleetTmuxMonitor) refresh(ctx context.Context) {
	m.refreshInventory(ctx)
	m.refreshMetrics(ctx)
}

func (m *fleetTmuxMonitor) run(ctx context.Context) {
	runFleetMonitorLoop(ctx, fleetTmuxPollInterval, m.gate, m.refresh)
}

func panePIDs(panes []fleetTmuxPaneInfo) []int {
	pids := make([]int, 0, len(panes))
	for _, pane := range panes {
		if pane.PID > 0 && !slices.Contains(pids, pane.PID) {
			pids = append(pids, pane.PID)
		}
	}
	return pids
}

// probeFleetProcessTrees builds a process table covering only the given pane
// processes and their descendants, instead of enumerating the whole host. Each
// generation costs one ps for the known pids and one pgrep for their children;
// the walk stops when a generation has no children or the depth bound is hit.
// An empty pid set spawns nothing.
func probeFleetProcessTrees(
	ctx context.Context, roots []int,
) (map[int]fleetProcessInfo, error) {
	processes := map[int]fleetProcessInfo{}
	pending := slices.Clone(roots)
	for depth := 0; len(pending) > 0 && depth <= fleetTmuxProcessTreeDepth; depth++ {
		list := joinPIDs(pending)
		psOut, err := fleetProcessProbeOutput(ctx, "fleet process probe",
			"ps", "-o", "pid=,ppid=,%cpu=,rss=,comm=", "-p", list)
		if err != nil {
			return nil, err
		}
		maps.Copy(processes, parseFleetProcessTable(string(psOut)))
		childrenOut, err := fleetProcessProbeOutput(ctx, "fleet process children probe",
			"pgrep", "-P", list)
		if err != nil {
			return nil, err
		}
		pending = pending[:0]
		for field := range strings.FieldsSeq(string(childrenOut)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				continue
			}
			if _, known := processes[pid]; known || slices.Contains(pending, pid) {
				continue
			}
			pending = append(pending, pid)
		}
	}
	return processes, nil
}

// fleetProcessProbeOutput runs one ps or pgrep query through the process
// limiter. Both tools exit non-zero when no listed pid exists, which is a
// normal outcome for a pane whose process just exited, so a clean exit
// status failure yields whatever they printed rather than an error.
func fleetProcessProbeOutput(
	ctx context.Context, reason, name string, args ...string,
) ([]byte, error) {
	cmd := procutil.CommandContext(ctx, name, args...)
	out, err := procutil.Output(ctx, cmd, reason)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && ctx.Err() == nil {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

func joinPIDs(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	return strings.Join(parts, ",")
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
	command := m.tmuxCmd
	if len(command) == 0 {
		command = config.DefaultTmuxCommand()
	}
	cmdArgs := make([]string, 0, len(command)-1+len(args))
	cmdArgs = append(cmdArgs, command[1:]...)
	cmdArgs = append(cmdArgs, args...)
	cmd := procutil.CommandContext(ctx, command[0], cmdArgs...)
	cmd.Env = localruntime.TmuxClientEnvironment(os.Environ(), nil)
	return cmd
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

// aggregateFleetTmuxMetrics folds a process table into per-session metrics,
// attributing each pane's process and every descendant present in the table
// to the pane's session.
func aggregateFleetTmuxMetrics(
	panes []fleetTmuxPaneInfo,
	processes map[int]fleetProcessInfo,
	sampledAt time.Time,
) map[string]fleetTmuxSessionMetrics {
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
