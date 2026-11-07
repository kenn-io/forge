package agentprocess

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCurrentFromSkipsOneHookShell(t *testing.T) {
	processes := map[int]processInfo{
		10: {parent: 9, name: "sh", start: "hook-shell"},
		9:  {parent: 8, name: "claude", start: "agent"},
		8:  {parent: 1, name: "zsh", start: "terminal"},
		7:  {parent: 8, name: "-zsh", start: "login-terminal"},
	}
	read := func(pid int) (processInfo, bool) {
		info, ok := processes[pid]
		return info, ok
	}
	assert := assert.New(t)
	assert.Equal(Identity{PID: 9, Start: "agent"}, currentFrom(10, read), "the hook ran through a shell")
	assert.Equal(Identity{PID: 9, Start: "agent"}, currentFrom(9, read), "the agent ran the hook directly")
	assert.Equal(Identity{PID: 8, Start: "terminal"}, currentFrom(7, read), "only one shell is skipped")
	assert.Equal(Identity{}, currentFrom(404, read), "an unreadable parent is unknown")
}

func TestGoneNeedsProofTheProcessEnded(t *testing.T) {
	assert := assert.New(t)
	assert.False(Identity{}.Gone(), "an unknown identity is not proof")
	assert.False(Identity{PID: os.Getpid()}.Gone(), "an identity with no start time is not proof")
	self, ok := readProcess(os.Getpid())
	if !ok {
		t.Skip("this platform cannot read process identity")
	}
	assert.False(Identity{PID: os.Getpid(), Start: self.start}.Gone(), "this process still runs")
	assert.True(Identity{PID: os.Getpid(), Start: self.start + "1"}.Gone(), "a reused PID is another process")
	assert.True(Identity{PID: 1 << 30, Start: "1"}.Gone(), "no process has this PID")
}
