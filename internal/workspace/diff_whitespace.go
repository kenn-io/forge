package workspace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/procutil"
)

func isGitWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

// gitWhitespaceRecordEqual matches xdiff's XDF_IGNORE_WHITESPACE record
// comparison: ASCII C-locale whitespace is discarded while line records keep
// their order and cardinality.
func gitWhitespaceRecordEqual(left, right string) bool {
	i, j := 0, 0
	for {
		for i < len(left) && isGitWhitespace(left[i]) {
			i++
		}
		for j < len(right) && isGitWhitespace(right[j]) {
			j++
		}
		if i == len(left) || j == len(right) {
			for i < len(left) && isGitWhitespace(left[i]) {
				i++
			}
			for j < len(right) && isGitWhitespace(right[j]) {
				j++
			}
			return i == len(left) && j == len(right)
		}
		if left[i] != right[j] {
			return false
		}
		i++
		j++
	}
}

func classifyWhitespaceOnly(
	files []gitclone.DiffFile,
	modeChanged map[string]bool,
) int {
	count, _ := classifyWhitespaceOnlyCandidates(files, modeChanged)
	return count
}

func classifyWorkspaceWhitespaceOnly(
	ctx context.Context,
	dir string,
	baseRef string,
	headRef string,
	includeUntracked bool,
	files []gitclone.DiffFile,
	modeChanged map[string]bool,
) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	count, candidates := classifyWhitespaceOnlyCandidates(files, modeChanged)
	if len(candidates) == 0 {
		return count, nil
	}

	type candidateContent struct {
		index   int
		oldSpec string
		newSpec string
	}
	candidateContents := make([]candidateContent, 0, len(candidates))
	objectSpecs := make([]string, 0, len(candidates)*2)
	for _, index := range candidates {
		file := files[index]
		oldPath := file.OldPath
		if oldPath == "" {
			oldPath = file.Path
		}
		if strings.ContainsAny(oldPath, "\r\n") || strings.ContainsAny(file.Path, "\r\n") {
			continue
		}
		candidate := candidateContent{
			index:   index,
			oldSpec: baseRef + ":" + oldPath,
		}
		objectSpecs = append(objectSpecs, candidate.oldSpec)
		if !includeUntracked {
			candidate.newSpec = headRef + ":" + file.Path
			objectSpecs = append(objectSpecs, candidate.newSpec)
		}
		candidateContents = append(candidateContents, candidate)
	}
	blobs, err := readWhitespaceBlobDigests(ctx, dir, objectSpecs)
	if err != nil {
		return 0, err
	}
	for _, candidate := range candidateContents {
		oldDigest := blobs[candidate.oldSpec]
		var newDigest [sha256.Size]byte
		if candidate.newSpec != "" {
			newDigest = blobs[candidate.newSpec]
		} else {
			var readErr error
			newDigest, readErr = readWorktreeWhitespaceDigest(
				ctx, dir, files[candidate.index].Path,
			)
			if readErr != nil {
				return 0, fmt.Errorf("read whitespace candidate %q: %w", files[candidate.index].Path, readErr)
			}
		}
		if oldDigest == newDigest {
			files[candidate.index].IsWhitespaceOnly = true
			count++
		}
	}
	return count, nil
}

func classifyWhitespaceOnlyCandidates(
	files []gitclone.DiffFile,
	modeChanged map[string]bool,
) (int, []int) {
	count := 0
	candidates := make([]int, 0)
	for i := range files {
		file := &files[i]
		if file.Status != "modified" || file.IsBinary || modeChanged[file.Path] || len(file.Hunks) == 0 {
			continue
		}
		whitespaceOnly := true
		for _, hunk := range file.Hunks {
			if !hunkWhitespaceOnly(hunk) {
				whitespaceOnly = false
				break
			}
		}
		if whitespaceOnly {
			file.IsWhitespaceOnly = true
			count++
			continue
		}
		if hunksWhitespaceRecordsEqual(file.Hunks) {
			candidates = append(candidates, i)
		}
	}
	return count, candidates
}

func hunkWhitespaceOnly(hunk gitclone.Hunk) bool {
	oldRecords := make([]string, 0, hunk.OldCount)
	newRecords := make([]string, 0, hunk.NewCount)
	for _, line := range hunk.Lines {
		switch line.Type {
		case "context":
			oldRecords = append(oldRecords, line.Content)
			newRecords = append(newRecords, line.Content)
		case "delete":
			oldRecords = append(oldRecords, line.Content)
		case "add":
			newRecords = append(newRecords, line.Content)
		}
	}
	return gitWhitespaceRecordsEqual(oldRecords, newRecords)
}

func hunksWhitespaceRecordsEqual(hunks []gitclone.Hunk) bool {
	oldRecords := make([]string, 0)
	newRecords := make([]string, 0)
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			switch line.Type {
			case "context":
				oldRecords = append(oldRecords, line.Content)
				newRecords = append(newRecords, line.Content)
			case "delete":
				oldRecords = append(oldRecords, line.Content)
			case "add":
				newRecords = append(newRecords, line.Content)
			}
		}
	}
	return gitWhitespaceRecordsEqual(oldRecords, newRecords)
}

func gitWhitespaceRecordsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !gitWhitespaceRecordEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

type gitWhitespaceDigestWriter struct {
	ctx        context.Context
	fileHash   hash.Hash
	recordHash hash.Hash
	finished   bool
}

func newGitWhitespaceDigestWriter(ctx context.Context) *gitWhitespaceDigestWriter {
	return &gitWhitespaceDigestWriter{
		ctx:        ctx,
		fileHash:   sha256.New(),
		recordHash: sha256.New(),
	}
}

func (w *gitWhitespaceDigestWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	normalized := make([]byte, 0, len(data))
	flush := func() {
		if len(normalized) == 0 {
			return
		}
		_, _ = w.recordHash.Write(normalized)
		normalized = normalized[:0]
	}
	for _, b := range data {
		if b == '\n' {
			flush()
			w.finishRecord()
			continue
		}
		if !isGitWhitespace(b) {
			normalized = append(normalized, b)
		}
	}
	flush()
	return len(data), nil
}

func (w *gitWhitespaceDigestWriter) finishRecord() {
	_, _ = w.fileHash.Write(w.recordHash.Sum(nil))
	w.recordHash.Reset()
}

func (w *gitWhitespaceDigestWriter) sum() [sha256.Size]byte {
	if !w.finished {
		w.finishRecord()
		w.finished = true
	}
	var digest [sha256.Size]byte
	copy(digest[:], w.fileHash.Sum(nil))
	return digest
}

func gitWhitespaceDigest(reader io.Reader, size int64) ([sha256.Size]byte, error) {
	return gitWhitespaceDigestContext(context.Background(), reader, size)
}

func gitWhitespaceDigestContext(
	ctx context.Context,
	reader io.Reader,
	size int64,
) ([sha256.Size]byte, error) {
	if size < 0 {
		return [sha256.Size]byte{}, errors.New("negative whitespace content size")
	}
	destination := newGitWhitespaceDigestWriter(ctx)
	if _, err := io.CopyN(destination, reader, size); err != nil {
		return [sha256.Size]byte{}, err
	}
	return destination.sum(), nil
}

func readWorktreeWhitespaceDigest(
	ctx context.Context,
	dir string,
	path string,
) ([sha256.Size]byte, error) {
	clean, err := cleanWorktreeDiffPath(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	opened, err := openWorktreePath(dir, clean)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if opened.file == nil {
		data := []byte(opened.symlinkTarget)
		return gitWhitespaceDigest(bytes.NewReader(data), int64(len(data)))
	}
	defer opened.file.Close()
	return gitWhitespaceDigestContext(ctx, opened.file, opened.info.Size())
}

func readWhitespaceBlobDigests(
	ctx context.Context,
	dir string,
	objectSpecs []string,
) (map[string][sha256.Size]byte, error) {
	unique := make([]string, 0, len(objectSpecs))
	seen := make(map[string]struct{}, len(objectSpecs))
	for _, spec := range objectSpecs {
		if _, ok := seen[spec]; ok {
			continue
		}
		seen[spec] = struct{}{}
		unique = append(unique, spec)
	}
	if len(unique) == 0 {
		return map[string][sha256.Size]byte{}, nil
	}
	if dir == "" {
		return nil, errors.New("empty worktree dir")
	}
	cmd := gitcmd.New().Command(ctx, dir, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(unique, "\n") + "\n")
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	type parseResult struct {
		digests map[string][sha256.Size]byte
		err     error
	}
	parsed := make(chan parseResult, 1)
	go func() {
		digests, err := parseWhitespaceBlobDigests(ctx, reader, unique)
		parsed <- parseResult{digests: digests, err: err}
		if err != nil {
			_ = reader.CloseWithError(err)
		}
	}()
	runErr := procutil.Run(ctx, cmd, "git workspace diff subprocess capacity")
	_ = writer.Close()
	parse := <-parsed
	_ = reader.Close()
	if parse.err != nil {
		if runErr != nil {
			return nil, fmt.Errorf(
				"parse whitespace candidate blobs: %v; git cat-file: %w: %s",
				parse.err,
				runErr,
				strings.TrimSpace(stderr.String()),
			)
		}
		return nil, parse.err
	}
	if runErr != nil {
		return nil, fmt.Errorf(
			"read whitespace candidate blobs: %w: %s",
			runErr, strings.TrimSpace(stderr.String()),
		)
	}
	return parse.digests, nil
}

func parseWhitespaceBlobDigests(
	ctx context.Context,
	input io.Reader,
	specs []string,
) (map[string][sha256.Size]byte, error) {
	reader := bufio.NewReader(input)
	result := make(map[string][sha256.Size]byte, len(specs))
	for _, spec := range specs {
		header, readErr := reader.ReadString('\n')
		if readErr != nil {
			return nil, fmt.Errorf("read blob header for %q: %w", spec, readErr)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("read blob %q: unexpected header %q", spec, strings.TrimSpace(header))
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size < 0 {
			return nil, fmt.Errorf("read blob %q: invalid size %q", spec, fields[2])
		}
		digest, readErr := gitWhitespaceDigestContext(ctx, reader, size)
		if readErr != nil {
			return nil, fmt.Errorf("read blob %q content: %w", spec, readErr)
		}
		terminator, readErr := reader.ReadByte()
		if readErr != nil || terminator != '\n' {
			return nil, fmt.Errorf("read blob %q terminator", spec)
		}
		result[spec] = digest
	}
	return result, nil
}
