package localruntime

import (
	"encoding/json/v2"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
)

// This is the pre-protocol owner wire response, including the missing cursor
// and entry identity fields that caused the browser to reject the transcript.
type OldACPUpdate struct {
	Revision uint64
	State    []byte
	Exited   bool
	ExitCode int
}
type OldACPOwner struct {
	listener net.Listener
	state    []byte
	parks    atomic.Int32
}

func (o *OldACPOwner) Snapshot(_ struct{}, reply *OldACPUpdate) error {
	*reply = OldACPUpdate{Revision: 1, State: o.state}
	return nil
}

func (o *OldACPOwner) Park(_ struct{}, _ *struct{}) error {
	o.parks.Add(1)
	return o.listener.Close()
}

func TestRestoreIncompatibleACPOwner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reloadable bool
		state      string
		wantError  string
	}{
		{name: "resumes saved conversation", reloadable: true},
		{name: "keeps active turn", state: `{"busy":true}`, reloadable: true, wantError: "active operation"},
		{name: "keeps configuring owner", state: `{"configuring":true}`, reloadable: true, wantError: "active operation"},
		{name: "keeps pending permission", state: `{"permissions":[{}]}`, reloadable: true, wantError: "compatible Forge version"},
		{name: "keeps pending elicitation", state: `{"elicitations":[{}]}`, reloadable: true, wantError: "compatible Forge version"},
		{name: "keeps queued prompt", state: `{"queue":[{}]}`, reloadable: true, wantError: "compatible Forge version"},
		{name: "keeps nonresumable owner", wantError: "does not confirm native session resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
			t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
			dir := t.TempDir()
			t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
			executable, err := os.Executable()
			require.NoError(t, err)
			command := []string{executable, "-test.run=^TestACPStdioHelper$"}
			manager := newACPTestManager(t, Options{ACPSessionsDir: filepath.Join(dir, "acp"), Targets: ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: command}}, nil, nil)})
			key := "workspace-chat"
			paths, err := ptyowner.NewSessionPaths(manager.acpSessionsDir, key)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
			cfg, err := json.Marshal(acpOwnerConfig{Info: SessionInfo{WorkspaceID: "workspace", Key: key}, Command: command, CWD: dir})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(paths.Dir, "config.json"), cfg, 0o600))
			saved := []byte(`{"SessionID":"fixture-session","LoadSession":true,"State":{"messages":[{"role":"user","text":"remember"},{"role":"assistant","text":"Saved answer"}]}}`)
			if !tc.reloadable {
				saved = []byte(`{"SessionID":"fixture-session","State":{"messages":[{"role":"user","text":"remember"}]}}`)
			}
			require.NoError(t, os.WriteFile(manager.acpSessionPath(key), saved, 0o600))
			require.NoError(t, os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", paths.Socket)
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			state := []byte(`{"messages":[{"role":"user","text":"remember"}],"busy":false,"connected":true}`)
			if tc.state != "" {
				state = []byte(tc.state)
			}
			old := &OldACPOwner{listener: listener, state: state}
			server := rpc.NewServer()
			require.NoError(t, server.RegisterName("ACP", old))
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					go server.ServeConn(conn)
				}
			}()
			err = manager.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: key, TargetKey: "chat", Kind: LaunchTargetACP, CWD: dir}})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Zero(t, old.parks.Load())
				data, readErr := os.ReadFile(manager.acpSessionPath(key))
				require.NoError(t, readErr)
				assert.Equal(t, saved, data)
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, old.parks.Load())
			chat, err := manager.ACP("workspace", key)
			require.NoError(t, err)
			data, err := chat.Snapshot()
			require.NoError(t, err)
			var restored ACPState
			require.NoError(t, json.Unmarshal(data, &restored))
			require.Len(t, restored.Messages, 2)
			assert.Equal(t, "remember", restored.Messages[0].Text)
			assert.Equal(t, "Saved answer", restored.Messages[1].Text)
			assert.NotEmpty(t, restored.Messages[0].ID)
			assert.NotEmpty(t, restored.LastMessageID)
			calls, err := os.ReadFile(filepath.Join(dir, "sessions"))
			require.NoError(t, err)
			assert.Equal(t, "session/load\n", string(calls), "must resume the native conversation, never create a new one")
		})
	}
}

func TestReloadableChatDoesNotLoseNativeContext(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	_, err = startACPSession(t.Context(), []string{executable, "-test.run=^TestACPStdioHelper$"}, t.TempDir(), nil, nil, nil, &acpSavedSession{SessionID: "fixture-session", LoadSession: true}, nil)
	require.ErrorIs(t, err, ErrACPCannotReload)
}
