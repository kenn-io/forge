package externalcontext

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
)

// The fixture consumes the actual command protocol and records invocations.
func TestExternalContextHelper(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == -1 {
		return
	}
	args := os.Args[separator+1:]
	mode, dir := args[0], args[1]
	if mode == "hold-pipes" {
		time.Sleep(time.Second)
		os.Exit(0)
	}
	data, err := io.ReadAll(os.Stdin)
	require.NoError(t, err)
	var request struct {
		Version     int         `json:"version"`
		Operation   string      `json:"operation"`
		ActionID    string      `json:"action_id"`
		PullRequest PullRequest `json:"pull_request"`
	}
	require.NoError(t, json.Unmarshal(data, &request))
	require.Equal(t, 1, request.Version)
	name := fmt.Sprintf("%d-%s-%d", os.Getpid(), request.Operation, request.PullRequest.Number)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	if mode == "gate" && request.Operation == "read" || strings.HasPrefix(mode, "gate-action") && request.Operation == "action" {
		for {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if mode == "gate-action-exit" && request.Operation == "action" {
		os.Exit(3)
	}
	switch mode {
	case "timeout":
		time.Sleep(time.Minute)
	case "stdout":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 1024*1024+1))
	case "stderr":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("x", 64*1024+1))
	case "exit":
		_, _ = io.WriteString(os.Stderr, "private diagnostic")
		os.Exit(3)
	case "raw":
		_, _ = io.WriteString(os.Stdout, args[2])
	case "inherited-pipes":
		cmd := procutil.Command(os.Args[0], "-test.run=^TestExternalContextHelper$", "--", "hold-pipes", dir)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		require.NoError(t, cmd.Start())
		require.NoError(t, cmd.Process.Release())
	default:
		summary := request.Operation + ":" + request.PullRequest.HeadSHA + ":" + request.ActionID
		result := ExternalContextResult{Card: &ExternalContextCard{Status: "success", Summary: summary, ResultHeadSHA: request.PullRequest.HeadSHA, RefreshAfterSeconds: 1}}
		require.NoError(t, json.MarshalWrite(os.Stdout, result))
	}
	os.Exit(0)
}

func fixtureSource(t *testing.T, mode string, extra ...string) (config.ExternalContextSource, string) {
	t.Helper()
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	args := []string{executable, "-test.run=^TestExternalContextHelper$", "--", mode, dir}
	return config.ExternalContextSource{ID: "metrics", Name: "Metrics", Command: append(args, extra...), Timeout: "5s"}, dir
}

func fixturePull() PullRequest {
	return PullRequest{Provider: "gitlab", PlatformHost: "git.example.test", PlatformRepoID: "123", RepoPath: "group/subgroup/project", Number: 42, URL: "https://git.example.test/group/subgroup/project/-/merge_requests/42", State: "open", HeadSHA: "head-one", BaseSHA: "base-one"}
}

func invocations(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var result []os.DirEntry
	for _, entry := range entries {
		if entry.Name() != "release" {
			result = append(result, entry)
		}
	}
	return result
}

func TestReadProtocolCacheAndAction(t *testing.T) {
	assert := assert.New(t)
	source, dir := fixtureSource(t, "card")
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	pull := fixturePull()
	result, err := runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	require.NotNil(t, result.Card)
	assert.Equal("read:head-one:", result.Card.Summary)
	assert.Equal(5, result.Card.RefreshAfterSeconds)
	files := invocations(t, dir)
	require.Len(t, files, 1)
	payload, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	require.NoError(t, err)
	assert.JSONEq(`{"version":1,"operation":"read","pull_request":{"provider":"gitlab","platform_host":"git.example.test","platform_repo_id":"123","repo_path":"group/subgroup/project","number":42,"url":"https://git.example.test/group/subgroup/project/-/merge_requests/42","state":"open","head_sha":"head-one","base_sha":"base-one"}}`, string(payload))
	_, err = runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	assert.Len(invocations(t, dir), 1)
	_, err = runner.Read(t.Context(), "metrics", pull, true)
	require.NoError(t, err)
	assert.Len(invocations(t, dir), 2)
	result, err = runner.Action(t.Context(), "metrics", pull, "run")
	require.NoError(t, err)
	assert.Equal("action:head-one:run", result.Card.Summary)
	assert.Len(invocations(t, dir), 3)
	_, err = runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	assert.Len(invocations(t, dir), 4)
}

func TestReadValidationAndFailureCache(t *testing.T) {
	for _, tt := range []struct {
		name, mode, output string
		want               error
	}{
		{"no card", "raw", `{"card":null}`, nil},
		{"missing card", "raw", `{}`, ErrInvalidResponse},
		{"trailing json", "raw", `{"card":null}{}`, ErrInvalidResponse},
		{"malformed", "raw", `private output`, ErrInvalidResponse},
		{"status", "raw", `{"card":{"status":"other","summary":"hello"}}`, ErrInvalidResponse},
		{"summary", "raw", `{"card":{"status":"success","summary":" "}}`, ErrInvalidResponse},
		{"summary limit", "raw", `{"card":{"status":"success","summary":"` + strings.Repeat("x", 4097) + `"}}`, ErrInvalidResponse},
		{"duplicate action", "raw", `{"card":{"status":"success","summary":"hello","actions":[{"id":"run","label":"Run"},{"id":"run","label":"Again"}]}}`, ErrInvalidResponse},
		{"stdout limit", "stdout", "", ErrOutputLimit},
		{"stderr limit", "stderr", "", ErrOutputLimit},
		{"exit", "exit", "", ErrInvocation},
		{"deadline", "timeout", "", ErrTimeout},
		{"inherited pipes", "inherited-pipes", "", ErrInvocation},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			source, dir := fixtureSource(t, tt.mode, tt.output)
			if tt.mode == "timeout" {
				source.Timeout = "100ms"
			}
			runner := New([]config.ExternalContextSource{source})
			t.Cleanup(runner.Close)
			for range 2 {
				result, err := runner.Read(t.Context(), "metrics", fixturePull(), false)
				require.ErrorIs(t, err, tt.want)
				assert.Nil(result.Card)
				if err != nil {
					assert.NotContains(err.Error(), "private")
				}
			}
			assert.Len(invocations(t, dir), 1)
		})
	}
}

func TestSharedReadSurvivesWaiterCancellation(t *testing.T) {
	source, dir := fixtureSource(t, "gate")
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := runner.Read(ctx, "metrics", fixturePull(), false); first <- err }()
	require.Eventually(t, func() bool { return len(invocations(t, dir)) == 1 }, 3*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	second := make(chan ExternalContextResult, 1)
	go func() { result, _ := runner.Read(t.Context(), "metrics", fixturePull(), false); second <- result }()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
	result := <-second
	require.NotNil(t, result.Card)
	assert.Equal(t, "read:head-one:", result.Card.Summary)
	assert.Len(t, invocations(t, dir), 1)
}

func TestInvalidationDiscardsInflightRead(t *testing.T) {
	for _, action := range []bool{false, true} {
		t.Run(fmt.Sprint("action=", action), func(t *testing.T) {
			source, dir := fixtureSource(t, "gate")
			runner := New([]config.ExternalContextSource{source})
			t.Cleanup(runner.Close)
			readDone := make(chan struct{})
			go func() { _, _ = runner.Read(t.Context(), "metrics", fixturePull(), false); close(readDone) }()
			require.Eventually(t, func() bool { return len(invocations(t, dir)) == 1 }, 3*time.Second, time.Millisecond)
			if action {
				_, err := runner.Action(t.Context(), "metrics", fixturePull(), "run")
				require.NoError(t, err)
			} else {
				runner.Update([]config.ExternalContextSource{source})
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
			<-readDone
			_, err := runner.Read(t.Context(), "metrics", fixturePull(), false)
			require.NoError(t, err)
			want := 2
			if action {
				want++
			}
			assert.Len(t, invocations(t, dir), want)
		})
	}
}

func TestActionsAreNotRetriedAndReportUncertainSubmission(t *testing.T) {
	source, dir := fixtureSource(t, "exit")
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	_, err := runner.Action(t.Context(), "metrics", fixturePull(), "run")
	require.ErrorIs(t, err, ErrInvocation)
	assert.Contains(t, err.Error(), "The action may have been submitted. Refresh to check its status.")
	assert.NotContains(t, err.Error(), "private diagnostic")
	assert.Len(t, invocations(t, dir), 1)
}

func TestProcessLimitIncludesActionsAndQueuedDeadline(t *testing.T) {
	source, dir := fixtureSource(t, "gate")
	queuedSource, queuedDir := fixtureSource(t, "card")
	queuedSource.ID, queuedSource.Timeout = "queued", "100ms"
	runner := New([]config.ExternalContextSource{source, queuedSource})
	t.Cleanup(runner.Close)
	readDone := make(chan error, 2)
	for number := range 2 {
		pull := fixturePull()
		pull.Number += number
		go func() {
			_, err := runner.Read(t.Context(), "metrics", pull, false)
			readDone <- err
		}()
	}
	require.Eventually(t, func() bool { return len(invocations(t, dir)) == 2 }, 3*time.Second, time.Millisecond)
	_, err := runner.Action(t.Context(), "queued", fixturePull(), "run")
	require.ErrorIs(t, err, ErrTimeout)
	assert.Empty(t, invocations(t, queuedDir))
	assert.NotContains(t, err.Error(), "may have been submitted")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
	for range 2 {
		require.NoError(t, <-readDone)
	}
}

func TestReadCacheUsesCompletePullSnapshot(t *testing.T) {
	source, dir := fixtureSource(t, "card")
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	for i, change := range []func(*PullRequest){
		func(*PullRequest) {},
		func(p *PullRequest) { p.Provider = "forgejo" },
		func(p *PullRequest) { p.PlatformHost = "other.example.test" },
		func(p *PullRequest) { p.PlatformRepoID = "456" },
		func(p *PullRequest) { p.RepoPath = "renamed/project" },
		func(p *PullRequest) { p.Number = 43 },
		func(p *PullRequest) { p.URL = "https://example.test/pull/42" },
		func(p *PullRequest) { p.State = "closed" },
		func(p *PullRequest) { p.HeadSHA = "head-two" },
		func(p *PullRequest) { p.BaseSHA = "base-two" },
	} {
		pull := fixturePull()
		change(&pull)
		_, err := runner.Read(t.Context(), "metrics", pull, false)
		require.NoError(t, err)
		assert.Len(t, invocations(t, dir), i+1)
	}
}

func TestCacheExpiresAndEvictsOldest(t *testing.T) {
	source, dir := fixtureSource(t, "raw", `{"card":null}`)
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	for number := range 65 {
		pull := fixturePull()
		pull.Number = number
		_, err := runner.Read(t.Context(), "metrics", pull, false)
		require.NoError(t, err)
	}
	pull := fixturePull()
	pull.Number = 64
	_, err := runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	assert.Len(t, invocations(t, dir), 65)
	pull.Number = 0
	_, err = runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	assert.Len(t, invocations(t, dir), 66)
	// Exercise the real fixed TTL; subprocess I/O cannot run inside synctest.
	time.Sleep(5 * time.Second)
	_, err = runner.Read(t.Context(), "metrics", pull, false)
	require.NoError(t, err)
	assert.Len(t, invocations(t, dir), 67)
}

func TestSourceSnapshotsAndClose(t *testing.T) {
	assert := assert.New(t)
	source, _ := fixtureSource(t, "card")
	sources := []config.ExternalContextSource{source}
	runner := New(sources)
	t.Cleanup(runner.Close)
	sources[0].Name = "Changed"
	sources[0].Command[0] = "/missing/command"
	result, err := runner.Read(t.Context(), "metrics", fixturePull(), false)
	require.NoError(t, err)
	require.NotNil(t, result.Card)
	assert.Equal([]ExternalContextSourceInfo{{ID: "metrics", Name: "Metrics"}}, runner.Sources())
	runner.Update(nil)
	assert.Empty(runner.Sources())
	_, err = runner.Read(t.Context(), "metrics", fixturePull(), false)
	require.ErrorIs(t, err, ErrUnknownSource)
	runner.Close()
	_, err = runner.Read(t.Context(), "metrics", fixturePull(), false)
	require.ErrorIs(t, err, ErrClosed)
}

func TestActionCompletionInvalidatesReadsOnSuccessAndFailure(t *testing.T) {
	for _, mode := range []string{"gate-action", "gate-action-exit"} {
		t.Run(mode, func(t *testing.T) {
			source, dir := fixtureSource(t, mode)
			runner := New([]config.ExternalContextSource{source})
			t.Cleanup(runner.Close)
			_, err := runner.Read(t.Context(), "metrics", fixturePull(), false)
			require.NoError(t, err)
			actionDone := make(chan error, 1)
			go func() {
				_, err := runner.Action(t.Context(), "metrics", fixturePull(), "run")
				actionDone <- err
			}()
			require.Eventually(t, func() bool { return len(invocations(t, dir)) == 2 }, 3*time.Second, time.Millisecond)
			_, err = runner.Read(t.Context(), "metrics", fixturePull(), false)
			require.NoError(t, err)
			assert.Len(t, invocations(t, dir), 3)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
			if mode == "gate-action-exit" {
				require.ErrorIs(t, <-actionDone, ErrInvocation)
			} else {
				require.NoError(t, <-actionDone)
			}
			_, err = runner.Read(t.Context(), "metrics", fixturePull(), false)
			require.NoError(t, err)
			assert.Len(t, invocations(t, dir), 4)
		})
	}
}

func TestCloseCancelsInflightCommand(t *testing.T) {
	source, dir := fixtureSource(t, "gate")
	runner := New([]config.ExternalContextSource{source})
	t.Cleanup(runner.Close)
	readDone := make(chan error, 1)
	go func() {
		_, err := runner.Read(t.Context(), "metrics", fixturePull(), false)
		readDone <- err
	}()
	require.Eventually(t, func() bool { return len(invocations(t, dir)) == 1 }, 3*time.Second, time.Millisecond)
	runner.Close()
	require.ErrorIs(t, <-readDone, ErrInvocation)
}
