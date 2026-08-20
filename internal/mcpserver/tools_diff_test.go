package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetItemDiffSummaryUsesBackendWithoutReturningPatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got ItemIdentity
	var includePatches bool
	backend := &fakeBackend{getPullDiffFn: func(_ context.Context, item ItemIdentity, patches bool) (Diff, error) {
		got = item
		includePatches = patches
		return Diff{Stale: true, Files: []DiffFile{
			{
				Path: "internal/db/queries.go", Status: "modified",
				Additions: 8, Deletions: 3, Patch: "must not leak",
			},
			{
				Path: "assets/logo.png", OldPath: "assets/old-logo.png",
				Status: "renamed", IsBinary: true,
			},
		}}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(err)
	assert.Equal(ItemIdentity{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42}, got)
	assert.False(includePatches)
	assert.True(out.Stale)
	assert.Equal(8, out.TotalAdditions)
	assert.Equal(3, out.TotalDeletions)
	assert.True(out.Files[1].IsBinary)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), "must not leak")
	assert.NotContains(string(raw), `"diff_file"`)
}

func TestGetItemDiffEmitWritesOneBackendSnapshotAndOverwrites(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	calls := 0
	backend := &fakeBackend{getPullDiffFn: func(_ context.Context, _ ItemIdentity, patches bool) (Diff, error) {
		assert.True(patches)
		calls++
		if calls == 1 {
			return Diff{Stale: true, Files: []DiffFile{
				{
					Path: "src/renamed.go", Status: "renamed", Additions: 1, Deletions: 1,
					Patch: "diff --git a/src/old.go b/src/renamed.go\nrename from src/old.go\nrename to src/renamed.go\n",
				},
				{
					Path: "assets/logo.png", Status: "modified", IsBinary: true,
					Patch: "diff --git a/assets/logo.png b/assets/logo.png\nBinary files differ\n",
				},
			}}, nil
		}
		return Diff{Files: []DiffFile{{
			Path: "src/new.go", Status: "added", Additions: 1,
			Patch: "diff --git a/src/new.go b/src/new.go\nnew file mode 100644\n",
		}}}, nil
	}}
	s := newMCPTestServer(t, backend)
	input := getItemDiffInput{
		Item: itemRefInput{
			Type: "pr", Provider: "gitlab", PlatformRepoID: "gitlab-project", PlatformHost: "git.example.test",
			Owner: "group/sub", Name: "project", Number: 42,
		},
		EmitDiffFile: true,
	}

	first, err := s.getItemDiff(t.Context(), input)
	require.NoError(err)
	require.NotNil(first.DiffFile)
	firstData, err := os.ReadFile(first.DiffFile.Path)
	require.NoError(err)
	assert.Equal(2, strings.Count(string(firstData), "diff --git "))
	info, err := os.Stat(first.DiffFile.Path)
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())

	second, err := s.getItemDiff(t.Context(), input)
	require.NoError(err)
	require.NotNil(second.DiffFile)
	assert.Equal(first.DiffFile.Path, second.DiffFile.Path)
	secondData, err := os.ReadFile(second.DiffFile.Path)
	require.NoError(err)
	assert.Equal("diff --git a/src/new.go b/src/new.go\nnew file mode 100644\n", string(secondData))
}

func TestGetItemDiffClassifiesUnavailableIdentityAndLocalFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "unavailable",
			err:  &Error{Kind: "not_found", Code: "notFound", Message: "diff not available"},
			want: "diff_unavailable",
		},
		{
			name: "missing identity",
			err:  &Error{Kind: "not_found", Code: "pullNotFound", Message: "pull request not found"},
			want: "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeBackend{getPullDiffFn: func(context.Context, ItemIdentity, bool) (Diff, error) {
				return Diff{}, tt.err
			}}
			s := newMCPTestServer(t, backend)
			_, err := s.getItemDiff(t.Context(), getItemDiffInput{
				Item: itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42},
			})
			assertBackendErrorKind(t, err, tt.want)
		})
	}

	s := newMCPTestServer(t, &fakeBackend{})
	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{Type: "issue", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 7},
	})
	assertBackendErrorKind(t, err, "invalid_request")
}

func TestGetItemDiffEmitSynthesizesEvidenceForFilesWithoutTextPatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	backend := &fakeBackend{getPullDiffFn: func(context.Context, ItemIdentity, bool) (Diff, error) {
		return Diff{Files: []DiffFile{
			{Path: "assets/logo.png", Status: "modified", IsBinary: true},
			{Path: "src/new.go", OldPath: "src/old.go", Status: "renamed"},
			{Path: "scripts/run.sh", Status: "modified"},
			{Path: "docs/empty.md", Status: "added"},
			{Path: "legacy/empty.cfg", Status: "deleted"},
			{Path: "assets/moved.png", OldPath: "assets/original.png", Status: "renamed", IsBinary: true},
			{Path: "assets/copy.png", OldPath: "assets/original.png", Status: "copied", IsBinary: true},
		}}, nil
	}}
	s := newMCPTestServer(t, backend)

	out, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 42,
		},
		EmitDiffFile: true,
	})

	require.NoError(err)
	require.NotNil(out.DiffFile)
	data, err := os.ReadFile(out.DiffFile.Path)
	require.NoError(err)
	patch := string(data)
	assert.Contains(patch, "diff --git a/assets/logo.png b/assets/logo.png\n")
	assert.Contains(patch, "Binary files a/assets/logo.png and b/assets/logo.png differ\n")
	assert.Contains(patch, "diff --git a/src/old.go b/src/new.go\n")
	assert.Contains(patch, "rename from src/old.go\nrename to src/new.go\n")
	assert.Contains(patch, "diff --git a/scripts/run.sh b/scripts/run.sh\n")
	assert.Contains(patch, "diff --git a/docs/empty.md b/docs/empty.md\n--- /dev/null\n+++ b/docs/empty.md\n")
	assert.Contains(patch, "diff --git a/legacy/empty.cfg b/legacy/empty.cfg\n--- a/legacy/empty.cfg\n+++ /dev/null\n")
	assert.Contains(patch, "rename from assets/original.png\nrename to assets/moved.png\nBinary files a/assets/original.png and b/assets/moved.png differ\n")
	assert.Contains(patch, "copy from assets/original.png\ncopy to assets/copy.png\nBinary files a/assets/original.png and b/assets/copy.png differ\n")
}

func TestGetItemDiffRejectsOversizedTempFile(t *testing.T) {
	backend := &fakeBackend{getPullDiffFn: func(context.Context, ItemIdentity, bool) (Diff, error) {
		return Diff{Files: []DiffFile{{
			Path: "big.txt", Patch: "diff --git a/big.txt b/big.txt\n" + strings.Repeat("x", (10<<20)+1),
		}}}, nil
	}}
	s := newMCPTestServer(t, backend)

	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item:         itemRefInput{Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 2},
		EmitDiffFile: true,
	})

	assertBackendErrorKind(t, err, "diff_too_large")
	assert.Nil(t, s.diffs)
}

func TestGetItemDiffRejectsFileLargerThanConfiguredCache(t *testing.T) {
	backend := &fakeBackend{getPullDiffFn: func(context.Context, ItemIdentity, bool) (Diff, error) {
		return Diff{Files: []DiffFile{{
			Path: "bounded.txt", Patch: "diff --git a/bounded.txt b/bounded.txt\n" + strings.Repeat("x", 64),
		}}}, nil
	}}
	s, err := New(Options{Backend: backend, Version: "test", DiffCacheBytes: 64})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget", Number: 2,
		},
		EmitDiffFile: true,
	})

	assertBackendErrorKind(t, err, "diff_too_large")
}

func TestDiffFileNameCanonicalizesAndSeparatesIdentities(t *testing.T) {
	assert := assert.New(t)
	omittedHost := diffFileName(itemRefInput{
		Type: "pr", Provider: "gh", PlatformRepoID: "repo-acme-widget",
		Owner: "Acme", Name: "Widget", Number: 7,
	})
	explicitHost := diffFileName(itemRefInput{
		Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", PlatformHost: "GITHUB.COM",
		Owner: "acme", Name: "widget", Number: 7,
	})
	collisionCandidate := diffFileName(itemRefInput{
		Type: "pr", Provider: "github", PlatformRepoID: "repo-acme-widget", Owner: "acme_widget", Name: "x", Number: 7,
	})
	forgejoUpper := diffFileName(itemRefInput{
		Type: "pr", Provider: "forgejo", PlatformRepoID: "forgejo-widget", PlatformHost: "forge.example.test",
		Owner: "Team", Name: "Widget", Number: 7,
	})
	forgejoLower := diffFileName(itemRefInput{
		Type: "pr", Provider: "forgejo", PlatformRepoID: "forgejo-widget", PlatformHost: "forge.example.test",
		Owner: "team", Name: "widget", Number: 7,
	})
	giteaUpper := diffFileName(itemRefInput{
		Type: "pr", Provider: "gitea", PlatformRepoID: "gitea-widget", PlatformHost: "git.example.test",
		Owner: "Team", Name: "Widget", Number: 7,
	})
	giteaLower := diffFileName(itemRefInput{
		Type: "pr", Provider: "gitea", PlatformRepoID: "gitea-widget", PlatformHost: "git.example.test",
		Owner: "team", Name: "widget", Number: 7,
	})
	assert.Equal(omittedHost, explicitHost)
	assert.NotEqual(omittedHost, collisionCandidate)
	assert.NotEqual(forgejoUpper, forgejoLower)
	assert.NotEqual(giteaUpper, giteaLower)
	assert.NotContains(omittedHost, "/")
	assert.LessOrEqual(len(omittedHost), maxMCPDiffFileNameBytes)
	assert.True(strings.HasSuffix(omittedHost, ".diff"))
}

func TestDiffFileStoreEvictsLeastRecentlyRequestedFiles(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	store, err := newDiffFileStore(8)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(store.Close()) })

	firstPath, _, err := store.write("first.diff", []byte("1111"))
	require.NoError(err)
	secondPath, _, err := store.write("second.diff", []byte("2222"))
	require.NoError(err)
	refreshedPath, _, err := store.write("first.diff", []byte("1111"))
	require.NoError(err)
	assert.Equal(firstPath, refreshedPath)
	thirdPath, _, err := store.write("third.diff", []byte("3333"))
	require.NoError(err)

	_, err = os.Stat(firstPath)
	require.NoError(err)
	_, err = os.Stat(secondPath)
	assert.True(os.IsNotExist(err), "expected least recently requested diff to be evicted, got %v", err)
	_, err = os.Stat(thirdPath)
	require.NoError(err)
}

func TestDiffFileStoreRejectsFileLargerThanCache(t *testing.T) {
	store, err := newDiffFileStore(4)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, _, err = store.write("large.diff", []byte("12345"))

	require.Error(t, err)
	assert.ErrorContains(t, err, "exceeds MCP diff cache")
}

func TestDiffFileStoreCloseRemovesDirectory(t *testing.T) {
	require := require.New(t)
	store, err := newDiffFileStore(64)
	require.NoError(err)
	path, _, err := store.write("one.diff", []byte("diff --git a/a b/a\n"))
	require.NoError(err)
	require.NoError(store.Close())
	_, err = os.Stat(filepath.Dir(path))
	assert.True(t, os.IsNotExist(err), "expected temp dir to be removed, got %v", err)
}

func assertBackendErrorKind(t *testing.T, err error, kind string) {
	t.Helper()
	var backendErr *Error
	require.ErrorAs(t, err, &backendErr, "expected Error, got %T", err)
	assert.Equal(t, kind, backendErr.Kind)
}
