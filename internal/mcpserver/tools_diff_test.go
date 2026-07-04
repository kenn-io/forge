package mcpserver

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetItemDiffSummaryOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/files", func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stale":true,
			"whitespace_only_count":99,
			"files":[{
				"path":"internal/db/queries.go","old_path":"","status":"modified",
				"is_binary":false,"is_generated":false,"is_whitespace_only":true,
				"additions":8,"deletions":3,"patch":"must not leak","hunks":[{"old_start":1}]
			},{
				"path":"assets/logo.png","old_path":"assets/old-logo.png","status":"renamed",
				"is_binary":true,"is_generated":false,"additions":0,"deletions":0
			}]
		}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(err)
	assert.True(out.Stale)
	assert.Equal(8, out.TotalAdditions)
	assert.Equal(3, out.TotalDeletions)
	require.Len(out.Files, 2)
	assert.Equal("internal/db/queries.go", out.Files[0].Path)
	assert.Equal("modified", out.Files[0].Status)
	assert.True(out.Files[1].IsBinary)
	assert.Equal("assets/old-logo.png", out.Files[1].OldPath)
	assert.Nil(out.DiffFile)

	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), `"patch"`)
	assert.NotContains(string(raw), `"hunks"`)
	assert.NotContains(string(raw), `"whitespace_only_count"`)
	assert.NotContains(string(raw), `"diff_file"`)
}

func TestGetItemDiffWritesVerbatimDiffFileAndOverwrites(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	diffCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/host/git.example.com/pulls/gitlab/Group%2FSub/Project/42/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if diffCalls == 0 {
			_, _ = w.Write([]byte(`{"files":[
				{"path":"src/renamed.go","status":"renamed","additions":1,"deletions":1},
				{"path":"src/copied.go","old_path":"src/source.go","status":"copied","additions":1,"deletions":0},
				{"path":"scripts/run.sh","status":"modified","additions":1,"deletions":1},
				{"path":"assets/logo.png","status":"modified","is_binary":true,"additions":0,"deletions":0}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"path":"src/newer.go","status":"added","additions":1,"deletions":0}]}`))
	})
	mux.HandleFunc("/api/v1/host/git.example.com/pulls/gitlab/Group%2FSub/Project/42/diff", func(w http.ResponseWriter, _ *http.Request) {
		diffCalls++
		w.Header().Set("Content-Type", "application/json")
		if diffCalls == 1 {
			_, _ = w.Write([]byte(`{"files":[
				{"path":"src/renamed.go","status":"renamed","additions":1,"deletions":1,"patch":"diff --git a/src/old.go b/src/renamed.go\nrename from src/old.go\nrename to src/renamed.go\n"},
				{"path":"src/copied.go","old_path":"src/source.go","status":"copied","additions":1,"deletions":0,"patch":"diff --git a/src/source.go b/src/copied.go\ncopy from src/source.go\ncopy to src/copied.go\n"},
				{"path":"scripts/run.sh","status":"modified","additions":1,"deletions":1,"patch":"diff --git a/scripts/run.sh b/scripts/run.sh\nold mode 100644\nnew mode 100755\n"},
				{"path":"assets/logo.png","status":"modified","is_binary":true,"additions":0,"deletions":0,"patch":"diff --git a/assets/logo.png b/assets/logo.png\nBinary files a/assets/logo.png and b/assets/logo.png differ\n"}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"files":[
			{"path":"src/newer.go","status":"added","additions":1,"deletions":0,"patch":"diff --git a/src/newer.go b/src/newer.go\nnew file mode 100644\n"}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	in := getItemDiffInput{
		Item: itemRefInput{
			Type: "pr", Provider: "gitlab", PlatformHost: "git.example.com",
			Owner: "Group/Sub", Name: "Project", Number: 42,
		},
		EmitDiffFile: true,
	}
	first, err := s.getItemDiff(t.Context(), in)
	require.NoError(err)
	require.NotNil(first.DiffFile)
	assert.Equal(filepath.Clean(first.DiffFile.Path), first.DiffFile.Path)
	assert.True(strings.HasPrefix(first.DiffFile.Path, s.diffs.dir+string(os.PathSeparator)))

	data, err := os.ReadFile(first.DiffFile.Path)
	require.NoError(err)
	firstContent := string(data)
	assert.Equal(first.DiffFile.Bytes, int64(len(data)))
	assert.Equal(4, strings.Count(firstContent, "diff --git "))
	assert.Contains(firstContent, "rename from src/old.go\n")
	assert.Contains(firstContent, "copy to src/copied.go\n")
	assert.Contains(firstContent, "old mode 100644\nnew mode 100755\n")
	assert.Contains(firstContent, "Binary files a/assets/logo.png and b/assets/logo.png differ\n")
	info, err := os.Stat(first.DiffFile.Path)
	require.NoError(err)
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())

	second, err := s.getItemDiff(t.Context(), in)
	require.NoError(err)
	require.NotNil(second.DiffFile)
	assert.Equal(first.DiffFile.Path, second.DiffFile.Path)
	updated, err := os.ReadFile(second.DiffFile.Path)
	require.NoError(err)
	assert.Equal("diff --git a/src/newer.go b/src/newer.go\nnew file mode 100644\n", string(updated))
}

func TestGetItemDiffRejectsIssueAndEmptyPatch(t *testing.T) {
	require := require.New(t)

	s := newMCPTestServer(t, http.NewServeMux())
	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{Type: "issue", Provider: "github", Owner: "acme", Name: "widget", Number: 7},
	})
	assertDaemonErrorKind(t, err, "invalid_request")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"path":"src/new.go","status":"added"}]}`))
	})
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/diff", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"path":"src/new.go","status":"added","patch":""}]}`))
	})
	s = newMCPTestServer(t, mux)
	_, err = s.getItemDiff(t.Context(), getItemDiffInput{
		Item:         itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		EmitDiffFile: true,
	})
	require.Error(err)
	assertDaemonErrorKind(t, err, "daemon_error")
}

func TestGetItemDiffRejectsMismatchedSummaryAndDiffFiles(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[
			{"path":"src/one.go","status":"modified","additions":2,"deletions":1},
			{"path":"src/two.go","status":"added","additions":1,"deletions":0}
		]}`))
	})
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/diff", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[
			{"path":"src/one.go","status":"modified","patch":"diff --git a/src/one.go b/src/one.go\n"}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item:         itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		EmitDiffFile: true,
	})

	assertDaemonErrorKind(t, err, "diff_incomplete")
	if s.diffs != nil {
		entries, readErr := os.ReadDir(s.diffs.dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	}
}

func TestGetItemDiffRejectsMismatchedSummaryAndDiffMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{
			"path":"src/one.go","status":"modified",
			"is_binary":false,"is_generated":false,"additions":2,"deletions":1
		}]}`))
	})
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/diff", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{
			"path":"src/one.go","status":"modified",
			"is_binary":false,"is_generated":false,"additions":1,"deletions":1,
			"patch":"diff --git a/src/one.go b/src/one.go\n"
		}]}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item:         itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		EmitDiffFile: true,
	})

	assertDaemonErrorKind(t, err, "diff_incomplete")
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("src/one.go", derr.Details["path"])
	assert.Equal("additions", derr.Details["field"])
	assert.Equal(2, derr.Details["summary"])
	assert.Equal(1, derr.Details["diff"])
}

func TestGetItemDiffReportsUnavailableAndTooLarge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/1/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"service_unavailable","detail":"diff view not available: clone manager not configured"}`))
	})
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/2/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"path":"big.txt","status":"modified"}]}`))
	})
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/2/diff", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"path":"big.txt","status":"modified","patch":"diff --git a/big.txt b/big.txt\n` + strings.Repeat("x", (10<<20)+1) + `"}]}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.getItemDiff(t.Context(), getItemDiffInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 1},
	})
	assertDaemonErrorKind(t, err, "diff_unavailable")

	_, err = s.getItemDiff(t.Context(), getItemDiffInput{
		Item:         itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 2},
		EmitDiffFile: true,
	})
	assertDaemonErrorKind(t, err, "diff_too_large")
	if s.diffs != nil {
		entries, readErr := os.ReadDir(s.diffs.dir)
		require.NoError(readErr)
		assert.Empty(entries)
	}
}

func TestDiffFileStoreCloseRemovesDirectory(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	store, err := newDiffFileStore()
	require.NoError(err)
	path, _, err := store.write("one.diff", []byte("diff --git a/a b/a\n"))
	require.NoError(err)
	_, err = os.Stat(path)
	require.NoError(err)

	require.NoError(store.Close())
	_, err = os.Stat(filepath.Dir(path))
	assert.True(os.IsNotExist(err), "expected temp dir to be removed, got %v", err)
}

func assertDaemonErrorKind(t *testing.T, err error, kind string) {
	t.Helper()
	require.Error(t, err)
	var derr *daemonError
	require.ErrorAs(t, err, &derr, "expected daemonError, got %T", err)
	assert.Equal(t, kind, derr.Kind)
}
