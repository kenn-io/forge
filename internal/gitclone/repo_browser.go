package gitclone

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RepoBrowserTreeEntryLimit      = 20000
	RepoBrowserBlobSizeLimit       = 1 << 20
	RepoBrowserLastChangedBatchMax = 250
	RepoBrowserHistoryLimit        = 50
)

var (
	ErrUnsafePath       = errors.New("unsafe repo browser path")
	ErrTooManyPaths     = errors.New("too many repo browser paths")
	ErrTooLargeAsset    = errors.New("repo browser asset too large")
	ErrUnsupportedAsset = errors.New("unsupported repo browser asset type")
)

type RepoBrowserRefType string

const (
	RepoBrowserRefBranch RepoBrowserRefType = "branch"
	RepoBrowserRefTag    RepoBrowserRefType = "tag"
	RepoBrowserRefCommit RepoBrowserRefType = "commit"
)

type RepoBrowserRepoRef struct {
	Host      string
	Owner     string
	Name      string
	RepoPath  string
	RemoteURL string
}

type RepoBrowserRef struct {
	Type         RepoBrowserRefType `json:"type"`
	Name         string             `json:"name"`
	SHA          string             `json:"sha"`
	RequestedSHA string             `json:"requested_sha,omitempty"`
	Stale        bool               `json:"stale"`
}

type RepoBrowserTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type RepoBrowserBlob struct {
	Path      string `json:"path"`
	SHA       string `json:"sha"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Binary    bool   `json:"binary"`
	TooLarge  bool   `json:"too_large"`
}

type RepoBrowserCommit struct {
	SHA         string    `json:"sha"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	AuthoredAt  time.Time `json:"authored_at"`
}

func (m *Manager) ListRepoBrowserRefs(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	defaultBranch string,
) ([]RepoBrowserRef, RepoBrowserRef, error) {
	dir, err := m.repoBrowserClonePath(repo)
	if err != nil {
		return nil, RepoBrowserRef{}, err
	}
	out, err := m.git(ctx, dir,
		"for-each-ref",
		"--format=%(refname)%00%(objectname)%00%(*objectname)",
		"refs/remotes/origin",
		"refs/tags",
	)
	if err != nil {
		return nil, RepoBrowserRef{}, fmt.Errorf("list repo browser refs: %w", err)
	}
	refs := parseRepoBrowserRefs(out)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].Name < refs[j].Name
	})
	branch, sha, err := m.ResolveDefaultBranch(ctx, repo.Host, repo.Owner, repo.Name, defaultBranch)
	if err != nil {
		return refs, RepoBrowserRef{}, err
	}
	return refs, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: branch, SHA: sha}, nil
}

func parseRepoBrowserRefs(out []byte) []RepoBrowserRef {
	var refs []RepoBrowserRef
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "\x00")
		if len(parts) < 2 {
			continue
		}
		refName := parts[0]
		sha := parts[1]
		if len(parts) > 2 && parts[2] != "" {
			sha = parts[2]
		}
		switch {
		case refName == "refs/remotes/origin/HEAD":
			continue
		case strings.HasPrefix(refName, "refs/remotes/origin/"):
			name := strings.TrimPrefix(refName, "refs/remotes/origin/")
			if name != "" {
				refs = append(refs, RepoBrowserRef{Type: RepoBrowserRefBranch, Name: name, SHA: sha})
			}
		case strings.HasPrefix(refName, "refs/tags/"):
			name := strings.TrimPrefix(refName, "refs/tags/")
			if name != "" {
				refs = append(refs, RepoBrowserRef{Type: RepoBrowserRefTag, Name: name, SHA: sha})
			}
		}
	}
	return refs
}

func (m *Manager) ListRepoBrowserTree(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
) ([]RepoBrowserTreeEntry, bool, error) {
	dir, sha, _, err := m.resolveRepoBrowserRef(ctx, repo, ref)
	if err != nil {
		return nil, false, err
	}
	out, err := m.git(ctx, dir,
		"ls-tree", "-r", "-z", "-l", "--full-tree", "--end-of-options", sha,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list repo browser tree: %w", err)
	}
	entries := parseRepoBrowserTree(out)
	truncated := len(entries) > RepoBrowserTreeEntryLimit
	if truncated {
		entries = entries[:RepoBrowserTreeEntryLimit]
	}
	return entries, truncated, nil
}

func parseRepoBrowserTree(out []byte) []RepoBrowserTreeEntry {
	records := bytes.Split(out, []byte{0})
	entries := make([]RepoBrowserTreeEntry, 0, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		header, pathBytes, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			continue
		}
		fields := strings.Fields(string(header))
		if len(fields) < 4 {
			continue
		}
		size := int64(0)
		if fields[3] != "-" {
			size, _ = strconv.ParseInt(fields[3], 10, 64)
		}
		entries = append(entries, RepoBrowserTreeEntry{
			Path: string(pathBytes),
			Type: fields[1],
			Size: size,
		})
	}
	SortRepoBrowserTree(entries)
	return entries
}

func SortRepoBrowserTree(entries []RepoBrowserTreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return compareDiffFilePaths(entries[i].Path, entries[j].Path) < 0
	})
}

func (m *Manager) ReadRepoBrowserBlob(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
	pathName string,
) (RepoBrowserBlob, error) {
	return m.readRepoBrowserBlob(ctx, repo, ref, pathName, false)
}

func (m *Manager) ReadRepoBrowserAsset(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
	pathName string,
) (RepoBrowserBlob, error) {
	blob, err := m.readRepoBrowserBlob(ctx, repo, ref, pathName, true)
	if err != nil {
		return RepoBrowserBlob{}, err
	}
	if blob.TooLarge {
		return RepoBrowserBlob{}, fmt.Errorf("%w: %w", ErrTooLargeAsset, ErrTooLarge)
	}
	if !repoBrowserAssetMediaTypeAllowed(blob.MediaType) {
		return RepoBrowserBlob{}, fmt.Errorf("%w: %s", ErrUnsupportedAsset, blob.MediaType)
	}
	return blob, nil
}

func (m *Manager) readRepoBrowserBlob(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
	pathName string,
	asset bool,
) (RepoBrowserBlob, error) {
	cleanPath, err := cleanRepoBrowserPath(pathName)
	if err != nil {
		return RepoBrowserBlob{}, err
	}
	dir, sha, _, err := m.resolveRepoBrowserRef(ctx, repo, ref)
	if err != nil {
		return RepoBrowserBlob{}, err
	}
	entry, err := m.lookupRepoBrowserTreeEntry(ctx, dir, sha, cleanPath)
	if err != nil {
		return RepoBrowserBlob{}, err
	}
	if entry.Type != "blob" {
		return RepoBrowserBlob{}, fmt.Errorf("%w: %s", ErrNotFound, cleanPath)
	}
	blob := RepoBrowserBlob{
		Path:      cleanPath,
		SHA:       entry.SHA,
		Size:      entry.Size,
		MediaType: mediaTypeForRepoBrowserPath(cleanPath),
	}
	if blob.Size > RepoBrowserBlobSizeLimit {
		blob.TooLarge = true
		return blob, nil
	}
	data, err := m.git(ctx, dir, "cat-file", "blob", entry.SHA)
	if err != nil {
		return RepoBrowserBlob{}, fmt.Errorf("read repo browser blob %s: %w", cleanPath, err)
	}
	if blob.MediaType == "" {
		blob.MediaType = http.DetectContentType(data)
	}
	blob.Binary = bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
	if blob.Binary && !asset {
		return blob, nil
	}
	blob.Encoding = "utf-8"
	blob.Content = string(data)
	return blob, nil
}

type repoBrowserTreeEntryLookup struct {
	Path string
	Type string
	SHA  string
	Size int64
}

func (m *Manager) lookupRepoBrowserTreeEntry(
	ctx context.Context,
	dir, sha, pathName string,
) (repoBrowserTreeEntryLookup, error) {
	out, err := m.git(ctx, dir,
		"ls-tree", "-z", "-l", "--full-tree", "--end-of-options", sha, "--", pathName,
	)
	if err != nil {
		return repoBrowserTreeEntryLookup{}, fmt.Errorf("lookup repo browser path %s: %w", pathName, err)
	}
	for record := range bytes.SplitSeq(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, pathBytes, ok := bytes.Cut(record, []byte{'\t'})
		if !ok || string(pathBytes) != pathName {
			continue
		}
		fields := strings.Fields(string(header))
		if len(fields) < 4 {
			continue
		}
		size := int64(0)
		if fields[3] != "-" {
			size, _ = strconv.ParseInt(fields[3], 10, 64)
		}
		return repoBrowserTreeEntryLookup{
			Path: pathName,
			Type: fields[1],
			SHA:  fields[2],
			Size: size,
		}, nil
	}
	return repoBrowserTreeEntryLookup{}, fmt.Errorf("%w: %s", ErrNotFound, pathName)
}

func (m *Manager) RepoBrowserLastChanged(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
	paths []string,
) (map[string]RepoBrowserCommit, error) {
	if len(paths) > RepoBrowserLastChangedBatchMax {
		return nil, ErrTooManyPaths
	}
	cleanPaths := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, pathName := range paths {
		cleanPath, err := cleanRepoBrowserPath(pathName)
		if err != nil {
			return nil, err
		}
		if seen[cleanPath] {
			continue
		}
		seen[cleanPath] = true
		cleanPaths = append(cleanPaths, cleanPath)
	}
	if len(cleanPaths) == 0 {
		return map[string]RepoBrowserCommit{}, nil
	}
	dir, sha, _, err := m.resolveRepoBrowserRef(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	args := []string{
		"log",
		"--format=" + repoBrowserCommitMarker + repoBrowserCommitFormat,
		"--name-only",
		"--end-of-options",
		sha,
		"--",
	}
	args = append(args, cleanPaths...)
	out, err := m.git(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("repo browser last changed: %w", err)
	}
	return parseRepoBrowserLastChanged(out, seen)
}

func parseRepoBrowserLastChanged(out []byte, wanted map[string]bool) (map[string]RepoBrowserCommit, error) {
	changed := make(map[string]RepoBrowserCommit, len(wanted))
	var current RepoBrowserCommit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if payload, ok := strings.CutPrefix(line, repoBrowserCommitMarker); ok {
			commit, err := parseRepoBrowserCommitLine(payload)
			if err != nil {
				return nil, err
			}
			current = commit
			continue
		}
		if wanted[line] {
			if _, exists := changed[line]; !exists {
				changed[line] = current
			}
		}
		if len(changed) == len(wanted) {
			break
		}
	}
	return changed, scanner.Err()
}

func (m *Manager) RepoBrowserFileHistory(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
	pathName string,
) ([]RepoBrowserCommit, error) {
	cleanPath, err := cleanRepoBrowserPath(pathName)
	if err != nil {
		return nil, err
	}
	dir, sha, _, err := m.resolveRepoBrowserRef(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	out, err := m.git(ctx, dir,
		"log",
		"--max-count="+strconv.Itoa(RepoBrowserHistoryLimit),
		"--format="+repoBrowserCommitFormat,
		"--end-of-options",
		sha,
		"--",
		cleanPath,
	)
	if err != nil {
		return nil, fmt.Errorf("repo browser file history %s: %w", cleanPath, err)
	}
	return parseRepoBrowserCommitLines(out)
}

func (m *Manager) RepoBrowserCommitDetail(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	root RepoBrowserRef,
	pathName string,
	sha string,
) (RepoBrowserCommit, error) {
	if _, err := cleanRepoBrowserPath(pathName); err != nil {
		return RepoBrowserCommit{}, err
	}
	if !isFullHexSHA(sha) {
		return RepoBrowserCommit{}, fmt.Errorf("%w: %s", ErrNotFound, sha)
	}
	dir, _, _, err := m.resolveRepoBrowserRef(ctx, repo, root)
	if err != nil {
		return RepoBrowserCommit{}, err
	}
	out, err := m.git(ctx, dir,
		"show", "-s", "--format="+repoBrowserCommitFormat, "--end-of-options", sha,
	)
	if err != nil {
		return RepoBrowserCommit{}, fmt.Errorf("repo browser commit detail %s: %w", sha, err)
	}
	commits, err := parseRepoBrowserCommitLines(out)
	if err != nil {
		return RepoBrowserCommit{}, err
	}
	if len(commits) == 0 {
		return RepoBrowserCommit{}, fmt.Errorf("%w: %s", ErrNotFound, sha)
	}
	return commits[0], nil
}

func (m *Manager) ResolveRepoBrowserRef(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
) (RepoBrowserRef, error) {
	_, sha, stale, err := m.resolveRepoBrowserRef(ctx, repo, ref)
	if err != nil {
		return RepoBrowserRef{}, err
	}
	resolved := ref
	resolved.SHA = sha
	resolved.Stale = stale
	if stale {
		resolved.RequestedSHA = strings.TrimSpace(ref.SHA)
	}
	return resolved, nil
}

const (
	repoBrowserCommitMarker = "commit:"
	repoBrowserCommitFormat = "%H%x1f%an%x1f%ae%x1f%aI%x1f%s"
)

func parseRepoBrowserCommitLines(out []byte) ([]RepoBrowserCommit, error) {
	var commits []RepoBrowserCommit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		commit, err := parseRepoBrowserCommitLine(line)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	return commits, scanner.Err()
}

func parseRepoBrowserCommitLine(line string) (RepoBrowserCommit, error) {
	parts := strings.SplitN(line, "\x1f", 5)
	if len(parts) != 5 {
		return RepoBrowserCommit{}, fmt.Errorf("unexpected repo browser commit line: %q", line)
	}
	authoredAt, err := time.Parse(time.RFC3339, parts[3])
	if err != nil {
		return RepoBrowserCommit{}, fmt.Errorf("parse repo browser commit time %q: %w", parts[3], err)
	}
	return RepoBrowserCommit{
		SHA:         parts[0],
		AuthorName:  truncateCommitText(parts[1], commitIdentityMaxBytes),
		AuthorEmail: truncateCommitText(parts[2], commitIdentityMaxBytes),
		AuthoredAt:  authoredAt,
		Subject:     truncateCommitText(parts[4], commitMessageMaxBytes),
	}, nil
}

func (m *Manager) resolveRepoBrowserRef(
	ctx context.Context,
	repo RepoBrowserRepoRef,
	ref RepoBrowserRef,
) (dir string, sha string, stale bool, err error) {
	dir, err = m.repoBrowserClonePath(repo)
	if err != nil {
		return "", "", false, err
	}
	switch ref.Type {
	case RepoBrowserRefBranch:
		if strings.TrimSpace(ref.Name) == "" {
			return "", "", false, fmt.Errorf("%w: empty branch", ErrNotFound)
		}
		sha, err = m.resolveRefInDir(ctx, dir, remoteBranchRef(ref.Name))
	case RepoBrowserRefTag:
		if strings.TrimSpace(ref.Name) == "" {
			return "", "", false, fmt.Errorf("%w: empty tag", ErrNotFound)
		}
		sha, err = m.resolveRefInDir(ctx, dir, "refs/tags/"+ref.Name)
	case RepoBrowserRefCommit:
		if !isFullHexSHA(ref.SHA) {
			return "", "", false, fmt.Errorf("%w: %s", ErrNotFound, ref.SHA)
		}
		sha, err = m.resolveRefInDir(ctx, dir, ref.SHA)
	default:
		err = fmt.Errorf("%w: unsupported ref type %q", ErrNotFound, ref.Type)
	}
	if err != nil {
		return "", "", false, err
	}
	return dir, sha, ref.SHA != "" && ref.SHA != sha, nil
}

func (m *Manager) repoBrowserClonePath(repo RepoBrowserRepoRef) (string, error) {
	return m.ClonePath(repo.Host, repo.Owner, repo.Name)
}

func cleanRepoBrowserPath(pathName string) (string, error) {
	if pathName == "" || strings.ContainsRune(pathName, 0) || path.IsAbs(pathName) {
		return "", ErrUnsafePath
	}
	cleaned := path.Clean(strings.ReplaceAll(pathName, "\\", "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrUnsafePath
	}
	return cleaned, nil
}

func mediaTypeForRepoBrowserPath(pathName string) string {
	ext := strings.ToLower(filepath.Ext(pathName))
	if ext == ".svg" {
		return "image/svg+xml"
	}
	if typ := mime.TypeByExtension(ext); typ != "" {
		return typ
	}
	return ""
}

func repoBrowserAssetMediaTypeAllowed(mediaType string) bool {
	typ, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		typ = mediaType
	}
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "image/avif", "image/bmp", "image/gif", "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func isFullHexSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, r := range sha {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
