package agentactivity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
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
	cacheReports []Report
}

func NewStore(root string) *Store {
	return &Store{root: root, now: time.Now}
}

func (s *Store) HandleHook(agent string, input io.Reader, runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	var hook HookEvent
	if err := json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&hook); err != nil {
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
		err := os.Remove(s.reportPath(agent, hook.SessionID))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			s.invalidateCache()
		}
		return err
	}

	cwd, err := canonicalWorkspacePath(hook.CWD)
	if err != nil {
		return nil
	}
	report := Report{
		Agent:             agent,
		SessionID:         hook.SessionID,
		RuntimeSessionKey: runtimeSessionKey,
		CWD:               cwd,
		State:             state,
		UpdatedAt:         s.now().UTC(),
	}
	if state == StateDone {
		previous, ok := s.readReport(s.reportPath(agent, hook.SessionID))
		if ok && previous.RuntimeSessionKey == runtimeSessionKey {
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

	reports := make([]Report, 0)
	for _, report := range s.reports() {
		if report.CWD != target {
			continue
		}
		if _, ok := live[report.RuntimeSessionKey]; !ok {
			continue
		}
		reports = append(reports, report)
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
	var errs []error
	for _, report := range s.reports() {
		if report.RuntimeSessionKey != runtimeSessionKey {
			continue
		}
		if err := os.Remove(s.reportPath(report.Agent, report.SessionID)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	s.invalidateCache()
	return errors.Join(errs...)
}

func (s *Store) reports() []Report {
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

	reports := make([]Report, 0, len(entries))
	cleanupPending := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		report, ok := s.readReport(path)
		if !ok {
			continue
		}
		report.Agent = strings.ToLower(strings.TrimSpace(report.Agent))
		if report.Agent == "" {
			if removeErr := os.Remove(path); removeErr == nil ||
				errors.Is(removeErr, os.ErrNotExist) {
				delete(files, entry.Name())
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
	tmp, err := os.CreateTemp(s.root, ".agent-activity-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.reportPath(report.Agent, report.SessionID)); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) readReport(path string) (Report, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, false
	}
	defer file.Close()
	var report Report
	if err := json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&report); err != nil {
		return Report{}, false
	}
	if statePriority(report.State) == 0 || report.RuntimeSessionKey == "" ||
		report.CWD == "" || report.UpdatedAt.IsZero() {
		return Report{}, false
	}
	cwd, err := canonicalWorkspacePath(report.CWD)
	if err != nil {
		return Report{}, false
	}
	report.CWD = cwd
	return report, true
}

func (s *Store) reportPath(agent string, sessionID string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + sessionID))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}
