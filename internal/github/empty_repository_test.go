package github

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

// An empty repository is a normal state, not a sync failure, so maintaining
// its clone and default-branch activity logs no warnings on any pass.
func TestEmptyRepositorySyncLogsNoWarnings(t *testing.T) {
	logs := captureDefaultLogs(t)
	dir := t.TempDir()
	remote := filepath.Join(dir, "empty.git")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	repo := RepoRef{Owner: "acme", Name: "empty", PlatformHost: "github.com", CloneURL: remote}
	syncer := NewSyncer(
		nil, openTestDB(t), gitclone.New(filepath.Join(dir, "clones"), nil),
		[]RepoRef{repo}, time.Minute, nil, nil,
	)

	for range 2 {
		require.NoError(t, syncer.ensureClone(t.Context(), repo))
		syncer.syncDefaultBranchActivity(t.Context(), repo, 1, "main", nil)
	}

	assert.NotContains(t, logs.String(), "level=WARN")
}
