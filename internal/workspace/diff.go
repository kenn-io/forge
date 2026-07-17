package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/procutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type WorktreeDiffBase string

const (
	WorktreeDiffBaseHead        WorktreeDiffBase = "head"
	WorktreeDiffBasePushed      WorktreeDiffBase = "pushed"
	WorktreeDiffBaseMergeTarget WorktreeDiffBase = "merge-target"
)

const maxUntrackedTextFileBytes = 1 << 20

var workspaceDiffTracer = otel.Tracer("go.kenn.io/middleman/internal/workspace/diff")

func WorktreeDiffFiles(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
	hideWhitespace bool,
) ([]gitclone.DiffFile, bool, error) {
	baseRef, ok, err := worktreeDiffBaseRef(ctx, dir, base)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFilesFromRef(ctx, dir, baseRef, hideWhitespace)
}

func WorktreeDiffWhitespaceOnlyCount(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
) (int, bool, error) {
	baseRef, ok, err := worktreeDiffBaseRef(ctx, dir, base)
	if err != nil || !ok {
		return 0, ok, err
	}

	count, err := worktreeWhitespaceOnlyCount(ctx, dir, baseRef, "", "")
	return count, true, err
}

func WorktreeDiffFilesAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
	hideWhitespace bool,
) ([]gitclone.DiffFile, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFilesFromRef(ctx, dir, baseRef, hideWhitespace)
}

func WorktreeDiffWhitespaceOnlyCountAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
) (int, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return 0, ok, err
	}

	count, err := worktreeWhitespaceOnlyCount(ctx, dir, baseRef, "", "")
	return count, true, err
}

func WorktreeDiffWhitespaceOnlyCountBetween(
	ctx context.Context,
	dir string,
	fromRef string,
	toRef string,
) (int, bool, error) {
	count, err := worktreeWhitespaceOnlyCount(ctx, dir, fromRef, toRef, "")
	return count, err == nil, err
}

func worktreeDiffFilesFromRef(
	ctx context.Context,
	dir string,
	baseRef string,
	hideWhitespace bool,
) ([]gitclone.DiffFile, bool, error) {
	files, err := worktreeDiffFilesFromRefs(
		ctx, dir, baseRef, "", hideWhitespace, true,
	)
	return files, err == nil, err
}

func WorktreeDiffFilesBetween(
	ctx context.Context,
	dir string,
	fromRef string,
	toRef string,
	hideWhitespace bool,
) ([]gitclone.DiffFile, bool, error) {
	files, err := worktreeDiffFilesFromRefs(
		ctx, dir, fromRef, toRef, hideWhitespace, false,
	)
	return files, err == nil, err
}

func worktreeDiffFilesFromRefs(
	ctx context.Context,
	dir string,
	baseRef string,
	headRef string,
	hideWhitespace bool,
	includeUntracked bool,
) ([]gitclone.DiffFile, error) {
	rawArgs := appendWorktreeHeadRef(gitclone.AddDiffWhitespaceFlag(gitclone.DiffArgs(
		"--raw", "-z", "-M", "-C", "--find-copies-harder", baseRef,
	), hideWhitespace), headRef)
	rawOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.raw", dir, rawArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff --raw: %w", err)
	}
	files := gitclone.ParseRawZ(rawOut)
	if files == nil {
		files = []gitclone.DiffFile{}
	}

	numstatArgs := appendWorktreeHeadRef(gitclone.AddDiffWhitespaceFlag(gitclone.DiffArgs(
		"--numstat", "-z", "-M", "-C", "--find-copies-harder", baseRef,
	), hideWhitespace), headRef)
	numstatOut, err := worktreeDiffGitPhase(
		ctx, "workspace.diff.git.numstat", dir, numstatArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}
	counts := parseWorktreeNumstatZ(numstatOut)
	applyWorktreeNumstat(files, counts)
	whitespaceFiles, err := worktreeWhitespaceOnlyFilesAggregate(
		ctx, dir, baseRef, headRef, "",
	)
	if err != nil {
		return nil, fmt.Errorf("whitespace files: %w", err)
	}
	for i := range files {
		files[i].IsWhitespaceOnly = whitespaceFiles[files[i].Path]
	}
	if hideWhitespace {
		filtered := files[:0]
		for i := range files {
			if files[i].Status == "modified" && whitespaceFiles[files[i].Path] {
				continue
			}
			filtered = append(filtered, files[i])
		}
		files = filtered
	}
	if includeUntracked {
		files = append(files, worktreeUntrackedFiles(ctx, dir, false, hideWhitespace)...)
	}
	markWorktreeGeneratedFiles(ctx, dir, files)
	gitclone.SortDiffFiles(files)
	return files, nil
}

func WorktreeDiff(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
	hideWhitespace bool,
) (*gitclone.DiffResult, bool, error) {
	baseRef, ok, err := worktreeDiffBaseRef(ctx, dir, base)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFromRef(ctx, dir, baseRef, hideWhitespace)
}

func WorktreeFileDiff(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
	hideWhitespace bool,
	path string,
) (*gitclone.DiffResult, bool, error) {
	baseRef, ok, err := worktreeDiffBaseRef(ctx, dir, base)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFromRefPath(ctx, dir, baseRef, hideWhitespace, path)
}

func WorktreeFileContent(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
	hideWhitespace bool,
	path string,
	side string,
	maxBytes int64,
) (*gitclone.FileContent, bool, error) {
	baseRef, ok, err := worktreeDiffBaseRef(ctx, dir, base)
	if err != nil || !ok {
		return nil, ok, err
	}

	content, err := worktreeFileContentFromRefs(
		ctx, dir, baseRef, "", hideWhitespace, path, side, true, maxBytes,
	)
	return content, err == nil, err
}

func WorktreeDiffAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
	hideWhitespace bool,
) (*gitclone.DiffResult, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFromRef(ctx, dir, baseRef, hideWhitespace)
}

func WorktreeFileDiffAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
	hideWhitespace bool,
	path string,
) (*gitclone.DiffResult, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return nil, ok, err
	}

	return worktreeDiffFromRefPath(ctx, dir, baseRef, hideWhitespace, path)
}

func WorktreeFileContentAgainstMergeTarget(
	ctx context.Context,
	dir string,
	targetBranch string,
	hideWhitespace bool,
	path string,
	side string,
	maxBytes int64,
) (*gitclone.FileContent, bool, error) {
	baseRef, ok, err := worktreeMergeTargetBaseRef(ctx, dir, targetBranch)
	if err != nil || !ok {
		return nil, ok, err
	}

	content, err := worktreeFileContentFromRefs(
		ctx, dir, baseRef, "", hideWhitespace, path, side, true, maxBytes,
	)
	return content, err == nil, err
}

func worktreeDiffFromRef(
	ctx context.Context,
	dir string,
	baseRef string,
	hideWhitespace bool,
) (*gitclone.DiffResult, bool, error) {
	return worktreeDiffFromRefPath(ctx, dir, baseRef, hideWhitespace, "")
}

func WorktreeDiffBetween(
	ctx context.Context,
	dir string,
	fromRef string,
	toRef string,
	hideWhitespace bool,
) (*gitclone.DiffResult, bool, error) {
	result, err := worktreeDiffFromRefsPath(
		ctx, dir, fromRef, toRef, hideWhitespace, "", false,
	)
	return result, err == nil, err
}

func WorktreeFileContentBetween(
	ctx context.Context,
	dir string,
	fromRef string,
	toRef string,
	hideWhitespace bool,
	path string,
	side string,
	maxBytes int64,
) (*gitclone.FileContent, bool, error) {
	content, err := worktreeFileContentFromRefs(
		ctx, dir, fromRef, toRef, hideWhitespace, path, side, false, maxBytes,
	)
	return content, err == nil, err
}

func WorktreeFileDiffBetween(
	ctx context.Context,
	dir string,
	fromRef string,
	toRef string,
	hideWhitespace bool,
	path string,
) (*gitclone.DiffResult, bool, error) {
	result, err := worktreeDiffFromRefsPath(
		ctx, dir, fromRef, toRef, hideWhitespace, path, false,
	)
	return result, err == nil, err
}

func worktreeFileContentFromRefs(
	ctx context.Context,
	dir string,
	baseRef string,
	headRef string,
	hideWhitespace bool,
	path string,
	side string,
	includeUntracked bool,
	maxBytes int64,
) (*gitclone.FileContent, error) {
	path, err := cleanWorktreeDiffPath(path)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("diff path is required")
	}

	diff, err := worktreeDiffFromRefsPath(
		ctx, dir, baseRef, headRef, hideWhitespace, path, includeUntracked,
	)
	if err != nil {
		return nil, err
	}

	var file *gitclone.DiffFile
	for i := range diff.Files {
		if diff.Files[i].Path == path {
			file = &diff.Files[i]
			break
		}
	}
	if file == nil {
		return nil, gitclone.ErrNotFound
	}

	ref := headRef
	previewPath := file.Path
	useWorktree := headRef == ""
	switch side {
	case "old":
		if file.Status == "added" {
			return nil, gitclone.ErrNotFound
		}
		ref = baseRef
		previewPath = file.OldPath
		if previewPath == "" {
			previewPath = file.Path
		}
		useWorktree = false
	case "new":
		if file.Status == "deleted" {
			return nil, gitclone.ErrNotFound
		}
	case "":
		if file.Status == "deleted" {
			ref = baseRef
			previewPath = file.OldPath
			if previewPath == "" {
				previewPath = file.Path
			}
			useWorktree = false
		}
	default:
		return nil, errors.New("side must be old or new")
	}

	previewPath, err = cleanWorktreeDiffPath(previewPath)
	if err != nil {
		return nil, err
	}
	if useWorktree {
		return readWorktreeFileContent(dir, previewPath, maxBytes)
	}
	return worktreeBlobContent(ctx, dir, ref, previewPath, maxBytes)
}

func worktreeDiffFromRefPath(
	ctx context.Context,
	dir string,
	baseRef string,
	hideWhitespace bool,
	path string,
) (*gitclone.DiffResult, bool, error) {
	result, err := worktreeDiffFromRefsPath(
		ctx, dir, baseRef, "", hideWhitespace, path, true,
	)
	return result, err == nil, err
}

func worktreeDiffFromRefsPath(
	ctx context.Context,
	dir string,
	baseRef string,
	headRef string,
	hideWhitespace bool,
	path string,
	includeUntracked bool,
) (*gitclone.DiffResult, error) {
	path, err := cleanWorktreeDiffPath(path)
	if err != nil {
		return nil, err
	}

	rawArgs := appendWorktreeHeadRef(gitclone.AddDiffWhitespaceFlag(gitclone.DiffArgs(
		"--raw", "-z", "-M", "-C", "--find-copies-harder",
		baseRef,
	), hideWhitespace), headRef)
	rawArgs = appendWorktreePathspec(rawArgs, path)
	rawOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.raw", dir, rawArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff --raw: %w", err)
	}
	files := gitclone.ParseRawZ(rawOut)

	numstatArgs := appendWorktreeHeadRef(gitclone.AddDiffWhitespaceFlag(gitclone.DiffArgs(
		"--numstat", "-z", "-M", "-C", "--find-copies-harder",
		baseRef,
	), hideWhitespace), headRef)
	numstatArgs = appendWorktreePathspec(numstatArgs, path)
	numstatOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.numstat", dir, numstatArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	patchArgs := appendWorktreeHeadRef(gitclone.AddDiffWhitespaceFlag(gitclone.DiffArgs(
		"-M", "-C", "--find-copies-harder", "-U3", baseRef,
	), hideWhitespace), headRef)
	patchArgs = appendWorktreePathspec(patchArgs, path)
	patchOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.patch", dir, patchArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff patch: %w", err)
	}
	assembleCtx, assembleSpan := workspaceDiffTracer.Start(ctx, "workspace.diff.assemble")
	defer assembleSpan.End()
	files = gitclone.ParsePatch(patchOut, files)
	if files == nil {
		files = []gitclone.DiffFile{}
	}
	counts := parseWorktreeNumstatZ(numstatOut)
	applyWorktreeNumstat(files, counts)
	if hideWhitespace {
		files = dropWhitespaceOnlyModifications(files, counts)
	}

	wsCount := 0
	whitespaceCtx, whitespaceSpan := workspaceDiffTracer.Start(
		assembleCtx, "workspace.diff.whitespace",
	)
	if hideWhitespace {
		wsCount, err = worktreeWhitespaceOnlyCountFromPatch(
			whitespaceCtx, dir, baseRef, headRef, path,
		)
		if err != nil {
			whitespaceSpan.RecordError(err)
			whitespaceSpan.End()
			return nil, fmt.Errorf("whitespace count: %w", err)
		}
	} else {
		wsCount = classifyWhitespaceOnly(files, worktreeRawModeChanges(rawOut))
	}
	whitespaceSpan.SetAttributes(attribute.Int("workspace.diff.whitespace_only_files", wsCount))
	whitespaceSpan.End()
	untrackedCtx, untrackedSpan := workspaceDiffTracer.Start(
		assembleCtx, "workspace.diff.untracked",
	)
	if includeUntracked && path == "" {
		files = append(files, worktreeUntrackedFiles(untrackedCtx, dir, true, hideWhitespace)...)
	} else if includeUntracked {
		if file, ok := worktreeUntrackedFile(
			untrackedCtx, dir, path, true, hideWhitespace,
		); ok {
			files = append(files, file)
		}
	}
	untrackedSpan.End()
	generatedCtx, generatedSpan := workspaceDiffTracer.Start(
		assembleCtx, "workspace.diff.generated_attributes",
	)
	markWorktreeGeneratedFiles(generatedCtx, dir, files)
	generatedSpan.End()
	gitclone.SortDiffFiles(files)
	assembleSpan.SetAttributes(attribute.Int("workspace.diff.file_count", len(files)))

	return &gitclone.DiffResult{
		WhitespaceOnlyCount: wsCount,
		Files:               files,
	}, nil
}

func worktreeBlobContent(
	ctx context.Context,
	dir string,
	ref string,
	path string,
	maxBytes int64,
) (*gitclone.FileContent, error) {
	object := ref + ":" + path
	sizeOut, err := worktreeGitOutput(ctx, dir, "cat-file", "-s", object)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", gitclone.ErrNotFound, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse blob size: %w", err)
	}
	if maxBytes > 0 && size > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", gitclone.ErrTooLarge, size)
	}
	data, err := worktreeGitOutput(ctx, dir, "cat-file", "blob", object)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", gitclone.ErrNotFound, err)
	}
	return &gitclone.FileContent{Path: path, Data: data, Size: size}, nil
}

func readWorktreeFileContent(
	dir string,
	path string,
	maxBytes int64,
) (*gitclone.FileContent, error) {
	fullPath := filepath.Join(dir, path)
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", gitclone.ErrNotFound, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", gitclone.ErrNotFound, err)
		}
		data := []byte(target)
		if maxBytes > 0 && int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("%w: %d bytes", gitclone.ErrTooLarge, len(data))
		}
		return &gitclone.FileContent{Path: path, Data: data, Size: int64(len(data))}, nil
	}
	if !info.Mode().IsRegular() {
		return nil, gitclone.ErrNotFound
	}

	file, info, err := openRegularUntrackedFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", gitclone.ErrNotFound, err)
	}
	defer file.Close()
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", gitclone.ErrTooLarge, info.Size())
	}
	limit := info.Size()
	if maxBytes > 0 {
		limit = maxBytes + 1
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", gitclone.ErrTooLarge, len(data))
	}
	return &gitclone.FileContent{Path: path, Data: data, Size: info.Size()}, nil
}

func markWorktreeGeneratedFiles(
	ctx context.Context,
	dir string,
	files []gitclone.DiffFile,
) {
	if len(files) == 0 {
		return
	}
	generated := map[string]bool{}
	input := gitclone.GeneratedAttributeInput(files)
	if len(input) > 0 {
		out, err := worktreeGitOutputWithInput(
			ctx, dir, input,
			"check-attr", "-z", "--stdin", "linguist-generated",
		)
		if err == nil {
			generated = gitclone.ParseLinguistGeneratedAttributes(out)
		}
	}
	gitclone.MarkGeneratedFiles(files, generated)
}

func applyWorktreeNumstat(
	files []gitclone.DiffFile,
	counts map[string]worktreeNumstatCount,
) {
	for i := range files {
		if count, ok := counts[files[i].Path]; ok {
			files[i].Additions = count.additions
			files[i].Deletions = count.deletions
		}
		if files[i].Hunks == nil {
			files[i].Hunks = []gitclone.Hunk{}
		}
	}
}

// dropWhitespaceOnlyModifications removes "modified" entries that --raw lists
// but --numstat omits under -w. git's --raw output ignores -w (it compares
// blob SHAs), while --numstat honors it, so absence from the numstat map
// reliably indicates a whitespace-only modification. Renames, copies, adds,
// and deletes are preserved since their inclusion in --raw still represents
// a real history change even with 0/0 counts.
func dropWhitespaceOnlyModifications(
	files []gitclone.DiffFile,
	counts map[string]worktreeNumstatCount,
) []gitclone.DiffFile {
	out := files[:0]
	for i := range files {
		if files[i].Status == "modified" {
			if _, ok := counts[files[i].Path]; !ok {
				continue
			}
		}
		out = append(out, files[i])
	}
	return out
}

func appendWorktreePathspec(args []string, path string) []string {
	if path == "" {
		return args
	}
	return append(args, "--", path)
}

func appendWorktreeHeadRef(args []string, headRef string) []string {
	if headRef == "" {
		return args
	}
	return append(args, headRef)
}

func cleanWorktreeDiffPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.Contains(path, "\x00") {
		return "", errors.New("diff path contains NUL byte")
	}
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "/") {
		return "", errors.New("diff path must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." ||
		clean == ".." ||
		strings.HasPrefix(clean, "../") {
		return "", errors.New("diff path must stay inside worktree")
	}
	return clean, nil
}

func worktreeUntrackedFiles(
	ctx context.Context,
	dir string,
	withHunks bool,
	hideWhitespace bool,
) []gitclone.DiffFile {
	out, err := worktreeGitOutput(
		ctx, dir, "ls-files", "--others", "--exclude-standard", "-z",
	)
	if err != nil {
		return nil
	}
	parts := bytes.Split(out, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path := string(part)
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	return worktreeUntrackedFilesFromPaths(
		dir, paths, withHunks, hideWhitespace,
	)
}

func worktreeUntrackedFile(
	ctx context.Context,
	dir string,
	path string,
	withHunks bool,
	hideWhitespace bool,
) (gitclone.DiffFile, bool) {
	out, err := worktreeGitOutput(
		ctx, dir, "ls-files", "--others", "--exclude-standard", "-z",
		"--", path,
	)
	if err != nil {
		return gitclone.DiffFile{}, false
	}
	for part := range bytes.SplitSeq(out, []byte{0}) {
		if string(part) != path {
			continue
		}
		files := worktreeUntrackedFilesFromPaths(
			dir, []string{path}, withHunks, hideWhitespace,
		)
		if len(files) == 0 {
			return gitclone.DiffFile{}, false
		}
		return files[0], true
	}
	return gitclone.DiffFile{}, false
}

func worktreeUntrackedFilesFromPaths(
	dir string,
	paths []string,
	withHunks bool,
	hideWhitespace bool,
) []gitclone.DiffFile {
	files := make([]gitclone.DiffFile, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		file := gitclone.DiffFile{
			Path:    filepath.ToSlash(path),
			OldPath: filepath.ToSlash(path),
			Status:  "added",
			Hunks:   []gitclone.Hunk{},
		}
		content, ok := readUntrackedFileContent(
			filepath.Join(dir, path),
		)
		if !ok {
			continue
		}
		if content == nil {
			file.IsBinary = true
			files = append(files, file)
			continue
		}
		if hideWhitespace && len(content) > 0 &&
			!bytes.Contains(content, []byte{0}) &&
			len(bytes.TrimSpace(content)) == 0 {
			continue
		}
		file.Additions = countAddedLines(content)
		if bytes.Contains(content, []byte{0}) {
			file.IsBinary = true
		} else if withHunks {
			file.Hunks = []gitclone.Hunk{
				untrackedFileHunk(content),
			}
			file.Patch = gitclone.BuildPatch(file)
		}
		files = append(files, file)
	}
	return files
}

func readUntrackedFileContent(path string) ([]byte, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return nil, false
		}
		return []byte(target), true
	}
	if !info.Mode().IsRegular() {
		return nil, false
	}
	file, info, err := openRegularUntrackedFile(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	if info.Size() > maxUntrackedTextFileBytes {
		return nil, true
	}
	content, err := io.ReadAll(io.LimitReader(file, maxUntrackedTextFileBytes+1))
	if err != nil {
		return nil, false
	}
	if len(content) > maxUntrackedTextFileBytes {
		return nil, true
	}
	return content, true
}

func countAddedLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}

func untrackedFileHunk(content []byte) gitclone.Hunk {
	text := string(content)
	rawLines := strings.Split(text, "\n")
	lines := make([]gitclone.Line, 0, len(rawLines))
	for i, line := range rawLines {
		if i == len(rawLines)-1 && line == "" {
			continue
		}
		lines = append(lines, gitclone.Line{
			Type:      "add",
			Content:   line,
			NewNum:    len(lines) + 1,
			NoNewline: i == len(rawLines)-1 && !strings.HasSuffix(text, "\n"),
		})
	}
	return gitclone.Hunk{
		OldStart: 0,
		OldCount: 0,
		NewStart: 1,
		NewCount: len(lines),
		Lines:    lines,
	}
}

type worktreeNumstatCount struct {
	additions int
	deletions int
}

func parseWorktreeNumstatZ(data []byte) map[string]worktreeNumstatCount {
	records := bytes.Split(data, []byte{0})
	counts := make(map[string]worktreeNumstatCount)
	for i := 0; i < len(records); {
		record := string(records[i])
		if record == "" {
			i++
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) < 3 {
			i++
			continue
		}
		path := fields[2]
		if path == "" && i+2 < len(records) {
			path = string(records[i+2])
			i += 3
		} else {
			i++
		}
		if path == "" {
			continue
		}
		counts[path] = worktreeNumstatCount{
			additions: parseWorktreeNumstatInt(fields[0]),
			deletions: parseWorktreeNumstatInt(fields[1]),
		}
	}
	return counts
}

func parseWorktreeNumstatInt(value string) int {
	if value == "-" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

func worktreeWhitespaceOnlyCount(
	ctx context.Context, dir string, baseRef string, headRef string, path string,
) (int, error) {
	files, err := worktreeWhitespaceOnlyFilesAggregate(ctx, dir, baseRef, headRef, path)
	return len(files), err
}

func worktreeWhitespaceOnlyFilesAggregate(
	ctx context.Context, dir string, baseRef string, headRef string, path string,
) (map[string]bool, error) {
	rawArgs := appendWorktreePathspec(appendWorktreeHeadRef(gitclone.DiffArgs(
		"--raw", "-z", "--no-renames", baseRef,
	), headRef), path)
	rawOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.raw", dir, rawArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff --raw: %w", err)
	}
	numstatArgs := appendWorktreePathspec(appendWorktreeHeadRef(gitclone.DiffArgs(
		"--numstat", "-z", "--no-renames", "-w", baseRef,
	), headRef), path)
	numstatOut, err := worktreeDiffGitPhase(
		ctx, "workspace.diff.git.numstat", dir, numstatArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat -w: %w", err)
	}
	nonWhitespace := parseWorktreeNumstatZ(numstatOut)
	modeChanges := worktreeRawModeChanges(rawOut)
	result := make(map[string]bool)
	for _, file := range gitclone.ParseRawZ(rawOut) {
		if file.Status != "modified" || modeChanges[file.Path] {
			continue
		}
		if _, ok := nonWhitespace[file.Path]; !ok {
			result[file.Path] = true
		}
	}
	return result, nil
}

func worktreeWhitespaceOnlyCountFromPatch(
	ctx context.Context, dir string, baseRef string, headRef string, path string,
) (int, error) {
	rawArgs := appendWorktreePathspec(appendWorktreeHeadRef(gitclone.DiffArgs(
		"--raw", "-z", "-M", "-C", "--find-copies-harder", baseRef,
	), headRef), path)
	rawOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.raw", dir, rawArgs...)
	if err != nil {
		return 0, fmt.Errorf("git diff --raw: %w", err)
	}
	patchArgs := appendWorktreePathspec(appendWorktreeHeadRef(gitclone.DiffArgs(
		"-M", "-C", "--find-copies-harder", "-U3", baseRef,
	), headRef), path)
	patchOut, err := worktreeDiffGitPhase(ctx, "workspace.diff.git.patch", dir, patchArgs...)
	if err != nil {
		return 0, fmt.Errorf("git diff patch: %w", err)
	}
	files := gitclone.ParsePatch(patchOut, gitclone.ParseRawZ(rawOut))
	if files == nil {
		files = []gitclone.DiffFile{}
	}
	return classifyWhitespaceOnly(files, worktreeRawModeChanges(rawOut)), nil
}

func worktreeRawModeChanges(data []byte) map[string]bool {
	parts := bytes.Split(data, []byte{0})
	changed := make(map[string]bool)
	for i := 0; i < len(parts); i++ {
		header := string(parts[i])
		if !strings.HasPrefix(header, ":") {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) < 5 {
			continue
		}
		i++
		if i >= len(parts) {
			break
		}
		path := string(parts[i])
		status := fields[4]
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			i++
			if i >= len(parts) {
				break
			}
			path = string(parts[i])
		}
		if fields[0] != ":"+fields[1] && path != "" {
			changed[path] = true
		}
	}
	return changed
}

func worktreeDiffBaseRef(
	ctx context.Context,
	dir string,
	base WorktreeDiffBase,
) (string, bool, error) {
	switch base {
	case WorktreeDiffBaseHead:
		return "HEAD", true, nil
	case WorktreeDiffBasePushed:
		_, ok, err := WorktreeDivergence(ctx, dir)
		if err != nil || !ok {
			return "", ok, err
		}
		return "@{upstream}", true, nil
	default:
		return "", false, fmt.Errorf("unknown worktree diff base %q", base)
	}
}

func worktreeMergeTargetBaseRef(
	ctx context.Context,
	dir string,
	targetBranch string,
) (string, bool, error) {
	targetBranch = strings.TrimSpace(targetBranch)
	if targetBranch == "" {
		return "", false, nil
	}
	if _, err := worktreeGitOutput(
		ctx, dir, "check-ref-format", "--branch", targetBranch,
	); err != nil {
		return "", false, nil
	}

	targetRef := "refs/remotes/origin/" + targetBranch
	if _, err := worktreeGitOutput(
		ctx, dir, "rev-parse", "--verify", "--quiet",
		targetRef+"^{commit}",
	); err != nil {
		return "", false, nil
	}
	out, err := worktreeGitOutput(
		ctx, dir, "merge-base", targetRef, "HEAD",
	)
	if err != nil {
		return "", false, fmt.Errorf("git merge-base: %w", err)
	}
	baseRef := strings.TrimSpace(string(out))
	if baseRef == "" {
		return "", false, nil
	}
	return baseRef, true, nil
}

func worktreeGitOutput(
	ctx context.Context,
	dir string,
	args ...string,
) ([]byte, error) {
	return worktreeGitOutputWithInput(ctx, dir, nil, args...)
}

func worktreeDiffGitPhase(
	ctx context.Context,
	name string,
	dir string,
	args ...string,
) ([]byte, error) {
	phaseCtx, span := workspaceDiffTracer.Start(ctx, name)
	defer span.End()
	out, err := worktreeGitOutput(phaseCtx, dir, args...)
	span.SetAttributes(attribute.Int("workspace.diff.output_bytes", len(out)))
	if err != nil {
		span.RecordError(err)
	}
	return out, err
}

func worktreeGitOutputWithInput(
	ctx context.Context,
	dir string,
	input []byte,
	args ...string,
) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("empty worktree dir")
	}
	// gitcmd.New provides middleman's required git hygiene for workspace
	// reads: strip inherited GIT_* hook state, ignore user/system config,
	// and disable interactive prompts. The procutil wrapper below preserves
	// the app-wide git subprocess capacity guard for potentially expensive
	// diff commands.
	cmd := gitcmd.New().Command(ctx, dir, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := procutil.Output(ctx, cmd, "git workspace diff subprocess capacity")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
