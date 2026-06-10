package docs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
)

type PorcelainRecord struct {
	X       byte
	Y       byte
	Path    string
	OldPath string
}

func parsePorcelainRecords(data []byte) ([]PorcelainRecord, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	scanner.Split(splitNUL)
	var out []PorcelainRecord
	for scanner.Scan() {
		record := scanner.Bytes()
		if len(record) < 4 {
			continue
		}
		rec := PorcelainRecord{
			X:    record[0],
			Y:    record[1],
			Path: filepath.ToSlash(string(record[3:])),
		}
		if isRenameOrCopy(rec.X) || isRenameOrCopy(rec.Y) {
			if !scanner.Scan() {
				return nil, fmt.Errorf("malformed rename: missing source path for %q", rec.Path)
			}
			rec.OldPath = filepath.ToSlash(string(scanner.Bytes()))
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func isRenameOrCopy(b byte) bool {
	return b == 'R' || b == 'C'
}

type GitChangeStatus string

const (
	GitChangeAdded     GitChangeStatus = "added"
	GitChangeDeleted   GitChangeStatus = "deleted"
	GitChangeModified  GitChangeStatus = "modified"
	GitChangeRenamed   GitChangeStatus = "renamed"
	GitChangeUntracked GitChangeStatus = "untracked"
)

type PublishChange struct {
	Path           string          `json:"path"`
	OldPath        string          `json:"old_path,omitempty"`
	Status         GitChangeStatus `json:"status"`
	WorktreeRename bool            `json:"-"`
}

type PublishSet struct {
	Changes                 []PublishChange
	PartiallyStagedMD       []string
	UnmergedMD              []string
	UnrelatedStagedPresent  bool
	IgnoredNonMarkdownCount int
	IgnoredMarkdownCount    int
}

func computePublishSet(records []PorcelainRecord) PublishSet {
	var set PublishSet
	for _, rec := range records {
		if rec.X == '!' && rec.Y == '!' {
			if isMarkdownPath(rec.Path) {
				set.IgnoredMarkdownCount++
			}
			continue
		}
		if isRenameOrCopy(rec.X) || isRenameOrCopy(rec.Y) {
			newMD := isMarkdownPath(rec.Path)
			oldMD := isMarkdownPath(rec.OldPath)
			if newMD != oldMD {
				set.IgnoredNonMarkdownCount++
				set.UnrelatedStagedPresent = true
				continue
			}
		}
		if !isMarkdownPath(rec.Path) {
			set.IgnoredNonMarkdownCount++
			if isIndexEntry(rec.X) {
				set.UnrelatedStagedPresent = true
			}
			continue
		}
		if isUnmergedPair(rec.X, rec.Y) {
			set.UnmergedMD = append(set.UnmergedMD, rec.Path)
			set.Changes = append(set.Changes, PublishChange{
				Path: rec.Path, Status: GitChangeModified,
			})
			continue
		}
		if isPartialStage(rec.X, rec.Y) {
			set.PartiallyStagedMD = append(set.PartiallyStagedMD, rec.Path)
		}
		change := PublishChange{
			Path:   rec.Path,
			Status: classifyPublish(rec.X, rec.Y),
		}
		if isRenameOrCopy(rec.X) || isRenameOrCopy(rec.Y) {
			change.OldPath = rec.OldPath
			if !isRenameOrCopy(rec.X) && isRenameOrCopy(rec.Y) {
				change.WorktreeRename = true
			}
		}
		set.Changes = append(set.Changes, change)
	}
	return set
}

func isMarkdownPath(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

func isIndexEntry(x byte) bool {
	return x != ' ' && x != '?' && x != '!'
}

func suggestedCommitMessage(changes []PublishChange) string {
	if len(changes) == 0 {
		return ""
	}
	var b strings.Builder
	if len(changes) == 1 {
		fmt.Fprintf(&b, "docs: update %s\n", changes[0].Path)
	} else {
		fmt.Fprintf(&b, "docs: update %d files\n", len(changes))
	}
	b.WriteString("\n")
	for _, c := range changes {
		fmt.Fprintf(&b, "- %s\n", c.Path)
	}
	return b.String()
}

func isPartialStage(x, y byte) bool {
	if x == '?' || x == '!' {
		return false
	}
	if isUnmergedPair(x, y) {
		return false
	}
	return x != ' ' && y != ' '
}

func classifyPublish(x, y byte) GitChangeStatus {
	if x == '?' && y == '?' {
		return GitChangeUntracked
	}
	primary := x
	if primary == ' ' {
		primary = y
	}
	switch primary {
	case 'A':
		return GitChangeAdded
	case 'D':
		return GitChangeDeleted
	case 'R', 'C':
		return GitChangeRenamed
	default:
		return GitChangeModified
	}
}

type GitChangesResponse struct {
	IsRepo                  bool            `json:"is_repo"`
	Branch                  string          `json:"branch,omitempty"`
	Upstream                string          `json:"upstream,omitempty"`
	Changes                 []PublishChange `json:"changes"`
	IgnoredNonMarkdownCount int             `json:"ignored_non_markdown_count"`
	SuggestedMessage        string          `json:"suggested_message,omitempty"`
}

func (r *Registry) GitChanges(ctx context.Context, folderID string) (GitChangesResponse, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return GitChangesResponse{}, err
	}
	if !isGitRepo(v.Path) {
		return GitChangesResponse{IsRepo: false, Changes: []PublishChange{}}, nil
	}
	// The preview is the UI's publishability signal, so it must apply the
	// same command-bearing-config gate publish does — otherwise a repo
	// with unsafe local config previews as publishable and only fails
	// once the user submits. Command-bearing config does not execute
	// during the status read itself (unlike filter attributes), so this
	// gate is for signal parity, not read-time safety.
	if err := assertSafeToPublish(ctx, v.Path); err != nil {
		return GitChangesResponse{}, err
	}
	branch, err := currentBranch(ctx, v.Path)
	if err != nil {
		return GitChangesResponse{}, err
	}
	upstream, _ := currentUpstream(ctx, v.Path, branch)
	if err := assertWorktreeAttributesSafe(ctx, v.Path); err != nil {
		return GitChangesResponse{}, err
	}
	records, err := readPorcelain(ctx, v.Path)
	if err != nil {
		return GitChangesResponse{}, err
	}
	set := computePublishSet(records)
	changes := set.Changes
	if changes == nil {
		changes = []PublishChange{}
	}
	return GitChangesResponse{
		IsRepo:                  true,
		Branch:                  branch,
		Upstream:                upstream,
		Changes:                 changes,
		IgnoredNonMarkdownCount: set.IgnoredNonMarkdownCount,
		SuggestedMessage:        suggestedCommitMessage(set.Changes),
	}, nil
}

func currentBranch(ctx context.Context, root string) (string, error) {
	cmd, err := gitCommand(ctx, root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	out, err := procutil.Output(ctx, cmd, "reading docs git branch")
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func currentUpstream(ctx context.Context, root, branch string) (string, error) {
	cmd, err := gitCommand(ctx, root, "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	if err != nil {
		return "", err
	}
	out, err := procutil.Output(ctx, cmd, "reading docs git upstream")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func currentUpstreamPushTarget(ctx context.Context, root, branch string) (remote, mergeRef string, err error) {
	remoteCmd, err := gitCommand(ctx, root, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		return "", "", err
	}
	remoteOut, err := procutil.Output(ctx, remoteCmd, "reading docs git upstream remote")
	if err != nil {
		return "", "", err
	}
	mergeCmd, err := gitCommand(ctx, root, "config", "--get", "branch."+branch+".merge")
	if err != nil {
		return "", "", err
	}
	mergeOut, err := procutil.Output(ctx, mergeCmd, "reading docs git upstream merge ref")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(remoteOut)), strings.TrimSpace(string(mergeOut)), nil
}

func readPorcelain(ctx context.Context, root string) ([]PorcelainRecord, error) {
	cmd, err := gitCommand(ctx, root,
		"-c", "color.status=false",
		"status", "--porcelain=v1", "-z",
		"--untracked-files=all",
		"--ignored",
	)
	if err != nil {
		return nil, err
	}
	out, err := procutil.Output(ctx, cmd, "reading docs git status")
	if err != nil {
		stderr := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git status: %w: %s", err, stderr)
	}
	return parsePorcelainRecords(out)
}

var (
	ErrEmptyMessage      = errors.New("commit message is empty")
	ErrNoMarkdownChanges = errors.New("no markdown changes to publish")
	ErrIndexNotClean     = errors.New("index contains changes outside the publish set or partial stages")
	ErrConflict          = errors.New("markdown publish set contains unmerged entries")
)

type CommitFailedError struct {
	Stderr string
}

func (e *CommitFailedError) Error() string {
	return fmt.Sprintf("git commit failed: %s", e.Stderr)
}

type PushFailedAfterCommitError struct {
	Commit string
	Stderr string
}

func (e *PushFailedAfterCommitError) Error() string {
	return fmt.Sprintf("commit %s succeeded but push failed: %s", e.Commit, e.Stderr)
}

type NoUpstreamError struct {
	Branch           string
	SuggestedCommand string
}

func (e *NoUpstreamError) Error() string {
	return fmt.Sprintf("no upstream configured for %s; run: %s", e.Branch, e.SuggestedCommand)
}

type PublishResponse struct {
	Commit      string          `json:"commit"`
	ShortCommit string          `json:"short_commit"`
	Branch      string          `json:"branch"`
	Upstream    string          `json:"upstream,omitempty"`
	Pushed      bool            `json:"pushed"`
	Files       []PublishChange `json:"files"`
}

func (r *Registry) GitPublish(ctx context.Context, folderID, message string) (PublishResponse, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return PublishResponse{}, err
	}
	if !isGitRepo(v.Path) {
		return PublishResponse{}, ErrNotAGitRepo
	}
	if strings.TrimSpace(message) == "" {
		return PublishResponse{}, ErrEmptyMessage
	}
	if err := assertSafeToPublish(ctx, v.Path); err != nil {
		return PublishResponse{}, err
	}
	// Gate attributes before git status: status rehashes modified tracked
	// files through any configured filter, so a repo-controlled filter
	// attribute must be rejected before status runs, not after.
	if err := assertWorktreeAttributesSafe(ctx, v.Path); err != nil {
		return PublishResponse{}, err
	}
	records, err := readPorcelain(ctx, v.Path)
	if err != nil {
		return PublishResponse{}, err
	}
	set := computePublishSet(records)
	if len(set.UnmergedMD) > 0 {
		return PublishResponse{}, ErrConflict
	}
	if len(set.PartiallyStagedMD) > 0 || set.UnrelatedStagedPresent {
		return PublishResponse{}, ErrIndexNotClean
	}
	publishable := filterPublishable(set.Changes, set.UnmergedMD)
	if len(publishable) == 0 {
		return PublishResponse{}, ErrNoMarkdownChanges
	}
	branch, err := currentBranch(ctx, v.Path)
	if err != nil {
		return PublishResponse{}, err
	}
	upstream, err := currentUpstream(ctx, v.Path, branch)
	if err != nil || upstream == "" {
		return PublishResponse{}, &NoUpstreamError{
			Branch:           branch,
			SuggestedCommand: fmt.Sprintf("git push -u origin %s", branch),
		}
	}
	upstreamRemote, upstreamMergeRef, err := currentUpstreamPushTarget(ctx, v.Path, branch)
	if err != nil || upstreamRemote == "" || upstreamMergeRef == "" {
		return PublishResponse{}, &NoUpstreamError{
			Branch:           branch,
			SuggestedCommand: fmt.Sprintf("git push -u origin %s", branch),
		}
	}
	// Validate where the push will land before staging or committing, so
	// an unsafe push target cannot leave a half-finished publish (commit
	// created, push refused) behind.
	pushTarget, err := assertPushTargetSafe(ctx, v.Path, upstreamRemote)
	if err != nil {
		return PublishResponse{}, err
	}
	if err := stagePublishSet(ctx, v.Path, publishable); err != nil {
		return PublishResponse{}, fmt.Errorf("git add: %w", err)
	}
	commitSHA, err := runCommit(ctx, v.Path, message)
	if err != nil {
		return PublishResponse{}, err
	}
	if stderr, err := runPush(ctx, v.Path, upstreamRemote, upstreamMergeRef, pushTarget); err != nil {
		return PublishResponse{}, &PushFailedAfterCommitError{
			Commit: commitSHA,
			Stderr: stderr,
		}
	}
	return PublishResponse{
		Commit:      commitSHA,
		ShortCommit: commitSHA[:7],
		Branch:      branch,
		Upstream:    upstream,
		Pushed:      true,
		Files:       publishable,
	}, nil
}

func filterPublishable(changes []PublishChange, unmerged []string) []PublishChange {
	if len(unmerged) == 0 {
		return changes
	}
	skip := make(map[string]struct{}, len(unmerged))
	for _, p := range unmerged {
		skip[p] = struct{}{}
	}
	out := make([]PublishChange, 0, len(changes))
	for _, c := range changes {
		if _, drop := skip[c.Path]; drop {
			continue
		}
		out = append(out, c)
	}
	return out
}

func stagePublishSet(ctx context.Context, root string, changes []PublishChange) error {
	args := []string{"--literal-pathspecs", "add", "-A", "--"}
	for _, c := range changes {
		args = append(args, c.Path)
		if c.OldPath == "" {
			continue
		}
		if c.WorktreeRename {
			args = append(args, c.OldPath)
			continue
		}
		_, statErr := os.Stat(filepath.Join(root, c.OldPath))
		if statErr == nil {
			args = append(args, c.OldPath)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("stat old path %s: %w", c.OldPath, statErr)
		}
	}
	cmd, err := gitCommand(ctx, root, args...)
	if err != nil {
		return err
	}
	out, err := procutil.CombinedOutput(ctx, cmd, "staging docs git changes")
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runCommit(ctx context.Context, root, message string) (string, error) {
	messageFile, err := os.CreateTemp("", "middleman-docs-commit-message-*")
	if err != nil {
		return "", err
	}
	messagePath := messageFile.Name()
	defer func() { _ = os.Remove(messagePath) }()
	if _, err := messageFile.WriteString(message); err != nil {
		_ = messageFile.Close()
		return "", err
	}
	if err := messageFile.Close(); err != nil {
		return "", err
	}

	cmd, err := gitCommand(ctx, root, "commit", "-F", messagePath)
	if err != nil {
		return "", err
	}
	out, err := procutil.CombinedOutput(ctx, cmd, "committing docs git changes")
	if err != nil {
		return "", &CommitFailedError{Stderr: strings.TrimSpace(string(out))}
	}
	cmd, err = gitCommand(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	headOut, err := procutil.Output(ctx, cmd, "reading docs git commit")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(headOut)), nil
}

func runPush(ctx context.Context, root, remote, mergeRef string, target pushTargetClass) (string, error) {
	args := []string{"push"}
	if target == pushTargetLocal {
		receivePack, err := localReceivePack()
		if err != nil {
			return "", err
		}
		args = append(args, "--receive-pack="+receivePack)
	}
	args = append(args, remote, "HEAD:"+mergeRef)
	cmd, err := gitCommand(ctx, root, args...)
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, "pushing docs git changes"); err != nil {
		raw := strings.TrimSpace(stderr.String())
		if raw == "" {
			raw = err.Error()
		}
		return raw, err
	}
	return "", nil
}
