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

const (
	maxHookInputBytes = 1 << 20
	maxReportBytes    = 64 << 10
)

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
	cacheFiles      map[string]os.FileInfo
	cacheValidUntil time.Time
	cacheReports    []Report
}

func NewStore(root string) *Store {
	return &Store{root: root, now: time.Now}
}

// StateDir names the report directory under a middleman data dir. Installed
// hooks and the server must agree on it, so both derive it from here.
func StateDir(dataDir string) string {
	return filepath.Join(dataDir, "agent-activity")
}

// enabled reports whether the store has a state directory to read and write.
// Callers hold a nil store when agent activity is not configured.
func (s *Store) enabled() bool {
	return s != nil && strings.TrimSpace(s.root) != ""
}

func (s *Store) HandleHook(input io.Reader, runtimeSessionKey string) error {
	if !s.enabled() {
		return nil
	}
	var hook hookInput
	if err := json.NewDecoder(io.LimitReader(input, maxHookInputBytes)).Decode(&hook); err != nil {
		return fmt.Errorf("decode agent hook: %w", err)
	}
	// Only the top-level session reports workspace state; subagent payloads
	// carry an agent ID and would otherwise overwrite it.
	if hook.AgentID != "" {
		return nil
	}
	sessionID := strings.TrimSpace(hook.SessionID)
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	if sessionID == "" || runtimeSessionKey == "" {
		return nil
	}

	outcome, state := hookEventOutcome(hook)
	switch outcome {
	case hookIgnored:
		return nil
	case hookEndsSession:
		return s.removeReport(sessionID)
	}

	cwd, err := canonicalWorkspacePath(hook.CWD)
	if err != nil {
		return nil
	}
	return s.writeReport(Report{
		SessionID:         sessionID,
		RuntimeSessionKey: runtimeSessionKey,
		CWD:               cwd,
		State:             state,
		UpdatedAt:         s.now().UTC(),
	})
}

// SnapshotForWorkspace returns the most urgent state reported by any live
// runtime session working in cwd.
func (s *Store) SnapshotForWorkspace(cwd string, liveSessionKeys []string) (Snapshot, bool) {
	if !s.enabled() {
		return Snapshot{}, false
	}
	target, err := canonicalWorkspacePath(cwd)
	if err != nil {
		return Snapshot{}, false
	}
	live := liveSessionKeySet(liveSessionKeys)
	if len(live) == 0 {
		return Snapshot{}, false
	}

	var reports []Report
	for _, report := range s.reports() {
		if report.CWD != target {
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
	best := slices.MaxFunc(reports, compareReportUrgency)
	return Snapshot{State: best.State, UpdatedAt: best.UpdatedAt}, true
}

// RemoveRuntimeSession removes every agent report owned by one launched
// runtime session.
func (s *Store) RemoveRuntimeSession(runtimeSessionKey string) error {
	if !s.enabled() {
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

func liveSessionKeySet(keys []string) map[string]struct{} {
	live := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			live[key] = struct{}{}
		}
	}
	return live
}

// compareReportUrgency orders reports by state priority, then by recency, so
// an approval prompt outranks background work from another session.
func compareReportUrgency(a, b Report) int {
	if priority := statePriority(a.State) - statePriority(b.State); priority != 0 {
		return priority
	}
	return a.UpdatedAt.Compare(b.UpdatedAt)
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

// reportDirListing is one view of the state directory: the report files to read
// plus the metadata used to notice writes from other middleman processes.
type reportDirListing struct {
	names    []string
	metadata map[string]os.FileInfo
	// metadataComplete is false when a file's metadata could not be read, which
	// makes the cache unusable because changes to it would go unnoticed.
	metadataComplete bool
}

// reportScan is the result of reading every report file in one listing.
type reportScan struct {
	reports []Report
	// validUntil is when the earliest-expiring report goes stale.
	validUntil time.Time
	// cleanupComplete is false when an expired report could not be deleted.
	cleanupComplete bool
}

func (s *Store) reports() []Report {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	listing, ok := s.listReportDir()
	if !ok {
		s.clearCacheLocked()
		return nil
	}
	now := s.now().UTC()
	if listing.metadataComplete && sameReportFiles(listing.metadata, s.cacheFiles) &&
		(s.cacheValidUntil.IsZero() || now.Before(s.cacheValidUntil)) {
		return slices.Clone(s.cacheReports)
	}

	scan := s.scanReports(listing, now)
	s.cacheFiles = listing.metadata
	if !scan.cleanupComplete || !listing.metadataComplete {
		s.cacheFiles = nil
	}
	s.cacheValidUntil = scan.validUntil
	s.cacheReports = slices.Clone(scan.reports)
	return scan.reports
}

func (s *Store) listReportDir() (reportDirListing, bool) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return reportDirListing{}, false
	}
	listing := reportDirListing{
		metadata:         make(map[string]os.FileInfo),
		metadataComplete: true,
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		listing.names = append(listing.names, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			listing.metadataComplete = false
			continue
		}
		listing.metadata[entry.Name()] = info
	}
	return listing, true
}

// scanReports reads every listed report, deleting the ones that have expired.
// Deleted names are dropped from listing.metadata so it still describes the
// directory once the scan finishes.
func (s *Store) scanReports(listing reportDirListing, now time.Time) reportScan {
	scan := reportScan{
		reports:         make([]Report, 0, len(listing.names)),
		cleanupComplete: true,
	}
	for _, name := range listing.names {
		path := filepath.Join(s.root, name)
		report, ok := s.readReport(path)
		if !ok {
			continue
		}
		expiresAt := report.UpdatedAt.Add(ReportFreshness)
		if !now.Before(expiresAt) {
			if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(listing.metadata, name)
			} else {
				scan.cleanupComplete = false
			}
			continue
		}
		if scan.validUntil.IsZero() || expiresAt.Before(scan.validUntil) {
			scan.validUntil = expiresAt
		}
		scan.reports = append(scan.reports, report)
	}
	return scan
}

func (s *Store) invalidateCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.clearCacheLocked()
}

func (s *Store) clearCacheLocked() {
	s.cacheFiles = nil
	s.cacheValidUntil = time.Time{}
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

// hookOutcome says what a hook event means for the session's stored report.
type hookOutcome int

const (
	// hookIgnored covers events that carry no state signal.
	hookIgnored hookOutcome = iota
	hookReportsState
	hookEndsSession
)

// hookEventStates holds the events whose state does not depend on the payload.
var hookEventStates = map[string]State{
	"SessionStart":       StateIdle,
	"UserPromptSubmit":   StateWorking,
	"PostToolUse":        StateWorking,
	"PostToolUseFailure": StateWorking,
	"PreCompact":         StateWorking,
	"PostCompact":        StateWorking,
	"PermissionRequest":  StateApproval,
	"Stop":               StateIdle,
	"Interrupt":          StateIdle,
}

// notificationStates holds the notification kinds that mean the agent is
// blocked on the user. Other notifications say nothing about the session.
var notificationStates = map[string]State{
	"permission_prompt":  StateApproval,
	"idle_prompt":        StateInput,
	"elicitation_dialog": StateInput,
}

func hookEventOutcome(input hookInput) (hookOutcome, State) {
	switch input.HookEventName {
	case "SessionEnd":
		return hookEndsSession, ""
	case "PreToolUse":
		if isUserInputTool(input.ToolName) {
			return hookReportsState, StateInput
		}
		return hookReportsState, StateWorking
	case "Notification":
		if state, ok := notificationStates[input.NotificationType]; ok {
			return hookReportsState, state
		}
		return hookIgnored, ""
	}
	if state, ok := hookEventStates[input.HookEventName]; ok {
		return hookReportsState, state
	}
	return hookIgnored, ""
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
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(
		s.reportPath(report.SessionID), ".agent-activity-*", data,
	); err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

func (s *Store) removeReport(sessionID string) error {
	err := os.Remove(s.reportPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		s.invalidateCache()
	}
	return err
}

func (s *Store) readReport(path string) (Report, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, false
	}
	defer file.Close()
	var report Report
	if err := json.NewDecoder(io.LimitReader(file, maxReportBytes)).Decode(&report); err != nil {
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

func (s *Store) reportPath(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}
