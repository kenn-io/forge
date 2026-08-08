package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpawnWorkspaceWithAgentCreatesPRWorkspaceAndDeliversMessage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	workspaceReads := 0
	sessionReads := 0
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		assert.Equal(http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("github", body["provider"])
		assert.Equal("github.com", body["platform_host"])
		assert.Equal("acme", body["owner"])
		assert.Equal("widget", body["name"])
		assert.InDelta(42, body["mr_number"], 0)
		assert.Equal(true, body["suppress_auto_assign"])
		writeJSONResponse(w, `{"id":"ws-new","status":"creating","created":true}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-new", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		workspaceReads++
		status := "creating"
		if workspaceReads > 1 {
			status = "ready"
		}
		writeJSONResponse(w, `{"id":"ws-new","status":"`+status+`"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-new/runtime/sessions", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		assert.Equal(http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("codex", body["target_key"])
		writeJSONResponse(w, `{"key":"runtime-new","workspace_id":"ws-new","target_key":"codex","kind":"agent","status":"running","created_at":"2026-08-07T15:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-new/agent-sessions", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		sessionReads++
		if sessionReads == 1 {
			writeJSONResponse(w, `{"sessions":[]}`)
			return
		}
		writeJSONResponse(w, `{"sessions":[{"agent":"codex","session_id":"coding-new","runtime_session_key":"runtime-new","target_key":"codex","state":"working","updated_at":"2026-08-07T15:00:01Z"}]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-new/runtime", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[{"key":"runtime-new","status":"running"}]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-new/runtime/sessions/runtime-new/initial-message", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		assert.Equal(http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("codex", body["agent"])
		assert.Equal("coding-new", body["session_id"])
		assert.Equal("review this\nthen implement", body["message"])
		writeJSONResponse(w, `{"agent":"codex","session_id":"coding-new","state":"delivered","message_bytes":26,"reserved_at":"2026-08-07T15:00:01Z","delivered_at":"2026-08-07T15:00:02Z"}`)
	})
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: "review this\r\nthen implement",
		Timeout: "2s",
	})
	require.NoError(err)
	assert.Equal("message_delivered", out.Stage)
	assert.True(out.MessageDelivered)
	assert.Equal("ws-new", out.Workspace.ID)
	assert.False(out.Workspace.Reused)
	assert.Equal("runtime-new", out.Runtime.SessionKey)
	assert.Equal("coding-new", out.CodingSession.SessionID)
	require.NotNil(out.InitialMessage)
	assert.Equal("delivered", out.InitialMessage.State)
	assert.Equal(1, strings.Count(strings.Join(calls, "\n"), "POST /api/v1/workspaces\n"))
}

func TestSpawnWorkspaceWithAgentReusesPRWorkspaceButLaunchesNewRuntime(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	workspaceCreates := 0
	runtimeLaunches := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":{"id":"ws-existing","status":"ready"}}`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
		workspaceCreates++
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-existing", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"ws-existing","status":"ready"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-existing/runtime/sessions", func(w http.ResponseWriter, _ *http.Request) {
		runtimeLaunches++
		writeJSONResponse(w, `{"key":"runtime-fresh","workspace_id":"ws-existing","target_key":"codex","kind":"agent","status":"running","created_at":"2026-08-07T15:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-existing/agent-sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[
			{"agent":"codex","session_id":"old","runtime_session_key":"runtime-old","target_key":"codex","state":"done","updated_at":"2026-08-07T14:00:00Z"},
			{"agent":"codex","session_id":"fresh","runtime_session_key":"runtime-fresh","target_key":"codex","state":"working","updated_at":"2026-08-07T15:00:01Z"}
		]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-existing/runtime/sessions/runtime-fresh/initial-message", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"agent":"codex","session_id":"fresh","state":"delivered","message_bytes":5,"reserved_at":"2026-08-07T15:00:01Z","delivered_at":"2026-08-07T15:00:02Z"}`)
	})
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: "start", Timeout: "2s",
	})
	require.NoError(err)
	assert.Equal(0, workspaceCreates)
	assert.Equal(1, runtimeLaunches)
	assert.True(out.Workspace.Reused)
	assert.Equal("runtime-fresh", out.Runtime.SessionKey)
	assert.Equal("fresh", out.CodingSession.SessionID)
}

func TestSpawnWorkspaceWithAgentTimeoutReturnsPartialStateWithoutCleanup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deletes := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
		}
		writeJSONResponse(w, `{"id":"ws-timeout","status":"creating","created":true}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-timeout", func(_ http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
		}
		<-r.Context().Done()
	})
	s := newMCPTestServer(t, mux)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: "start", Timeout: "20ms",
	})
	var daemonErr *daemonError
	require.ErrorAs(err, &daemonErr)
	assert.Equal("agent_handoff_timeout", daemonErr.Kind)
	assert.Equal("workspace_created", daemonErr.Details["last_completed_stage"])
	assert.Equal("workspace_ready", daemonErr.Details["failed_stage"])
	assert.Equal("ws-timeout", daemonErr.Details["workspace_id"])
	assert.Equal(0, deletes)
}

func TestSpawnWorkspaceWithAgentReportsObservedWorkspaceErrorStatus(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"ws-error","status":"creating","created":true}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-error", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"ws-error","status":"error","error_message":"clone failed"}`)
	})
	s := newMCPTestServer(t, mux)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))
	var daemonErr *daemonError
	require.ErrorAs(err, &daemonErr)
	assert.Equal("agent_handoff_failed", daemonErr.Kind)
	assert.Equal("error", daemonErr.Details["workspace_status"])
	assert.Contains(daemonErr.Message, "clone failed")
}

func TestSpawnWorkspaceWithAgentTargetDiscoveryTimeoutUsesHandoffEnvelope(t *testing.T) {
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	s := newMCPTestServer(t, mux)

	input := prSpawnInput("start")
	input.Timeout = "20ms"
	_, err := s.spawnWorkspaceWithAgent(t.Context(), input)
	var daemonErr *daemonError
	require.ErrorAs(err, &daemonErr)
	assert.Equal(t, "agent_handoff_timeout", daemonErr.Kind)
}

func TestSpawnWorkspaceWithAgentMutationTimeoutPreservesAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failRuntime bool
		failedStage string
	}{
		{name: "workspace creation", failedStage: "workspace_created"},
		{name: "runtime launch", failRuntime: true, failedStage: "runtime_launched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
				writeAgentTargetSettings(w, true)
			})
			mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
			})
			mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
				if !tc.failRuntime {
					time.Sleep(100 * time.Millisecond)
					return
				}
				writeJSONResponse(w, `{"id":"ws-timeout","status":"ready","created":true}`)
			})
			if tc.failRuntime {
				mux.HandleFunc("/api/v1/workspaces/ws-timeout", func(w http.ResponseWriter, _ *http.Request) {
					writeJSONResponse(w, `{"id":"ws-timeout","status":"ready"}`)
				})
				mux.HandleFunc("/api/v1/workspaces/ws-timeout/runtime/sessions", func(_ http.ResponseWriter, _ *http.Request) {
					time.Sleep(100 * time.Millisecond)
				})
			}
			s := newMCPTestServer(t, mux)

			input := prSpawnInput("start")
			input.Timeout = "30ms"
			_, err := s.spawnWorkspaceWithAgent(t.Context(), input)
			var daemonErr *daemonError
			require.ErrorAs(err, &daemonErr)
			assert.Equal("agent_handoff_timeout", daemonErr.Kind)
			assert.True(daemonErr.Ambiguous)
			assert.Equal(tc.failedStage, daemonErr.Details["failed_stage"])
		})
	}
}

func TestSpawnWorkspaceWithAgentRejectsInvalidInputBeforeWorkspaceMutation(t *testing.T) {
	require := require.New(t)
	workspaceMutations := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			workspaceMutations++
		}
	})
	s := newMCPTestServer(t, mux)

	tests := []spawnWorkspaceWithAgentInput{
		{AgentTarget: "codex", InitialMessage: "start", Timeout: "16m"},
		{Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42,
		}}, AgentTarget: "codex", InitialMessage: " \n\t"},
	}
	for _, input := range tests {
		_, err := s.spawnWorkspaceWithAgent(t.Context(), input)
		require.Error(err)
	}
	require.Equal(0, workspaceMutations)
}

func TestSpawnWorkspaceWithAgentCreatesIssueWorkspace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	issueCreates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/issues/github/acme/widget/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"issue":{"Number":7},"workspace":null}`)
	})
	mux.HandleFunc("/api/v1/host/github.com/issues/github/acme/widget/7/workspace", func(w http.ResponseWriter, r *http.Request) {
		issueCreates++
		assert.Equal(http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.NotContains(body, "git_head_ref")
		assert.Equal(true, body["suppress_auto_assign"])
		writeJSONResponse(w, `{"id":"ws-issue","status":"creating","created":true,"git_head_ref":"work/issue-7"}`)
	})
	registerSuccessfulAgentHandoff(t, mux, "ws-issue", "runtime-issue", "coding-issue")
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "issue", Provider: "github", Owner: "acme", Name: "widget", Number: 7,
		}},
		AgentTarget: "codex", InitialMessage: "fix the issue", Timeout: "2s",
	})
	require.NoError(err)
	assert.Equal(1, issueCreates)
	assert.Equal("message_delivered", out.Stage)
	assert.Equal("ws-issue", out.Workspace.ID)
	assert.False(out.Workspace.Reused)
	assert.Equal("coding-issue", out.CodingSession.SessionID)
}

func TestSpawnWorkspaceWithAgentCreatesAdHocWorkspaceAndReturnsGeneratedBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	adHocCreates := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/repo/github/acme/widget/workspaces", func(w http.ResponseWriter, r *http.Request) {
		adHocCreates++
		assert.Equal(http.MethodPost, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.NotContains(body, "branch")
		writeJSONResponse(w, `{"id":"ws-adhoc","status":"creating","created":true,"git_head_ref":"kenn-forge/work-abc123"}`)
	})
	registerSuccessfulAgentHandoff(t, mux, "ws-adhoc", "runtime-adhoc", "coding-adhoc")
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "adhoc", AdHoc: &adHocWorkspaceSource{
			Repo: repoFilterInput{Provider: "github", Owner: "acme", Name: "widget"},
		}},
		AgentTarget: "codex", InitialMessage: "start the work", Timeout: "2s",
	})
	require.NoError(err)
	assert.Equal(1, adHocCreates)
	assert.Equal("message_delivered", out.Stage)
	assert.Equal("ws-adhoc", out.Workspace.ID)
	assert.Equal("kenn-forge/work-abc123", out.Source.AdHoc.Branch)
}

func TestSpawnWorkspaceWithAgentForwardsExplicitAdHocBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/gitlab.example/repo/gitlab/group%2Fsubgroup/widget/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("work/explicit", body["branch"])
		writeJSONResponse(w, `{"id":"ws-explicit","status":"ready","created":true,"git_head_ref":"work/explicit"}`)
	})
	registerSuccessfulAgentHandoff(t, mux, "ws-explicit", "runtime-explicit", "coding-explicit")
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "adhoc", AdHoc: &adHocWorkspaceSource{
			Repo: repoFilterInput{
				Provider: "gitlab", PlatformHost: "gitlab.example",
				RepoPath: "group/subgroup/widget",
			},
			Branch: "work/explicit",
		}},
		AgentTarget: "codex", InitialMessage: "start the work", Timeout: "2s",
	})
	require.NoError(err)
	assert.Equal("message_delivered", out.Stage)
	assert.Equal("work/explicit", out.Source.AdHoc.Branch)
}

func TestSpawnWorkspaceWithAgentRecoversOnlyReceiptAfterAmbiguousMessageResponse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	messagePosts := 0
	receiptReads := 0
	mux := successfulPRHandoffMuxWithoutMessage(t, "ws-recovery", "runtime-recovery", "coding-recovery")
	mux.HandleFunc("/api/v1/workspaces/ws-recovery/runtime/sessions/runtime-recovery/initial-message", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			messagePosts++
			writeJSONResponse(w, `{"agent":"codex","session_id":"coding-recovery","state":"delivered","message_bytes":5} {}`)
		case http.MethodGet:
			receiptReads++
			writeJSONResponse(w, `{"agent":"codex","session_id":"coding-recovery","state":"delivered","message_bytes":5,"delivered_at":"2026-08-07T15:00:02Z"}`)
		}
	})
	s := newMCPTestServer(t, mux)

	out, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))
	require.NoError(err)
	assert.True(out.MessageDelivered)
	assert.Equal(1, messagePosts)
	assert.Equal(1, receiptReads)
}

func TestSpawnWorkspaceWithAgentReceiptRecoverySurvivesOuterTimeout(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	messagePosts := 0
	receiptReads := 0
	mux := successfulPRHandoffMuxWithoutMessage(t, "ws-recovery", "runtime-recovery", "coding-recovery")
	mux.HandleFunc("/api/v1/workspaces/ws-recovery/runtime/sessions/runtime-recovery/initial-message", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			messagePosts++
			time.Sleep(100 * time.Millisecond)
		case http.MethodGet:
			receiptReads++
			writeJSONResponse(w, `{"agent":"codex","session_id":"coding-recovery","state":"delivered","message_bytes":5,"delivered_at":"2026-08-07T15:00:02Z"}`)
		}
	})
	s := newMCPTestServer(t, mux)

	input := prSpawnInput("start")
	input.Timeout = "30ms"
	out, err := s.spawnWorkspaceWithAgent(t.Context(), input)
	require.NoError(err)
	assert.True(out.MessageDelivered)
	assert.Equal(1, messagePosts)
	assert.Equal(1, receiptReads)
}

func TestSpawnWorkspaceWithAgentReceiptRecoveryClassifiesStates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		receiptStates []string
		wantDelivered bool
		wantState     string
	}{
		{name: "pending then delivered", receiptStates: []string{"pending", "delivered"}, wantDelivered: true},
		{name: "uncertain", receiptStates: []string{"uncertain"}, wantState: "uncertain"},
		{name: "unresolved pending", receiptStates: []string{"pending"}, wantState: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			messagePosts := 0
			receiptReads := 0
			mux := successfulPRHandoffMuxWithoutMessage(t, "ws-recovery", "runtime-recovery", "coding-recovery")
			mux.HandleFunc("/api/v1/workspaces/ws-recovery/runtime/sessions/runtime-recovery/initial-message", func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					messagePosts++
					writeJSONResponse(w, `{"agent":"codex","session_id":"coding-recovery","state":"delivered","message_bytes":5} {}`)
				case http.MethodGet:
					state := tc.receiptStates[min(receiptReads, len(tc.receiptStates)-1)]
					receiptReads++
					writeJSONResponse(w, `{"agent":"codex","session_id":"coding-recovery","state":"`+state+`","message_bytes":5}`)
				}
			})
			s := newMCPTestServer(t, mux)

			input := prSpawnInput("start")
			input.Timeout = "40ms"
			out, err := s.spawnWorkspaceWithAgent(t.Context(), input)
			assert.Equal(1, messagePosts)
			assert.Positive(receiptReads)
			if tc.wantDelivered {
				require.NoError(err)
				assert.True(out.MessageDelivered)
				return
			}
			var daemonErr *daemonError
			require.ErrorAs(err, &daemonErr)
			assert.True(daemonErr.Ambiguous)
			assert.Equal(tc.wantState, daemonErr.Details["initial_message_state"])
		})
	}
}

func TestSpawnWorkspaceWithAgentDoesNotRetryAmbiguousWorkspaceOrRuntimeMutation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failRuntime bool
		failedStage string
	}{
		{name: "workspace create", failedStage: "workspace_created"},
		{name: "runtime launch", failRuntime: true, failedStage: "runtime_launched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			workspaceCreates := 0
			runtimeLaunches := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
				writeAgentTargetSettings(w, true)
			})
			mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
			})
			mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
				workspaceCreates++
				body := `{"id":"ws-ambiguous","status":"ready","created":true}`
				if !tc.failRuntime {
					body += ` {}`
				}
				writeJSONResponse(w, body)
			})
			if tc.failRuntime {
				mux.HandleFunc("/api/v1/workspaces/ws-ambiguous", func(w http.ResponseWriter, _ *http.Request) {
					writeJSONResponse(w, `{"id":"ws-ambiguous","status":"ready"}`)
				})
				mux.HandleFunc("/api/v1/workspaces/ws-ambiguous/runtime/sessions", func(w http.ResponseWriter, _ *http.Request) {
					runtimeLaunches++
					writeJSONResponse(w, `{"key":"runtime-ambiguous","target_key":"codex","status":"running"} {}`)
				})
			}
			s := newMCPTestServer(t, mux)

			_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))
			var daemonErr *daemonError
			require.ErrorAs(err, &daemonErr)
			assert.True(daemonErr.Ambiguous)
			assert.Equal(tc.failedStage, daemonErr.Details["failed_stage"])
			assert.Equal(1, workspaceCreates)
			if tc.failRuntime {
				assert.Equal(1, runtimeLaunches)
			}
		})
	}
}

func TestSpawnWorkspaceWithAgentReportsRuntimeExitBeforeHookSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	messagePosts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":{"id":"ws-exit","status":"ready"}}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-exit", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"ws-exit","status":"ready"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-exit/runtime/sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"key":"runtime-exit","target_key":"codex","status":"running","created_at":"2026-08-07T15:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-exit/agent-sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-exit/runtime", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[{"key":"runtime-exit","status":"error","exit_code":1}]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/ws-exit/runtime/sessions/runtime-exit/initial-message", func(w http.ResponseWriter, _ *http.Request) {
		messagePosts++
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := newMCPTestServer(t, mux)

	_, err := s.spawnWorkspaceWithAgent(t.Context(), prSpawnInput("start"))
	var daemonErr *daemonError
	require.ErrorAs(err, &daemonErr)
	assert.Equal("agent_handoff_failed", daemonErr.Kind)
	assert.Equal("coding_session_observed", daemonErr.Details["failed_stage"])
	assert.Equal("runtime-exit", daemonErr.Details["runtime_session_key"])
	assert.Contains(daemonErr.Message, "runtime exited")
	assert.Equal(0, messagePosts)
}

func registerSuccessfulAgentHandoff(
	t *testing.T,
	mux *http.ServeMux,
	workspaceID string,
	runtimeKey string,
	codingSessionID string,
) {
	t.Helper()
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"`+workspaceID+`","status":"ready"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID+"/runtime/sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"key":"`+runtimeKey+`","target_key":"codex","status":"running","created_at":"2026-08-07T15:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID+"/agent-sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[{"agent":"codex","session_id":"`+codingSessionID+`","runtime_session_key":"`+runtimeKey+`","target_key":"codex","state":"working","updated_at":"2026-08-07T15:00:01Z"}]}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID+"/runtime/sessions/"+runtimeKey+"/initial-message", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"agent":"codex","session_id":"`+codingSessionID+`","state":"delivered","message_bytes":12,"delivered_at":"2026-08-07T15:00:02Z"}`)
	})
}

func successfulPRHandoffMuxWithoutMessage(t *testing.T, workspaceID, runtimeKey, codingSessionID string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeAgentTargetSettings(w, true)
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"merge_request":{"Number":42},"workspace":null}`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"`+workspaceID+`","status":"ready","created":true}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"id":"`+workspaceID+`","status":"ready"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID+"/runtime/sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"key":"`+runtimeKey+`","target_key":"codex","status":"running","created_at":"2026-08-07T15:00:00Z"}`)
	})
	mux.HandleFunc("/api/v1/workspaces/"+workspaceID+"/agent-sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(w, `{"sessions":[{"agent":"codex","session_id":"`+codingSessionID+`","runtime_session_key":"`+runtimeKey+`","target_key":"codex","state":"working","updated_at":"2026-08-07T15:00:01Z"}]}`)
	})
	return mux
}

func prSpawnInput(message string) spawnWorkspaceWithAgentInput {
	return spawnWorkspaceWithAgentInput{
		Source: workspaceSourceInput{Type: "item", Item: &itemRefInput{
			Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42,
		}},
		AgentTarget: "codex", InitialMessage: message, Timeout: "2s",
	}
}

func writeAgentTargetSettings(w http.ResponseWriter, available bool) {
	writeJSONResponse(w, `{"launch_targets":[{"key":"codex","label":"Codex","kind":"agent","source":"config","available":`+
		map[bool]string{true: "true", false: "false"}[available]+`}]}`)
}

func writeJSONResponse(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
