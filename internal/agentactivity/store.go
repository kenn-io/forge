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
)

const RuntimeSessionKeyEnv = "MIDDLEMAN_RUNTIME_SESSION_KEY"

// ReportFreshness bounds how long a hook state can override tmux activity
// without another lifecycle event confirming it.
const ReportFreshness = 30 * time.Minute

type Report struct {
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

type hookInput struct {
	SessionID        string `json:"session_id"`
	CWD              string `json:"cwd"`
	HookEventName    string `json:"hook_event_name"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	AgentID          string `json:"agent_id"`
}

type Store struct {
	root string
	now  func() time.Time

	cacheMu         sync.Mutex
	cacheDirModTime time.Time
	cacheValidUntil time.Time
	cacheReports    []Report
}

func NewStore(root string) *Store {
	return &Store{root: root, now: time.Now}
}

func (s *Store) HandleHook(input io.Reader, runtimeSessionKey string) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil
	}
	var hook hookInput
	if err := json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&hook); err != nil {
		return fmt.Errorf("decode agent hook: %w", err)
	}
	if hook.AgentID != "" {
		return nil
	}
	hook.SessionID = strings.TrimSpace(hook.SessionID)
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	if hook.SessionID == "" || runtimeSessionKey == "" {
		return nil
	}

	state, remove, ok := stateForHook(hook)
	if !ok {
		return nil
	}
	if remove {
		err := os.Remove(s.reportPath(hook.SessionID))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			s.invalidateCache()
		}
		return err
	}

	cwd, err := filepath.Abs(strings.TrimSpace(hook.CWD))
	if err != nil || strings.TrimSpace(hook.CWD) == "" {
		return nil
	}
	report := Report{
		SessionID:         hook.SessionID,
		RuntimeSessionKey: runtimeSessionKey,
		CWD:               filepath.Clean(cwd),
		State:             state,
		UpdatedAt:         s.now().UTC(),
	}
	return s.writeReport(report)
}

func (s *Store) SnapshotForWorkspace(cwd string, liveSessionKeys []string) (Snapshot, bool) {
	if s == nil || strings.TrimSpace(s.root) == "" || len(liveSessionKeys) == 0 {
		return Snapshot{}, false
	}
	absCWD, err := filepath.Abs(strings.TrimSpace(cwd))
	if err != nil || strings.TrimSpace(cwd) == "" {
		return Snapshot{}, false
	}
	live := make(map[string]struct{}, len(liveSessionKeys))
	for _, key := range liveSessionKeys {
		if key = strings.TrimSpace(key); key != "" {
			live[key] = struct{}{}
		}
	}
	if len(live) == 0 {
		return Snapshot{}, false
	}

	target := filepath.Clean(absCWD)
	var reports []Report
	for _, report := range s.reports() {
		if filepath.Clean(report.CWD) != target {
			continue
		}
		if _, ok := live[report.RuntimeSessionKey]; !ok {
			continue
		}
		reports = append(reports, report)
	}
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
		if err := os.Remove(s.reportPath(report.SessionID)); err != nil &&
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

	info, err := os.Stat(s.root)
	if err != nil {
		s.clearCacheLocked()
		return nil
	}
	now := s.now().UTC()
	if info.ModTime().Equal(s.cacheDirModTime) &&
		(s.cacheValidUntil.IsZero() || now.Before(s.cacheValidUntil)) {
		return slices.Clone(s.cacheReports)
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		s.clearCacheLocked()
		return nil
	}
	reports := make([]Report, 0, len(entries))
	validUntil := time.Time{}
	removed := false
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
		expiresAt := report.UpdatedAt.Add(ReportFreshness)
		if !now.Before(expiresAt) {
			if removeErr := os.Remove(path); removeErr == nil ||
				errors.Is(removeErr, os.ErrNotExist) {
				removed = true
			} else {
				cleanupPending = true
			}
			continue
		}
		if validUntil.IsZero() || expiresAt.Before(validUntil) {
			validUntil = expiresAt
		}
		reports = append(reports, report)
	}
	if removed {
		if refreshed, statErr := os.Stat(s.root); statErr == nil {
			info = refreshed
		}
	}
	s.cacheDirModTime = info.ModTime()
	if cleanupPending {
		s.cacheDirModTime = time.Time{}
	}
	s.cacheValidUntil = validUntil
	s.cacheReports = slices.Clone(reports)
	return reports
}

func (s *Store) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.clearCacheLocked()
}

func (s *Store) clearCacheLocked() {
	s.cacheDirModTime = time.Time{}
	s.cacheValidUntil = time.Time{}
	s.cacheReports = nil
}

func stateForHook(input hookInput) (State, bool, bool) {
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
		case "idle_prompt", "elicitation_dialog":
			return StateInput, false, true
		default:
			return "", false, false
		}
	case "Stop", "Interrupt":
		return StateIdle, false, true
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
		return 4
	case StateInput:
		return 3
	case StateWorking:
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
	if err := os.Rename(tmpPath, s.reportPath(report.SessionID)); err != nil {
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
	return report, true
}

func (s *Store) reportPath(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}
