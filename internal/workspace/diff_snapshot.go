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
	if err := fingerprintRepositoryAttributes(ctx, h, current.WorktreePath); err != nil {
		span.RecordError(err)
		return "", err
	}

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
	var bytesRead int64
	for _, path := range orderedPaths {
		read, err := fingerprintWorktreePath(h, current.WorktreePath, path)
		if err != nil {
			span.RecordError(err)
			return "", err
		}
		bytesRead += read
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

func fingerprintWorktreePath(h hash.Hash, dir, path string) (int64, error) {
	clean, err := cleanWorktreeDiffPath(path)
	if err != nil {
		return 0, err
	}
	writeDiffFingerprintField(h, []byte(clean))
	fullPath := filepath.Join(dir, filepath.FromSlash(clean))
	info, err := os.Lstat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		writeDiffFingerprintField(h, []byte("missing"))
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	writeDiffFingerprintField(h, []byte(info.Mode().String()))
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return 0, err
		}
		writeDiffFingerprintField(h, []byte(target))
		return 0, nil
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	digest, bytesRead, err := diffContentDigest(fullPath, info)
	if err != nil {
		return 0, err
	}
	writeDiffFingerprintField(h, digest[:])
	return bytesRead, nil
}

func diffContentDigest(path string, info os.FileInfo) ([sha256.Size]byte, int64, error) {
	identity := fmt.Sprintf(
		"%d|%d|%s|%#v",
		info.Size(), info.ModTime().UnixNano(), info.Mode(), info.Sys(),
	)
	now := time.Now()
	diffContentDigests.Lock()
	if cached, ok := diffContentDigests.entries[path]; ok && cached.identity == identity {
		cached.usedAt = now
		diffContentDigests.entries[path] = cached
		diffContentDigests.Unlock()
		return cached.digest, 0, nil
	}
	diffContentDigests.Unlock()

	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, 0, err
	}
	defer file.Close()
	digestHash := sha256.New()
	bytesRead, err := io.Copy(digestHash, file)
	if err != nil {
		return [sha256.Size]byte{}, bytesRead, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], digestHash.Sum(nil))

	diffContentDigests.Lock()
	diffContentDigests.entries[path] = diffContentDigestEntry{
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

func writeDiffFingerprintField(h hash.Hash, value []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
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
