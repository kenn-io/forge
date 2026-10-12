// Command rebootsim seeds ready ad-hoc workspaces for the Docker reboot
// simulation in scripts/reboot-sim.sh. It runs inside the simulation
// container, never against a developer's data directory.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/platform"
)

const configTemplate = `host = "127.0.0.1"
port = 8091
data_dir = %q

[[repos]]
owner = "acme"
name = "widget"

[[agents]]
key = "claude"
label = "Claude"
command = [%q]
`

func main() {
	dataDir := flag.String("data", "/data", "simulation data directory")
	count := flag.Int("n", 4, "number of workspaces to seed")
	agent := flag.String("agent", "/usr/local/bin/fake-agent", "fake agent executable")
	flag.Parse()
	if err := seed(context.Background(), *dataDir, *count, *agent); err != nil {
		fmt.Fprintln(os.Stderr, "rebootsim:", err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, dataDir string, count int, agent string) error {
	if err := refuseInsideCheckout(ctx, dataDir); err != nil {
		return err
	}
	config := fmt.Sprintf(configTemplate, dataDir, agent)
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), []byte(config), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	database, err := db.Open(filepath.Join(dataDir, "forge.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	// Workspaces without a provider-verified repository are retired at
	// startup, so register the repository as a sync would have.
	repo, err := database.ObserveRepository(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Key: platform.RepositoryIDKey(1), Owner: "acme", Name: "widget",
	})
	if err != nil {
		return fmt.Errorf("observe repository: %w", err)
	}
	for i := range count {
		id := fmt.Sprintf("5e5e5e5e%08x", i+1)
		branch := fmt.Sprintf("work/sim-%02d", i+1)
		dir := filepath.Join(dataDir, "worktrees", "sim", id)
		if err := initWorktree(ctx, dir, branch); err != nil {
			return err
		}
		if err := database.InsertWorkspace(ctx, &db.Workspace{
			ID: id, Platform: "github", PlatformHost: "github.com",
			RepoOwner: "acme", RepoName: "widget", RepoID: repo.Repository.ID, ItemType: db.WorkspaceItemTypeAdHoc,
			ItemKey: db.AdHocWorkspaceItemKey(branch), GitHeadRef: branch,
			WorkspaceBranch: branch, WorktreePath: dir, Status: "ready",
			TmuxSession: "forge-" + id,
		}); err != nil {
			return fmt.Errorf("insert workspace %s: %w", id, err)
		}
		fmt.Println(id)
	}
	return nil
}

// refuseInsideCheckout keeps fixture repositories out of any existing
// repository, where Git would nest them or resolve commands to the parent. It
// asks Git itself from the resolved location, so symlinks, linked worktrees,
// bare repositories, and mount points cannot hide a parent repository.
func refuseInsideCheckout(ctx context.Context, path string) error {
	dir, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	cmd := procutil.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir")
	cmd.Dir = dir
	cmd.Env = append(isolatedGitEnv(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return fmt.Errorf("%s is inside the Git repository %s; use a scratch directory",
			path, strings.TrimSpace(string(out)))
	}
	if strings.Contains(string(out), "not a git repository") {
		return nil
	}
	return fmt.Errorf("check %s for a Git repository: %w: %s", path, err, out)
}

// isolatedGitEnv drops inherited Git variables, which could redirect commands
// into another repository, and replaces user and system config with none.
func isolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

// initWorktree runs Git only in a directory it creates, so it never commits
// into an existing repository's staged changes.
func initWorktree(ctx context.Context, dir, branch string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("create worktree parent: %w", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("create fresh worktree: %w", err)
	}
	if err := refuseInsideCheckout(ctx, dir); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", branch},
		{"-c", "user.name=Sim", "-c", "user.email=sim@example.com", "commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		cmd := procutil.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = isolatedGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v in %s: %w: %s", args, dir, err, out)
		}
	}
	return nil
}
