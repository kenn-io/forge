package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/gitclone"
	"go.opentelemetry.io/otel/attribute"
)

type DiffSnapshotSpec struct {
	WorktreePath      string
	Base              WorktreeDiffBase
	MergeTargetBranch string
	FromSHA           string
	ToSHA             string
	HideWhitespace    bool
}

type ResolvedDiffSnapshotSpec struct {
	DiffSnapshotSpec
	BaseRef          string
	HeadRef          string
	BaseOID          string
	HeadOID          string
	IncludeUntracked bool
}

type DiffFingerprint string

const maxDiffContentDigestEntries = 4096

var errWorktreePathNotRegular = errors.New("worktree path is not a regular file or symlink")

var diffContentDigests = struct {
	sync.Mutex
	entries map[string]diffContentDigestEntry
}{entries: make(map[string]diffContentDigestEntry)}

type diffContentDigestEntry struct {
	identity string
	digest   [sha256.Size]byte
	usedAt   time.Time
}

func ResolveDiffSnapshotSpec(
	ctx context.Context,
	spec DiffSnapshotSpec,
) (ResolvedDiffSnapshotSpec, bool, error) {
	ctx, span := workspaceDiffTracer.Start(ctx, "workspace.diff.resolve")
	defer span.End()

	absPath, err := filepath.Abs(spec.WorktreePath)
	if err != nil {
		span.RecordError(err)
		return ResolvedDiffSnapshotSpec{}, false, err
	}
	spec.WorktreePath = filepath.Clean(absPath)

	resolved := ResolvedDiffSnapshotSpec{DiffSnapshotSpec: spec}
	if spec.FromSHA != "" || spec.ToSHA != "" {
		if spec.FromSHA == "" || spec.ToSHA == "" {
			return ResolvedDiffSnapshotSpec{}, false, errors.New("both diff range refs are required")
		}
		resolved.BaseRef = spec.FromSHA
		resolved.HeadRef = spec.ToSHA
	} else {
		if resolved.Base == "" {
			resolved.Base = WorktreeDiffBaseHead
		}
		var ok bool
		switch resolved.Base {
		case WorktreeDiffBaseMergeTarget:
			resolved.BaseRef, ok, err = worktreeMergeTargetBaseRef(
				ctx, resolved.WorktreePath, resolved.MergeTargetBranch,
			)
		default:
			resolved.BaseRef, ok, err = worktreeDiffBaseRef(
				ctx, resolved.WorktreePath, resolved.Base,
			)
		}
		if err != nil || !ok {
			if err != nil {
				span.RecordError(err)
			}
			return ResolvedDiffSnapshotSpec{}, ok, err
		}
		resolved.IncludeUntracked = true
	}

	resolved.BaseOID, err = resolveDiffOID(ctx, resolved.WorktreePath, resolved.BaseRef, "object")
	if err != nil {
		span.RecordError(err)
		return ResolvedDiffSnapshotSpec{}, false, err
	}
	headRef := resolved.HeadRef
	if headRef == "" {
		headRef = "HEAD"
	}
	resolved.HeadOID, err = resolveDiffOID(ctx, resolved.WorktreePath, headRef, "commit")
	if err != nil {
		span.RecordError(err)
		return ResolvedDiffSnapshotSpec{}, false, err
	}
	return resolved, true, nil
}

func resolveDiffOID(ctx context.Context, dir, ref, objectType string) (string, error) {
	out, err := worktreeGitOutput(ctx, dir, "rev-parse", "--verify", ref+"^{"+objectType+"}")
	if err != nil {
		return "", fmt.Errorf("resolve diff ref %q: %w", ref, err)
	}
	oid := strings.TrimSpace(string(out))
	if oid == "" {
		return "", fmt.Errorf("resolve diff ref %q: empty object id", ref)
	}
	return oid, nil
}

func FingerprintDiffSnapshot(
	ctx context.Context,
	resolved ResolvedDiffSnapshotSpec,
) (DiffFingerprint, error) {
	ctx, span := workspaceDiffTracer.Start(ctx, "workspace.diff.fingerprint")
	defer span.End()

	current, ok, err := ResolveDiffSnapshotSpec(ctx, resolved.DiffSnapshotSpec)
	if err != nil {
		span.RecordError(err)
		return "", err
	}
	if !ok {
		return "", errors.New("diff base is no longer available")
	}

	h := sha256.New()
	writeDiffFingerprintField(h, []byte("middleman-workspace-diff-v1"))
	writeDiffFingerprintField(h, []byte(current.WorktreePath))
	writeDiffFingerprintField(h, []byte(current.BaseOID))
	writeDiffFingerprintField(h, []byte(current.HeadOID))
	writeDiffFingerprintField(h, []byte(current.Base))
	writeDiffFingerprintField(h, []byte(current.MergeTargetBranch))
	if current.HideWhitespace {
		writeDiffFingerprintField(h, []byte{1})
	} else {
		writeDiffFingerprintField(h, []byte{0})
	}
	if err := fingerprintRepositoryAttributes(ctx, h, current.WorktreePath); err != nil {
		span.RecordError(err)
		return "", err
	}
	if !current.IncludeUntracked {
		return DiffFingerprint(fmt.Sprintf("%x", h.Sum(nil))), nil
	}

	rawOut, err := worktreeGitOutput(
		ctx, current.WorktreePath,
		gitclone.DiffArgs("--raw", "-z", "--no-renames", current.BaseOID)...,
	)
	if err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("fingerprint tracked changes: %w", err)
	}
	untrackedOut, err := worktreeGitOutput(
		ctx, current.WorktreePath,
		"ls-files", "--others", "--exclude-standard", "-z",
	)
	if err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("fingerprint untracked changes: %w", err)
	}
	writeDiffFingerprintField(h, rawOut)
	writeDiffFingerprintField(h, untrackedOut)
	paths := make(map[string]struct{})
	for _, file := range gitclone.ParseRawZ(rawOut) {
		paths[file.Path] = struct{}{}
	}
	for part := range bytes.SplitSeq(untrackedOut, []byte{0}) {
		if len(part) > 0 {
			paths[string(part)] = struct{}{}
		}
	}
	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)
	type pathFingerprint struct {
		encoded   []byte
		bytesRead int64
	}
	pathFingerprints := make([]pathFingerprint, len(orderedPaths))
	err = untrackedFileReads.run(ctx, orderedPaths, func(
		readCtx context.Context, index int, path string,
	) error {
		encoded, read, readErr := fingerprintWorktreePath(
			readCtx, current.WorktreePath, path,
		)
		if readErr != nil {
			return readErr
		}
		pathFingerprints[index] = pathFingerprint{encoded: encoded, bytesRead: read}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return "", err
	}
	var bytesRead int64
	for _, fingerprint := range pathFingerprints {
		_, _ = h.Write(fingerprint.encoded)
		bytesRead += fingerprint.bytesRead
	}
	span.SetAttributes(
		attribute.Int("workspace.diff.fingerprint_paths", len(orderedPaths)),
		attribute.Int64("workspace.diff.fingerprint_bytes_read", bytesRead),
	)
	return DiffFingerprint(fmt.Sprintf("%x", h.Sum(nil))), nil
}

func fingerprintRepositoryAttributes(
	ctx context.Context,
	h hash.Hash,
	dir string,
) error {
	out, err := worktreeGitOutput(ctx, dir, "rev-parse", "--git-path", "info/attributes")
	if err != nil {
		return fmt.Errorf("resolve repository attributes: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		writeDiffFingerprintField(h, []byte("attributes-missing"))
		return nil
	}
	if err != nil {
		return fmt.Errorf("read repository attributes: %w", err)
	}
	writeDiffFingerprintField(h, data)
	return nil
}

func fingerprintWorktreePath(ctx context.Context, dir, path string) ([]byte, int64, error) {
	clean, err := cleanWorktreeDiffPath(path)
	if err != nil {
		return nil, 0, err
	}
	var encoded bytes.Buffer
	writeDiffFingerprintField(&encoded, []byte(clean))
	fullPath := filepath.Join(dir, filepath.FromSlash(clean))
	opened, err := openWorktreePath(dir, clean)
	if errors.Is(err, os.ErrNotExist) {
		writeDiffFingerprintField(&encoded, []byte("missing"))
		return encoded.Bytes(), 0, nil
	}
	if errors.Is(err, errWorktreePathNotRegular) {
		writeDiffFingerprintField(&encoded, []byte("nonregular"))
		return encoded.Bytes(), 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if opened.file == nil {
		writeDiffFingerprintField(&encoded, []byte("symlink"))
		writeDiffFingerprintField(&encoded, []byte(opened.symlinkTarget))
		return encoded.Bytes(), 0, nil
	}
	defer opened.file.Close()
	writeDiffFingerprintField(&encoded, []byte(opened.info.Mode().String()))
	digest, bytesRead, err := diffContentDigestFile(ctx, fullPath, opened.file, opened.info)
	if err != nil {
		return nil, 0, err
	}
	writeDiffFingerprintField(&encoded, digest[:])
	return encoded.Bytes(), bytesRead, nil
}

func diffContentDigest(ctx context.Context, path string) ([sha256.Size]byte, int64, error) {
	file, info, err := openRegularUntrackedFile(path)
	if err != nil {
		return [sha256.Size]byte{}, 0, err
	}
	defer file.Close()
	return diffContentDigestFile(ctx, path, file, info)
}

func diffContentDigestFile(
	ctx context.Context,
	cacheKey string,
	file *os.File,
	info os.FileInfo,
) ([sha256.Size]byte, int64, error) {
	identity := fmt.Sprintf(
		"%d|%d|%s|%#v",
		info.Size(), info.ModTime().UnixNano(), info.Mode(), info.Sys(),
	)
	now := time.Now()
	diffContentDigests.Lock()
	if cached, ok := diffContentDigests.entries[cacheKey]; ok && cached.identity == identity {
		cached.usedAt = now
		diffContentDigests.entries[cacheKey] = cached
		diffContentDigests.Unlock()
		return cached.digest, 0, nil
	}
	diffContentDigests.Unlock()

	digestHash := sha256.New()
	bytesRead, err := hashDiffContent(ctx, digestHash, file)
	if err != nil {
		return [sha256.Size]byte{}, bytesRead, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], digestHash.Sum(nil))

	diffContentDigests.Lock()
	diffContentDigests.entries[cacheKey] = diffContentDigestEntry{
		identity: identity,
		digest:   digest,
		usedAt:   now,
	}
	if len(diffContentDigests.entries) > maxDiffContentDigestEntries {
		var oldestPath string
		var oldest time.Time
		for candidatePath, candidate := range diffContentDigests.entries {
			if oldestPath == "" || candidate.usedAt.Before(oldest) {
				oldestPath = candidatePath
				oldest = candidate.usedAt
			}
		}
		delete(diffContentDigests.entries, oldestPath)
	}
	diffContentDigests.Unlock()
	return digest, bytesRead, nil
}

func hashDiffContent(ctx context.Context, destination hash.Hash, file *os.File) (int64, error) {
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, err := file.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, errors.New("short diff fingerprint hash write")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

func writeDiffFingerprintField(w io.Writer, value []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func PrepareDiffSnapshot(
	ctx context.Context,
	resolved ResolvedDiffSnapshotSpec,
) (*gitclone.DiffResult, error) {
	ctx, span := workspaceDiffTracer.Start(ctx, "workspace.diff.prepare")
	defer span.End()
	headRef := ""
	if !resolved.IncludeUntracked {
		headRef = resolved.HeadOID
	}
	result, err := worktreeDiffFromRefsPath(
		ctx,
		resolved.WorktreePath,
		resolved.BaseOID,
		headRef,
		resolved.HideWhitespace,
		"",
		resolved.IncludeUntracked,
	)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("workspace.diff.file_count", len(result.Files)))
	return result, nil
}

func ReadDiffSnapshotFile(
	ctx context.Context,
	resolved ResolvedDiffSnapshotSpec,
	file gitclone.DiffFile,
	side string,
	maxBytes int64,
) (*gitclone.FileContent, error) {
	ref := resolved.HeadOID
	previewPath := file.Path
	useWorktree := resolved.IncludeUntracked
	switch side {
	case "old":
		if file.Status == "added" {
			return nil, gitclone.ErrNotFound
		}
		ref = resolved.BaseOID
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
			ref = resolved.BaseOID
			previewPath = file.OldPath
			if previewPath == "" {
				previewPath = file.Path
			}
			useWorktree = false
		}
	default:
		return nil, errors.New("side must be old or new")
	}

	previewPath, err := cleanWorktreeDiffPath(previewPath)
	if err != nil {
		return nil, err
	}
	if useWorktree {
		return readWorktreeFileContent(resolved.WorktreePath, previewPath, maxBytes)
	}
	return worktreeBlobContent(ctx, resolved.WorktreePath, ref, previewPath, maxBytes)
}
