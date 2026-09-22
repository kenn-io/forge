package workspace

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

func TestExecutionWorkerSeparatesNewWorktreesFromRuntimeData(t *testing.T) {
	assert := assert.New(t)
	database := openTestDB(t)
	manager := NewManager(database, t.TempDir())
	spec := launchSpecForTest()
	seedLaunchSpecRepository(t, database, spec)
	manager.SetNow(func() time.Time { return spec.IssuedAt })
	existing, err := manager.CreateFromLaunchSpec(t.Context(), spec)
	require.NoError(t, err)
	root := t.TempDir()
	manager.SetExecutionWorker(config.ExecutionWorker{Enabled: true, WorktreeDir: root}, "/opt/example/forge")
	spec.ItemNumber++
	spec.ItemKey = "8"
	created, err := manager.CreateFromLaunchSpec(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(filepath.Join(root, spec.Repository.Owner, spec.Repository.Name), filepath.Dir(created.WorktreePath))
	persisted, err := database.GetWorkspace(t.Context(), existing.ID)
	require.NoError(t, err)
	assert.Equal(existing.WorktreePath, persisted.WorktreePath)
}

func TestExecutionWorkerConfiguresRealCommitIdentity(t *testing.T) {
	assert := assert.New(t)
	work := gitfixture.DivergenceWorktree(t)
	m := NewManager(nil, t.TempDir())
	m.SetExecutionWorker(config.ExecutionWorker{
		Enabled: true, UID: 1001, GitHubUserID: 42, BrokerSocket: "/run/example/broker.sock",
		CommitName: "Developer A", CommitEmail: "42+developer-a@users.noreply.github.com",
	}, "/opt/example/bin/forge")
	runWorkspaceTestGit(t, work, "config", "--add", "credential.helper", "unwanted-helper")
	require.NoError(t, m.configureExecutionWorktree(t.Context(), work))
	runWorkspaceTestGit(t, work, "commit", "--allow-empty", "-m", "worker attribution")
	identity := string(runWorkspaceTestGit(t, work, "show", "-s", "--format=%an|%ae|%cn|%ce"))
	assert.Equal("Developer A|42+developer-a@users.noreply.github.com|Developer A|42+developer-a@users.noreply.github.com", strings.TrimSpace(identity))
	helpers := string(runWorkspaceTestGit(t, work, "config", "--get-all", "credential.helper"))
	assert.True(strings.HasPrefix(helpers, "\n!"), "the empty helper resets inherited credentials")
	assert.NotContains(helpers, "unwanted-helper")
	runWorkspaceTestGit(t, work, "config", "user.email", "wrong@example.org")
	assert.ErrorIs(m.ValidateExecutionIdentity(t.Context(), work), ErrExecutionIdentity)
}
