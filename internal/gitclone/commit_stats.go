package gitclone

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// CommitStats counts text lines changed against a commit's first parent.
// Binary files contribute no lines; root commits compare with the empty tree.
type CommitStats struct {
	Additions int `json:"additions" doc:"Text lines added by this commit"`
	Deletions int `json:"deletions" doc:"Text lines removed by this commit"`
}

// CommitStats reads counts from the identity-resolved clone for the listed commits.
func (m *Manager) CommitStats(ctx context.Context, platform, host, owner, name string, commits []Commit) (map[string]*CommitStats, error) {
	dir, err := m.clonePathForContext(ctx, platform, host, owner, name)
	if err != nil {
		return nil, err
	}
	return ReadCommitStats(ctx, dir, commits)
}

// ReadCommitStats batches immutable commit reads for both clones and worktrees.
// Keep this out of commit membership validation: it needs trees, not just history.
func ReadCommitStats(ctx context.Context, dir string, commits []Commit) (map[string]*CommitStats, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	var input strings.Builder
	for _, commit := range commits {
		input.WriteString(commit.SHA)
		input.WriteByte('\n')
	}
	out, stderr, err := runGitCommand(ctx, newGitRunner(), dir, []byte(input.String()),
		"log", "--no-walk=unsorted", "--stdin", "--format=%H", "--numstat", "-z",
		"--root", "--diff-merges=first-parent", "--no-ext-diff", "--no-textconv", "-M", "-C", "--find-copies-harder")
	if err != nil {
		return nil, fmt.Errorf("read commit stats: %w", wrapGitError(err, stderr))
	}
	return parseCommitStats(out)
}

func parseCommitStats(out []byte) (map[string]*CommitStats, error) {
	stats := make(map[string]*CommitStats)
	var current *CommitStats
	records := bytes.Split(out, []byte{0})
	for i := 0; i < len(records); i++ {
		record := strings.TrimLeft(string(records[i]), "\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) == 1 {
			current = &CommitStats{}
			stats[record] = current
			continue
		}
		if len(fields) != 3 || current == nil {
			return nil, fmt.Errorf("unexpected commit numstat record %q", record)
		}
		current.Additions += parseNumstatInt(fields[0])
		current.Deletions += parseNumstatInt(fields[1])
		if fields[2] == "" {
			// With -z, renames/copies have two separate NUL-delimited paths.
			// Skip both even when a path looks like a SHA or contains tabs/newlines.
			if i+2 >= len(records) {
				return nil, fmt.Errorf("incomplete commit numstat rename")
			}
			i += 2
		}
	}
	return stats, nil
}
