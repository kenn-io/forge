package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunUntrackedPathReadsDoesNotSerializeFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		limit          int
		runCount       int
		pathCount      int
		wantConcurrent int
	}{
		{name: "paths below budget", limit: 4, runCount: 1, pathCount: 2, wantConcurrent: 2},
		{name: "concurrent runs share budget", limit: 3, runCount: 2, pathCount: 5, wantConcurrent: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				pool := newUntrackedReadPool(tt.limit)
				paths := make([]string, tt.pathCount)
				for i := range paths {
					paths[i] = fmt.Sprintf("file-%d", i)
				}
				started := make(chan struct{}, tt.runCount*tt.pathCount)
				release := make(chan struct{})
				done := make(chan error, tt.runCount)
				for range tt.runCount {
					go func() {
						done <- pool.run(t.Context(), paths, func(_ context.Context, _ int, _ string) error {
							started <- struct{}{}
							<-release
							return nil
						})
					}()
				}

				synctest.Wait()
				assert.Len(t, started, tt.wantConcurrent)
				close(release)
				synctest.Wait()
				for range tt.runCount {
					require.NoError(t, <-done)
				}
			})
		})
	}
}

func TestUntrackedFileReadsRespectCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("content\n"), 0o600))

	tests := []struct {
		name string
		read func(context.Context) error
	}{
		{
			name: "path enumeration",
			read: func(ctx context.Context) error {
				_, err := worktreeUntrackedFilesFromPaths(ctx, root, []string{"file.txt"}, true, false)
				return err
			},
		},
		{
			name: "file content",
			read: func(ctx context.Context) error {
				_, _, err := readUntrackedFileContent(ctx, root, "file.txt")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, tt.read(ctx), context.Canceled)
		})
	}
}

func TestReadAllWithContextStopsBetweenReads(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	go func() {
		_, _ = writer.Write([]byte("first"))
		cancel()
		_ = writer.Close()
	}()

	_, err := readAllWithContext(ctx, reader, 1024)
	require.ErrorIs(t, err, context.Canceled)
}

func TestReadUntrackedFileContentRejectsIntermediateSymlink(t *testing.T) {
	t.Parallel()
	requirements := require.New(t)
	worktree := t.TempDir()
	outside := t.TempDir()
	requirements.NoError(os.Mkdir(filepath.Join(worktree, "safe"), 0o700))
	requirements.NoError(os.WriteFile(filepath.Join(worktree, "safe", "file.txt"), []byte("safe\n"), 0o600))
	requirements.NoError(os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600))
	requirements.NoError(os.Symlink(outside, filepath.Join(worktree, "linked")))

	tests := []struct {
		name     string
		path     string
		wantOK   bool
		wantData string
	}{
		{name: "nested regular file", path: "safe/file.txt", wantOK: true, wantData: "safe\n"},
		{name: "intermediate symlink", path: "linked/secret.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content, ok, err := readUntrackedFileContent(t.Context(), worktree, tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantData, string(content))
		})
	}
}

func TestWorktreeDiffFilesAgainstHead(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 2)
	assert.Equal("dirty-test.txt", files[0].Path)
	assert.Equal("added", files[0].Status)
	assert.Equal("f.txt", files[1].Path)
	assert.Equal("modified", files[1].Status)
	assert.Equal(1, files[1].Additions)
	assert.Equal(1, files[1].Deletions)
}

func TestWorktreeDiffFilesHidesWhitespaceOnlyChanges(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("f1  \n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, true,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 1)
	assert.Equal("dirty-test.txt", files[0].Path)
}

func TestWorktreeDiffFilesHidesWhitespaceOnlyUntrackedFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "z-blank.txt"), []byte(" \t\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "z-empty.txt"), nil, 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, true,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 2)
	assert.Equal("dirty-test.txt", files[0].Path)
	assert.Equal("z-empty.txt", files[1].Path)
}

func TestWorktreeDiffFilesMarksGeneratedFiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"),
		[]byte("dist/** linguist-generated\n"), 0o644,
	))
	require.NoError(os.MkdirAll(filepath.Join(work, "dist"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dist", "api.ts"), []byte("export const api = 1;\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "src.ts"), []byte("export const src = 1;\n"), 0o644,
	))

	files, ok, err := WorktreeDiffFiles(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.Len(files, 3)

	generated := map[string]bool{}
	for _, file := range files {
		generated[file.Path] = file.IsGenerated
	}
	assert.False(generated[".gitattributes"])
	assert.True(generated["dist/api.ts"])
	assert.False(generated["src.ts"])
}

func TestWorktreeDiffIgnoresExternalDiffConfig(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "external-diff.sh")
	marker := filepath.Join(scriptDir, "marker")
	require.NoError(os.WriteFile(
		script,
		[]byte("#!/bin/sh\nprintf ran > \"$(dirname \"$0\")/marker\"\nexit 42\n"),
		0o755,
	))
	runWorkspaceTestGit(t, work, "config", "diff.external", script)
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)

	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	_, statErr := os.Stat(marker)
	assert.True(os.IsNotExist(statErr), "diff.external must not run")
}

func TestWorktreeDiffIgnoresTextconvConfig(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	scriptDir := t.TempDir()
	script := filepath.Join(scriptDir, "textconv.sh")
	marker := filepath.Join(scriptDir, "marker")
	require.NoError(os.WriteFile(
		script,
		[]byte("#!/bin/sh\nprintf ran > \"$(dirname \"$0\")/marker\"\nexit 42\n"),
		0o755,
	))
	runWorkspaceTestGit(t, work, "config", "diff.demo.textconv", script)
	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"), []byte("*.txt diff=demo\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".gitattributes")
	runWorkspaceTestGit(t, work, "commit", "-m", "add diff attributes")
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)

	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	_, statErr := os.Stat(marker)
	assert.True(os.IsNotExist(statErr), "textconv must not run")
}

func TestWorktreeFileDiffAgainstHeadScopesPatchToOnePath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("dirty\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty-test.txt"), []byte("test\n"), 0o644,
	))

	diff, ok, err := WorktreeFileDiff(
		t.Context(), work, WorktreeDiffBaseHead, false, "f.txt",
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)

	file := diff.Files[0]
	assert.Equal("f.txt", file.Path)
	assert.Equal("modified", file.Status)
	assert.Equal(1, file.Additions)
	assert.Equal(1, file.Deletions)
	require.Len(file.Hunks, 1)
	assert.NotEmpty(file.Hunks[0].Lines)
}

func TestWorktreeFileDiffAgainstHeadBuildsPatchForUntrackedPath(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "first.go"), []byte("package first\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "second.go"), []byte("package second\n"), 0o644,
	))

	diff, ok, err := WorktreeFileDiff(
		t.Context(), work, WorktreeDiffBaseHead, false, "first.go",
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)

	file := diff.Files[0]
	assert.Equal("first.go", file.Path)
	assert.Equal("added", file.Status)
	assert.Contains(file.Patch, "diff --git a/first.go b/first.go\n")
	assert.Contains(file.Patch, "new file mode 100644\n")
	assert.Contains(file.Patch, "@@ -0,0 +1 @@\n")
	assert.Contains(file.Patch, "+package first\n")
	require.Len(file.Hunks, 1)
	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		paths = append(paths, file.Path)
	}
	assert.NotContains(paths, "second.go")
}

func TestWorktreeDiffAgainstPushedBranchIncludesLocalCommitsAndDirtyChanges(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	require.NoError(os.WriteFile(
		filepath.Join(work, "committed.go"), []byte("package committed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local commit")
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.go"), []byte("package dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBasePushed, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)

	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		paths = append(paths, file.Path)
	}
	assert.Contains(paths, "committed.go")
	assert.Contains(paths, "dirty.go")
	assert.Equal(0, diff.WhitespaceOnlyCount)
}

func TestWorktreeDiffWhitespaceOnlyCountBetweenUsesRangeRefs(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	baseSHA := strings.TrimSpace(
		string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")),
	)

	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("f1  \n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "whitespace change")
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base changed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "content change")
	headSHA := strings.TrimSpace(
		string(runWorkspaceTestGit(t, work, "rev-parse", "HEAD")),
	)

	count, ok, err := WorktreeDiffWhitespaceOnlyCountBetween(
		t.Context(), work, baseSHA, headSHA,
	)
	require.NoError(err)
	require.True(ok)
	assert.New(t).Equal(1, count)
}

func TestWorktreeDiffAgainstMergeTargetUsesMergeBase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)

	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	require.NoError(os.WriteFile(
		filepath.Join(other, "target-only.txt"), []byte("target\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "target branch advance")
	runWorkspaceTestGit(t, other, "push", "origin", "main")
	runWorkspaceTestGit(t, work, "fetch", "origin", "main")

	require.NoError(os.WriteFile(
		filepath.Join(work, "committed.go"), []byte("package committed\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local commit")
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.go"), []byte("package dirty\n"), 0o644,
	))

	diff, ok, err := WorktreeDiffAgainstMergeTarget(
		t.Context(), work, "main", false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)

	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		paths = append(paths, file.Path)
	}
	assert.Contains(paths, "f.txt")
	assert.Contains(paths, "committed.go")
	assert.Contains(paths, "dirty.go")
	assert.NotContains(paths, "target-only.txt")
}

func TestWorktreeDiffAgainstPushedBranchWithoutTrackingBranch(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "x.txt"), []byte("x\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBasePushed, false,
	)
	require.NoError(err)
	require.False(ok)
	require.Nil(diff)
}

func TestWorktreeDiffRendersUntrackedSymlinkTarget(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	secret := filepath.Join(root, "secret.txt")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "t@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "tracked.txt"), []byte("tracked\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "init")
	require.NoError(os.WriteFile(secret, []byte("do not expose\n"), 0o644))
	requireSymlink(t, secret, filepath.Join(work, "secret-link"))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)
	require.Len(diff.Files[0].Hunks, 1)

	file := diff.Files[0]
	assert.Equal("secret-link", file.Path)
	assert.Equal("added", file.Status)
	assert.Equal(1, file.Additions)
	assert.False(file.IsBinary)
	require.Len(file.Hunks[0].Lines, 1)
	assert.Equal(secret, file.Hunks[0].Lines[0].Content)
	assert.True(file.Hunks[0].Lines[0].NoNewline)
	assert.NotContains(file.Hunks[0].Lines[0].Content, "do not expose")
}

func TestOpenRegularUntrackedFileRejectsSymlinks(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	link := filepath.Join(root, "secret-link")
	require.NoError(os.WriteFile(secret, []byte("do not expose\n"), 0o644))
	requireSymlink(t, secret, link)

	file, _, err := openRegularUntrackedFile(link)
	require.Error(err)
	require.Nil(file)
}

func requireSymlink(t *testing.T, oldname string, newname string) {
	t.Helper()
	err := os.Symlink(oldname, newname)
	if err != nil && strings.Contains(
		err.Error(),
		"A required privilege is not held by the client",
	) {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	require.NoError(t, err)
}

func TestWorktreeDiffMarksLargeUntrackedFileBinary(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "large.txt"),
		bytes.Repeat([]byte("x"), maxUntrackedTextFileBytes+1),
		0o644,
	))

	diff, ok, err := WorktreeDiff(
		t.Context(), work, WorktreeDiffBaseHead, false,
	)
	require.NoError(err)
	require.True(ok)
	require.NotNil(diff)
	require.Len(diff.Files, 1)

	file := diff.Files[0]
	assert.Equal("large.txt", file.Path)
	assert.True(file.IsBinary)
	assert.Zero(file.Additions)
	assert.Empty(file.Hunks)
}
