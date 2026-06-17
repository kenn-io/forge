package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/procutil"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "seed fleet fixture: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fleet-seed", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the middleman SQLite database")
	projectPath := fs.String("project-path", "", "project path to seed")
	worktreePath := fs.String("worktree-path", "", "workspace worktree path to seed")
	startTmux := fs.Bool("start-tmux", false, "start the seeded workspace tmux session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		return fmt.Errorf("-db is required")
	}
	if *projectPath == "" {
		return fmt.Errorf("-project-path is required")
	}
	if *worktreePath == "" {
		return fmt.Errorf("-worktree-path is required")
	}

	registeredWorktreePath := filepath.Join(
		filepath.Dir(*worktreePath), "registered-runtime",
	)
	for _, path := range []string{
		filepath.Dir(*dbPath), *projectPath, *worktreePath,
		registeredWorktreePath,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	if err := ensureWorkspaceGitFixture(ctx, *worktreePath); err != nil {
		return err
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	if err := resetFixtureRows(ctx, database, *projectPath); err != nil {
		return err
	}
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "fleet-widget",
		RepoPath:     "acme/fleet-widget",
	})
	if err != nil {
		return fmt.Errorf("upsert repo: %w", err)
	}
	now := time.Now().UTC()
	if _, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		Number:          7,
		URL:             "https://github.com/acme/fleet-widget/pull/7",
		Title:           "Fleet widget",
		Author:          "fleet",
		State:           db.MergeRequestStateOpen,
		HeadBranch:      "feature/fleet-read",
		BaseBranch:      "main",
		PlatformHeadSHA: "fleet-head",
		PlatformBaseSHA: "fleet-base",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	}); err != nil {
		return fmt.Errorf("upsert merge request: %w", err)
	}
	project, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName:   "fleet-widget",
		LocalPath:     *projectPath,
		DefaultBranch: "main",
	})
	if err != nil {
		if !errors.Is(err, db.ErrProjectPathTaken) {
			return fmt.Errorf("create project: %w", err)
		}
		project, err = database.GetProjectByLocalPath(ctx, *projectPath)
		if err != nil {
			return fmt.Errorf("get existing project: %w", err)
		}
	}
	if _, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime/registered",
		Path:      registeredWorktreePath,
	}); err != nil && !errors.Is(err, db.ErrWorktreePathTaken) {
		return fmt.Errorf("create registered worktree: %w", err)
	}
	if err := database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "fleet-member-ws-7",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "fleet-widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      7,
		GitHeadRef:      "feature/fleet-read",
		WorkspaceBranch: "feature/fleet-read",
		WorktreePath:    *worktreePath,
		TmuxSession:     "middleman-fleet-member-ws-7",
		TerminalBackend: "tmux",
		Status:          "ready",
	}); err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	if *startTmux {
		if err := startWorkspaceTmux(ctx, "middleman-fleet-member-ws-7", *worktreePath); err != nil {
			return err
		}
	}

	return nil
}

func ensureWorkspaceGitFixture(ctx context.Context, worktreePath string) error {
	root := filepath.Dir(worktreePath)
	remotePath := filepath.Join(root, "origin.git")
	if err := os.RemoveAll(remotePath); err != nil {
		return fmt.Errorf("reset origin: %w", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("reset worktree: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}
	if err := runGit(ctx, root, "init", "--bare", "--initial-branch=main", remotePath); err != nil {
		return err
	}
	if err := runGit(ctx, root, "clone", remotePath, worktreePath); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "config", "user.email", "fleet@example.test"); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "config", "user.name", "Fleet Fixture"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "base.txt"), []byte("base\n"), 0o600); err != nil {
		return fmt.Errorf("write base: %w", err)
	}
	if err := runGit(ctx, worktreePath, "add", "."); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "commit", "-m", "base"); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "push", "origin", "main"); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "checkout", "-b", "feature/fleet-read"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		return fmt.Errorf("write feature: %w", err)
	}
	if err := runGit(ctx, worktreePath, "add", "."); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "commit", "-m", "feature commit"); err != nil {
		return err
	}
	if err := runGit(ctx, worktreePath, "push", "-u", "origin", "feature/fleet-read"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		return fmt.Errorf("write dirty: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := procutil.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v in %s: %w: %s", args, dir, err, output)
	}
	return nil
}

func resetFixtureRows(ctx context.Context, database *db.DB, projectPath string) error {
	if _, err := database.WriteDB().ExecContext(
		ctx,
		`DELETE FROM middleman_workspaces WHERE id = ?`,
		"fleet-member-ws-7",
	); err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	if _, err := database.WriteDB().ExecContext(
		ctx,
		`DELETE FROM middleman_projects WHERE local_path = ?`,
		projectPath,
	); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

func startWorkspaceTmux(ctx context.Context, session, worktreePath string) error {
	_ = procutil.CommandContext(ctx, "tmux", "kill-session", "-t", session).Run()
	cmd := procutil.CommandContext(
		ctx,
		"tmux", "new-session", "-d", "-s", session, "-c", worktreePath, "sh",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start tmux session %q: %w: %s", session, err, output)
	}
	return nil
}
