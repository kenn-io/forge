package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/web"
)

func TestWriteServerInfoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-info.json")
	info := e2eServerInfo{
		Host:    "127.0.0.1",
		Port:    43123,
		BaseURL: "http://127.0.0.1:43123",
		PID:     4242,
	}

	require.NoError(t, writeServerInfoFile(path, info))

	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var got e2eServerInfo
	require.NoError(t, json.Unmarshal(content, &got))
	assert := Assert.New(t)
	assert.Equal(info, got)
}

func TestCleanupServerInfoFileRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-info.json")
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))

	cleanupServerInfoFile(path)

	_, err := os.Stat(path)
	assert := Assert.New(t)
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestPatchFixturePRSHAsUpdatesOpenPRs(t *testing.T) {
	openPR := &gh.PullRequest{
		Number: new(1),
		Head:   &gh.PullRequestBranch{},
		Base:   &gh.PullRequestBranch{},
	}
	fc := &testutil.FixtureClient{
		OpenPRs: map[string][]*gh.PullRequest{
			"acme/widgets": {openPR},
		},
	}

	patchFixturePRSHAs(fc, "acme", "widgets", 1, "head-sha", "base-sha")

	assert := Assert.New(t)
	updated := fc.OpenPRs["acme/widgets"][0]
	assert.Equal("head-sha", updated.GetHead().GetSHA())
	assert.Equal("base-sha", updated.GetBase().GetSHA())
	assert.Empty(openPR.GetHead().GetSHA(), "update should replace fixture PR instead of mutating shared pointer")
}

func TestPatchFixturePRSHAsUpdatesLookupPRs(t *testing.T) {
	lookupPR := &gh.PullRequest{
		Number: new(1),
		Head:   &gh.PullRequestBranch{},
		Base:   &gh.PullRequestBranch{},
	}
	fc := &testutil.FixtureClient{
		PRs: map[string][]*gh.PullRequest{
			"acme/widgets": {lookupPR},
		},
	}

	patchFixturePRSHAs(fc, "acme", "widgets", 1, "head-sha", "base-sha")

	assert := Assert.New(t)
	updated := fc.PRs["acme/widgets"][0]
	assert.Equal("head-sha", updated.GetHead().GetSHA())
	assert.Equal("base-sha", updated.GetBase().GetSHA())
	assert.Empty(lookupPR.GetHead().GetSHA(), "update should replace fixture PR instead of mutating shared pointer")
}

func TestAppStateRegistryWaitsForInFlightHandlersAfterSwap(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	oldState := &appState{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	newState := &appState{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
	}
	states := newAppStateRegistry(oldState)

	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		states.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/", nil),
		)
	}()
	<-started

	require := require.New(t)
	swapped := states.Swap(newState)
	require.Same(oldState, swapped)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		swapped.waitForHandlers()
	}()

	select {
	case <-drained:
		require.Fail("old state drained before its in-flight handler returned")
	case <-time.After(100 * time.Millisecond):
	}

	newRecorder := httptest.NewRecorder()
	states.ServeHTTP(newRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(http.StatusAccepted, newRecorder.Code)

	close(release)
	select {
	case <-oldDone:
	case <-time.After(5 * time.Second):
		require.Fail("old handler did not return")
	}
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		require.Fail("old state did not drain after handler returned")
	}
}

// TestDefaultRoborevEndpointIsUnbindable pins the e2e server's
// roborev flag default to a loopback address with a privileged
// port. Reverting this default would re-introduce silent forwarding
// to a real local roborev daemon at 127.0.0.1:7373 during direct
// playwright runs.
func TestDefaultRoborevEndpointIsUnbindable(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	u, err := url.Parse(defaultRoborevEndpoint)
	require.NoError(err)
	assert.Equal("127.0.0.1", u.Hostname(),
		"default roborev endpoint must be loopback")

	port, err := strconv.Atoi(u.Port())
	require.NoError(err)
	assert.Less(port, 1024,
		"default roborev port must be privileged so it cannot be "+
			"silently bound by an unrelated developer process")
	assert.Positive(port)
}

func TestBuildAppStateSeedsReviewedHeadsForUTCMergeTargets(t *testing.T) {
	assets, err := web.Assets()
	require.NoError(t, err)

	state, err := buildAppState(t.Context(), assets, appOptions{
		roborevEndpoint: defaultRoborevEndpoint,
	})
	require.NoError(t, err)
	t.Cleanup(state.close)

	for _, target := range []struct {
		owner  string
		repo   string
		number int
	}{
		{owner: "acme", repo: "widgets", number: 7},
		{owner: "acme", repo: "tools", number: 1},
	} {
		t.Run(target.repo+"-"+strconv.Itoa(target.number), func(t *testing.T) {
			require := require.New(t)
			assert := Assert.New(t)

			repo, err := state.database.GetRepoByOwnerName(
				t.Context(), target.owner, target.repo,
			)
			require.NoError(err)
			require.NotNil(repo)

			mr, err := state.database.GetMergeRequestByRepoIDAndNumber(
				t.Context(), repo.ID, target.number,
			)
			require.NoError(err)
			require.NotNil(mr)

			assert.Equal(mr.PlatformHeadSHA, mr.DiffHeadSHA)
			assert.NotEmpty(mr.DiffHeadSHA)
			assert.Equal(mr.PlatformBaseSHA, mr.DiffBaseSHA)

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodGet,
				"http://127.0.0.1/api/v1/pulls/github/"+
					target.owner+"/"+target.repo+"/"+strconv.Itoa(target.number),
				nil,
			)
			state.handler.ServeHTTP(recorder, request)
			require.Equal(http.StatusOK, recorder.Code)

			var detail map[string]any
			require.NoError(json.Unmarshal(recorder.Body.Bytes(), &detail))
			assert.Equal(mr.DiffHeadSHA, detail["reviewed_head_sha"])
		})
	}
}

// TestRunDefaultRoborevFailsClosedThroughProxy is the behavioral
// guard the test review asked for: it calls run() with the default
// roborev endpoint (no -roborev override) and verifies that the
// full e2e-server startup path wires the proxy so
// /api/roborev/api/status returns a closed-fail 502 with the
// proxy's "not reachable" error JSON, instead of silently
// forwarding the probe to a real local daemon.
//
// The test exercises run() directly (not server.New) so a later
// regression anywhere in the run() wiring — config population,
// roborev endpoint propagation, proxy registration — would break
// this test. Pairs with TestDefaultRoborevEndpointIsUnbindable
// which pins the constant value itself.
func TestRunDefaultRoborevFailsClosedThroughProxy(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	serverInfoFile := filepath.Join(t.TempDir(), "server-info.json")

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, 0, defaultRoborevEndpoint, serverInfoFile, "github.com", false, false)
	}()

	baseURL := waitForServerInfoBaseURL(t, serverInfoFile, done)

	resp, err := http.Get(baseURL + "/api/roborev/api/status")
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusBadGateway, resp.StatusCode,
		"default roborev endpoint must fail closed through the proxy")

	body, err := io.ReadAll(resp.Body)
	require.NoError(err)
	var payload map[string]string
	require.NoError(json.Unmarshal(body, &payload))
	assert.Contains(payload["error"], "roborev daemon is not reachable")

	// Cancel and confirm run() exits cleanly so the goroutine does
	// not outlive the test.
	cancel()
	select {
	case runErr := <-done:
		require.NoError(runErr)
	case <-time.After(10 * time.Second):
		require.Fail("run() did not exit within 10s of cancellation")
	}
}

// waitForServerInfoBaseURL polls the server-info file until run()
// writes it, then returns the BaseURL. Fails the test if run()
// returns early (done is closed with an error) or the file does
// not appear within the timeout.
func waitForServerInfoBaseURL(
	t *testing.T, path string, done <-chan error,
) string {
	t.Helper()
	r := require.New(t)
	// 30s headroom: run() does SeedFixtures + SetupDiffRepo + stack
	// detection before it starts listening, and `go test ./...` can
	// run this test under parallel load.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			r.Failf("run exited early",
				"run() exited before server-info was written: %v", err)
		default:
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var info e2eServerInfo
			if jsonErr := json.Unmarshal(data, &info); jsonErr == nil && info.BaseURL != "" {
				return info.BaseURL
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.FailNow("timed out waiting for server-info file")
	return ""
}

// TestResetSwapsFixtureState pins the /__e2e/reset contract that the
// Playwright server pool depends on: a reset discards mutations made
// against the old state, serves the rebuilt state on the same port,
// reports the new config path, honors option overrides, and rejects
// malformed bodies instead of silently resetting to defaults.
func TestResetSwapsFixtureState(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	serverInfoFile := filepath.Join(t.TempDir(), "server-info.json")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, 0, defaultRoborevEndpoint, serverInfoFile, "github.com", false, false)
	}()
	baseURL := waitForServerInfoBaseURL(t, serverInfoFile, done)

	infoBefore := readInfoFile(t, serverInfoFile)

	seededDetail := getJSON(t, baseURL+"/api/v1/pulls/github/acme/widgets/1")
	seededMR, ok := seededDetail["merge_request"].(map[string]any)
	require.True(ok, "detail payload missing merge_request")
	seededCIStatus := seededMR["CIStatus"]

	// Mutate old-state fixture data away from the seeded value.
	resp, err := http.Post(baseURL+"/__e2e/pr-ci-state/mixed", "application/json", nil)
	require.NoError(err)
	resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	mutatedDetail := getJSON(t, baseURL+"/api/v1/pulls/github/acme/widgets/1")
	mutatedMR, ok := mutatedDetail["merge_request"].(map[string]any)
	require.True(ok, "detail payload missing merge_request")
	require.NotEqual(seededCIStatus, mutatedMR["CIStatus"], "fixture mutation must change CI status")

	// Malformed JSON must fail loudly, not reset.
	resp, err = http.Post(baseURL+"/__e2e/reset", "application/json", strings.NewReader("{not json"))
	require.NoError(err)
	resp.Body.Close()
	assert.Equal(http.StatusBadRequest, resp.StatusCode)

	// A real reset succeeds and reports a fresh config path.
	resp, err = http.Post(baseURL+"/__e2e/reset", "application/json", nil)
	require.NoError(err)
	var resetInfo e2eServerInfo
	require.NoError(json.NewDecoder(resp.Body).Decode(&resetInfo))
	resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal(infoBefore.BaseURL, resetInfo.BaseURL, "reset must keep the listener and port")
	assert.NotEqual(infoBefore.ConfigPath, resetInfo.ConfigPath, "reset must rebuild the seeded config")

	// The mutation must not survive into the rebuilt state.
	detail := getJSON(t, baseURL+"/api/v1/pulls/github/acme/widgets/1")
	mr, ok := detail["merge_request"].(map[string]any)
	require.True(ok, "detail payload missing merge_request")
	assert.Equal(seededCIStatus, mr["CIStatus"], "seeded CI state must be restored after reset")

	// Option overrides apply to the rebuilt state.
	resp, err = http.Post(
		baseURL+"/__e2e/reset",
		"application/json",
		strings.NewReader(`{"default_platform_host":"ghe.example.com"}`),
	)
	require.NoError(err)
	resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	repos := getJSONList(t, baseURL+"/api/v1/repos")
	foundEnterprise := false
	for _, repo := range repos {
		row, ok := repo.(map[string]any)
		if !ok {
			continue
		}
		if row["Owner"] == "enterprise" && row["Name"] == "service" {
			foundEnterprise = true
		}
	}
	assert.True(foundEnterprise, "host override must reseed the enterprise repo set")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(runErr)
	case <-time.After(10 * time.Second):
		require.Fail("run() did not exit within 10s of cancellation")
	}
}

func readInfoFile(t *testing.T, path string) e2eServerInfo {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var info e2eServerInfo
	require.NoError(t, json.Unmarshal(data, &info))
	return info
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload
}

func getJSONList(t *testing.T, url string) []any {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload []any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload
}
