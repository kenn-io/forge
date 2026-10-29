//go:build linux

package localruntime

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
)

const liveACPTestsEnv = "KENN_FORGE_LIVE_ACP_TESTS"

// TestLiveACPSharedHost runs real agents on a shared host. Each turn costs
// real model usage, so it runs only on request, never in CI:
//
//	KENN_FORGE_LIVE_ACP_TESTS=1 KENN_FORGE_LIVE_ACP_AGENTS='claude-agent-acp;omp acp' \
//	  go test ./internal/workspace/localruntime -run TestLiveACPSharedHost
//
// Agents are separated by semicolons and must be ones acpSharedAgents lists.
func TestLiveACPSharedHost(t *testing.T) {
	if os.Getenv(liveACPTestsEnv) != "1" {
		t.Skipf("set %s=1 and KENN_FORGE_LIVE_ACP_AGENTS to run real agents", liveACPTestsEnv)
	}
	for agent := range strings.SplitSeq(os.Getenv("KENN_FORGE_LIVE_ACP_AGENTS"), ";") {
		command := strings.Fields(agent)
		if len(command) == 0 {
			continue
		}
		t.Run(filepath.Base(command[0]), func(t *testing.T) { liveACPSharedHost(t, command) })
	}
}

func liveACPSharedHost(t *testing.T, command []string) {
	t.Setenv("KENN_FORGE_ACP_HOST_FIXTURE", "1")
	dir := t.TempDir()
	markers := map[string]string{"alpha": "alpha-7731", "bravo": "bravo-2290"}
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 8)
	manager := newACPTestManager(t, Options{
		ACPSessionsDir:   filepath.Join(dir, "state", "acp"),
		AgentActivityDir: filepath.Join(dir, "activity"),
		ShareACPAgents:   true,
		ACPHostCommand:   []string{executable, "-test.run=^TestACPHostHelper$", "--"},
		OnSessionExit:    func(info SessionInfo) { exits <- info },
		Targets:          ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", DisableForgeTools: true, Command: command}}, nil, nil),
	})
	chats := make(map[string]SessionInfo)
	worktrees := make(map[string]string)
	for name, marker := range markers {
		worktree := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(worktree, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(worktree, "MARKER.txt"), []byte(marker+"\n"), 0o600))
		info, err := manager.Launch(t.Context(), "workspace", worktree, "chat")
		require.NoError(t, err)
		chats[name] = info
		worktrees[name] = worktree
	}
	hosts := liveACPHosts(t, dir)
	require.Len(t, hosts, 1, "both chats share one host")

	// Both turns run at once, each in its own worktree.
	ask := "Read the file MARKER.txt in your current working directory and reply with only its exact contents. Do not modify anything."
	before := make(map[string]int)
	for name, info := range chats {
		chat, err := manager.ACP("workspace", info.Key)
		require.NoError(t, err)
		before[name] = len(publishedACPState(t, chat).Messages)
		require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: ask, ID: name + "-marker"}))
	}
	for name, marker := range markers {
		assert.Contains(t, liveACPAnswer(t, manager, chats[name], before[name]), marker)
	}

	// Stopping one chat leaves the other running on the same agent.
	require.NoError(t, manager.Stop(t.Context(), "workspace", chats["alpha"].Key))
	assert.Contains(t, liveACPAsk(t, manager, chats["bravo"], "Reply with just the word: still-here"), "still-here")

	// Losing the host parks the chat; it reloads with its context on a new host.
	host, err := os.FindProcess(hosts[0])
	require.NoError(t, err)
	require.NoError(t, host.Kill())
	require.Eventually(t, func() bool {
		select {
		case info := <-exits:
			return info.Key == chats["bravo"].Key && info.Status == SessionStatusParked
		default:
			return false
		}
	}, time.Minute, 50*time.Millisecond)
	require.Eventually(t, func() bool { return restoreACPChat(manager, chats["bravo"], worktrees["bravo"]) == nil }, time.Minute, 250*time.Millisecond)
	assert.Contains(t, liveACPAsk(t, manager, chats["bravo"], "What were the exact contents of MARKER.txt that you reported earlier? Reply with only that."), markers["bravo"])
}

// liveACPHosts lists the host processes serving state under dir.
func liveACPHosts(t *testing.T, dir string) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	require.NoError(t, err)
	var hosts []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && strings.Contains(string(cmdline), "TestACPHostHelper") && strings.Contains(string(cmdline), dir) {
			hosts = append(hosts, pid)
		}
	}
	return hosts
}

func liveACPAsk(t *testing.T, manager *Manager, info SessionInfo, text string) string {
	t.Helper()
	chat, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	before := len(publishedACPState(t, chat).Messages)
	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: text}))
	return liveACPAnswer(t, manager, info, before)
}

// liveACPAnswer waits for the turn after message index before to finish and
// returns the agent's last message.
func liveACPAnswer(t *testing.T, manager *Manager, info SessionInfo, before int) string {
	t.Helper()
	chat, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	var state ACPState
	require.Eventually(t, func() bool {
		state = publishedACPState(t, chat)
		return !state.Busy && len(state.Messages) > before+1
	}, 3*time.Minute, 250*time.Millisecond)
	return state.Messages[len(state.Messages)-1].Text
}
