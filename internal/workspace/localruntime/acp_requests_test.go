package localruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

// requestChat is an ACP chat on the stdio fixture whose owner can be killed
// and restored from its saved session.
type requestChat struct {
	t       *testing.T
	dir     string
	owner   *ptyowner.Client
	options Options
	manager *Manager
	info    SessionInfo
	chat    ACPChat
}

func launchRequestChat(t *testing.T) *requestChat {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	options := Options{
		ACPSessionsDir:  filepath.Join(dir, "acp"),
		PtyOwnerRuntime: ptyownerruntime.New(owner, nil),
		Targets: ResolveLaunchTargets([]config.Agent{{
			Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"},
		}}, nil, nil),
	}
	chat := &requestChat{t: t, dir: dir, owner: owner, options: options}
	chat.manager = newACPTestManager(t, options)
	chat.info, err = chat.manager.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	chat.chat, err = chat.manager.ACP("workspace", chat.info.Key)
	require.NoError(t, err)
	return chat
}

func (c *requestChat) state() ACPState {
	c.t.Helper()
	data, err := c.chat.Snapshot()
	require.NoError(c.t, err)
	var state ACPState
	require.NoError(c.t, json.Unmarshal(data, &state))
	return state
}

func (c *requestChat) awaitState(ready func(ACPState) bool) ACPState {
	c.t.Helper()
	var state ACPState
	require.Eventually(c.t, func() bool {
		data, err := c.chat.Snapshot()
		return err == nil && json.Unmarshal(data, &state) == nil && ready(state)
	}, 5*time.Second, 10*time.Millisecond)
	return state
}

func (c *requestChat) awaitIdle() ACPState {
	c.t.Helper()
	return c.awaitState(func(s ACPState) bool { return !s.Busy && len(s.Messages) > 0 })
}

// restart kills the agent, which ends the owner, and restores the chat in a
// replacement owner from the saved session.
func (c *requestChat) restart() {
	c.t.Helper()
	c.manager.Shutdown()
	pidText, err := os.ReadFile(filepath.Join(c.dir, "pid"))
	require.NoError(c.t, err)
	pid, err := strconv.Atoi(string(pidText))
	require.NoError(c.t, err)
	process, err := os.FindProcess(pid)
	require.NoError(c.t, err)
	require.NoError(c.t, process.Kill())
	require.Eventually(c.t, func() bool { return !c.owner.HasState(c.info.Key) }, 10*time.Second, 10*time.Millisecond)
	c.manager = newACPTestManager(c.t, c.options)
	require.NoError(c.t, c.manager.RestoreRuntimeSessions(c.t.Context(), []RestoredRuntimeSession{{
		WorkspaceID: "workspace", SessionKey: c.info.Key, TargetKey: "chat", Kind: LaunchTargetACP, CWD: c.dir, CreatedAt: c.info.CreatedAt,
	}}))
	c.chat, err = c.manager.ACP("workspace", c.info.Key)
	require.NoError(c.t, err)
}

func (c *requestChat) fixtureResponses() []string {
	c.t.Helper()
	data, err := os.ReadFile(filepath.Join(c.dir, "responses"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(c.t, err)
	return strings.Fields(string(data))
}

func (c *requestChat) askPermission(submission string) ACPPermission {
	c.t.Helper()
	require.NoError(c.t, c.chat.Command(ACPCommand{Type: "prompt", Text: "permission", ID: submission}))
	return c.awaitState(func(s ACPState) bool { return len(s.Permissions) == 1 }).Permissions[0]
}

func TestACPRuntimeGenerationChangesOnEveryOwnerStart(t *testing.T) {
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "remember", ID: "first"}))
	first := c.awaitIdle().RuntimeGeneration
	require.Len(t, first, 12)

	c.restart()
	second := c.state().RuntimeGeneration
	require.Len(t, second, 12)
	assert.NotEqual(t, first, second, "a saved generation must never be restored")

	permission := c.askPermission("second")
	assert.Equal(t, second+"-p1", permission.ID)
}

func TestACPRequestIDsCarryTheGenerationAndKind(t *testing.T) {
	c := launchRequestChat(t)
	generation := c.state().RuntimeGeneration
	permission := c.askPermission("permission")
	assert.Equal(t, generation+"-p1", permission.ID)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
	c.awaitIdle()

	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "elicit", ID: "elicit"}))
	elicitation := c.awaitState(func(s ACPState) bool { return len(s.Elicitations) == 1 }).Elicitations[0]
	assert.Equal(t, generation+"-e2", elicitation.ID)
}

func TestACPPermissionAnswerReachesTheAgentOnce(t *testing.T) {
	c := launchRequestChat(t)
	permission := c.askPermission("permission")
	answer := ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}
	require.NoError(t, c.chat.Command(answer))
	c.awaitIdle()
	require.NoError(t, c.chat.Command(answer), "a retry of the same answer is idempotent")

	require.ErrorContains(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "deny"}), "request was already answered")
	assert.Equal(t, []string{"permission:allow"}, c.fixtureResponses())
	assert.Empty(t, c.state().Answered, "the ledger never leaves the owner")
	answered := savedACPSession(t, c.manager, c.info.Key).State.Answered
	require.Len(t, answered, 1, "the saved session keeps the ledger for a replacement owner")
	assert.Equal(t, ACPAnsweredRequest{ID: permission.ID, Answer: "allow", AnswerAt: answered[0].AnswerAt}, answered[0])
	assert.NotEmpty(t, answered[0].AnswerAt)
}

func TestACPUnknownRequestInTheCurrentGenerationIsNotPending(t *testing.T) {
	c := launchRequestChat(t)
	generation := c.state().RuntimeGeneration
	require.ErrorContains(t, c.chat.Command(ACPCommand{Type: "permission", ID: generation + "-p99", OptionID: "allow"}), "no longer pending")
	require.ErrorContains(t, c.chat.Command(ACPCommand{Type: "elicitation", ID: generation + "-e99", Action: "decline"}), "no longer pending")
}

func TestACPElicitationAnswersCompareFieldValues(t *testing.T) {
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "elicit", ID: "elicit"}))
	elicitation := c.awaitState(func(s ACPState) bool { return len(s.Elicitations) == 1 }).Elicitations[0]
	answer := ACPCommand{Type: "elicitation", ID: elicitation.ID, Action: "accept", Content: jsontext.Value(`{"choice":"b"}`)}
	require.NoError(t, c.chat.Command(answer))
	c.awaitIdle()

	require.NoError(t, c.chat.Command(answer))
	reordered := answer
	reordered.Content = jsontext.Value(`{ "choice" : "b" }`)
	require.NoError(t, c.chat.Command(reordered), "formatting does not change the answer")
	different := answer
	different.Content = jsontext.Value(`{"choice":"a"}`)
	require.ErrorContains(t, c.chat.Command(different), "request was already answered")
	declined := ACPCommand{Type: "elicitation", ID: elicitation.ID, Action: "decline"}
	require.ErrorContains(t, c.chat.Command(declined), "request was already answered")
	assert.Equal(t, []string{"elicitation:b"}, c.fixtureResponses())
}

func TestACPAnsweredRequestsKeepTheNewestHundred(t *testing.T) {
	agent := newDetachedACP(t)
	agent.mu.Lock()
	for i := range acpAnsweredLimit + 5 {
		agent.recordAnswerLocked("G-p"+strconv.Itoa(i), "allow")
	}
	answered := agent.state.Answered
	agent.mu.Unlock()
	require.Len(t, answered, acpAnsweredLimit)
	assert.Equal(t, "G-p5", answered[0].ID)
	assert.Equal(t, "G-p104", answered[len(answered)-1].ID)
}

func TestACPStaleRequestFromAnotherGeneration(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.RuntimeGeneration = "CURRENT"
	for _, command := range []ACPCommand{
		{Type: "permission", ID: "EARLIER-p1", OptionID: "allow"},
		{Type: "elicitation", ID: "EARLIER-e1", Action: "decline"},
	} {
		require.ErrorIs(t, agent.Command(command), ErrACPStaleRequest, command.Type)
	}
}

func TestACPRequestsFromAnEarlierOwnerAfterRestart(t *testing.T) {
	c := launchRequestChat(t)
	permission := c.askPermission("permission")
	require.NoError(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
	c.awaitIdle()
	earlier := strings.TrimSuffix(permission.ID, "-p1")

	c.restart()
	require.NotEqual(t, earlier, c.state().RuntimeGeneration)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}),
		"a retry after restart still gets the idempotent result")
	require.ErrorContains(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "deny"}), "request was already answered")

	for _, command := range []ACPCommand{
		{Type: "permission", ID: earlier + "-p9", OptionID: "allow"},
		{Type: "elicitation", ID: earlier + "-e9", Action: "decline"},
	} {
		require.ErrorIs(t, c.chat.Command(command), ErrACPStaleRequest, command.Type)
	}
	assert.Equal(t, []string{"permission:allow"}, c.fixtureResponses(), "nothing reached the restarted agent")
}

func TestElicitationAnswerKeyKeepsFormValuesOutOfTheLedger(t *testing.T) {
	key, err := elicitationAnswerKey("accept", map[string]any{"token": "private-value", "count": 2})
	require.NoError(t, err)
	assert.NotContains(t, key, "private-value")

	same, err := elicitationAnswerKey("accept", map[string]any{"count": 2, "token": "private-value"})
	require.NoError(t, err)
	assert.Equal(t, key, same)

	other, err := elicitationAnswerKey("accept", map[string]any{"token": "other-value", "count": 2})
	require.NoError(t, err)
	assert.NotEqual(t, key, other)
}

// Outcomes a coordinator must settle cross the owner RPC as codes, so the
// daemon can tell them from malformed input.
func TestACPSettledOutcomesCrossTheOwnerAsCodes(t *testing.T) {
	c := launchRequestChat(t)
	generation := c.state().RuntimeGeneration
	err := c.chat.Command(ACPCommand{Type: "permission", ID: generation + "-p99", OptionID: "allow"})
	require.ErrorIs(t, err, errACPNotPending)
	assert.Equal(t, "not_pending", ACPErrorCode(err))
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "elicitation", ID: generation + "-e99", Action: "decline"}), errACPNotPending)

	permission := c.askPermission("permission")
	require.NoError(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
	c.awaitIdle()
	err = c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "deny"})
	require.ErrorIs(t, err, errACPAlreadyAnswered)
	assert.Equal(t, "already_answered", ACPErrorCode(err))

	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "running"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "later", ID: "queued"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	err = c.chat.Command(ACPCommand{Type: "prompt", Text: "later", ID: "queued"})
	require.ErrorIs(t, err, errACPSubmissionWithdrawn)
	assert.Equal(t, "withdrawn", ACPErrorCode(err))
}

// unsavableACP is a detached chat whose session file can never be written:
// a directory cannot be replaced by it.
func unsavableACP(t *testing.T) *ACP {
	t.Helper()
	agent := newDetachedACP(t)
	agent.recordPath = t.TempDir()
	return agent
}

// An answer the agent was sent has taken effect, so a failed save after it
// must not report the answer as refused. The chat shows the save error, and
// a retry still gets the same result.
func TestACPAnswersSucceedWhenTheSaveAfterThemFails(t *testing.T) {
	t.Run("permission", func(t *testing.T) {
		agent := unsavableACP(t)
		outcome := make(chan acpsdk.RequestPermissionResponse, 1)
		go func() {
			response, _ := agent.RequestPermission(t.Context(), acpsdk.RequestPermissionRequest{
				SessionId: "session",
				ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "build"},
				Options:   []acpsdk.PermissionOption{{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
			})
			outcome <- response
		}()
		require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Permissions) == 1 }, 5*time.Second, 10*time.Millisecond)
		answer := ACPCommand{Type: "permission", ID: publishedACPState(t, agent).Permissions[0].ID, OptionID: "allow"}

		require.NoError(t, agent.Command(answer))
		response := <-outcome
		require.NotNil(t, response.Outcome.Selected)
		assert.Equal(t, acpsdk.PermissionOptionId("allow"), response.Outcome.Selected.OptionId)
		assert.Contains(t, publishedACPState(t, agent).Error, "save chat after answering a permission")
		require.NoError(t, agent.Command(answer), "a retry gets the same result")
	})
	t.Run("elicitation", func(t *testing.T) {
		agent := unsavableACP(t)
		outcome := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
		go func() {
			response, _ := agent.UnstableCreateElicitation(t.Context(), acpsdk.UnstableCreateElicitationRequest{
				Form: &acpsdk.UnstableCreateElicitationForm{Message: "Pick one"},
			})
			outcome <- response
		}()
		require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Elicitations) == 1 }, 5*time.Second, 10*time.Millisecond)
		answer := ACPCommand{Type: "elicitation", ID: publishedACPState(t, agent).Elicitations[0].ID, Action: "decline"}

		require.NoError(t, agent.Command(answer))
		assert.NotNil(t, (<-outcome).Decline)
		assert.Contains(t, publishedACPState(t, agent).Error, "save chat after answering an elicitation")
		require.NoError(t, agent.Command(answer), "a retry gets the same result")
	})
}
