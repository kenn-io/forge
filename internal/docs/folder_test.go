package docs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	must := func(rel, body string) {
		req := require.New(t)
		full := filepath.Join(root, rel)
		req.NoError(os.MkdirAll(filepath.Dir(full), 0o755))
		req.NoError(os.WriteFile(full, []byte(body), 0o644))
	}
	must("index.md", "# Home")
	must("notes/daily/2026-05-15.md", "# Today")
	must("notes/daily/2026-05-16.md", "# Tomorrow")
	must("notes/ideas.md", "# Ideas")
	must("README.md", "# Readme")
	// Should be skipped:
	must(".git/HEAD", "ref")
	must("node_modules/foo/bar.md", "# nope")
	must("notes/image.png", "binary")

	return NewRegistry([]config.DocFolder{
		{ID: "notes", Name: "Notes", Path: root},
	}), root
}

func TestTreeListsMarkdownAndSkipsHidden(t *testing.T) {
	assert := Assert.New(t)
	r, _ := newTestRegistry(t)
	tree, err := r.Tree("notes")
	require.NoError(t, err)
	assert.Equal("Notes", tree.Name)

	names := walkNames(tree)
	mustHave := []string{"index.md", "README.md", "notes/daily/2026-05-15.md", "notes/ideas.md"}
	for _, want := range mustHave {
		assert.Contains(names, want, "tree names")
	}
	for _, banned := range []string{".git/HEAD", "node_modules/foo/bar.md", "notes/image.png"} {
		assert.NotContains(names, banned, "tree names")
	}
}

func TestTreeAndSearchSkipNonRegularMarkdownEntries(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	targetDir := filepath.Join(root, "notes", "target")
	req.NoError(os.MkdirAll(targetDir, 0o755))
	link := filepath.Join(root, "notes", "directory-link.md")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	targetFile := filepath.Join(root, "notes", "target-file.md")
	req.NoError(os.WriteFile(targetFile, []byte("symlinked target\n"), 0o644))
	fileLink := filepath.Join(root, "notes", "regular-link.md")
	req.NoError(os.Symlink(targetFile, fileLink))

	tree, err := r.Tree("notes")
	req.NoError(err)
	assert := Assert.New(t)
	names := walkNames(tree)
	assert.NotContains(names, "notes/directory-link.md")
	assert.Contains(names, "notes/regular-link.md")

	hits, err := r.Search("notes", "directory-link", 0)
	req.NoError(err)
	assert.Empty(hits)
	hits, err = r.Search("notes", "regular-link", 0)
	req.NoError(err)
	req.NotEmpty(hits)
	assert.Equal("notes/regular-link.md", hits[0].RelPath)
}

func TestTreeUnknownFolder(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, err := r.Tree("missing")
	Assert.ErrorIs(t, err, ErrFolderNotFound)
}

func TestReadFileRoundTrip(t *testing.T) {
	r, _ := newTestRegistry(t)
	body, err := r.ReadFile("notes", "notes/ideas.md")
	require.NoError(t, err)
	Assert.Equal(t, "# Ideas", string(body))
}

func TestReadFileRefusesTraversal(t *testing.T) {
	r, _ := newTestRegistry(t)
	cases := []string{
		"../../../etc/passwd",
		"notes/../../escape.md",
		"/etc/passwd",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			_, err := r.ReadFile("notes", p)
			Assert.ErrorIs(t, err, ErrOutsideFolder)
		})
	}
}

func TestReadFileRefusesEscapingSymlink(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	req.NoError(os.WriteFile(outside, []byte("hidden"), 0o600))
	link := filepath.Join(root, "escape.md")
	req.NoError(os.Symlink(outside, link))
	_, err := r.ReadFile("notes", "escape.md")
	Assert.ErrorIs(t, err, ErrOutsideFolder, "symlink escape should be refused")
}

func TestWriteFileAtomic(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(r.WriteFile("notes", "notes/ideas.md", []byte("new body")))
	body, err := os.ReadFile(filepath.Join(root, "notes/ideas.md"))
	req.NoError(err)
	Assert.Equal(t, "new body", string(body))
}

func TestWriteFilePreservesMode(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	target := filepath.Join(root, "notes/ideas.md")
	req.NoError(os.Chmod(target, 0o664))

	req.NoError(r.WriteFile("notes", "notes/ideas.md", []byte("new body")))

	info, err := os.Stat(target)
	req.NoError(err)
	Assert.Equal(t, fs.FileMode(0o664), info.Mode().Perm())
}

func TestWriteFileRefusesNonMarkdown(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.WriteFile("notes", "notes/foo.bin", []byte("x"))
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "only .md")
}

func TestWriteFileRefusesMissingParent(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.WriteFile("notes", "no/such/dir/file.md", []byte("x"))
	require.Error(t, err, "expected parent-missing error")
}

func TestReadBlobServesImageWithMime(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	pngData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	req.NoError(os.WriteFile(filepath.Join(root, "notes/avatar.png"), pngData, 0o644))
	blob, err := r.ReadBlob("notes", "notes/avatar.png")
	req.NoError(err)
	assert := Assert.New(t)
	assert.Equal("image/png", blob.ContentType)
	assert.True(bytes.Equal(blob.Body, pngData), "body mismatch")
}

func TestReadBlobRefusesNonImage(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, err := r.ReadBlob("notes", "notes/ideas.md")
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "unsupported extension")
}

func TestReadBlobRefusesTraversal(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, err := r.ReadBlob("notes", "../escape.png")
	Assert.ErrorIs(t, err, ErrOutsideFolder)
}

func TestReadBlobRefusesSVG(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(os.WriteFile(filepath.Join(root, "notes/icon.svg"), []byte("<svg/>"), 0o644))
	_, err := r.ReadBlob("notes", "notes/icon.svg")
	req.Error(err)
	Assert.ErrorIs(t, err, ErrUnsupportedExtension)
}

func TestReadFileRefusesNonMarkdown(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(os.WriteFile(filepath.Join(root, "notes/secret.bin"), []byte("x"), 0o644))
	_, err := r.ReadFile("notes", "notes/secret.bin")
	req.Error(err)
	Assert.Contains(t, err.Error(), "only .md")
}

func TestReadFileRefusesNonRegularMarkdownTarget(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(os.Mkdir(filepath.Join(root, "notes", "folder.md"), 0o755))

	_, err := r.ReadFile("notes", "notes/folder.md")
	req.ErrorIs(err, ErrInvalidFolder)
}

func TestReadBlobRefusesNonRegularImageTarget(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(os.Mkdir(filepath.Join(root, "notes", "avatar-dir.png"), 0o755))

	_, err := r.ReadBlob("notes", "notes/avatar-dir.png")
	req.ErrorIs(err, ErrInvalidFolder)
}

func TestReadFileRefusesIgnoredDir(t *testing.T) {
	r, _ := newTestRegistry(t)
	// .git/HEAD exists in the fixture; reading it would expose repo state.
	_, err := r.ReadFile("notes", ".git/HEAD")
	Assert.ErrorIs(t, err, ErrOutsideFolder)
}

func TestCreateFileFailsOnExisting(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.CreateFile("notes", "README.md", []byte("clobber"))
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestCreateFileWritesNewFile(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(r.CreateFile("notes", "notes/new.md", []byte("# Fresh")))
	body, err := os.ReadFile(filepath.Join(root, "notes/new.md"))
	req.NoError(err)
	Assert.Equal(t, "# Fresh", string(body))
}

func TestFileMutationsRefuseExistingNonRegularTargets(t *testing.T) {
	req := require.New(t)
	assert := Assert.New(t)
	r, root := newTestRegistry(t)

	writeTarget := filepath.Join(root, "notes", "write-dir.md")
	createTarget := filepath.Join(root, "notes", "create-dir.md")
	deleteTarget := filepath.Join(root, "notes", "delete-dir.md")
	renameSource := filepath.Join(root, "notes", "rename-source.md")
	renameDest := filepath.Join(root, "notes", "rename-dest.md")
	for _, path := range []string{writeTarget, createTarget, deleteTarget, renameSource, renameDest} {
		req.NoError(os.Mkdir(path, 0o755))
	}

	err := r.WriteFile("notes", "notes/write-dir.md", []byte("x"))
	req.ErrorIs(err, ErrInvalidFolder)
	err = r.CreateFile("notes", "notes/create-dir.md", []byte("x"))
	req.ErrorIs(err, ErrInvalidFolder)
	err = r.DeleteFile("notes", "notes/delete-dir.md")
	req.ErrorIs(err, ErrInvalidFolder)
	err = r.RenameFile("notes", "notes/rename-source.md", "notes/new-name.md")
	req.ErrorIs(err, ErrInvalidFolder)
	err = r.RenameFile("notes", "README.md", "notes/rename-dest.md")
	req.ErrorIs(err, ErrInvalidFolder)

	for _, path := range []string{writeTarget, createTarget, deleteTarget, renameSource, renameDest} {
		info, statErr := os.Stat(path)
		req.NoError(statErr)
		assert.True(info.IsDir(), "%s should remain a directory", path)
	}
}

func TestDeleteFileRemovesAndRefuses(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(r.DeleteFile("notes", "notes/ideas.md"))
	if _, err := os.Stat(filepath.Join(root, "notes/ideas.md")); !errors.Is(err, os.ErrNotExist) {
		req.ErrorIs(err, os.ErrNotExist, "file should be gone")
	}
	// Refuses non-markdown.
	req.NoError(os.WriteFile(filepath.Join(root, "notes/image.png"), []byte{0}, 0o644))
	if err := r.DeleteFile("notes", "notes/image.png"); err == nil || !strings.Contains(err.Error(), "only .md") {
		req.Error(err)
		Assert.Contains(t, err.Error(), "only .md")
	}
}

func TestDeleteFileRemovesSymlinkNotTarget(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	target := filepath.Join(root, "notes", "target.md")
	link := filepath.Join(root, "notes", "linked.md")
	req.NoError(os.WriteFile(target, []byte("target"), 0o644))
	req.NoError(os.Symlink(target, link))

	req.NoError(r.DeleteFile("notes", "notes/linked.md"))

	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		req.ErrorIs(err, os.ErrNotExist)
	}
	body, err := os.ReadFile(target)
	req.NoError(err)
	Assert.Equal(t, "target", string(body))
}

func TestRenameFileMovesAndRefusesCollision(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	req.NoError(r.RenameFile("notes", "notes/ideas.md", "notes/ideas-renamed.md"))
	if _, err := os.Stat(filepath.Join(root, "notes/ideas-renamed.md")); err != nil {
		req.NoError(err, "dest missing")
	}
	// Refuses to clobber an existing destination.
	err := r.RenameFile("notes", "README.md", "notes/ideas-renamed.md")
	Assert.ErrorIs(t, err, ErrAlreadyExists)
}

func TestRenameFileMovesSymlinkNotTarget(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	target := filepath.Join(root, "notes", "target.md")
	link := filepath.Join(root, "notes", "linked.md")
	renamed := filepath.Join(root, "notes", "linked-renamed.md")
	req.NoError(os.WriteFile(target, []byte("target"), 0o644))
	req.NoError(os.Symlink(target, link))

	req.NoError(r.RenameFile("notes", "notes/linked.md", "notes/linked-renamed.md"))

	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		req.ErrorIs(err, os.ErrNotExist)
	}
	info, err := os.Lstat(renamed)
	req.NoError(err)
	Assert.Equal(t, fs.ModeSymlink, info.Mode()&fs.ModeSymlink)
	body, err := os.ReadFile(target)
	req.NoError(err)
	Assert.Equal(t, "target", string(body))
}

func TestWriteFileRefusesSymlinkParentEscape(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)
	outside := t.TempDir()
	escape := filepath.Join(root, "escape-dir")
	req.NoError(os.Symlink(outside, escape))
	err := r.WriteFile("notes", "escape-dir/new.md", []byte("hello"))
	req.ErrorIs(err, ErrOutsideFolder, "expected ErrOutsideFolder for symlinked parent")
	if _, statErr := os.Stat(filepath.Join(outside, "new.md")); statErr == nil {
		req.Error(statErr, "write should not have created file outside folder")
	}
}

func TestWriteFileAllowsLeadingDotDotFilename(t *testing.T) {
	req := require.New(t)
	r, root := newTestRegistry(t)

	req.NoError(r.WriteFile("notes", "..notes.md", []byte("ok")))

	got, err := os.ReadFile(filepath.Join(root, "..notes.md"))
	req.NoError(err)
	Assert.Equal(t, "ok", string(got))
}

func TestSearchScoresExactBeforeSubstring(t *testing.T) {
	req := require.New(t)
	r, _ := newTestRegistry(t)
	hits, err := r.Search("notes", "ideas", 0)
	req.NoError(err)
	req.NotEmpty(hits, "expected hits for 'ideas'")
	Assert.Equal(t, "ideas.md", hits[0].Name)
}

func TestSearchRespectsLimit(t *testing.T) {
	r, _ := newTestRegistry(t)
	hits, err := r.Search("notes", "2026", 1)
	require.NoError(t, err)
	Assert.Len(t, hits, 1, "limit ignored")
}

// helpers --------------------------------------------------------------

func walkNames(n Node) []string {
	var out []string
	var walk func(Node)
	walk = func(node Node) {
		for _, child := range node.Children {
			if child.IsDir {
				walk(child)
				continue
			}
			out = append(out, child.RelPath)
		}
	}
	walk(n)
	return out
}

func TestRegistryAdd(t *testing.T) {
	req := require.New(t)
	assert := Assert.New(t)
	r := NewRegistry(nil)
	dir := t.TempDir()
	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: dir}))
	v, err := r.Lookup("notes")
	req.NoError(err)
	// EvalSymlinks resolves /var -> /private/var on macOS; compare against
	// whatever the resolver produced rather than the literal temp dir.
	want, _ := filepath.EvalSymlinks(dir)
	assert.Equal(want, v.Path)
	assert.Equal(filepath.Base(dir), v.Name)
	assert.Len(r.Folders(), 1)
}

func TestRegistryAddResolvesSymlinkRoot(t *testing.T) {
	req := require.New(t)
	assert := Assert.New(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	link := filepath.Join(parent, "link")
	req.NoError(os.Mkdir(target, 0o755))
	req.NoError(os.Symlink(target, link))
	r := NewRegistry(nil)

	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: link}))

	got, err := r.Lookup("notes")
	req.NoError(err)
	want, err := filepath.EvalSymlinks(target)
	req.NoError(err)
	assert.Equal(want, got.Path)
}

func TestRegistryAddRejectsDuplicateID(t *testing.T) {
	r := NewRegistry(nil)
	dir := t.TempDir()
	require.NoError(t, r.Add(config.DocFolder{ID: "notes", Path: dir}))
	err := r.Add(config.DocFolder{ID: "notes", Path: dir})
	Assert.ErrorIs(t, err, ErrDuplicateFolderID)
}

func TestRegistryAddRejectsMissingPath(t *testing.T) {
	r := NewRegistry(nil)
	err := r.Add(config.DocFolder{ID: "ghost", Path: "/nope/definitely-not-here"})
	require.Error(t, err, "expected error for missing path")
}

func TestRegistryAddRejectsFilePath(t *testing.T) {
	req := require.New(t)
	r := NewRegistry(nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.md")
	req.NoError(os.WriteFile(file, []byte("hi"), 0o644))
	err := r.Add(config.DocFolder{ID: "bad", Path: file})
	req.Error(err)
	Assert.Contains(t, err.Error(), "not a directory")
}

func TestRegistryAddRequiresIDAndPath(t *testing.T) {
	r := NewRegistry(nil)
	require.Error(t, r.Add(config.DocFolder{Path: t.TempDir()}), "expected error when id is empty")
	require.Error(t, r.Add(config.DocFolder{ID: "x"}), "expected error when path is empty")
}

func TestRegistryAddRejectsNonSegmentSafeID(t *testing.T) {
	assert := Assert.New(t)
	dir := t.TempDir()
	for _, id := range []string{"a/b", "with space", "..", ".", "tab\tid", "weird?id", "emoji😀"} {
		err := NewRegistry(nil).Add(config.DocFolder{ID: id, Path: dir})
		assert.ErrorIs(err, ErrInvalidFolder, "id %q should be rejected", id)
	}
}

func TestRegistryAddAcceptsSegmentSafeID(t *testing.T) {
	assert := Assert.New(t)
	for _, id := range []string{"notes", "My_Docs", "team.docs", "a-b-2"} {
		err := NewRegistry(nil).Add(config.DocFolder{ID: id, Path: t.TempDir()})
		assert.NoError(err, "id %q should be accepted", id)
	}
}

func TestValidateFolderID(t *testing.T) {
	require := require.New(t)
	require.NoError(ValidateFolderID("notes"))
	require.NoError(ValidateFolderID("a.b_c-2"))
	require.ErrorIs(ValidateFolderID(""), ErrInvalidFolder)
	require.ErrorIs(ValidateFolderID("a/b"), ErrInvalidFolder)
	require.ErrorIs(ValidateFolderID(".."), ErrInvalidFolder)
}

// Duplicate ID must trump a bogus path so the error surface stays
// predictable when someone retries an "already registered" id against a
// path that's since been moved or deleted.
func TestRegistryAddDuplicateIDBeatsBadPath(t *testing.T) {
	r := NewRegistry(nil)
	dir := t.TempDir()
	require.NoError(t, r.Add(config.DocFolder{ID: "notes", Path: dir}))
	err := r.Add(config.DocFolder{ID: "notes", Path: "/nope/missing"})
	Assert.ErrorIs(t, err, ErrDuplicateFolderID, "duplicate ID must beat path error")
}

func TestRegistryAddExpandsTilde(t *testing.T) {
	req := require.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	sub := filepath.Join(home, "Notes")
	req.NoError(os.MkdirAll(sub, 0o755))
	r := NewRegistry(nil)
	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: "~/Notes"}))
	got, err := r.Lookup("notes")
	req.NoError(err)
	assert := Assert.New(t)
	assert.True(strings.HasSuffix(got.Path, "Notes"), "stored path = %q, want path ending in 'Notes'", got.Path)
	assert.True(filepath.IsAbs(got.Path), "stored path = %q, want absolute", got.Path)
}

func TestRegistryAddResolvesRelative(t *testing.T) {
	req := require.New(t)
	dir := t.TempDir()
	t.Chdir(dir)
	req.NoError(os.Mkdir("Notes", 0o755))
	r := NewRegistry(nil)
	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: "Notes"}))
	got, err := r.Lookup("notes")
	req.NoError(err)
	Assert.True(t, filepath.IsAbs(got.Path), "stored path = %q, want absolute", got.Path)
}

func TestRegistryRemove(t *testing.T) {
	req := require.New(t)
	r := NewRegistry(nil)
	dir := t.TempDir()
	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: dir}))
	req.NoError(r.Remove("notes"))
	if _, err := r.Lookup("notes"); !errors.Is(err, ErrFolderNotFound) {
		Assert.ErrorIs(t, err, ErrFolderNotFound)
	}
	Assert.Empty(t, r.Folders())
}

func TestRegistryRemoveUnknown(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.Remove("ghost"); !errors.Is(err, ErrFolderNotFound) {
		Assert.ErrorIs(t, err, ErrFolderNotFound)
	}
}

func TestRegistryRemovePreservesOthers(t *testing.T) {
	r := NewRegistry(nil)
	a := t.TempDir()
	b := t.TempDir()
	req := require.New(t)
	req.NoError(r.Add(config.DocFolder{ID: "a", Path: a}))
	req.NoError(r.Add(config.DocFolder{ID: "b", Path: b}))
	req.NoError(r.Remove("a"))
	got := r.Folders()
	require.Len(t, got, 1)
	Assert.Equal(t, "b", got[0].ID)
}

func TestRegistryRename(t *testing.T) {
	r := NewRegistry(nil)
	dir := t.TempDir()
	req := require.New(t)
	req.NoError(r.Add(config.DocFolder{ID: "notes", Path: dir}))
	req.NoError(r.Rename("notes", "My Notes"))
	v, _ := r.Lookup("notes")
	assert := Assert.New(t)
	assert.Equal("My Notes", v.Name)
	assert.Equal("My Notes", r.Folders()[0].Name)
}

func TestRegistryRenameRejectsEmpty(t *testing.T) {
	r := NewRegistry(nil)
	dir := t.TempDir()
	require.NoError(t, r.Add(config.DocFolder{ID: "notes", Path: dir}))
	if err := r.Rename("notes", ""); err == nil {
		Assert.Error(t, err, "expected error for empty name")
	}
}

func TestRegistryRenameUnknown(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.Rename("ghost", "anything"); !errors.Is(err, ErrFolderNotFound) {
		Assert.ErrorIs(t, err, ErrFolderNotFound)
	}
}

func TestDeriveFolderIDSanitizes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/tmp/My Notes!", "my-notes"},
		{"/tmp/research_papers", "research-papers"},
		{"/tmp/Notes 2026", "notes-2026"},
		{"/tmp/...", "folder"},
	}
	for _, tc := range cases {
		got := DeriveFolderID(tc.in, nil)
		Assert.Equal(t, tc.want, got, "DeriveFolderID(%q)", tc.in)
	}
}

func TestDeriveFolderIDAvoidsCollisions(t *testing.T) {
	existing := []config.DocFolder{{ID: "notes"}, {ID: "notes-2"}}
	Assert.Equal(t, "notes-3", DeriveFolderID("/tmp/notes", existing))
}
