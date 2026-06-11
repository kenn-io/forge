// Package docs is the filesystem half of the middleman markdown folder
// feature. It exposes a Registry that resolves folder IDs to canonical
// paths, walks folder trees safely (refusing to follow links outside the
// root), and reads/writes files within the folder.
package docs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.kenn.io/middleman/internal/config"
)

// folderIDPattern constrains a folder id to characters that survive as a
// single URL path segment (routes address folders as /docs/folders/{id}/...).
var folderIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateFolderID rejects ids that cannot be addressed as a single path
// segment. Auto-derived ids already satisfy this; explicit ids from the
// API or config must too, or the folder becomes unreachable through the
// {id} routes even though it persists. Returns ErrInvalidFolder so HTTP
// handlers map it to a 400 with reason "invalidFolder".
func ValidateFolderID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidFolder)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w: id %q is reserved", ErrInvalidFolder, id)
	}
	if !folderIDPattern.MatchString(id) {
		return fmt.Errorf("%w: id %q may contain only letters, digits, '.', '_' or '-'", ErrInvalidFolder, id)
	}
	return nil
}

// ErrFolderNotFound is returned when a request references an unknown folder id.
var ErrFolderNotFound = errors.New("folder not found")

// ErrDuplicateFolderID is returned by Add when the id is already registered.
var ErrDuplicateFolderID = errors.New("duplicate folder id")

// ErrOutsideFolder is returned when a requested path resolves outside the
// folder root after following symlinks.
var ErrOutsideFolder = errors.New("path escapes folder root")

// ErrUnsupportedExtension is returned by ReadBlob when the requested file
// is not an allowed image type. The endpoint maps this to 415.
var ErrUnsupportedExtension = errors.New("unsupported extension")

// ErrAlreadyExists is returned by CreateFile / RenameFile when the
// destination path already has a file - the file ops UI uses this to
// surface a "name in use" error instead of overwriting.
var ErrAlreadyExists = errors.New("file already exists")

// ErrInvalidFolder is returned by Add / Rename when the caller-supplied
// folder fields fail validation (missing id/path/name, path is a
// regular file, etc.). HTTP handlers can errors.Is() against this
// sentinel to map registry validation back to 400 instead of 500.
var ErrInvalidFolder = errors.New("invalid folder")

// File extensions surfaced by the tree walker. .markdown is rare but
// real; .txt is intentionally excluded so the docs viewer doesn't pick
// up arbitrary text files in a repo.
var markdownExts = map[string]struct{}{
	".md":       {},
	".markdown": {},
}

// Image extensions the blob endpoint will serve. Anything outside this
// allowlist is refused so we don't accidentally turn the docs server
// into a generic file-server for arbitrary file types. SVG is
// intentionally excluded - folder-hosted SVGs can embed <script>, and
// serving them same-origin with image/svg+xml would let that script
// run in the middleman origin.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// Ignore rules are sourced from a per-folder matcher built in
// ignore.go: a hardcoded baseline (.git, node_modules, .DS_Store, ...)
// plus any .gitignore (nested) and .middlemanignore (root-only) the
// folder ships.

// Registry resolves folder IDs to their canonical root. Built once at
// startup from the loaded config and mutated only through Add / Remove
// / Rename when the user edits the folder list at runtime.
//
// The `mu` RWMutex protects the folder list itself (folders + byID) so
// readers like Folders / Lookup are wait-free against each other but
// see consistent snapshots when a writer is mid-mutation. The
// `writeMu` mutex serializes file mutations (Create/Delete/Rename/Write)
// across all folders so the exists-check + write/rename pairs in
// CreateFile and RenameFile are atomic against other in-process
// callers - a per-process lock is sufficient since the docs server is
// the only writer in the supported single-user deployment.
type Registry struct {
	mu      sync.RWMutex
	folders []config.DocFolder
	byID    map[string]config.DocFolder
	writeMu sync.Mutex
}

// NewRegistry returns a Registry over the given folders. Paths are
// resolved through filepath.EvalSymlinks so that path-safety checks
// compare apples to apples on platforms (macOS) where the TempDir and
// real path differ via a top-level symlink.
func NewRegistry(folders []config.DocFolder) *Registry {
	resolved := make([]config.DocFolder, 0, len(folders))
	byID := make(map[string]config.DocFolder, len(folders))
	for _, v := range folders {
		if real, err := filepath.EvalSymlinks(v.Path); err == nil {
			v.Path = real
		}
		resolved = append(resolved, v)
		byID[v.ID] = v
	}
	return &Registry{folders: resolved, byID: byID}
}

// Replace swaps the registered folder snapshot in place. Readers keep a
// consistent view through the registry mutex, and ongoing file operations
// that already resolved a folder continue against that resolved path.
func (r *Registry) Replace(folders []config.DocFolder) {
	next := NewRegistry(folders)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.folders = next.folders
	r.byID = next.byID
}

// Folders returns the registered folders, with their canonical paths.
func (r *Registry) Folders() []config.DocFolder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]config.DocFolder, len(r.folders))
	copy(out, r.folders)
	return out
}

// Lookup resolves a folder id to its config entry.
func (r *Registry) Lookup(id string) (config.DocFolder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.byID[id]
	if !ok {
		return config.DocFolder{}, ErrFolderNotFound
	}
	return v, nil
}

// Add registers a new folder at runtime. The path is normalized the
// same way config loading does (tilde expansion, absolute, symlink
// resolution) so callers can pass user-entered strings directly; the
// path must then exist as a directory. Returns ErrDuplicateFolderID
// if the id is already in use, and wraps the underlying stat error if
// the path is missing or not a directory.
//
// Duplicate ID is checked before any filesystem work so a duplicate add
// against a bogus path still surfaces as ErrDuplicateFolderID rather
// than a confusing "no such file" message. The check is repeated under
// the write lock to close the window where two concurrent Add calls
// race on the same id between the early check and the insert.
func (r *Registry) Add(v config.DocFolder) error {
	v.ID = strings.TrimSpace(v.ID)
	v.Name = strings.TrimSpace(v.Name)
	v.Path = strings.TrimSpace(v.Path)
	if err := ValidateFolderID(v.ID); err != nil {
		return err
	}
	if v.Path == "" {
		return fmt.Errorf("%w: path is required", ErrInvalidFolder)
	}
	r.mu.RLock()
	if _, dup := r.byID[v.ID]; dup {
		r.mu.RUnlock()
		return fmt.Errorf("%w: %q", ErrDuplicateFolderID, v.ID)
	}
	r.mu.RUnlock()
	expanded, err := expandTilde(v.Path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFolder, err)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFolder, err)
	}
	v.Path = abs
	info, err := os.Stat(v.Path)
	if err != nil {
		return fmt.Errorf("folder path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: path %q is not a directory", ErrInvalidFolder, v.Path)
	}
	if real, err := filepath.EvalSymlinks(v.Path); err == nil {
		v.Path = real
	}
	if v.Name == "" {
		v.Name = filepath.Base(v.Path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[v.ID]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicateFolderID, v.ID)
	}
	r.folders = append(r.folders, v)
	r.byID[v.ID] = v
	return nil
}

// DeriveFolderID picks a default folder id from a folder path:
// lowercase the basename, collapse non-alphanumeric runs into single
// dashes, trim trailing dashes, fall back to "folder" when the
// sanitization would otherwise produce an empty string, and append a
// numeric suffix (-2, -3, ...) until the id is unique against the
// provided existing folders. Callers pass an absolute path so the
// derivation is independent of the working directory.
func DeriveFolderID(absPath string, existing []config.DocFolder) string {
	base := strings.ToLower(filepath.Base(absPath))
	var b strings.Builder
	prevDash := false
	for _, r := range base {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	id := strings.TrimRight(b.String(), "-")
	if id == "" {
		id = "folder"
	}
	taken := make(map[string]struct{}, len(existing))
	for _, v := range existing {
		taken[v.ID] = struct{}{}
	}
	if _, dup := taken[id]; !dup {
		return id
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", id, i)
		if _, dup := taken[candidate]; !dup {
			return candidate
		}
	}
}

// expandTilde mirrors config.expandTilde so the registry accepts the
// same "~/Notes" shorthand the TOML loader does.
func expandTilde(path string) (string, error) {
	if path == "~" || (len(path) >= 2 && path[:2] == "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// Remove drops a folder from the registry. Returns ErrFolderNotFound
// when the id is unknown. The underlying filesystem is untouched -
// removal is a registry-only operation.
func (r *Registry) Remove(id string) error {
	id = strings.TrimSpace(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return ErrFolderNotFound
	}
	delete(r.byID, id)
	out := r.folders[:0]
	for _, v := range r.folders {
		if v.ID != id {
			out = append(out, v)
		}
	}
	r.folders = out
	return nil
}

// Rename updates the display name of an existing folder. The id and
// canonical path are unchanged. Returns ErrFolderNotFound when the id
// is unknown.
func (r *Registry) Rename(id, newName string) error {
	id = strings.TrimSpace(id)
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidFolder)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.byID[id]
	if !ok {
		return ErrFolderNotFound
	}
	v.Name = newName
	r.byID[id] = v
	for i := range r.folders {
		if r.folders[i].ID == id {
			r.folders[i].Name = newName
			break
		}
	}
	return nil
}

// Node is one entry in a folder tree. Directories list their children;
// files have Size and a posix-style RelPath rooted at the folder.
type Node struct {
	Name     string `json:"name"`
	RelPath  string `json:"rel_path"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size,omitempty"`
	Children []Node `json:"children,omitempty"`
}

// Tree returns the recursive markdown file tree for a folder. Empty
// directories are dropped so the UI doesn't render dead branches.
func (r *Registry) Tree(folderID string) (Node, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return Node{}, err
	}
	ig, err := loadFolderIgnore(v.Path)
	if err != nil {
		return Node{}, err
	}
	root, err := buildTree(v.Path, v.Path, ig)
	if err != nil {
		return Node{}, err
	}
	root.Name = v.Name
	root.RelPath = ""
	return root, nil
}

func buildTree(root, dir string, ig *folderIgnore) (Node, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Node{}, err
	}
	node := Node{
		Name:    filepath.Base(dir),
		RelPath: relPath(root, dir),
		IsDir:   true,
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(dir, name)
		rel := relPath(root, full)
		if ig.Match(rel, entry.IsDir()) {
			continue
		}
		if entry.IsDir() {
			child, err := buildTree(root, full, ig)
			if err != nil {
				return Node{}, err
			}
			if len(child.Children) > 0 {
				node.Children = append(node.Children, child)
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := markdownExts[ext]; !ok {
			continue
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Node{}, err
		}
		if !pathWithin(root, resolved) {
			continue
		}
		if err := ensureMarkdown(resolved); err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return Node{}, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		node.Children = append(node.Children, Node{
			Name:    name,
			RelPath: rel,
			IsDir:   false,
			Size:    info.Size(),
		})
	}
	return node, nil
}

func relPath(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ReadFile returns the bytes of relPath within the folder. It refuses
// paths that resolve outside the folder root (e.g. via ..) and refuses
// any non-markdown file or anything matching the folder's ignore
// rules, so `?path=.git/...` and binary blobs aren't readable through
// this API.
func (r *Registry) ReadFile(folderID, relPath string) ([]byte, error) {
	if err := r.checkIgnored(folderID, relPath); err != nil {
		return nil, err
	}
	resolved, err := r.resolve(folderID, relPath)
	if err != nil {
		return nil, err
	}
	if err := ensureMarkdown(resolved); err != nil {
		return nil, err
	}
	if err := ensureRegularFile(resolved); err != nil {
		return nil, err
	}
	return os.ReadFile(resolved)
}

// checkIgnored looks up the folder, compiles its ignore matcher, and
// returns ErrOutsideFolder if relPath matches. Used by every read /
// write / rename / delete entry point so a stale URL to an ignored
// file gets the same 404 as the tree's hidden state - and so a
// future write can't resurrect content under an ignored path.
func (r *Registry) checkIgnored(folderID, rel string) error {
	v, err := r.Lookup(folderID)
	if err != nil {
		return err
	}
	ig, err := loadFolderIgnore(v.Path)
	if err != nil {
		return err
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if ig.Match(clean, false) || ig.Match(clean, true) {
		return ErrOutsideFolder
	}
	return nil
}

// Blob is the content + content-type for an image served through the
// blob endpoint. Markdown viewers render <img> tags that point at the
// endpoint, so we need to set the right Content-Type header.
type Blob struct {
	ContentType string
	Body        []byte
}

// ReadBlob returns an image file from the folder. Non-image extensions
// are rejected so the endpoint can't be used to fetch arbitrary files.
// The extension check runs on the resolved path so an in-folder symlink
// pointing at a non-image (e.g. a script) is also refused.
func (r *Registry) ReadBlob(folderID, relPath string) (Blob, error) {
	if err := r.checkIgnored(folderID, relPath); err != nil {
		return Blob{}, err
	}
	resolved, err := r.resolve(folderID, relPath)
	if err != nil {
		return Blob{}, err
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	mime, ok := imageExts[ext]
	if !ok {
		return Blob{}, fmt.Errorf("%w: %q", ErrUnsupportedExtension, ext)
	}
	if err := ensureRegularFile(resolved); err != nil {
		return Blob{}, err
	}
	body, err := os.ReadFile(resolved)
	if err != nil {
		return Blob{}, err
	}
	return Blob{ContentType: mime, Body: body}, nil
}

// WriteFile writes content atomically: write to a sibling tempfile then
// rename. Refuses paths outside the folder root and refuses to create
// files outside an existing directory (so we don't materialize whole
// trees by accident from a typo'd path).
//
// Holds writeMu around the final rename so a concurrent RenameFile
// (which Lstats the destination then renames into it) can't see an
// "absent" destination here right before this rename creates it.
func (r *Registry) WriteFile(folderID, relPath string, content []byte) error {
	if err := r.checkIgnored(folderID, relPath); err != nil {
		return err
	}
	resolved, err := r.resolve(folderID, relPath)
	if err != nil {
		return err
	}
	if err := ensureMarkdown(resolved); err != nil {
		return err
	}
	if err := ensureRegularFileIfExists(resolved); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(resolved); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(resolved)
	if info, err := os.Stat(dir); err != nil {
		return fmt.Errorf("parent dir: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("parent %q is not a directory", dir)
	}
	tmp, err := os.CreateTemp(dir, ".middleman-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return os.Rename(tmpName, resolved)
}

// CreateFile writes content to a new file at relPath. Unlike WriteFile,
// it refuses to overwrite an existing file - the file ops UI uses this
// to materialize a fresh document without accidentally clobbering one.
//
// Uses O_CREATE|O_EXCL so the kernel enforces no-overwrite atomically.
// A separate exists-check pair (Lstat then WriteFile) would leave a TOCTTOU
// gap that lets a concurrent caller clobber a file in the race window.
func (r *Registry) CreateFile(folderID, relPath string, content []byte) error {
	if err := r.checkIgnored(folderID, relPath); err != nil {
		return err
	}
	resolved, err := r.resolve(folderID, relPath)
	if err != nil {
		return err
	}
	if err := ensureMarkdown(resolved); err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	if info, err := os.Stat(dir); err != nil {
		return fmt.Errorf("parent dir: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("parent %q is not a directory", dir)
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := ensureRegularFileIfExists(resolved); err != nil {
		return err
	}
	f, err := os.OpenFile(resolved, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyExists
		}
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(content); err != nil {
		return err
	}
	return nil
}

// DeleteFile removes the file at relPath. Refuses non-markdown,
// outside-folder, and ignored-dir paths so the API can't be used to
// drop arbitrary files in the user's home directory.
func (r *Registry) DeleteFile(folderID, relPath string) error {
	if err := r.checkIgnored(folderID, relPath); err != nil {
		return err
	}
	lexical, err := r.lexicalPath(folderID, relPath)
	if err != nil {
		return err
	}
	if err := ensureMarkdown(lexical); err != nil {
		return err
	}
	resolved, err := r.resolve(folderID, relPath)
	if err != nil {
		return err
	}
	if err := ensureRegularFile(resolved); err != nil {
		return err
	}
	return os.Remove(lexical)
}

// RenameFile moves the file at `from` to `to` within the same folder.
// Both endpoints must be inside the folder and end in .md/.markdown.
// Refuses to overwrite an existing destination so a typo doesn't
// silently merge two notes.
//
// Holds writeMu around the exists-check + rename pair so a concurrent
// CreateFile/RenameFile can't sneak a file into the destination between
// the Lstat and Rename. Cross-platform RENAME_NOREPLACE isn't available
// in stdlib, so process-level serialization is the portable substitute.
func (r *Registry) RenameFile(folderID, from, to string) error {
	if err := r.checkIgnored(folderID, from); err != nil {
		return err
	}
	if err := r.checkIgnored(folderID, to); err != nil {
		return err
	}
	srcLexical, err := r.lexicalPath(folderID, from)
	if err != nil {
		return err
	}
	if err := ensureMarkdown(srcLexical); err != nil {
		return err
	}
	srcResolved, err := r.resolve(folderID, from)
	if err != nil {
		return err
	}
	if err := ensureRegularFile(srcResolved); err != nil {
		return err
	}
	dstLexical, err := r.lexicalPath(folderID, to)
	if err != nil {
		return err
	}
	if err := ensureMarkdown(dstLexical); err != nil {
		return err
	}
	dstResolved, err := r.resolve(folderID, to)
	if err != nil {
		return err
	}
	if srcLexical == dstLexical {
		return nil
	}
	if _, err := os.Stat(filepath.Dir(dstResolved)); err != nil {
		return fmt.Errorf("parent dir: %w", err)
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := ensureRegularFileIfExists(dstResolved); err != nil {
		return err
	}
	if _, err := os.Lstat(dstLexical); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(srcLexical, dstLexical)
}

// resolve canonicalizes relPath against the folder root and verifies the
// final path is inside the root (after symlink resolution if the file
// exists). For new files the parent directory's canonical path is
// resolved instead, so an in-folder symlinked directory pointing outside
// the folder can't be used to create files outside the folder root.
func (r *Registry) resolve(folderID, relPath string) (string, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return "", err
	}
	clean, err := cleanRelativePath(relPath)
	if err != nil {
		return "", err
	}
	full := filepath.Join(v.Path, clean)
	// Resolve symlinks if the target exists.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		if !pathWithin(v.Path, real) {
			return "", ErrOutsideFolder
		}
		return real, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// Target doesn't exist (new file). Resolve the parent dir's
	// canonical path and confirm it's still inside the folder - a
	// lexical check on `full` can pass while the parent symlinks out.
	parent := filepath.Dir(full)
	realParent, parentErr := filepath.EvalSymlinks(parent)
	if parentErr == nil {
		if !pathWithin(v.Path, realParent) {
			return "", ErrOutsideFolder
		}
		return filepath.Join(realParent, filepath.Base(clean)), nil
	}
	if !errors.Is(parentErr, fs.ErrNotExist) {
		return "", parentErr
	}
	// Parent missing too - WriteFile will fail on the parent stat;
	// fall back to the lexical bound so the caller still sees a
	// reasonable error rather than a misleading symlink message.
	if !pathWithin(v.Path, full) {
		return "", ErrOutsideFolder
	}
	return full, nil
}

func (r *Registry) lexicalPath(folderID, relPath string) (string, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return "", err
	}
	clean, err := cleanRelativePath(relPath)
	if err != nil {
		return "", err
	}
	full := filepath.Join(v.Path, clean)
	if !pathWithin(v.Path, full) {
		return "", ErrOutsideFolder
	}
	return full, nil
}

func cleanRelativePath(relPath string) (string, error) {
	clean := filepath.Clean(relPath)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrOutsideFolder
	}
	return clean, nil
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ensureMarkdown(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := markdownExts[ext]; !ok {
		// Wrap so callers can errors.Is(err, ErrUnsupportedExtension)
		// and the HTTP layer returns 415, matching mock-mode parity.
		return fmt.Errorf("%w: only .md/.markdown files can be written, got %q", ErrUnsupportedExtension, ext)
	}
	return nil
}

func ensureRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: path %q is not a regular file", ErrInvalidFolder, path)
	}
	return nil
}

func ensureRegularFileIfExists(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: path %q is not a regular file", ErrInvalidFolder, path)
	}
	return nil
}
