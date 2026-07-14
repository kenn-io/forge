package docs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/procutil"
)

// docsGitBase is the base kit runner for every git command against a docs
// folder. StripEnv drops inherited GIT_* variables so a docs git command
// can never bind to another repository or splice in caller config, and the
// secret stripping keeps middleman and msgvault credentials out of git
// child processes. Stripping is deliberately wholesale — env-based
// customization such as GIT_SSH_COMMAND and GIT_AUTHOR_*/GIT_COMMITTER_*
// goes too, rather than resurrecting a hand-maintained allowlist; the
// supported customization surface is gitconfig (core.sshCommand,
// user.name/user.email, credential.helper). Unlike gitcmd.New(), global
// and system config stay readable: docs commits rely on the maintainer's
// identity, filters, and credential helpers. A package variable so tests
// can substitute fully isolated config.
var docsGitBase = gitcmd.Runner{Env: stripDocsSecretEnv(os.Environ()), StripEnv: true}

// emptyHooksDir is an empty directory used as core.hooksPath so that
// hooks shipped inside a docs folder's .git/hooks (or pointed to by a
// core.hooksPath override in its repo config) never execute when
// middleman drives git. Docs folders are user data, not trusted code.
// A randomly named temp dir avoids predictable-path pre-creation.
var emptyHooksDir = sync.OnceValues(func() (string, error) {
	return os.MkdirTemp("", "middleman-docs-no-hooks-")
})

// docsGitRunner returns the kit runner with command-scope overrides that
// neutralize the command-execution vectors git would otherwise honor from
// an untrusted docs repo's local config or tracked .gitattributes:
//
//   - core.hooksPath: ignore any .git/hooks or hooksPath override.
//   - core.fsmonitor=false: never run a configured fsmonitor program
//     (it would execute on read commands such as `git status`).
//   - protocol.allow=never with explicit always entries for file, git,
//     http(s), and ssh: every other transport resolves to a
//     git-remote-<scheme> helper process with a repo-chosen address
//     (ext:: most directly), so the allowlist blocks them at git's own
//     policy layer even if URL classification in assertPushTargetSafe
//     were ever bypassed.
//
// These overrides have no legitimate-use cost for the docs git flows.
// Other command-bearing config (clean/smudge filters used by git-lfs,
// gpg.program for signed commits, credential.helper, core.sshCommand) is
// left intact because disabling it would break real workflows; treat
// such repos as trusted before registering them as docs folders.
func docsGitRunner() (gitcmd.Runner, error) {
	hooksDir, err := emptyHooksDir()
	if err != nil {
		return gitcmd.Runner{}, fmt.Errorf("creating empty git hooks dir: %w", err)
	}
	return docsGitBase.
		WithConfig("core.hooksPath", hooksDir).
		WithConfig("core.fsmonitor", "false").
		WithConfig("protocol.allow", "never").
		WithConfig("protocol.file.allow", "always").
		WithConfig("protocol.git.allow", "always").
		WithConfig("protocol.http.allow", "always").
		WithConfig("protocol.https.allow", "always").
		WithConfig("protocol.ssh.allow", "always"), nil
}

// runDocsGit runs one git command against a docs folder root under the
// shared subprocess limiter. Failures unwrap to *gitcmd.GitError, whose
// message and Stderr field carry git's trimmed stderr.
func runDocsGit(ctx context.Context, root string, stdin io.Reader, args ...string) ([]byte, error) {
	runner, err := docsGitRunner()
	if err != nil {
		return nil, err
	}
	release, err := procutil.TryAcquire(ctx, "git subprocess capacity")
	if err != nil {
		return nil, err
	}
	defer release()
	stdout, _, err := runner.Run(ctx, root, stdin, args...)
	return stdout, err
}

// gitStderr extracts trimmed stderr from a failed docs git command,
// falling back to the error text when no stderr was captured.
func gitStderr(err error) string {
	if ge, ok := errors.AsType[*gitcmd.GitError](err); ok && ge.Stderr != "" {
		return ge.Stderr
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func stripDocsSecretEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if isDocsSecretEnv(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func isDocsSecretEnv(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return false
	}
	if strings.HasPrefix(upper, "MIDDLEMAN_") && strings.Contains(upper, "TOKEN") {
		return true
	}
	if strings.HasPrefix(upper, "MSGVAULT_") {
		return true
	}
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY"} {
		if upper == part || strings.HasSuffix(upper, "_"+part) || strings.Contains(upper, part+"_") {
			return true
		}
	}
	return false
}

// GitStatus is the per-file decoration the UI surfaces on tree rows.
type GitStatus string

const (
	GitStatusAdded     GitStatus = "added"
	GitStatusDeleted   GitStatus = "deleted"
	GitStatusIgnored   GitStatus = "ignored"
	GitStatusModified  GitStatus = "modified"
	GitStatusRenamed   GitStatus = "renamed"
	GitStatusUntracked GitStatus = "untracked"
)

// GitStatusEntry pairs a folder-relative path with its status. Paths
// use forward slashes regardless of the host OS so the JSON contract
// is stable.
type GitStatusEntry struct {
	Path   string    `json:"path"`
	Status GitStatus `json:"status"`
}

// GitStatusResponse is the wire shape returned by the docs git status route.
type GitStatusResponse struct {
	IsRepo  bool             `json:"is_repo"`
	Entries []GitStatusEntry `json:"entries"`
}

// ErrNotAGitRepo is returned when the folder root has no .git directory.
var ErrNotAGitRepo = errors.New("folder is not a git repository")

// GitStatus runs `git status --porcelain=v1` against the folder root
// and returns parsed entries. Non-repositories return IsRepo=false.
func (r *Registry) GitStatus(ctx context.Context, folderID string) (GitStatusResponse, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return GitStatusResponse{}, err
	}
	if !isGitRepo(v.Path) {
		return GitStatusResponse{IsRepo: false, Entries: []GitStatusEntry{}}, nil
	}
	if err := assertWorktreeAttributesSafe(ctx, v.Path); err != nil {
		return GitStatusResponse{}, err
	}
	entries, err := runGitStatus(ctx, v.Path)
	if err != nil {
		return GitStatusResponse{}, err
	}
	return GitStatusResponse{IsRepo: true, Entries: entries}, nil
}

func isGitRepo(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

func runGitStatus(ctx context.Context, root string) ([]GitStatusEntry, error) {
	out, err := runDocsGit(ctx, root, nil,
		"-c", "color.status=false",
		"status", "--porcelain=v1", "-z",
		"--untracked-files=all",
		"--ignored",
	)
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	return parsePorcelainV1(out)
}

// parsePorcelainV1 reads `git status --porcelain=v1 -z` output. Each
// entry is `XY <space> path\0`; renames/copies emit a second NUL token
// for the source path. The API exposes the destination/current path.
func parsePorcelainV1(data []byte) ([]GitStatusEntry, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	scanner.Split(splitNUL)
	var entries []GitStatusEntry
	for scanner.Scan() {
		record := scanner.Bytes()
		if len(record) < 4 {
			continue
		}
		x := record[0]
		y := record[1]
		path := string(record[3:])
		if isRenameOrCopy(x) || isRenameOrCopy(y) {
			if !scanner.Scan() {
				return nil, fmt.Errorf("malformed rename entry: missing source path")
			}
		}
		entries = append(entries, GitStatusEntry{
			Path:   filepath.ToSlash(path),
			Status: classify(x, y),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []GitStatusEntry{}
	}
	return entries, nil
}

func classify(x, y byte) GitStatus {
	if x == '?' && y == '?' {
		return GitStatusUntracked
	}
	if x == '!' && y == '!' {
		return GitStatusIgnored
	}
	if isUnmergedPair(x, y) {
		return GitStatusModified
	}
	primary := x
	if primary == ' ' {
		primary = y
	}
	switch primary {
	case 'A':
		return GitStatusAdded
	case 'D':
		return GitStatusDeleted
	case 'R', 'C':
		return GitStatusRenamed
	default:
		return GitStatusModified
	}
}

func isUnmergedPair(x, y byte) bool {
	switch {
	case x == 'D' && y == 'D':
		return true
	case x == 'A' && y == 'A':
		return true
	case x == 'U' || y == 'U':
		return true
	}
	return false
}

func splitNUL(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
