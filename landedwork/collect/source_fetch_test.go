package collect_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
	gittest "go.kenn.io/kit/git/test"
)

var proofLimits = landedwork.Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}

// The HTTP boundary supplies observed SHAs, never the Git objects themselves.
func collectGitHubProof(t *testing.T, ctx context.Context, path string, b landedwork.Bounds, sources []string, host string) (*landedwork.Interval, landedwork.Evidence) {
	t.Helper()
	require, assert := require.New(t), assert.New(t)
	p, err := landedwork.Prepare(ctx, path, b, proofLimits)
	require.NoError(err)
	require.True(p.Query().Complete)
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.Path {
		case "/repos/example/project":
			body = `{"id":12,"name":"project","owner":{"login":"example"}}`
		case "/repos/example/project/pulls/3":
			body = fmt.Sprintf(`{"id":7,"number":3,"merged":true,"merge_commit_sha":%q,"commits":%d,"base":{"ref":"main","repo":{"id":12}},"head":{"sha":%q,"repo":{"id":12}}}`, b.Head, len(sources), sources[len(sources)-1])
		case "/repos/example/project/pulls/3/commits":
			rows := make([]string, len(sources))
			for i, sha := range sources {
				rows[i] = fmt.Sprintf(`{"sha":%q}`, sha)
			}
			body = "[" + strings.Join(rows, ",") + "]"
		default:
			sha := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/repos/example/project/commits/"), "/pulls")
			require.True(slices.Contains(p.Query().Commits, sha), "unexpected association: %s", req.URL.Path)
			body = `[{"id":7,"number":3,"base":{"repo":{"id":12}}}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	client, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(err)
	reader, err := github.NewProvider(github.ProviderConfig{Host: host, Client: client, Clock: time.Now})
	require.NoError(err)
	r := route
	r.Host = host
	c, err := collect.Collect(ctx, reader, r, p.Query(), limits)
	require.NoError(err)
	if host == "github.com" {
		require.True(c.Evidence.Inventory.Complete)
		require.Len(c.Evidence.Candidates, 1)
		assert.Equal(sources, c.Evidence.Candidates[0].Source)
		assert.Empty(c.Evidence.Candidates[0].Method)
		assert.Empty(c.Evidence.Candidates[0].MethodEvidence)
	}
	return p, c.Evidence
}

func TestGitHubSingleParentCollection(t *testing.T) {
	for _, mode := range []string{"squash", "rebase", "enterprise"} {
		t.Run(mode, func(t *testing.T) {
			r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
			r.Runner = gitsafe.Runner()
			base := r.CommitFile("work.txt", "old\nkeep\n", "base")
			r.Checkout("-b", "topic")
			sources := []string{r.CommitFile("work.txt", "new\nkeep\n", "first")}
			sources = append(sources, r.CommitFile("work.txt", "new\nkeep\nlast\n", "second"))
			r.Checkout("main")
			var spine []string
			proofs := []string{"squash"}
			if mode == "rebase" {
				base = r.CommitFile("context", "advance\n", "advance")
				spine = append(spine, r.CommitFile("work.txt", "new\nkeep\n", "replay first"))
				r.CommitFile("work.txt", "new\nkeep\nlast\n", "replay second")
				proofs = []string{"rebase"}
			} else {
				r.Run("merge", "--squash", "topic")
				r.Run("commit", "-m", "squash")
			}
			head := r.Head()
			spine = append(spine, head)
			b := landedwork.Bounds{Repository: query.Bounds.Repository, Base: base, Head: head}
			if mode == "enterprise" {
				b.Repository.Host = "code.example.com"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			p, e := collectGitHubProof(t, ctx, r.Root, b, sources, b.Repository.Host)
			result, err := landedwork.Analyze(ctx, p, e, proofLimits)
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(result.DirectPushes)
			if mode == "enterprise" {
				assert.Empty(result.Landings)
				assert.False(result.Coverage.Complete)
				assert.Equal(base, result.Coverage.CertifiedHead)
				assert.Equal("unsupported_discovery", result.Coverage.Inventory.Reason)
				return
			}
			assert.Equal([]landedwork.Landing{{CandidateID: "7", Before: base, Terminal: head, Source: sources,
				Proofs: proofs, Spine: spine, Introduced: spine}}, result.Landings)
			assert.True(result.Coverage.Complete)
			assert.Equal(head, result.Coverage.CertifiedHead)
			assert.Empty(result.Coverage.Gaps)
		})
	}
}

func TestCallerFetchesObservedSource(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprintf("moved=%t", moved), func(t *testing.T) {
			require, assert := require.New(t), assert.New(t)
			r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
			r.Runner = gitsafe.Runner()
			base := r.CommitFile("work.txt", "old\nkeep\n", "base")
			r.Checkout("-b", "topic")
			sources := []string{r.CommitFile("work.txt", "new\nkeep\n", "first")}
			sources = append(sources, r.CommitFile("work.txt", "new\nkeep\nlast\n", "second"))
			source := sources[1]
			r.Checkout("main")
			r.Run("merge", "--squash", "topic")
			r.Run("commit", "-m", "squash")
			head := r.Head()
			remote, clone := filepath.Join(t.TempDir(), "remote.git"), filepath.Join(t.TempDir(), "clone")
			r.Run("clone", "--bare", "--no-local", r.Root, remote)
			r.Run("--git-dir="+remote, "update-ref", "refs/pull/3/head", source)
			r.Run("--git-dir="+remote, "update-ref", "-d", "refs/heads/topic")
			fetched := source
			if moved {
				fetched = r.CommitFile("unrelated", "new head\n", "repushed head")
				r.Run("push", remote, "+"+fetched+":refs/pull/3/head")
			}
			r.Run("clone", "--no-local", "--single-branch", "--branch", "main", "--no-tags", remote, clone)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			_, _, err := r.Runner.Run(ctx, clone, nil, "cat-file", "-e", source+"^{commit}")
			require.Error(err, "default-only clone must not contain the original source")
			b := landedwork.Bounds{Repository: query.Bounds.Repository, Base: base, Head: head}
			p, e := collectGitHubProof(t, ctx, clone, b, sources, "github.com")
			missing, err := landedwork.Analyze(ctx, p, e, proofLimits)
			require.NoError(err)
			assert.Empty(missing.Landings)
			assert.Empty(missing.DirectPushes)
			assert.False(missing.Coverage.Complete)
			assert.Equal(base, missing.Coverage.CertifiedHead)
			require.Len(missing.Coverage.Gaps, 1)
			assert.Equal("objects_unavailable", missing.Coverage.Gaps[0].Reason)
			assert.Equal(sources[0], missing.Coverage.Gaps[0].ObjectID)
			_, _, err = r.Runner.Run(ctx, clone, nil, "fetch", remote, "refs/pull/3/head:refs/test/pull/3/head")
			require.NoError(err)
			_, _, err = r.Runner.Run(ctx, clone, nil, "cat-file", "-e", fetched+"^{commit}")
			require.NoError(err)
			_, _, err = r.Runner.Run(ctx, clone, nil, "cat-file", "-e", source+"^{commit}")
			if moved {
				require.Error(err)
			} else {
				require.NoError(err)
			}
			result, err := landedwork.Analyze(ctx, p, e, proofLimits)
			require.NoError(err)
			if moved {
				assert.Equal(missing, result, "a different fetched head cannot replace observed evidence")
				return
			}
			assert.Equal([]landedwork.Landing{{CandidateID: "7", Proofs: []string{"squash"}, Before: base, Terminal: head,
				Source: sources, Spine: []string{head}, Introduced: []string{head}}}, result.Landings)
			assert.True(result.Coverage.Complete)
			assert.Equal(head, result.Coverage.CertifiedHead)
			assert.Empty(result.Coverage.Gaps)
			assert.Empty(result.DirectPushes)
		})
	}
}
