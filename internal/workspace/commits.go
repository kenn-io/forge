package workspace

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/gitclone"
)

const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// WorktreeCommitsAgainstMergeTarget returns first-parent commits on the
// checked-out workspace branch that are not reachable from the merge target.
func WorktreeCommitsAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
) ([]gitclone.Commit, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return nil, ok, err
	}
	commits, err := worktreeCommits(ctx, dir, baseRef, "HEAD")
	return commits, true, err
}

func worktreeCommits(
	ctx context.Context,
	dir string,
	baseRef string,
	headRef string,
) ([]gitclone.Commit, error) {
	args := []string{
		"log",
		"--first-parent",
		"--format=%H%x00%an%x00%aI%x00%s",
		baseRef + ".." + headRef,
	}
	out, err := worktreeGitOutput(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}

	var commits []gitclone.Commit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("unexpected git log line: %q", line)
		}
		t, err := time.Parse(time.RFC3339, parts[2])
		if err != nil {
			return nil, fmt.Errorf("parse commit date %q: %w", parts[2], err)
		}
		commits = append(commits, gitclone.Commit{
			SHA:        parts[0],
			AuthorName: parts[1],
			AuthoredAt: t,
			Message:    parts[3],
		})
	}
	return commits, scanner.Err()
}

func WorktreeParentOf(
	ctx context.Context,
	dir string,
	sha string,
) (string, error) {
	out, err := worktreeGitOutput(ctx, dir,
		"rev-list", "--parents", "-n", "1", sha,
	)
	if err != nil {
		return "", fmt.Errorf("parent of %s: %w", sha, err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "", fmt.Errorf("parent of %s: empty rev-list output", sha)
	}
	if len(fields) == 1 {
		return emptyTreeSHA, nil
	}
	return fields[1], nil
}
