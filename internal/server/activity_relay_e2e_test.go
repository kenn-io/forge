package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestActivityRelayEndToEnd(t *testing.T) {
	require := require.New(t)
	if testing.Short() {
		t.Skip("builds and launches the relay executable")
	}
	assert := assert.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	binary := filepath.Join(dir, "kenn-forge-relay")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := procutil.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, "./cmd/kenn-forge-relay")
	build.Dir = filepath.Join("..", "..")
	output, err := build.CombinedOutput()
	require.NoError(err, string(output))
	secret := "synthetic-webhook-secret"
	secretFile := filepath.Join(dir, "signing-secret")
	require.NoError(os.WriteFile(secretFile, []byte(secret), 0o600))
	configFile := filepath.Join(dir, "relay.toml")
	databaseFile := filepath.Join(dir, "relay.db")
	webhookAddress, feedAddress := "127.0.0.1:0", "127.0.0.1:0"
	startRelay := func() func() {
		t.Helper()
		body := fmt.Sprintf("database = %q\nwebhook_listen = %q\nfeed_listen = %q\n[sources.team]\nsecret_file = %q\nrepository_ids = [12345]\n", databaseFile, webhookAddress, feedAddress, secretFile)
		require.NoError(os.WriteFile(configFile, []byte(body), 0o600))
		processCtx, cancel := context.WithCancel(ctx)
		command := procutil.CommandContext(processCtx, binary, "--config", configFile)
		stdout, err := command.StdoutPipe()
		require.NoError(err)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		require.NoError(command.Start())
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				_ = command.Wait()
				assert.NotContains(stderr.String(), "private-payload-marker")
				assert.NotContains(stderr.String(), secret)
			})
		}
		t.Cleanup(stop)
		ready := make(chan []byte, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			if scanner.Scan() {
				ready <- bytes.Clone(scanner.Bytes())
			} else {
				ready <- nil
			}
		}()
		select {
		case line := <-ready:
			var addresses struct {
				Webhook string `json:"webhook"`
				Feed    string `json:"feed"`
			}
			require.NoError(jsonv2.Unmarshal(line, &addresses))
			require.NotEmpty(addresses.Webhook)
			require.NotEmpty(addresses.Feed)
			webhookAddress, feedAddress = addresses.Webhook, addresses.Feed
		case <-time.After(30 * time.Second):
			stop()
			require.FailNow("relay did not become ready", stderr.String())
		}
		return stop
	}
	stopRelay := startRelay()
	relayURL := "http://" + feedAddress
	var checkReads, detailReads atomic.Int32
	var issueComment atomic.Pointer[string]
	var unchangedIssues atomic.Int32
	issueComment.Store(new("Original comment"))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/api/v3") {
		case "/repos/team/project":
			_, _ = io.WriteString(w, `{"id":12345,"node_id":"R_test_project","name":"project","full_name":"team/project","owner":{"login":"team"},"default_branch":"main","has_issues":true}`)
		case "/repos/team/project/pulls/7":
			detailReads.Add(1)
			_, _ = io.WriteString(w, `{"id":700,"node_id":"PR_test_7","number":7,"title":"Fresh from GitHub","state":"open","user":{"login":"user-a"},"head":{"sha":"abcdef","ref":"feature"},"base":{"sha":"base123","ref":"main","repo":{"id":12345,"node_id":"R_test_project","name":"project","owner":{"login":"team"}}},"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-15T00:00:00Z"}`)
		case "/repos/team/project/issues/9":
			if r.Header.Get("If-None-Match") == `"issue-stable"` {
				unchangedIssues.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"issue-stable"`)
			_, _ = io.WriteString(w, `{"id":900,"node_id":"I_test_9","number":9,"title":"Test issue","state":"open","user":{"login":"user-a"},"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-15T00:00:00Z"}`)
		case "/repos/team/project/issues/9/comments":
			if body := issueComment.Load(); body != nil {
				_, _ = fmt.Fprintf(w, `[{"id":901,"node_id":"IC_test_901","body":%q,"user":{"login":"user-a"},"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-15T00:00:00Z"}]`, *body)
			} else {
				_, _ = io.WriteString(w, `[]`)
			}
		case "/repos/team/project/commits/abcdef/check-runs":
			checkReads.Add(1)
			_, _ = io.WriteString(w, `{"total_count":1,"check_runs":[{"id":1,"name":"ci","status":"completed","conclusion":"success"}]}`)
		case "/repos/team/project/commits/abcdef/status":
			_, _ = io.WriteString(w, `{"state":"success","total_count":0,"statuses":[]}`)
		case "/repos/team/project/actions/runs":
			_, _ = io.WriteString(w, `{"total_count":0,"workflow_runs":[]}`)
		case "/api/graphql":
			_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{}}}}`)
		case "/users/user-a":
			_, _ = io.WriteString(w, `{"login":"user-a","name":"Test User"}`)
		case "/repos/team/project/issues/7/comments", "/repos/team/project/pulls/7/reviews", "/repos/team/project/pulls/7/commits", "/repos/team/project/issues/7/timeline", "/repos/team/project/issues/9/timeline":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.Error(w, "unexpected provider request", http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	database := dbtest.OpenWithMigrationsAt(t, filepath.Join(dir, "forge.db"))
	repo := ghclient.RepoRef{Platform: "github", PlatformHost: "github.com", PlatformRepoID: 12345, PlatformExternalID: "R_test_project", Owner: "team", Name: "project"}
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", PlatformRepoID: "R_test_project", Owner: "team", Name: "project"})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, Number: 8, PlatformID: 800, PlatformExternalID: "PR_test_8", Title: "Unrelated",
		State: "open", PlatformHeadSHA: "unrelated-sha", CIStatus: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	require.NoError(err)
	budget := ghclient.NewSyncBudget(1000)
	upstream, err := ghclient.NewClient(testTokenSource("test-token"), "github.com", nil, budget, ghclient.WithBaseURLForTesting(provider.URL))
	require.NoError(err)
	// A blocked clone directory produces a real non-fatal diff failure without
	// contacting a remote. PR metadata and CI must still be published once.
	cloneDirectory := filepath.Join(dir, "blocked-clones")
	require.NoError(os.WriteFile(cloneDirectory, []byte("not a directory"), 0o600))
	newConsumer := func() *ghclient.Syncer {
		return ghclient.NewSyncer(map[string]ghclient.Client{"github.com": upstream}, database, gitclone.New(cloneDirectory, nil), []ghclient.RepoRef{repo}, time.Minute, nil, map[string]*ghclient.SyncBudget{"github.com": budget})
	}
	syncer := newConsumer()
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{}, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := &http.Client{Timeout: 10 * time.Second}
	head, err := activityrelay.Fetch(ctx, client, relayURL, "")
	require.NoError(err)
	require.NoError(database.SaveRelayPage(ctx, relayURL, head.NextCursor, nil))
	deliver := func(event, payload string) {
		t.Helper()
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = io.WriteString(mac, payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+webhookAddress+"/webhooks/github/team", strings.NewReader(payload))
		require.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		response, err := client.Do(req)
		require.NoError(err)
		defer response.Body.Close()
		require.Equal(http.StatusNoContent, response.StatusCode)
	}
	pull := `{"repository":{"id":12345,"node_id":"R_test_project"},"pull_request":{"number":7,"title":"private-payload-marker"}}`
	deliver("pull_request", pull)
	require.NoError(syncer.PollRelay(ctx, relayURL, client))
	first, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(first)
	assert.Equal("Fresh from GitHub", first.Title)
	assert.Equal("success", first.CIStatus)
	api := setupTestClient(t, srv)
	status, err := api.HTTP.GetSyncStatusWithResponse(ctx)
	require.NoError(err)
	require.NotNil(status.JSON200)
	require.NotNil(status.JSON200.Relay)
	require.Len(status.JSON200.Relay.Recent, 1)
	assert.Equal("team/project", status.JSON200.Relay.Recent[0].Repository)
	assert.Equal(int64(7), status.JSON200.Relay.Recent[0].Number)
	assert.False(status.JSON200.Relay.Unavailable)
	pulls, err := api.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.NotNil(pulls.JSON200)
	var found bool
	for _, pull := range *pulls.JSON200 {
		if pull.Number == 7 {
			assert.Equal("Fresh from GitHub", pull.Title)
			found = true
		}
	}
	assert.True(found, "relay refresh must reach the Forge API")
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(stale)
	var refreshed bool
	for _, event := range events {
		if event.Event.Type == "pr_detail_refreshed" {
			refreshed = true
		}
	}
	assert.True(refreshed, "relay refresh must invalidate an open PR view")
	assert.Equal(int32(1), detailReads.Load())
	stopRelay()
	stopRelay = startRelay()
	defer stopRelay()
	deliver("issue_comment", `{"repository":{"id":12345,"node_id":"R_test_project"},"issue":{"number":7,"pull_request":{}}}`)
	syncer.Stop()
	syncer = newConsumer()
	t.Cleanup(syncer.Stop)
	syncer.SetOnRelayRefresh(srv.broadcastRelayRefresh)
	require.NoError(syncer.PollRelay(ctx, relayURL, client))
	assert.Equal(int32(2), detailReads.Load(), "PR comments must refresh details after restart")
	assert.Equal(int32(2), checkReads.Load())
	deliver("check_run", `{"repository":{"id":12345,"node_id":"R_test_project"},"check_run":{"pull_requests":[{"number":7}]}}`)
	require.NoError(syncer.PollRelay(ctx, relayURL, client))
	assert.Equal(int32(2), checkReads.Load())
	assert.Equal(int32(2), detailReads.Load(), "check events must not trigger refreshes")
	unrelated, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	assert.Equal("pending", unrelated.CIStatus)
	issueHint := `{"repository":{"id":12345,"node_id":"R_test_project"},"issue":{"number":9}}`
	for index, body := range []*string{new("Original comment"), new("Edited comment"), nil} {
		issueComment.Store(body)
		if index == 1 {
			// Regular conditional detail sees a 304 for the unchanged parent.
			// The subsequent webhook must still replace the edited comment.
			require.NoError(syncer.SyncIssue(ctx, "team", "project", 9))
			assert.Equal(int32(1), unchangedIssues.Load())
		}
		deliver("issue_comment", issueHint)
		require.NoError(syncer.PollRelay(ctx, relayURL, client))
		issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 9)
		require.NoError(err)
		require.NotNil(issue)
		events, err := database.ListIssueEvents(ctx, issue.ID)
		require.NoError(err)
		var bodies []string
		for _, event := range events {
			if event.EventType == "issue_comment" {
				bodies = append(bodies, event.Body)
			}
		}
		if body == nil {
			assert.Empty(bodies)
		} else {
			assert.Equal([]string{*body}, bodies)
		}
		require.NoError(database.UpsertHTTPEtag(ctx, "github", "github.com", "team", "project", "issue", 9, `"issue-stable"`))
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(databaseFile + suffix)
		require.NoError(err)
		assert.NotContains(string(data), "private-payload-marker")
		assert.NotContains(string(data), secret)
	}
}
