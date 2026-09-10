package collect_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
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

func TestIntegratedRoleObservations(t *testing.T) {
	for _, mode := range []string{"reported", "changed", "removed"} {
		t.Run(mode, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
			r.Runner = gitsafe.Runner()
			base := r.CommitFile("base.txt", "base\n", "base")
			r.Checkout("-b", "topic")
			source := r.CommitFile("work.txt", "work\n", "change")
			r.Checkout("-b", "integration", base)
			side := r.CommitFile("side.txt", "side\n", "integration starts")
			r.Run("merge", "--no-ff", "topic", "-m", "inner")
			inner := r.Head()
			r.Checkout("main")
			r.Run("merge", "--no-ff", "integration", "-m", "outer")
			head := r.Head()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			analysisLimits := landedwork.Limits{Records: 1000, Nodes: 1000, InputBytes: 1 << 20, OutputBytes: 1 << 20}
			prepared, err := landedwork.Prepare(ctx, r.Root, landedwork.Bounds{Repository: query.Bounds.Repository, Base: base, Head: head}, analysisLimits)
			require.NoError(err)
			calls := 0
			transport := integratedRoleTransport(t, mode, source, side, inner, head)
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return transport.RoundTrip(req)
			})}
			client, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
			require.NoError(err)
			reader, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: time.Now})
			require.NoError(err)
			got, err := collect.Collect(ctx, reader, route, prepared.Query(), limits)
			require.NoError(err)
			require.Len(got.Observations, 2)
			assert.Equal(11, calls, "repository + four associations + two sets of detail/source/recheck")
			for i, o := range got.Observations {
				if mode == "removed" {
					assert.Nil(o.Change.Author)
					assert.Nil(o.Change.Merger)
					assert.Nil(o.Change.OpenedAt)
					assert.Nil(o.Change.MergedAt)
					continue
				}
				require.NotNil(o.Change.Author)
				require.NotNil(o.Change.Merger)
				assert.Equal(new(int64(21+i*10)), o.Change.Author.ID)
				assert.Equal(platform.AccountTypeUser, o.Change.Author.Type)
				assert.Equal(new(int64(22+i*10)), o.Change.Merger.ID)
				assert.Equal(platform.AccountTypeBot, o.Change.Merger.Type)
				assert.Equal(new(time.Date(2026, 1, 2+i, 0, 0, 0, 0, time.UTC)), o.Change.MergedAt)
				if mode == "changed" {
					assert.Equal(new("renamed"), o.Change.Author.Login)
					assert.Nil(o.Change.OpenedAt)
				} else {
					assert.Equal(new(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)), o.Change.OpenedAt)
				}
			}
			analysis, err := landedwork.Analyze(ctx, prepared, got.Evidence, analysisLimits)
			require.NoError(err)
			assert.True(analysis.Coverage.Complete)
			assert.Equal(head, analysis.Coverage.CertifiedHead)
			require.Len(analysis.Landings, 1)
			assert.Equal("8", analysis.Landings[0].CandidateID)
			assert.Equal([]landedwork.IntegratedCandidate{{CandidateID: "7", ThroughCandidateID: "8"}}, analysis.Integrated)
			assert.Empty(analysis.DirectPushes)
			assert.Empty(analysis.Unattributed)
		})
	}
}

func integratedRoleTransport(t *testing.T, mode, source, side, inner, head string) http.RoundTripper {
	t.Helper()
	details := make(map[int]int)
	return platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case req.URL.Path == "/repos/example/project":
			body = `{"id":12,"name":"project","owner":{"login":"example"}}`
		case strings.HasSuffix(req.URL.Path, "/pulls"):
			id, number := 8, 4
			if strings.Contains(req.URL.Path, source) {
				id, number = 7, 3
			}
			body = fmt.Sprintf(`[{"id":%d,"number":%d,"base":{"repo":{"id":12}}}]`, id, number)
		case req.URL.Path == "/repos/example/project/pulls/3/commits":
			body = fmt.Sprintf(`[{"sha":%q}]`, source)
		case req.URL.Path == "/repos/example/project/pulls/4/commits":
			body = fmt.Sprintf(`[{"sha":%q},{"sha":%q},{"sha":%q}]`, source, side, inner)
		case req.URL.Path == "/repos/example/project/pulls/3", req.URL.Path == "/repos/example/project/pulls/4":
			id, number, count, terminal, sourceHead, branch, author := 7, 3, 1, inner, source, "integration", 21
			if strings.HasSuffix(req.URL.Path, "/4") {
				id, number, count, terminal, sourceHead, branch, author = 8, 4, 3, head, inner, "main", 31
			}
			details[number]++
			body = fmt.Sprintf(`{"id":%d,"number":%d,"merged":true,"merge_commit_sha":%q,"commits":%d,"base":{"ref":%q,"repo":{"id":12}},"head":{"sha":%q,"repo":{"id":12}}}`, id, number, terminal, count, branch, sourceHead)
			var fields map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &fields))
			if mode != "removed" || details[number] == 1 {
				login := "user-a"
				if mode == "changed" && details[number] == 2 {
					login = "renamed"
				} else {
					fields["created_at"] = "2026-01-01T03:00:00+02:00"
				}
				fields["user"] = map[string]any{"id": author, "login": login, "type": "User"}
				fields["merged_by"] = map[string]any{"id": author + 1, "type": "Bot"}
				fields["merged_at"] = fmt.Sprintf("2026-01-%02dT00:00:00Z", number-1)
			}
			encoded, err := json.Marshal(fields)
			require.NoError(t, err)
			body = string(encoded)
		default:
			require.Fail(t, "unexpected request", req.URL.String())
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
}
