package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"go.kenn.io/forge/internal/procutil"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitworktree "go.kenn.io/kit/git/worktree"
)

const (
	repositoryHookCommandTimeout = 30 * time.Second
	repositoryHookOutputLimit    = 64 << 10
	roborevDefaultSnapshotDir    = ".roborev"
)

type roborevRepositoryConfig struct {
	SnapshotDir string `toml:"snapshot_dir"`
}

func roborevManagedCloneServerAddress(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return "", fmt.Errorf("roborev managed-clone initialization requires a loopback HTTP endpoint")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("roborev managed-clone initialization requires an origin-only loopback HTTP endpoint")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return "", fmt.Errorf("roborev managed-clone initialization requires 127.0.0.1, localhost, or [::1]")
	}
	if parsed.Port() == "" {
		return "", fmt.Errorf("roborev managed-clone initialization requires an explicit endpoint port")
	}
	return parsed.Host, nil
}

func normalizeRepositoryRelativeDir(raw string) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return "", fmt.Errorf("snapshot_dir is empty")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("snapshot_dir %q must not contain control characters", raw)
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if filepath.IsAbs(value) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("snapshot_dir %q must stay inside the repository", raw)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("snapshot_dir %q must not be inside .git", raw)
	}
	return clean, nil
}

func roborevSnapshotDirAllowed(workspaceDir, trustedDir string) bool {
	workspaceDir, err := normalizeRepositoryRelativeDir(workspaceDir)
	if err != nil {
		return false
	}
	trustedDir, err = normalizeRepositoryRelativeDir(trustedDir)
	if err != nil {
		return false
	}
	return workspaceDir == trustedDir || workspaceDir == roborevDefaultSnapshotDir
}

func parseRoborevSnapshotDir(content []byte, source string) (string, error) {
	if len(content) == 0 {
		return roborevDefaultSnapshotDir, nil
	}
	var cfg roborevRepositoryConfig
	if _, err := toml.Decode(string(content), &cfg); err != nil {
		return "", fmt.Errorf("parse %s .roborev.toml: %w", source, err)
	}
	if strings.TrimSpace(cfg.SnapshotDir) == "" {
		return roborevDefaultSnapshotDir, nil
	}
	dir, err := normalizeRepositoryRelativeDir(cfg.SnapshotDir)
	if err != nil {
		return "", fmt.Errorf("validate %s .roborev.toml: %w", source, err)
	}
	return dir, nil
}

type cappedOutputBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *cappedOutputBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := repositoryHookOutputLimit - b.buffer.Len()
	if remaining > 0 {
		_, _ = b.buffer.Write(p[:min(len(p), remaining)])
	}
	if original > remaining {
		b.overflow = true
	}
	return original, nil
}

func runRepositoryHookTool(
	ctx context.Context, workspacePath, tool string, args ...string,
) error {
	resolved, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("required repository hook tool %q is not installed", tool)
	}
	commandCtx, cancel := context.WithTimeout(ctx, repositoryHookCommandTimeout)
	defer cancel()
	command := procutil.CommandContext(commandCtx, resolved, args...)
	command.Dir = workspacePath
	command.Env = workspaceGitCommand(commandCtx, workspacePath).Env
	var stdout, stderr cappedOutputBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = procutil.Run(commandCtx, command, "repository hook setup")
	if stdout.overflow || stderr.overflow {
		return fmt.Errorf("%s output exceeded %d bytes per stream", tool, repositoryHookOutputLimit)
	}
	if err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out after %s", tool, repositoryHookCommandTimeout)
		}
		return fmt.Errorf("%s failed: %w: %s", tool, err, strings.TrimSpace(stderr.buffer.String()))
	}
	return nil
}

func trackedWorkspaceStatus(ctx context.Context, workspacePath string) (string, error) {
	out, err := gitCombinedOutput(ctx, workspacePath, "status", "--porcelain=v1", "--untracked-files=no")
	if err != nil {
		return "", fmt.Errorf("inspect tracked workspace status: %w", err)
	}
	return out, nil
}

func trustedRootFile(
	ctx context.Context, commonDir, commitSHA, name string,
) ([]byte, bool, error) {
	out, err := gitCombinedOutput(ctx, commonDir, "ls-tree", commitSHA, "--", name)
	if err != nil {
		return nil, false, fmt.Errorf("inspect trusted %s: %w", name, err)
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return nil, false, nil
	}
	fields := strings.Fields(line)
	if len(fields) < 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return nil, false, fmt.Errorf("trusted %s must be a regular root file", name)
	}
	content, err := gitCombinedOutput(ctx, commonDir, "show", commitSHA+":"+name)
	if err != nil {
		return nil, false, fmt.Errorf("read trusted %s: %w", name, err)
	}
	return []byte(content), true, nil
}

func workspaceRoborevConfig(workspacePath string) ([]byte, error) {
	path := filepath.Join(workspacePath, ".roborev.toml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect workspace .roborev.toml: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace .roborev.toml must be a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace .roborev.toml: %w", err)
	}
	return content, nil
}

func rejectTrackedOrSymlinkedSnapshotDir(
	ctx context.Context, workspacePath, relativeDir string,
) error {
	out, err := gitCombinedOutput(ctx, workspacePath, "ls-files", "--cached", "--", relativeDir)
	if err != nil {
		return fmt.Errorf("inspect tracked Roborev snapshot directory: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("roborev snapshot directory %q is tracked", relativeDir)
	}
	current := workspacePath
	for component := range strings.SplitSeq(filepath.FromSlash(relativeDir), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Roborev snapshot directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("roborev snapshot directory %q crosses a symlink", relativeDir)
		}
	}
	return nil
}

func ensureManagedCloneExclude(
	ctx context.Context, commonDir, workspacePath, relativeDir string,
) error {
	gitDir, err := gitCombinedOutput(
		ctx, workspacePath, "rev-parse", "--path-format=absolute", "--git-dir",
	)
	if err != nil {
		return fmt.Errorf("resolve managed worktree Git directory: %w", err)
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" || !filepath.IsAbs(gitDir) {
		return fmt.Errorf("resolve managed worktree Git directory: invalid path %q", gitDir)
	}
	canonicalGitDir, err := canonicalFilesystemPath(gitDir)
	if err != nil {
		return fmt.Errorf("resolve managed worktree Git directory: %w", err)
	}
	canonicalCommonDir, err := canonicalFilesystemPath(commonDir)
	if err != nil {
		return fmt.Errorf("resolve managed clone common directory: %w", err)
	}
	if canonicalGitDir == canonicalCommonDir {
		return fmt.Errorf("managed workspace must be a linked worktree")
	}

	sharedBare, err := gitCombinedOutput(
		ctx, commonDir, "config", "--local", "--bool", "--get", "core.bare",
	)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return fmt.Errorf("inspect managed clone core.bare: %w", err)
		}
	}
	if strings.TrimSpace(sharedBare) == "true" {
		worktreeList, err := gitCombinedOutput(ctx, commonDir, "worktree", "list", "--porcelain")
		if err != nil {
			return fmt.Errorf("list managed linked worktrees: %w", err)
		}
		for _, worktree := range gitworktree.ParsePorcelain(worktreeList) {
			if worktree.Bare || worktree.Prunable {
				continue
			}
			worktreeGitDir, err := gitCombinedOutput(
				ctx, worktree.Path, "-c", "core.bare=false",
				"rev-parse", "--path-format=absolute", "--git-dir",
			)
			if err != nil {
				return fmt.Errorf("resolve managed linked worktree Git directory: %w", err)
			}
			if _, err := gitCombinedOutput(
				ctx, commonDir, "config", "--file",
				filepath.Join(strings.TrimSpace(worktreeGitDir), "config.worktree"),
				"core.bare", "false",
			); err != nil {
				return fmt.Errorf("configure managed linked worktree: %w", err)
			}
		}
	}
	if _, err := gitCombinedOutput(
		ctx, commonDir, "config", "--local", "extensions.worktreeConfig", "true",
	); err != nil {
		return fmt.Errorf("enable managed clone worktree configuration: %w", err)
	}

	excludePath := filepath.Join(canonicalGitDir, "forge-roborev-exclude")
	baseExclude, err := effectiveBaseExclude(
		ctx, workspacePath, canonicalGitDir, excludePath,
	)
	if err != nil {
		return err
	}
	if err := writeRoborevExclude(
		excludePath, baseExclude, gitIgnoreLiteralDirPattern(relativeDir),
	); err != nil {
		return err
	}
	if _, err := gitCombinedOutput(
		ctx, workspacePath, "config", "--worktree", "core.excludesFile", excludePath,
	); err != nil {
		return fmt.Errorf("configure worktree Roborev exclude file: %w", err)
	}
	probePath := filepath.ToSlash(filepath.Join(relativeDir, ".forge-ignore-probe"))
	if _, err := gitCombinedOutput(ctx, workspacePath, "check-ignore", "--quiet", "--no-index", "--", probePath); err != nil {
		return fmt.Errorf("verify worktree Roborev exclude for %q: %w", relativeDir, err)
	}
	return nil
}

type gitConfigPathEntry struct {
	origin string
	path   string
}

func userExcludeEntries(
	ctx context.Context, workspacePath string,
) ([]gitConfigPathEntry, error) {
	stdout, _, err := (gitcmd.Runner{StripEnv: true}).Run(
		ctx, workspacePath, nil,
		"config", "--null", "--show-origin", "--path", "--get-all", "core.excludesFile",
	)
	if err != nil {
		if gitcmd.IsExitCode(err, 1) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect user Git excludes file: %w", err)
	}
	parts := bytes.Split(stdout, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts)%2 != 0 {
		return nil, fmt.Errorf("inspect user Git excludes file: invalid Git config output")
	}
	entries := make([]gitConfigPathEntry, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		entries = append(entries, gitConfigPathEntry{
			origin: string(parts[i]), path: string(parts[i+1]),
		})
	}
	return entries, nil
}

func worktreeRoborevBaseExclude(
	ctx context.Context, workspacePath string,
) (string, error) {
	out, err := gitCombinedOutput(
		ctx, workspacePath, "config", "--worktree", "--path", "--get",
		"forge.roborevBaseExcludesFile",
	)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("inspect saved user Git excludes file: %w", err)
	}
	return strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r"), nil
}

func gitConfigOriginPath(origin string) string {
	path, ok := strings.CutPrefix(origin, "file:")
	if !ok {
		return ""
	}
	return filepath.Clean(path)
}

func implicitGlobalExcludePath() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(configHome) {
		return filepath.Join(configHome, "git", "ignore")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "git", "ignore")
}

func effectiveBaseExclude(
	ctx context.Context, workspacePath, gitDir, generatedPath string,
) ([]byte, error) {
	entries, err := userExcludeEntries(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	storedPath, err := worktreeRoborevBaseExclude(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	basePath := ""
	if len(entries) > 0 {
		current := entries[len(entries)-1]
		if current.path != generatedPath {
			basePath = current.path
			worktreeConfig := filepath.Join(gitDir, "config.worktree")
			if gitConfigOriginPath(current.origin) == filepath.Clean(worktreeConfig) {
				if _, err := gitCombinedOutput(
					ctx, workspacePath, "config", "--worktree",
					"forge.roborevBaseExcludesFile", basePath,
				); err != nil {
					return nil, fmt.Errorf("save user Git excludes file: %w", err)
				}
			} else if storedPath != "" {
				if _, err := gitCombinedOutput(
					ctx, workspacePath, "config", "--worktree", "--unset-all",
					"forge.roborevBaseExcludesFile",
				); err != nil {
					return nil, fmt.Errorf("clear saved user Git excludes file: %w", err)
				}
			}
		} else if storedPath != "" {
			basePath = storedPath
		} else {
			for i := len(entries) - 2; i >= 0; i-- {
				if entries[i].path != generatedPath {
					basePath = entries[i].path
					break
				}
			}
		}
	}
	if basePath == "" {
		basePath = implicitGlobalExcludePath()
	}
	if basePath == "" {
		return nil, nil
	}
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(workspacePath, basePath)
	}
	content, err := os.ReadFile(basePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read user Git excludes file: %w", err)
	}
	return content, nil
}

func writeRoborevExclude(path string, base []byte, pattern string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("worktree Roborev exclude must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect worktree Roborev exclude: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".forge-roborev-exclude-*")
	if err != nil {
		return fmt.Errorf("create worktree Roborev exclude: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	content := append([]byte(nil), base...)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, pattern...)
	content = append(content, '\n')
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write worktree Roborev exclude: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close worktree Roborev exclude: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod worktree Roborev exclude: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install worktree Roborev exclude: %w", err)
	}
	return nil
}

func gitIgnoreLiteralDirPattern(relativeDir string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
	).Replace(strings.Trim(relativeDir, "/"))
	return "/" + escaped + "/"
}

func effectiveHooksDir(ctx context.Context, workspacePath string) (string, error) {
	out, err := gitCombinedOutput(ctx, workspacePath, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", fmt.Errorf("resolve effective hooks directory: %w", err)
	}
	dir := strings.TrimSpace(out)
	if dir == "" || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("resolve effective hooks directory: invalid path %q", dir)
	}
	return dir, nil
}

func userEffectiveHooksDir(ctx context.Context, workspacePath string) (string, error) {
	stdout, stderr, err := (gitcmd.Runner{StripEnv: true}).Run(
		ctx, workspacePath, nil,
		"rev-parse", "--path-format=absolute", "--git-path", "hooks",
	)
	if err != nil {
		return "", fmt.Errorf(
			"resolve user hooks directory: %w: %s",
			err, strings.TrimSpace(string(stderr)),
		)
	}
	dir := strings.TrimSpace(string(stdout))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("resolve user hooks directory: invalid path %q", dir)
	}
	return canonicalFilesystemPath(dir)
}

func managedCloneHooksDir(commonDir string) (string, error) {
	canonicalCommonDir, err := canonicalFilesystemPath(commonDir)
	if err != nil {
		return "", fmt.Errorf("resolve managed clone common directory: %w", err)
	}
	return filepath.Join(canonicalCommonDir, "hooks"), nil
}

func validateManagedCloneHooksDir(
	ctx context.Context, commonDir, workspacePath string,
) error {
	hooksDir, err := managedCloneHooksDir(commonDir)
	if err != nil {
		return err
	}
	existingDir, err := userEffectiveHooksDir(ctx, workspacePath)
	if err != nil {
		return err
	}
	if existingDir != hooksDir {
		return fmt.Errorf(
			"managed clone uses existing hooks directory %q; cannot replace it with %q",
			existingDir, hooksDir,
		)
	}
	return nil
}

type repositoryHookFileState struct {
	path    string
	content []byte
	mode    os.FileMode
	exists  bool
}

type managedHooksCheckpoint struct {
	files []repositoryHookFileState
}

func captureRepositoryHookFile(path string) (repositoryHookFileState, error) {
	state := repositoryHookFileState{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("inspect hook %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return state, fmt.Errorf(
			"preserve hook %s: existing hook is not a regular file",
			filepath.Base(path),
		)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return state, fmt.Errorf("read hook %s: %w", filepath.Base(path), err)
	}
	if len(content) > repositoryHookOutputLimit {
		return state, fmt.Errorf("preserve hook %s: existing hook is oversized", filepath.Base(path))
	}
	state.content = content
	state.mode = info.Mode()
	state.exists = true
	return state, nil
}

func beginManagedHooksUpdate(
	ctx context.Context, commonDir, workspacePath string,
) (*managedHooksCheckpoint, error) {
	hooksDir, err := managedCloneHooksDir(commonDir)
	if err != nil {
		return nil, err
	}
	if err := validateManagedCloneHooksDir(ctx, commonDir, workspacePath); err != nil {
		return nil, err
	}
	checkpoint := &managedHooksCheckpoint{}
	for _, name := range []string{"post-commit", "post-rewrite"} {
		state, err := captureRepositoryHookFile(filepath.Join(hooksDir, name))
		if err != nil {
			return nil, err
		}
		checkpoint.files = append(checkpoint.files, state)
	}
	return checkpoint, nil
}

func (c *managedHooksCheckpoint) restore() error {
	var errs []error
	for _, state := range c.files {
		if !state.exists {
			if err := os.Remove(state.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove new hook %s: %w", filepath.Base(state.path), err))
			}
			continue
		}
		if err := os.WriteFile(state.path, state.content, state.mode.Perm()); err != nil {
			errs = append(errs, fmt.Errorf("restore hook %s: %w", filepath.Base(state.path), err))
			continue
		}
		if err := os.Chmod(state.path, state.mode.Perm()); err != nil {
			errs = append(errs, fmt.Errorf("restore hook mode %s: %w", filepath.Base(state.path), err))
		}
	}
	return errors.Join(errs...)
}

func verifyExecutableHook(path, marker string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify hook %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("verify hook %s: hook is not an executable regular file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify hook %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, repositoryHookOutputLimit+1))
	if err != nil || len(content) > repositoryHookOutputLimit {
		return fmt.Errorf("verify hook %s: hook is unreadable or oversized", filepath.Base(path))
	}
	if !strings.Contains(strings.ToLower(string(content)), strings.ToLower(marker)) {
		return fmt.Errorf("verify hook %s: expected %s marker", filepath.Base(path), marker)
	}
	return nil
}

func installRoborevHooks(
	ctx context.Context, workspacePath, endpoint string,
) error {
	serverAddress, err := roborevManagedCloneServerAddress(endpoint)
	if err != nil {
		return err
	}
	if err := runRepositoryHookTool(
		ctx, workspacePath, "roborev", "--server", serverAddress, "init", "--no-daemon",
	); err != nil {
		return err
	}
	hooksDir, err := effectiveHooksDir(ctx, workspacePath)
	if err != nil {
		return err
	}
	if err := verifyExecutableHook(filepath.Join(hooksDir, "post-commit"), "roborev post-commit hook"); err != nil {
		return err
	}
	return verifyExecutableHook(filepath.Join(hooksDir, "post-rewrite"), "roborev post-rewrite hook")
}

func (m *Manager) resolveTrustedDefaultCommit(
	ctx context.Context, commonDir string, ws *Workspace,
) (string, error) {
	if m.clones == nil {
		return "", fmt.Errorf("clone manager not set")
	}
	preferred := ""
	if m.db != nil {
		identity, err := workspaceRepoIdentity(ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName)
		if err != nil {
			return "", err
		}
		repo, err := m.db.GetRepoByIdentity(ctx, identity)
		if err != nil {
			return "", fmt.Errorf("load repository default branch: %w", err)
		}
		if repo != nil {
			preferred = repo.DefaultBranch
		}
	}
	_, sha, err := m.clones.ResolveRemoteDefaultBranchInDir(ctx, commonDir, preferred)
	if err == nil {
		return sha, nil
	}
	repairCtx, cancel := context.WithTimeout(ctx, repositoryHookCommandTimeout)
	defer cancel()
	if _, repairErr := m.clones.RunGitForRepo(
		repairCtx, ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
		commonDir, "remote", "set-head", "origin", "--auto",
	); repairErr != nil {
		return "", fmt.Errorf("resolve trusted default branch and repair origin/HEAD: %w", errors.Join(err, repairErr))
	}
	_, sha, err = m.clones.ResolveRemoteDefaultBranchInDir(ctx, commonDir, preferred)
	if err != nil {
		return "", fmt.Errorf("resolve trusted default branch after repairing origin/HEAD: %w", err)
	}
	return sha, nil
}

func (m *Manager) setupManagedRepositoryHooksLocked(
	ctx context.Context, commonDir string, ws *Workspace,
) (checkpoint *managedHooksCheckpoint, err error) {
	if _, err := roborevManagedCloneServerAddress(m.roborevEndpoint); err != nil {
		return nil, err
	}
	before, err := trackedWorkspaceStatus(ctx, ws.WorktreePath)
	if err != nil {
		return nil, err
	}
	trustedSHA, err := m.resolveTrustedDefaultCommit(ctx, commonDir, ws)
	if err != nil {
		return nil, err
	}
	trustedContent, _, err := trustedRootFile(ctx, commonDir, trustedSHA, ".roborev.toml")
	if err != nil {
		return nil, err
	}
	trustedSnapshotDir, err := parseRoborevSnapshotDir(trustedContent, "trusted default branch")
	if err != nil {
		return nil, err
	}
	workspaceContent, err := workspaceRoborevConfig(ws.WorktreePath)
	if err != nil {
		return nil, err
	}
	workspaceSnapshotDir, err := parseRoborevSnapshotDir(workspaceContent, "workspace")
	if err != nil {
		return nil, err
	}
	if !roborevSnapshotDirAllowed(workspaceSnapshotDir, trustedSnapshotDir) {
		return nil, fmt.Errorf(
			"workspace Roborev snapshot_dir %q differs from trusted default branch value %q",
			workspaceSnapshotDir, trustedSnapshotDir,
		)
	}
	if err := rejectTrackedOrSymlinkedSnapshotDir(ctx, ws.WorktreePath, workspaceSnapshotDir); err != nil {
		return nil, err
	}
	if err := ensureManagedCloneExclude(ctx, commonDir, ws.WorktreePath, workspaceSnapshotDir); err != nil {
		return nil, err
	}
	checkpoint, err = beginManagedHooksUpdate(ctx, commonDir, ws.WorktreePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil {
			return
		}
		err = errors.Join(err, checkpoint.restore())
		checkpoint = nil
	}()
	if err := installRoborevHooks(ctx, ws.WorktreePath, m.roborevEndpoint); err != nil {
		return checkpoint, fmt.Errorf("initialize Roborev: %w", err)
	}
	after, err := trackedWorkspaceStatus(ctx, ws.WorktreePath)
	if err != nil {
		return checkpoint, err
	}
	if after != before {
		return checkpoint, fmt.Errorf("repository hook setup changed tracked workspace files: before %q, after %q", strings.TrimSpace(before), strings.TrimSpace(after))
	}
	return checkpoint, nil
}

type roborevRegistrationInventory struct {
	Repos []struct {
		RootPath string `json:"root_path"`
	} `json:"repos"`
	TotalCount *int `json:"total_count"`
}

func confirmRoborevRegistration(
	ctx context.Context, endpoint, workspacePath string,
) error {
	requestCtx, cancel := context.WithTimeout(ctx, repositoryHookCommandTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/api/repos", nil,
	)
	if err != nil {
		return fmt.Errorf("build Roborev registration request: %w", err)
	}
	response, err := (&http.Client{Timeout: repositoryHookCommandTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("confirm Roborev registration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("confirm Roborev registration: daemon returned status %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(content) > 2<<20 {
		return fmt.Errorf("confirm Roborev registration: invalid daemon response")
	}
	var inventory roborevRegistrationInventory
	if err := json.Unmarshal(content, &inventory); err != nil || inventory.TotalCount == nil {
		return fmt.Errorf("confirm Roborev registration: invalid daemon response")
	}
	want, err := canonicalFilesystemPath(workspacePath)
	if err != nil {
		return fmt.Errorf("confirm Roborev registration path: %w", err)
	}
	for _, repository := range inventory.Repos {
		got, err := canonicalFilesystemPath(repository.RootPath)
		if err == nil && got == want {
			return nil
		}
	}
	return fmt.Errorf("confirm Roborev registration: workspace is absent from daemon inventory")
}

func (m *Manager) setupManagedRepositoryHooks(
	ctx context.Context, commonDir string, ws *Workspace,
) error {
	err := m.withRepoLockForGitDir(ctx, commonDir, func() error {
		checkpoint, err := m.setupManagedRepositoryHooksLocked(ctx, commonDir, ws)
		if err != nil {
			return err
		}
		if err := confirmRoborevRegistration(
			ctx, m.roborevEndpoint, ws.WorktreePath,
		); err != nil {
			return errors.Join(err, checkpoint.restore())
		}
		if err := m.verifyWorkspaceRouteUnoccupied(ctx, ws); err != nil {
			return errors.Join(err, checkpoint.restore())
		}
		return nil
	})
	if err != nil {
		return err
	}
	if m.roborevRepositoryInvalidator != nil {
		m.roborevRepositoryInvalidator()
	}
	return nil
}
