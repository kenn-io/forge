package gitclone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrTooLarge is returned when a requested blob exceeds the caller's limit.
var ErrTooLarge = errors.New("git blob too large")

// DiffFiles returns file metadata (path, status, renames) without patch
// content. It combines git diff --raw and --numstat, which is much faster
// than a full patch diff for large PRs.
func (m *Manager) DiffFiles(
	ctx context.Context,
	host, owner, name, mergeBase, headSHA string,
) ([]DiffFile, error) {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return nil, err
	}
	rawOut, err := m.git(ctx, host, clonePath,
		"diff", "--raw", "-z", "-M", "-C",
		"--find-copies-harder", "--end-of-options", mergeBase, headSHA,
	)
	if err != nil {
		return nil, fmt.Errorf("git diff --raw: %w", err)
	}
	files := ParseRawZ(rawOut)
	if files == nil {
		files = []DiffFile{}
	}
	numstatOut, err := m.git(ctx, host, clonePath,
		"diff", "--numstat", "-z", "-M", "-C",
		"--find-copies-harder", "--end-of-options", mergeBase, headSHA,
	)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}
	counts := parseNumstatZ(numstatOut)
	// Ensure Hunks is never nil so JSON serializes as [] not null.
	for i := range files {
		if count, ok := counts[files[i].Path]; ok {
			files[i].Additions = count.additions
			files[i].Deletions = count.deletions
		}
		if files[i].Hunks == nil {
			files[i].Hunks = []Hunk{}
		}
	}
	m.markGenerated(ctx, host, clonePath, headSHA, files)
	SortDiffFiles(files)
	return files, nil
}

// SortDiffFiles orders files the same way Git path lists and the Pierre file
// tree displays them: folders before files at each level, then bytewise path
// segment order, with old_path as a stable tie-breaker.
func SortDiffFiles(files []DiffFile) {
	slices.SortFunc(files, func(a, b DiffFile) int {
		if cmp := compareDiffFilePaths(a.Path, b.Path); cmp != 0 {
			return cmp
		}
		return compareDiffFilePaths(a.OldPath, b.OldPath)
	})
}

func compareDiffFilePaths(left string, right string) int {
	leftParts := strings.Split(left, "/")
	rightParts := strings.Split(right, "/")
	partCount := min(len(leftParts), len(rightParts))
	for i := range partCount {
		if leftParts[i] == rightParts[i] {
			continue
		}
		leftIsDir := len(leftParts) > i+1
		rightIsDir := len(rightParts) > i+1
		if leftIsDir && !rightIsDir {
			return -1
		}
		if !leftIsDir && rightIsDir {
			return 1
		}
		return strings.Compare(leftParts[i], rightParts[i])
	}
	return len(leftParts) - len(rightParts)
}

type numstatCount struct {
	additions int
	deletions int
}

func parseNumstatZ(data []byte) map[string]numstatCount {
	records := bytes.Split(data, []byte{0})
	counts := make(map[string]numstatCount)
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
		counts[path] = numstatCount{
			additions: parseNumstatInt(fields[0]),
			deletions: parseNumstatInt(fields[1]),
		}
	}
	return counts
}

func parseNumstatInt(value string) int {
	if value == "-" {
		return 0
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

// Diff runs a two-dot git diff between mergeBase and headSHA and returns
// structured diff data. If hideWhitespace is true, passes -w to git diff.
func (m *Manager) Diff(
	ctx context.Context,
	host, owner, name, mergeBase, headSHA string,
	hideWhitespace bool,
) (*DiffResult, error) {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return nil, err
	}

	// Step 1: Compute whitespace-only file count.
	wsCount, err := m.computeWhitespaceOnlyCount(
		ctx, host, clonePath, mergeBase, headSHA)
	if err != nil {
		return nil, fmt.Errorf("whitespace count: %w", err)
	}

	// Step 2: Get file metadata from --raw -z (with rename/copy detection).
	rawArgs := diffRawArgs(mergeBase, headSHA, hideWhitespace)
	rawOut, err := m.git(ctx, host, clonePath, rawArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff --raw: %w", err)
	}
	files := ParseRawZ(rawOut)

	// Step 3: Get patch content.
	patchArgs := []string{
		"diff", "-M", "-C", "--find-copies-harder",
		"-U3",
	}
	if hideWhitespace {
		patchArgs = append(patchArgs[:2],
			append([]string{"-w"}, patchArgs[2:]...)...)
	}
	patchArgs = append(patchArgs, "--end-of-options", mergeBase, headSHA)
	patchOut, err := m.git(ctx, host, clonePath, patchArgs...)
	if err != nil {
		return nil, fmt.Errorf("git diff patch: %w", err)
	}

	files = ParsePatch(patchOut, files)
	if files == nil {
		files = []DiffFile{}
	}

	// Step 4: Tag or drop whitespace-only modifications.
	// In default mode they stay in the list and get tagged so the UI can
	// surface them; in hide mode they leave the list entirely. Both modes
	// need this server-side filter because git before 2.50 doesn't honor
	// -w in --raw output, so step 2 returns whitespace-only files even
	// when the caller asked to hide them.
	wsFiles := m.getWhitespaceOnlyFiles(
		ctx, host, clonePath, mergeBase, headSHA)
	if hideWhitespace {
		filtered := files[:0]
		for i := range files {
			if files[i].Status == "modified" && wsFiles[files[i].Path] {
				continue
			}
			filtered = append(filtered, files[i])
		}
		files = filtered
	} else {
		for i := range files {
			if wsFiles[files[i].Path] {
				files[i].IsWhitespaceOnly = true
			}
		}
	}
	m.markGenerated(ctx, host, clonePath, headSHA, files)
	SortDiffFiles(files)

	return &DiffResult{
		WhitespaceOnlyCount: wsCount,
		Files:               files,
	}, nil
}

func (m *Manager) markGenerated(
	ctx context.Context,
	host string,
	clonePath string,
	attrSource string,
	files []DiffFile,
) {
	if len(files) == 0 {
		return
	}
	generated := map[string]bool{}
	input := GeneratedAttributeInput(files)
	if len(input) > 0 {
		args := []string{"check-attr", "-z", "--stdin"}
		if attrSource != "" {
			args = append(args, "--source", attrSource)
		}
		args = append(args, "linguist-generated")
		if out, err := m.gitWithInput(ctx, host, clonePath, input, args...); err == nil {
			generated = ParseLinguistGeneratedAttributes(out)
		}
	}
	MarkGeneratedFiles(files, generated)
}

func (m *Manager) computeWhitespaceOnlyCount(
	ctx context.Context, host, clonePath, mergeBase, headSHA string,
) (int, error) {
	whitespaceOnlyFiles, err := m.whitespaceOnlyFiles(ctx, host, clonePath, mergeBase, headSHA)
	if err != nil {
		return 0, err
	}
	return len(whitespaceOnlyFiles), nil
}

func (m *Manager) getWhitespaceOnlyFiles(
	ctx context.Context, host, clonePath, mergeBase, headSHA string,
) map[string]bool {
	files, err := m.whitespaceOnlyFiles(ctx, host, clonePath, mergeBase, headSHA)
	if err != nil {
		return nil
	}
	return files
}

func (m *Manager) whitespaceOnlyFiles(
	ctx context.Context, host, clonePath, mergeBase, headSHA string,
) (map[string]bool, error) {
	// Compare --raw (full file list) against --numstat -w (files with
	// non-whitespace changes). --raw -w would be more symmetric, but git
	// before 2.50 ignores -w in --raw mode (it compares blob SHAs), so the
	// two outputs match and we conclude no file is whitespace-only — wrong
	// on every Ubuntu image that ships an older git, including the
	// Playwright noble image used by the e2e job (git 2.43). --numstat
	// honors -w in every supported git version, so we pivot through it.
	rawOut, err := m.git(ctx, host, clonePath, diffRawNoRenameArgs(mergeBase, headSHA, false)...)
	if err != nil {
		return nil, err
	}
	numstatOut, err := m.git(ctx, host, clonePath,
		"diff", "--numstat", "-z", "--no-renames", "-w",
		"--end-of-options", mergeBase, headSHA,
	)
	if err != nil {
		return nil, err
	}

	allFiles := parseRawZPaths(rawOut)
	nonWhitespace := parseNumstatZ(numstatOut)

	result := make(map[string]bool)
	for f := range allFiles {
		if _, ok := nonWhitespace[f]; !ok {
			result[f] = true
		}
	}
	return result, nil
}

func diffRawArgs(mergeBase, headSHA string, hideWhitespace bool) []string {
	args := []string{
		"diff", "--raw", "-z", "-M", "-C",
		"--find-copies-harder",
	}
	if hideWhitespace {
		args = append(args[:2], append([]string{"-w"}, args[2:]...)...)
	}
	return append(args, "--end-of-options", mergeBase, headSHA)
}

func diffRawNoRenameArgs(mergeBase, headSHA string, hideWhitespace bool) []string {
	args := []string{"diff", "--raw", "-z", "--no-renames"}
	if hideWhitespace {
		args = append(args, "-w")
	}
	return append(args, "--end-of-options", mergeBase, headSHA)
}

// FileContent returns one file's blob content at ref. maxBytes guards API
// callers from accidentally loading very large assets into memory.
func (m *Manager) FileContent(
	ctx context.Context,
	host, owner, name, ref, filePath string,
	maxBytes int64,
) (*FileContent, error) {
	clonePath, err := m.ClonePath(host, owner, name)
	if err != nil {
		return nil, err
	}
	object := ref + ":" + filePath
	sizeOut, err := m.git(ctx, host, clonePath, "cat-file", "-s", object)
	if err != nil {
		return nil, fmt.Errorf("git cat-file -s: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse blob size: %w", err)
	}
	if maxBytes > 0 && size > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, size)
	}
	data, err := m.git(ctx, host, clonePath, "cat-file", "blob", object)
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob: %w", err)
	}
	return &FileContent{
		Path: filePath,
		Data: data,
		Size: size,
	}, nil
}

// parseRawZPaths extracts just the file paths from --raw -z output.
func parseRawZPaths(data []byte) map[string]bool {
	files := ParseRawZ(data)
	paths := make(map[string]bool, len(files))
	for _, f := range files {
		paths[f.Path] = true
	}
	return paths
}
