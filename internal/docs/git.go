package docs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"go.kenn.io/middleman/internal/procutil"
)

var isolatedGitEnv = cleanGitEnv(os.Environ())

// emptyHooksDir is an empty directory used as core.hooksPath so that
// hooks shipped inside a docs folder's .git/hooks (or pointed to by a
// core.hooksPath override in its repo config) never execute when
// middleman drives git. Docs folders are user data, not trusted code.
// A randomly named temp dir avoids predictable-path pre-creation.
var emptyHooksDir = sync.OnceValues(func() (string, error) {
	return os.MkdirTemp("", "middleman-docs-no-hooks-")
})

// safeGitConfigArgs returns `-c` overrides that neutralize the
// command-execution vectors git would otherwise honor from an untrusted
// docs repo's local config or tracked .gitattributes:
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
// These overrides have no legitimate-use cost for the docs publish flow.
// Other command-bearing config (clean/smudge filters used by git-lfs,
// gpg.program for signed commits, credential.helper, core.sshCommand) is
// left intact because disabling it would break real workflows; treat
// such repos as trusted before registering them as docs folders.
func safeGitConfigArgs() ([]string, error) {
	hooksDir, err := emptyHooksDir()
	if err != nil {
		return nil, fmt.Errorf("creating empty git hooks dir: %w", err)
	}
	return []string{
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "protocol.git.allow=always",
		"-c", "protocol.http.allow=always",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=always",
	}, nil
}

// gitCommand builds a git invocation against a docs folder root with the
// safe config overrides applied. It is fallible only because preparing
// the empty hooks directory can fail.
func gitCommand(ctx context.Context, root string, args ...string) (*exec.Cmd, error) {
	safe, err := safeGitConfigArgs()
	if err != nil {
		return nil, err
	}
	cmd := procutil.CommandContext(ctx, "git", append(safe, args...)...)
	cmd.Dir = root
	cmd.Env = isolatedGitEnv
	return cmd, nil
}

func cleanGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if isDocsGitBindingEnv(key) || isDocsSecretEnv(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func isDocsGitBindingEnv(key string) bool {
	switch key {
	case "GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR",
		"GIT_NAMESPACE",
		"GIT_PREFIX":
		return true
	default:
		return strings.HasPrefix(key, "GIT_CONFIG")
	}
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
	cmd, err := gitCommand(ctx, root,
		"-c", "color.status=false",
		"status", "--porcelain=v1", "-z",
		"--untracked-files=all",
		"--ignored",
	)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, "running docs git status"); err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parsePorcelainV1(stdout.Bytes())
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
