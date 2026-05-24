package gitclone

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// emptyTreeSHA is git's well-known SHA for an empty tree object.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Commit holds metadata for a single commit in a PR's history.
type Commit struct {
	SHA            string
	AuthorName     string
	AuthorEmail    string
	AuthoredAt     time.Time
	CommitterName  string
	CommitterEmail string
	CommittedAt    time.Time
	Message        string // first line only
}

type CommitTimelinePoint struct {
	SHA         string
	Message     string
	CommittedAt time.Time
}

// ListCommits returns commits between mergeBase and headSHA, newest first,
// following only the first-parent chain. If mergeBase is the empty tree
// sentinel (parentless root), all commits up to headSHA are returned.
func (m *Manager) ListCommits(
	ctx context.Context,
	host, owner, name, mergeBase, headSHA string,
) ([]Commit, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return nil, err
	}

	args := []string{"log", "--first-parent", "--format=" + commitLogFormat}
	if mergeBase == emptyTreeSHA {
		// Empty tree is not a commit — list all ancestors of head.
		args = append(args, headSHA)
	} else {
		args = append(args, mergeBase+".."+headSHA)
	}

	out, err := m.git(ctx, host, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}

	return parseCommitLog(out)
}

const (
	commitIdentityMaxBytes = 256
	commitMessageMaxBytes  = 512
	commitLogFormat        = "%H%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s"
)

func parseCommitLog(out []byte) ([]Commit, error) {
	var commits []Commit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 8)
		if len(parts) != 8 {
			return nil, fmt.Errorf("unexpected git log line: %q", line)
		}
		authoredAt, err := time.Parse(time.RFC3339, parts[3])
		if err != nil {
			return nil, fmt.Errorf("parse authored date %q: %w", parts[3], err)
		}
		committedAt, err := time.Parse(time.RFC3339, parts[6])
		if err != nil {
			return nil, fmt.Errorf("parse committed date %q: %w", parts[6], err)
		}
		commits = append(commits, Commit{
			SHA:            parts[0],
			AuthorName:     truncateCommitText(parts[1], commitIdentityMaxBytes),
			AuthorEmail:    truncateCommitText(parts[2], commitIdentityMaxBytes),
			AuthoredAt:     authoredAt,
			CommitterName:  truncateCommitText(parts[4], commitIdentityMaxBytes),
			CommitterEmail: truncateCommitText(parts[5], commitIdentityMaxBytes),
			CommittedAt:    committedAt,
			Message:        truncateCommitText(parts[7], commitMessageMaxBytes),
		})
	}
	return commits, scanner.Err()
}

func truncateCommitText(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	truncated := s[:maxBytes]
	for !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

// CommitTimelineSinceTag returns first-parent commits on the default branch
// after tagName. The count covers the full range; points contains the newest
// commits up to limit for compact UI timelines.
func (m *Manager) CommitTimelineSinceTag(
	ctx context.Context,
	host, owner, name, tagName string,
	limit int,
) (int, []CommitTimelinePoint, error) {
	if tagName == "" {
		return 0, nil, nil
	}
	if limit < 1 {
		limit = 1
	}

	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return 0, nil, err
	}
	defaultRef := m.defaultTimelineRef(ctx, host, dir)
	rangeSpec := tagName + ".." + defaultRef
	countOut, err := m.git(ctx, host, dir,
		"rev-list", "--first-parent", "--count", rangeSpec,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("count commits since tag %s: %w", tagName, err)
	}
	countText := strings.TrimSpace(string(countOut))
	var count int
	if _, err := fmt.Sscanf(countText, "%d", &count); err != nil {
		return 0, nil, fmt.Errorf("parse commit count %q: %w", countText, err)
	}

	out, err := m.git(ctx, host, dir,
		"log", "--first-parent",
		fmt.Sprintf("--max-count=%d", limit),
		"--format=%H%x00%aI%x00%s",
		rangeSpec,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("list commit timeline since tag %s: %w", tagName, err)
	}

	var points []CommitTimelinePoint
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			return 0, nil, fmt.Errorf("unexpected git log line: %q", line)
		}
		t, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			return 0, nil, fmt.Errorf("parse commit date %q: %w", parts[1], err)
		}
		points = append(points, CommitTimelinePoint{
			SHA:         parts[0],
			CommittedAt: t,
			Message:     truncateCommitText(parts[2], commitMessageMaxBytes),
		})
	}
	if err := scanner.Err(); err != nil {
		return 0, nil, err
	}
	return count, points, nil
}

func (m *Manager) defaultTimelineRef(
	ctx context.Context,
	host, dir string,
) string {
	if _, err := m.git(ctx, host, dir,
		"rev-parse", "--verify", "refs/remotes/origin/HEAD",
	); err == nil {
		return "refs/remotes/origin/HEAD"
	}

	out, err := m.git(ctx, host, dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "HEAD"
	}
	branch, ok := strings.CutPrefix(
		strings.TrimSpace(string(out)),
		"refs/heads/",
	)
	if !ok || branch == "" {
		return "HEAD"
	}

	remoteRef := "refs/remotes/origin/" + branch
	if _, err := m.git(ctx, host, dir,
		"rev-parse", "--verify", remoteRef,
	); err == nil {
		return remoteRef
	}
	return "HEAD"
}

// ParentOf returns the first parent SHA of the given commit.
// For a parentless (root) commit it returns the empty tree sentinel.
// The caller must ensure sha exists in the clone; any failure here
// is a genuine server-side error, not a client-input issue.
func (m *Manager) ParentOf(
	ctx context.Context,
	host, owner, name, sha string,
) (string, error) {
	dir, err := m.ClonePath(host, owner, name)
	if err != nil {
		return "", err
	}
	out, err := m.git(ctx, host, dir,
		"rev-list", "--parents", "-n", "1", "--end-of-options", sha,
	)
	if err != nil {
		return "", fmt.Errorf("parent of %s: %w", sha, err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "", fmt.Errorf("parent of %s: empty rev-list output", sha)
	}
	if len(fields) == 1 {
		// Parentless commit — diff against the empty tree.
		return emptyTreeSHA, nil
	}
	return fields[1], nil
}
