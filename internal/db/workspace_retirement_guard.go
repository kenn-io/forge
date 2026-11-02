package db

import (
	"strings"
	"sync"
)

// RepositoryUnresolved reports whether the workspace names a provider
// repository that has no resolved repository row. The workspace PR monitor
// retires such workspaces.
func (w *Workspace) RepositoryUnresolved() bool {
	return w != nil && w.RepoID == 0 &&
		strings.TrimSpace(w.PlatformHost) != "" &&
		strings.TrimSpace(w.RepoOwner) != "" &&
		strings.TrimSpace(w.RepoName) != ""
}

// workspaceRetirementGuard exists only on test databases. A test that inserts
// an unresolved workspace while a workspace PR monitor loop runs on the same
// database races the monitor's retirement of that workspace. The guard records
// both facts so the test fails every time instead of only when the race goes
// the wrong way.
type workspaceRetirementGuard struct {
	mu             sync.Mutex
	monitorStarted bool
	unresolvedIDs  []string
}

// NoteWorkspaceRetirementMonitorStarted records that a workspace PR monitor
// loop that retires unresolved workspaces runs against this database. It does
// nothing outside tests.
func (d *DB) NoteWorkspaceRetirementMonitorStarted() {
	if d.retirementGuard == nil {
		return
	}
	d.retirementGuard.mu.Lock()
	defer d.retirementGuard.mu.Unlock()
	d.retirementGuard.monitorStarted = true
}

func (d *DB) noteWorkspaceInserted(ws *Workspace) {
	if d.retirementGuard == nil || !ws.RepositoryUnresolved() {
		return
	}
	d.retirementGuard.mu.Lock()
	defer d.retirementGuard.mu.Unlock()
	d.retirementGuard.unresolvedIDs = append(d.retirementGuard.unresolvedIDs, ws.ID)
}

// UnresolvedWorkspacesUnderRetirementMonitor returns the unresolved workspaces
// inserted into a test database on which a workspace PR monitor loop also ran,
// in either order.
func (d *DB) UnresolvedWorkspacesUnderRetirementMonitor() []string {
	if d.retirementGuard == nil {
		return nil
	}
	d.retirementGuard.mu.Lock()
	defer d.retirementGuard.mu.Unlock()
	if !d.retirementGuard.monitorStarted {
		return nil
	}
	return append([]string(nil), d.retirementGuard.unresolvedIDs...)
}
