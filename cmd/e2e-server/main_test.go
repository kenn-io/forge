package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/testutil"
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
		done <- run(ctx, 0, defaultRoborevEndpoint, serverInfoFile, "github.com", false)
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
