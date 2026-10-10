package agentactivity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.kenn.io/kit/atomicfile"
)

type State string

const (
	StateIdle     State = "idle"
	StateWorking  State = "working"
	StateInput    State = "input"
	StateApproval State = "approval"
	StateDone     State = "done"
)

const RuntimeSessionKeyEnv = "KENN_FORGE_RUNTIME_SESSION_KEY"

type Report struct {
	Agent             string    `json:"agent"`
	SessionID         string    `json:"session_id"`
	RuntimeSessionKey string    `json:"runtime_session_key"`
	CWD               string    `json:"cwd"`
	State             State     `json:"state"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Snapshot struct {
	State     State
	UpdatedAt time.Time
}

type storedReport struct {
	Report
	path string
}

// HookEvent is the agent-neutral lifecycle payload shared by hook integrations.
// Agent-specific payload fields are ignored unless they affect activity state.
type HookEvent struct {
	SessionID        string   `json:"session_id"`
	CWD              string   `json:"cwd"`
	HookEventName    string   `json:"hook_event_name"`
	ToolName         string   `json:"tool_name,omitempty"`
	NotificationType string   `json:"notification_type,omitempty"`
	AgentID          string   `json:"agent_id,omitempty"`
	_                struct{} `json:"-" additionalProperties:"true"`
}

type Store struct {
	root string
	now  func() time.Time

	cacheMu      sync.Mutex
	cacheFiles   map[string]os.FileInfo
	cacheReports []storedReport
}

func NewStore(root string) *Store {
	return &Store{root: root, now: time.Now}
}

func (s *Store) HandleHook(agent string, input io.Reader, runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	var hook HookEvent
	if err := json.UnmarshalRead(io.LimitReader(input, 1<<20), &hook); err != nil {
		return fmt.Errorf("decode agent hook: %w", err)
	}
	return s.HandleEvent(agent, hook, runtimeSessionKey)
}

// HandleEvent records one decoded lifecycle event for a launched runtime
// session. Events that do not map to a visible activity transition are ignored.
func (s *Store) HandleEvent(agent string, hook HookEvent, runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	if hook.AgentID != "" {
		return nil
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	hook.SessionID = strings.TrimSpace(hook.SessionID)
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	if agent == "" || hook.SessionID == "" || runtimeSessionKey == "" {
		return nil
	}

	state, remove, ok := stateForHook(hook)
	if !ok {
		return nil
	}
	if remove {
		return s.Remove(agent, hook.SessionID, runtimeSessionKey)
	}

	cwd, err := canonicalWorkspacePath(hook.CWD)
	if err == nil {
		report := Report{
			Agent:             agent,
			SessionID:         hook.SessionID,
			RuntimeSessionKey: runtimeSessionKey,
			CWD:               cwd,
			State:             state,
			UpdatedAt:         s.now().UTC(),
		}
		if state == StateDone {
			previous, ok := s.previousReport(agent, hook.SessionID, runtimeSessionKey)
			if ok {
				switch {
				case isIdlePrompt(hook) && (previous.State == StateInput ||
					previous.State == StateApproval):
					// Claude Code raises idle_prompt after a minute of waiting for
					// input whatever it is waiting for; an unanswered question or
					// permission prompt is still pending, not finished.
					return nil
				case previous.State == StateDone:
					// A completion that is already recorded keeps its original
					// timestamp: idle_prompt follows Stop, and a fresh timestamp
					// would make an acknowledged "done" reappear as new.
					report.UpdatedAt = previous.UpdatedAt
				}
			}
		}
		return s.writeReport(report)
	}
	return nil
}

// Record stores state reported directly by a runtime protocol rather than by a
// hook, such as an ACP connection. A repeated completion keeps its original
// timestamp so an acknowledged done does not reappear as new.
func (s *Store) Record(agent, sessionID, runtimeSessionKey, cwd string, state State) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	sessionID = strings.TrimSpace(sessionID)
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	if agent == "" || sessionID == "" || runtimeSessionKey == "" {
		return nil
	}
	if statePriority(state) == 0 {
		return fmt.Errorf("unknown agent activity state %q", state)
	}
	canonicalCWD, err := canonicalWorkspacePath(cwd)
	if err != nil {
		return err
	}
	report := Report{
		Agent: agent, SessionID: sessionID, RuntimeSessionKey: runtimeSessionKey,
		CWD: canonicalCWD, State: state, UpdatedAt: s.now().UTC(),
	}
	if state == StateDone {
		previous, ok := s.previousReport(agent, sessionID, runtimeSessionKey)
		if ok && previous.State == StateDone {
			report.UpdatedAt = previous.UpdatedAt
		}
	}
	return s.writeReport(report)
}

// Remove deletes one session's report when its runtime protocol ends it.
func (s *Store) Remove(agent, sessionID, runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	sessionID = strings.TrimSpace(sessionID)
	if agent == "" || sessionID == "" {
		return nil
	}
	legacy := s.legacyReportPath(agent, sessionID)
	paths := []string{s.reportPath(agent, sessionID, runtimeSessionKey)}
	var errs []error
	report, ok, err := s.readReport(legacy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	} else if ok && report.RuntimeSessionKey == runtimeSessionKey {
		paths = append(paths, legacy)
	}
	removed := false
	for _, path := range paths {
		err := os.Remove(path)
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, err)
		}
	}
	if removed {
		s.invalidateCache()
	}
	return errors.Join(errs...)
}

func isIdlePrompt(hook HookEvent) bool {
	return hook.HookEventName == "Notification" && hook.NotificationType == "idle_prompt"
}

func (s *Store) SnapshotForWorkspace(cwd string, liveSessionKeys []string) (Snapshot, bool) {
	reports := s.LiveReportsForWorkspace(cwd, liveSessionKeys)
	if len(reports) == 0 {
		return Snapshot{}, false
	}
	slices.SortFunc(reports, func(a, b Report) int {
		if priority := statePriority(b.State) - statePriority(a.State); priority != 0 {
			return priority
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return Snapshot{State: reports[0].State, UpdatedAt: reports[0].UpdatedAt}, true
}

// LiveReportsForWorkspace returns reports whose canonical worktree and
// runtime-session key match the supplied live inventory, newest first. A report lives until
// its session ends or its runtime session is removed; there is no time-based
// expiry, because a launched agent keeps reporting until it is torn down and
// its hook state must not lapse back to weaker signals in between.
func (s *Store) LiveReportsForWorkspace(cwd string, liveSessionKeys []string) []Report {
	if s == nil || strings.TrimSpace(s.root) == "" || len(liveSessionKeys) == 0 {
		return nil
	}
	target, err := canonicalWorkspacePath(cwd)
	if err != nil {
		return nil
	}
	live := make(map[string]struct{}, len(liveSessionKeys))
	for _, key := range liveSessionKeys {
		if key = strings.TrimSpace(key); key != "" {
			live[key] = struct{}{}
		}
	}
	if len(live) == 0 {
		return nil
	}

	// Reports saved before terminals had their own name can sit beside a newer copy, and an agent started before the upgrade keeps writing the old one.
	type identity struct{ agent, session, runtime string }
	newest := make(map[identity]int)
	reports := make([]Report, 0)
	for _, report := range s.reports() {
		if report.CWD != target {
			continue
		}
		if _, ok := live[report.RuntimeSessionKey]; !ok {
			continue
		}
		id := identity{report.Agent, report.SessionID, report.RuntimeSessionKey}
		if i, ok := newest[id]; ok {
			if report.UpdatedAt.After(reports[i].UpdatedAt) {
				reports[i] = report.Report
			}
			continue
		}
		newest[id] = len(reports)
		reports = append(reports, report.Report)
	}
	slices.SortFunc(reports, func(a, b Report) int {
		if order := b.UpdatedAt.Compare(a.UpdatedAt); order != 0 {
			return order
		}
		if order := strings.Compare(a.Agent, b.Agent); order != 0 {
			return order
		}
		if order := strings.Compare(a.SessionID, b.SessionID); order != 0 {
			return order
		}
		return strings.Compare(a.RuntimeSessionKey, b.RuntimeSessionKey)
	})
	return reports
}

func canonicalWorkspacePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(clean); resolveErr == nil {
		return filepath.Clean(resolved), nil
	}
	return clean, nil
}

// RemoveRuntimeSession removes every agent report owned by one launched
// runtime session.
func (s *Store) RemoveRuntimeSession(runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	if runtimeSessionKey == "" {
		return nil
	}
	reports, errs := s.scanReports()
	for _, report := range reports {
		if report.RuntimeSessionKey != runtimeSessionKey {
			continue
		}
		if err := os.Remove(report.path); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	s.invalidateCache()
	return errors.Join(errs...)
}

// RetainRuntimeSessions removes every report whose runtime session key is not
// in keep. Startup pruning and the periodic missing-tmux prune delete runtime
// rows without an exit hook, so this is how their reports follow them.
func (s *Store) RetainRuntimeSessions(keep map[string]struct{}) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	reports, errs := s.scanReports()
	removed := false
	for _, report := range reports {
		if _, ok := keep[report.RuntimeSessionKey]; ok {
			continue
		}
		err := os.Remove(report.path)
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, err)
		}
	}
	if removed {
		s.invalidateCache()
	}
	return errors.Join(errs...)
}

func (s *Store) scanReports() ([]storedReport, []error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	return s.loadReports(entries)
}

func (s *Store) loadReports(entries []os.DirEntry) ([]storedReport, []error) {
	var reports []storedReport
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		report, ok, err := s.readReport(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("read activity report %s: %w", path, err))
		}
		if ok {
			reports = append(reports, storedReport{Report: report, path: path})
		}
	}
	return reports, errs
}

func (s *Store) reports() []storedReport {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		s.clearCacheLocked()
		return nil
	}
	files := make(map[string]os.FileInfo)
	metadataComplete := true
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			metadataComplete = false
			continue
		}
		files[entry.Name()] = info
	}
	if metadataComplete && sameReportFiles(files, s.cacheFiles) {
		return slices.Clone(s.cacheReports)
	}

	loaded, _ := s.loadReports(entries)
	reports := make([]storedReport, 0, len(loaded))
	cleanupPending := false
	for _, report := range loaded {
		report.Agent = strings.ToLower(strings.TrimSpace(report.Agent))
		if report.Agent == "" {
			if removeErr := os.Remove(report.path); removeErr == nil ||
				errors.Is(removeErr, os.ErrNotExist) {
				delete(files, filepath.Base(report.path))
			} else {
				cleanupPending = true
			}
			continue
		}
		reports = append(reports, report)
	}
	s.cacheFiles = files
	if cleanupPending || !metadataComplete {
		s.cacheFiles = nil
	}
	s.cacheReports = slices.Clone(reports)
	return reports
}

func (s *Store) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.clearCacheLocked()
}

func (s *Store) clearCacheLocked() {
	s.cacheFiles = nil
	s.cacheReports = nil
}

func sameReportFiles(current, cached map[string]os.FileInfo) bool {
	if cached == nil || len(current) != len(cached) {
		return false
	}
	for name, currentInfo := range current {
		cachedInfo, ok := cached[name]
		if !ok || !os.SameFile(currentInfo, cachedInfo) ||
			currentInfo.Size() != cachedInfo.Size() ||
			!currentInfo.ModTime().Equal(cachedInfo.ModTime()) {
			return false
		}
	}
	return true
}

func stateForHook(input HookEvent) (State, bool, bool) {
	switch input.HookEventName {
	case "SessionStart":
		return StateIdle, false, true
	case "UserPromptSubmit":
		return StateWorking, false, true
	case "PreToolUse":
		if isUserInputTool(input.ToolName) {
			return StateInput, false, true
		}
		return StateWorking, false, true
	case "PostToolUse", "PostToolUseFailure", "PreCompact", "PostCompact":
		return StateWorking, false, true
	case "PermissionRequest":
		return StateApproval, false, true
	case "Notification":
		switch input.NotificationType {
		case "permission_prompt":
			return StateApproval, false, true
		case "elicitation_dialog":
			return StateInput, false, true
		case "idle_prompt":
			// Claude Code raises idle_prompt about a minute after a turn
			// ends with nothing pending. It follows Stop and would otherwise
			// flip a finished session from done to input.
			return StateDone, false, true
		default:
			return "", false, false
		}
	case "Stop", "Interrupt":
		return StateDone, false, true
	case "SessionEnd":
		return "", true, true
	default:
		return "", false, false
	}
}

func isUserInputTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "askuserquestion", "request_user_input", "tool_user_input":
		return true
	default:
		return false
	}
}

func statePriority(state State) int {
	switch state {
	case StateApproval:
		return 5
	case StateInput:
		return 4
	case StateWorking:
		return 3
	case StateDone:
		return 2
	case StateIdle:
		return 1
	default:
		return 0
	}
}

func (s *Store) writeReport(report Report) error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	// Reports are rewritten on every activity event, so skip fsync as
	// before. ErrPublished means the report is already visible.
	err = atomicfile.WriteFile(s.reportPath(report.Agent, report.SessionID, report.RuntimeSessionKey), data, atomicfile.WithoutSync())
	if err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) readReport(path string) (Report, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return Report{}, false, err
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, false, nil
	}
	if statePriority(report.State) == 0 || report.RuntimeSessionKey == "" ||
		report.CWD == "" || report.UpdatedAt.IsZero() {
		return Report{}, false, nil
	}
	cwd, err := canonicalWorkspacePath(report.CWD)
	if err != nil {
		return Report{}, false, nil
	}
	report.CWD = cwd
	return report, true, nil
}

// previousReport returns the newest report for one terminal, including one saved under its name from before terminals had their own.
func (s *Store) previousReport(agent, sessionID, runtimeSessionKey string) (Report, bool) {
	report, ok, _ := s.readReport(s.reportPath(agent, sessionID, runtimeSessionKey))
	legacy, legacyOK, _ := s.readReport(s.legacyReportPath(agent, sessionID))
	if legacyOK && legacy.RuntimeSessionKey == runtimeSessionKey &&
		(!ok || legacy.UpdatedAt.After(report.UpdatedAt)) {
		return legacy, true
	}
	return report, ok
}

func (s *Store) legacyReportPath(agent, sessionID string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + sessionID))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}

func (s *Store) reportPath(agent, sessionID, runtimeSessionKey string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + sessionID + "\x00" + runtimeSessionKey))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}
