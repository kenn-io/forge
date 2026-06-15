package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

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

	database, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	if err := resetFixtureRows(ctx, database, *projectPath); err != nil {
		return err
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
