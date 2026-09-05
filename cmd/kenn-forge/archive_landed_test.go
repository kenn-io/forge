package main

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/archive/report"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/platform"
)

func TestArchiveLandedCommandAnalyzesLocalGitWithoutChangingActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	git := func(args ...string) string {
		out, stderr, err := gitsafe.Runner().Run(t.Context(), dir, nil, append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
		require.NoError(err, "%s", stderr)
		return strings.TrimSpace(string(out))
	}
	git("init", "--template=", "--initial-branch=main")
	git("config", "user.name", "Author A")
	git("config", "user.email", "AUTHOR@EXAMPLE.ORG")
	git("remote", "add", "origin", "https://github.com/old-team/old-name.git")
	require.NoError(os.WriteFile(filepath.Join(dir, "main.go"), []byte("one\n"), 0o600))
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	require.NoError(os.WriteFile(filepath.Join(dir, "main.go"), []byte("one\ntwo\n"), 0o600))
	git("add", ".")
	git("commit", "-m", "direct change")
	head := git("rev-parse", "HEAD")
	now := time.Now().UTC()
	var complete atomic.Bool
	complete.Store(true)
	var evidenceCalls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/base/api/v1/archive/report":
			assert.NoError(json.MarshalWrite(w, report.Model{Schema: report.Schema, Start: now.Add(-24 * time.Hour), End: now, Totals: report.Counts{MergeRequestsMerged: 7}}))
		case "/base/api/v1/archive/landing-evidence":
			evidenceCalls.Add(1)
			assert.Equal("github|github.com/new-team/new-name", r.URL.Query().Get("repo"))
			assert.NoError(json.MarshalWrite(w, platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema,
				Repository:   platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, Owner: "new-team", Name: "new-name", DefaultBranch: "main", HeadSHA: head},
				Capabilities: platform.LandingCapabilities{CompleteCandidateInventory: true}, Coverage: platform.LandingCoverage{Complete: complete.Load()}, StartedAt: now, CompletedAt: now,
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	cfg := archiveCLITestConfig(t, api.URL, "/base", "")
	args := []string{"report", "--config", cfg, "--days", "1", "--repo", "github|github.com/new-team/new-name", "--landed-work", "--git-dir", dir, "--base-sha", base, "--head-sha", head}
	var output bytes.Buffer
	require.NoError(runArchiveCLIAt(append(args, "--format", "json"), &output, func() time.Time { return now }))
	var result report.Model
	require.NoError(json.Unmarshal(output.Bytes(), &result))
	require.NotNil(result.LandedWork)
	assert.Equal(7, result.Totals.MergeRequestsMerged)
	assert.Equal("17", result.LandedWork.Evidence.Repository.ID)
	assert.Equal(1, result.LandedWork.Graph.DirectPushLandings)
	require.Len(result.LandedWork.Evidence.Landings, 1)
	claim := result.LandedWork.Evidence.Landings[0].TerminalCommit.Claims[0]
	assert.Equal("YXV0aG9yQGV4YW1wbGUub3Jn", claim.Email.Base64)
	assert.Equal(new(float64(1)), result.LandedWork.Graph.DirectPushLandingShare)
	complete.Store(false)
	output.Reset()
	require.NoError(runArchiveCLIAt(args, &output, func() time.Time { return now }))
	assert.Contains(output.String(), "Graph interval (partial)")
	assert.Contains(output.String(), "Direct-push landing share: unknown")
	assert.Contains(output.String(), "old\\-team/old\\-name")
	output.Reset()
	require.NoError(runArchiveCLIAt([]string{"report", "--config", cfg, "--days", "1"}, &output, func() time.Time { return now }))
	assert.Equal(int64(2), evidenceCalls.Load(), "ordinary report stays database-only")
}

func TestArchiveLandedFlagsFailBeforeDaemonDiscovery(t *testing.T) {
	for _, extra := range [][]string{
		{"--git-dir="}, {"--base-sha", "abc"}, {"--head-sha", "abc"},
		{"--landed-work"}, {"--landed-work", "--repo", "github|github.com/team/project", "--git-dir", "/unused"},
	} {
		err := runArchiveCLIAt(append([]string{"report", "--days", "1", "--config", filepath.Join(t.TempDir(), "absent.toml")}, extra...), new(bytes.Buffer), time.Now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "landed-work")
	}
}

func TestArchiveLandedConversionPreservesUnknownObjectsAndRawClaims(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var input generated.ArchiveReportResponse
	require.NoError(json.Unmarshal([]byte(`{"schema":"kenn-forge-archive-report/1","landed_work":{"schema":"forge-landed-work/1","evidence":{"schema":"forge-landed-work/1","landings":[{"churn":{"raw":null,"code":null,"files":[{"old_path":{"base64":"/wA="},"new_path":{"base64":""}}]},"terminal_commit":{"claims":[{"raw_byline":{"base64":""},"raw_email":{"base64":"/w=="},"email":{"base64":"/w=="}}]}}]}}}`), &input))
	model, err := archiveReportFromAPI(input)
	require.NoError(err)
	require.NotNil(model.LandedWork)
	require.Len(model.LandedWork.Evidence.Landings, 1)
	landing := model.LandedWork.Evidence.Landings[0]
	assert.Nil(landing.Churn.Raw)
	assert.Nil(landing.Churn.Code)
	path, err := landing.Churn.Files[0].OldPath.Bytes()
	require.NoError(err)
	assert.Equal([]byte{255, 0}, path)
	email, err := landing.TerminalCommit.Claims[0].Email.Bytes()
	require.NoError(err)
	assert.Equal([]byte{255}, email)
	input.LandedWork.Evidence.Landings = []generated.LandedWorkLanding{{TerminalCommit: generated.LandedWorkCommitEvidence{Claims: []generated.LandedWorkClaim{{Email: generated.PlatformRawBytes{Base64: "/x=="}}}}}}
	_, err = archiveReportFromAPI(input)
	require.Error(err)
}

func TestArchiveLandingConversionPreservesMissingProviderFields(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var input generated.ArchiveLandingResponse
	require.NoError(json.Unmarshal([]byte(`{"schema":"forge-landing-snapshot/1","candidates":[{"id":"31","author":null,"merger":null,"source_repository":null,"base_repository":null,"possible_span":null}]}`), &input))
	snapshot, err := archiveLandingFromAPI(input)
	require.NoError(err)
	require.Len(snapshot.Candidates, 1)
	assert.Nil(snapshot.Candidates[0].Author)
	assert.Nil(snapshot.Candidates[0].SourceRepository)
	assert.Nil(snapshot.Candidates[0].PossibleSpan)
}
