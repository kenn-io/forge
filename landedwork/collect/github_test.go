package collect_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
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

func TestMain(m *testing.M) { os.Exit(gitsafe.RunIsolatedMain(m)) }

func TestGitHubCollectionAnalysis(t *testing.T) {
	for _, mode := range []string{"merge", "no associations", "failed page", "changed detail", "one parent", "foreign association", "missing base", "malformed terminal", "malformed source head"} {
		t.Run(mode, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
			r.Runner = gitsafe.Runner()
			base := r.CommitFile("work.txt", "old\n", "base")
			r.Checkout("-b", "topic")
			source := r.CommitFile("work.txt", "new\n", "change")
			r.Checkout("main")
			if mode == "one parent" {
				r.Run("merge", "--squash", "topic")
				r.Run("commit", "-m", "squash")
			} else {
				r.Run("merge", "--no-ff", "topic", "-m", "merge")
			}
			head := r.Head()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			analysisLimits := landedwork.Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}
			prepared, err := landedwork.Prepare(ctx, r.Root, landedwork.Bounds{Repository: query.Bounds.Repository, Base: base, Head: head}, analysisLimits)
			require.NoError(err)
			details := 0
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := ""
				status := 200
				header := make(http.Header)
				switch {
				case req.URL.Path == "/repos/example/project":
					body = `{"id":12,"name":"project","owner":{"login":"example"}}`
				case strings.HasSuffix(req.URL.Path, "/pulls"):
					body = `[{"id":7,"number":3,"base":{"repo":{"id":12}}}]`
					if mode == "foreign association" {
						body = `[{"id":8,"number":3,"base":{"repo":{"id":99}}}]`
					}
					if mode == "no associations" {
						body = `[]`
					}
					if mode == "failed page" {
						if req.URL.Query().Get("page") == "2" {
							status = 503
							body = `{}`
						} else {
							header.Set("Link", fmt.Sprintf(`<https://api.github.com%s?page=2>; rel="next"`, req.URL.Path))
						}
					}
				case req.URL.Path == "/repos/example/project/pulls/3":
					details++
					count := 1
					if mode == "changed detail" && details == 2 {
						count = 2
					}
					terminal, sourceHead := head, source
					if mode == "malformed terminal" {
						terminal = "not-an-object-id"
					}
					if mode == "malformed source head" {
						sourceHead = strings.Repeat("a", 64) // Wrong object format for this repository.
					}
					body = fmt.Sprintf(`{"id":7,"number":3,"merged":true,"merge_commit_sha":%q,"commits":%d,"base":{"ref":"main","repo":{"id":12}},"head":{"sha":%q,"repo":{"id":12}}}`, terminal, count, sourceHead)
					if mode == "missing base" {
						body = `{"id":7,"number":3,"merged":true}`
					}
				case req.URL.Path == "/repos/example/project/pulls/3/commits":
					body = fmt.Sprintf(`[{"sha":%q}]`, source)
				default:
					require.Fail("unexpected request", req.URL.String())
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			client, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
			require.NoError(err)
			reader, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: time.Now})
			require.NoError(err)
			collected, err := collect.Collect(ctx, reader, route, prepared.Query(), limits)
			require.NoError(err)
			if mode == "foreign association" {
				assert.Zero(details, "foreign PR numbers must not be fetched in the local repository")
				assert.Empty(collected.Evidence.Candidates)
				require.Len(collected.Observations, 1)
				assert.Equal("foreign_repository", collected.Observations[0].Reason)
				assert.Equal("ambiguous_absence", collected.Evidence.Inventory.Reason)
			}
			if mode == "missing base" || strings.HasPrefix(mode, "malformed ") {
				require.Len(collected.Evidence.Candidates, 1)
				assert.Equal("invalid_observation", collected.Evidence.Inventory.Reason)
			}
			analysis, err := landedwork.Analyze(ctx, prepared, collected.Evidence, analysisLimits)
			require.NoError(err)
			if strings.HasPrefix(mode, "malformed ") {
				require.Len(collected.Observations, 1)
				assert.Equal("7", collected.Evidence.Candidates[0].ID)
				assert.False(collected.Evidence.Candidates[0].SourceComplete)
				assert.Equal("invalid_observation", analysis.Coverage.Inventory.Reason)
				assert.Empty(analysis.Landings)
				if mode == "malformed terminal" {
					assert.Equal("not-an-object-id", collected.Observations[0].Change.Terminal)
					assert.Empty(collected.Evidence.Candidates[0].Terminal)
					assert.Empty(collected.Evidence.Candidates[0].TerminalEvidence)
					assert.Equal(source, collected.Evidence.Candidates[0].SourceHead)
				} else {
					assert.Equal(new(strings.Repeat("a", 64)), collected.Observations[0].Change.SourceHead)
					assert.Empty(collected.Evidence.Candidates[0].SourceHead)
					assert.Equal(head, collected.Evidence.Candidates[0].Terminal)
				}
			}
			switch mode {
			case "merge", "one parent":
				assert.True(analysis.Coverage.Complete)
				assert.Equal(head, analysis.Coverage.CertifiedHead)
				require.Len(analysis.Landings, 1)
				assert.Equal(base, analysis.Landings[0].Before)
				assert.Equal(head, analysis.Landings[0].Terminal)
				if mode == "one parent" {
					assert.Equal([]string{"rebase", "squash"}, analysis.Landings[0].Proofs)
					assert.Equal([]string{head}, analysis.Landings[0].Spine)
				}
			case "no associations":
				assert.True(analysis.Coverage.Complete)
				require.Len(analysis.DirectPushes, 1)
				assert.Equal(head, analysis.DirectPushes[0].Terminal)
			default:
				assert.False(analysis.Coverage.Complete)
				assert.Equal(base, analysis.Coverage.CertifiedHead)
				assert.Empty(analysis.DirectPushes)
			}
		})
	}
}
