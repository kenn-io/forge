package ptyowner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerAttachInputAndReplay(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("in-process PTY owner test requires a host PTY")
	}

	root := t.TempDir()
	ctx := t.Context()

	done := make(chan error, 1)
	go func() {
		done <- RunOwner(ctx, Options{
			Root:    root,
			Session: "middleman-test",
			Cwd:     t.TempDir(),
			Command: []string{"sh", "-c", "printf ready; while IFS= read -r line; do echo got:$line; done"},
		})
	}()

	client := Client{Root: root}
	waitOwnerReady(t, done, client, "middleman-test")

	first, err := client.Attach(context.Background(), "middleman-test", 120, 30)
	require.NoError(err)
	defer first.Close()

	require.Contains(readUntil(t, first.Output, "ready"), "ready")
	require.NoError(first.Write([]byte("hello\n")))
	require.Contains(readUntil(t, first.Output, "got:hello"), "got:hello")
	first.Close()

	second, err := client.Attach(context.Background(), "middleman-test", 100, 20)
	require.NoError(err)
	defer second.Close()

	assert.Contains(readUntil(t, second.Output, "got:hello"), "got:hello")
	require.NoError(second.Resize(90, 25))
	require.NoError(client.Stop(context.Background(), "middleman-test"))

	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(2 * time.Second):
		require.Fail("owner did not stop")
	}
}

func TestOwnerStopWhileRunOwnerReturns(t *testing.T) {
	require := require.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("in-process PTY owner test requires a host PTY")
	}

	root := t.TempDir()
	paths, err := NewSessionPaths(root, "middleman-stop-race")
	require.NoError(err)
	done := make(chan error, 1)
	go func() {
		done <- RunOwner(t.Context(), Options{
			Root:    root,
			Session: "middleman-stop-race",
			Cwd:     t.TempDir(),
			Command: []string{"sh", "-c", "while :; do sleep 0.05; done"},
		})
	}()

	client := Client{Root: root}
	waitOwnerReady(t, done, client, "middleman-stop-race")

	require.NoError(client.Stop(context.Background(), "middleman-stop-race"))
	_, err = os.Stat(paths.Dir)
	require.True(os.IsNotExist(err))
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(2 * time.Second):
		require.Fail("owner did not stop")
	}
}

func TestTerminalTitleParserTracksOSCSequences(t *testing.T) {
	assert := Assert.New(t)
	var parser terminalTitleParser

	title, ok := parser.Update([]byte("before\x1b]0;busy title\x07after"))
	assert.True(ok)
	assert.Equal("busy title", title)

	title, ok = parser.Update([]byte("\x1b]2;split"))
	assert.False(ok)
	assert.Empty(title)

	title, ok = parser.Update([]byte(" title\x1b\\tail"))
	assert.True(ok)
	assert.Equal("split title", title)

	title, ok = parser.Update([]byte("\x1b"))
	assert.False(ok)
	assert.Empty(title)

	title, ok = parser.Update([]byte("]0;edge title\x07"))
	assert.True(ok)
	assert.Equal("edge title", title)
}

func TestOwnerRejectsOversizedUnauthenticatedRequest(t *testing.T) {
	require := require.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("in-process PTY owner test requires a host PTY")
	}

	root := t.TempDir()
	session := "middleman-oversized-request"
	done := make(chan error, 1)
	go func() {
		done <- RunOwner(t.Context(), Options{
			Root:    root,
			Session: session,
			Cwd:     t.TempDir(),
			Command: []string{"sh", "-c", "while :; do sleep 0.05; done"},
		})
	}()

	client := Client{Root: root}
	require.Eventually(func() bool {
		return client.Ping(context.Background(), session) == nil
	}, 2*time.Second, 20*time.Millisecond)

	paths, err := NewSessionPaths(root, session)
	require.NoError(err)
	state, err := readState(paths)
	require.NoError(err)
	network, addr, err := ownerDialTarget(state.Addr)
	require.NoError(err)
	conn, err := net.Dial(network, addr)
	require.NoError(err)
	_, err = conn.Write([]byte(
		`{"type":"status","token":"wrong","data":"` +
			strings.Repeat("A", maxOwnerFirstRequestSize) +
			`"}` + "\n",
	))
	require.NoError(err)
	require.NoError(conn.SetReadDeadline(time.Now().Add(time.Second)))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	require.Error(err)
	require.NoError(conn.Close())

	require.NoError(client.Ping(context.Background(), session))
	require.NoError(client.Stop(context.Background(), session))
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(2 * time.Second):
		require.Fail("owner did not stop")
	}
}

func TestOwnerHelperEnvironmentStripsCredentials(t *testing.T) {
	out := ownerHelperEnvironment([]string{
		"PATH=/usr/bin",
		"MIDDLEMAN_GITHUB_TOKEN=secret-1",
		"GITHUB_TOKEN=secret-2",
		"GH_TOKEN_WORK=secret-3",
		"KEEP=value",
	})

	require.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"KEEP=value",
	}, out)
}

func TestClientOwnerHelperEnvironmentStripsConfiguredVariables(t *testing.T) {
	client := Client{StripEnvVars: []string{"WORKSPACE_TOKEN"}}
	out := client.ownerHelperEnvironment([]string{
		"PATH=/usr/bin",
		"WORKSPACE_TOKEN=secret",
		"KEEP=value",
	})

	require.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"KEEP=value",
	}, out)
}

func TestClientStopTreatsStaleOwnerStateAsAbsent(t *testing.T) {
	require := require.New(t)

	root := t.TempDir()
	paths, err := NewSessionPaths(root, "middleman-stale")
	require.NoError(err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := listener.Addr().String()
	require.NoError(listener.Close())
	require.NoError(writeState(paths, ownerState{
		Session: "middleman-stale",
		Addr:    addr,
		Token:   "token",
		Cwd:     t.TempDir(),
	}))

	err = (&Client{Root: root}).Stop(context.Background(), "middleman-stale")

	require.NoError(err)
	_, err = os.Stat(paths.Dir)
	require.True(os.IsNotExist(err))
}

func TestClientEnsurePreservesStateOnContextCancellation(t *testing.T) {
	require := require.New(t)

	root := t.TempDir()
	paths, err := NewSessionPaths(root, "middleman-canceled-ensure")
	require.NoError(err)
	require.NoError(writeState(paths, ownerState{
		Session: "middleman-canceled-ensure",
		Addr:    "127.0.0.1:1",
		Token:   "token",
		Cwd:     t.TempDir(),
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = (&Client{Root: root}).Ensure(ctx, "middleman-canceled-ensure", t.TempDir())

	require.Error(err)
	_, err = os.Stat(paths.Dir)
	require.NoError(err)
}

func TestClientEnsureSerializesConcurrentStarts(t *testing.T) {
	require := require.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("in-process PTY owner test requires a host PTY")
	}

	root := t.TempDir()
	dir := t.TempDir()
	record := filepath.Join(dir, "starts")
	stop := filepath.Join(dir, "stop")
	client := &Client{
		Root:      root,
		InProcess: true,
		Command: []string{
			"sh", "-c",
			"printf start >> \"$MIDDLEMAN_START_RECORD\"; " +
				"while [ ! -f \"$MIDDLEMAN_STOP_FILE\" ]; do sleep 0.05; done",
		},
	}
	t.Setenv("MIDDLEMAN_START_RECORD", record)
	t.Setenv("MIDDLEMAN_STOP_FILE", stop)
	t.Cleanup(func() {
		_ = os.WriteFile(stop, []byte("stop"), 0o644)
		_ = client.Stop(context.Background(), "middleman-concurrent-ensure")
	})

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			<-start
			errs <- client.Ensure(
				context.Background(),
				"middleman-concurrent-ensure",
				dir,
			)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(err)
	}

	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		return err == nil && string(data) == "start"
	}, 2*time.Second, 20*time.Millisecond)
}

func TestClientStopPreservesStateOnContextCancellation(t *testing.T) {
	require := require.New(t)

	root := t.TempDir()
	paths, err := NewSessionPaths(root, "middleman-canceled-stop")
	require.NoError(err)
	require.NoError(writeState(paths, ownerState{
		Session: "middleman-canceled-stop",
		Addr:    "127.0.0.1:1",
		Token:   "token",
		Cwd:     t.TempDir(),
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = (&Client{Root: root}).Stop(ctx, "middleman-canceled-stop")

	require.Error(err)
	_, err = os.Stat(paths.Dir)
	require.NoError(err)
}

func TestClientPingHonorsContextAfterConnect(t *testing.T) {
	require := require.New(t)

	root := t.TempDir()
	paths, err := NewSessionPaths(root, "middleman-silent-owner")
	require.NoError(err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	require.NoError(writeState(paths, ownerState{
		Session: "middleman-silent-owner",
		Addr:    listener.Addr().String(),
		Token:   "token",
		Cwd:     t.TempDir(),
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = (&Client{Root: root}).Ping(ctx, "middleman-silent-owner")

	require.Error(err)
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}

func TestClientOwnerCommandUsesExternalManagerDirectly(t *testing.T) {
	assert := Assert.New(t)

	client := Client{
		Root:        "/tmp/owner-root",
		ExeArgs:     []string{"kept"},
		ManagerPath: "/tmp/middleman-pty-manager",
	}

	exe, args := client.ownerCommand(
		"/tmp/middleman",
		"middleman-test",
		"/tmp/worktree",
		`["sh"]`,
	)

	assert.Equal("/tmp/middleman-pty-manager", exe)
	assert.Equal([]string{
		"kept",
		"-root", "/tmp/owner-root",
		"-session", "middleman-test",
		"-cwd", "/tmp/worktree",
		"-command-json", `["sh"]`,
	}, args)
}

func TestOwnerDialTargetParsesTransportAddresses(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantNetwork string
		wantAddr    string
		wantErr     string
	}{
		{
			name:        "legacy tcp",
			raw:         "127.0.0.1:1234",
			wantNetwork: "tcp",
			wantAddr:    "127.0.0.1:1234",
		},
		{
			name:        "explicit tcp",
			raw:         "tcp://127.0.0.1:1234",
			wantNetwork: "tcp",
			wantAddr:    "127.0.0.1:1234",
		},
		{
			name:        "unix",
			raw:         "unix:///tmp/middleman.sock",
			wantNetwork: "unix",
			wantAddr:    "/tmp/middleman.sock",
		},
		{
			name:    "unsupported scheme",
			raw:     "npipe://middleman/test",
			wantErr: "unsupported pty owner address scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, addr, err := ownerDialTarget(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert := Assert.New(t)
			assert.Equal(tt.wantNetwork, network)
			assert.Equal(tt.wantAddr, addr)
		})
	}
}

func TestClientEnsuresExternalManager(t *testing.T) {
	require := require.New(t)
	managerPath := os.Getenv("MIDDLEMAN_PTY_MANAGER_TEST")
	if managerPath == "" {
		t.Skip("set MIDDLEMAN_PTY_MANAGER_TEST to an external pty manager binary")
	}

	root := t.TempDir()
	client := Client{
		Root:        root,
		ManagerPath: managerPath,
		Command: []string{
			"sh", "-c",
			"printf ready; while IFS= read -r line; do echo got:$line; done",
		},
	}

	require.NoError(client.Ensure(t.Context(), "middleman-rust-test", t.TempDir()))
	t.Cleanup(func() {
		_ = client.Stop(context.Background(), "middleman-rust-test")
	})

	attachment, err := client.Attach(
		context.Background(), "middleman-rust-test", 120, 30,
	)
	require.NoError(err)
	defer attachment.Close()

	require.Contains(readUntil(t, attachment.Output, "ready"), "ready")
	require.NoError(attachment.Write([]byte("hello\r")))
	require.Contains(readUntil(t, attachment.Output, "got:hello"), "got:hello")
}

func TestClientEnsuresExternalManagerWithPowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell PTY manager startup coverage is Windows-specific")
	}
	require := require.New(t)
	managerPath := os.Getenv("MIDDLEMAN_PTY_MANAGER_TEST")
	if managerPath == "" {
		t.Skip("set MIDDLEMAN_PTY_MANAGER_TEST to an external pty manager binary")
	}

	root := filepath.Join(t.TempDir(), strings.Repeat("long-owner-root-", 8))
	require.NoError(os.MkdirAll(root, 0o755))
	client := Client{
		Root:        root,
		ManagerPath: managerPath,
		Command: []string{
			"powershell.exe", "-NoLogo", "-NoProfile", "-WindowStyle", "Hidden", "-NoExit",
		},
	}

	require.NoError(client.Ensure(t.Context(), "middleman-powershell-test", t.TempDir()))
	t.Cleanup(func() {
		_ = client.Stop(context.Background(), "middleman-powershell-test")
	})

	attachment, err := client.Attach(
		context.Background(), "middleman-powershell-test", 120, 30,
	)
	require.NoError(err)
	defer attachment.Close()

	require.NoError(attachment.Write([]byte("Write-Output powershell-ready\r")))
	require.Contains(readUntil(t, attachment.Output, "powershell-ready"), "powershell-ready")
}

func TestClientEnsuresExternalManagerWithGoTestHelper(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows ConPTY coverage for Go test helpers")
	}
	require := require.New(t)
	managerPath := os.Getenv("MIDDLEMAN_PTY_MANAGER_TEST")
	if managerPath == "" {
		t.Skip("set MIDDLEMAN_PTY_MANAGER_TEST to an external pty manager binary")
	}
	t.Setenv("MIDDLEMAN_PTYOWNER_TEST_HELPER", "1")

	root := filepath.Join(t.TempDir(), strings.Repeat("long-owner-root-", 8))
	require.NoError(os.MkdirAll(root, 0o755))
	client := Client{
		Root:        root,
		ManagerPath: managerPath,
		Command: []string{
			os.Args[0],
			"-test.run=TestPtyOwnerEchoHelperProcess",
			"--",
			"echo",
		},
	}

	require.NoError(client.Ensure(t.Context(), "middleman-go-helper-test", t.TempDir()))
	t.Cleanup(func() {
		_ = client.Stop(context.Background(), "middleman-go-helper-test")
	})

	attachment, err := client.Attach(
		context.Background(), "middleman-go-helper-test", 120, 30,
	)
	require.NoError(err)
	defer attachment.Close()

	require.NoError(attachment.Write([]byte("ping\r")))
	require.Contains(readUntil(t, attachment.Output, "echo:ping"), "echo:ping")
}

func TestBoundedOutputBufferRetainsTail(t *testing.T) {
	assert := Assert.New(t)
	buf := newBoundedOutputBuffer(8)

	n, err := buf.Write([]byte("hello "))
	require.NoError(t, err)
	assert.Equal(6, n)
	n, err = buf.Write([]byte("world"))
	require.NoError(t, err)
	assert.Equal(5, n)

	assert.Equal("lo world", buf.String())
}

func TestPtyOwnerEchoHelperProcess(t *testing.T) {
	if os.Getenv("MIDDLEMAN_PTYOWNER_TEST_HELPER") != "1" {
		return
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err == nil {
		fmt.Print("echo:" + line)
	}
	blockPtyOwnerTestHelper()
}

func blockPtyOwnerTestHelper() {
	for {
		time.Sleep(time.Hour)
	}
}

func TestExternalManagerAttachmentWritesUseAttachConnection(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	if runtime.GOOS == "windows" {
		t.Skip("external pty manager uses Unix sockets")
	}

	root := t.TempDir()
	session := "middleman-rust-attach"
	paths, err := NewSessionPaths(root, session)
	require.NoError(err)
	require.NoError(createPrivateDir(paths.Dir))
	if paths.SocketDir != "" {
		require.NoError(createPrivateSocketDir(paths.SocketDir))
	}

	listener, err := net.Listen("unix", paths.Socket)
	require.NoError(err)
	defer listener.Close()

	token := "attach-token"
	require.NoError(writeState(paths, ownerState{
		Session: session,
		Addr:    "unix://" + paths.Socket,
		Token:   token,
		Cwd:     t.TempDir(),
		PID:     os.Getpid(),
	}))

	requests := make(chan Request, 2)
	connections := make(chan struct{}, 2)
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			connections <- struct{}{}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				var req Request
				if decodeErr := json.NewDecoder(reader).Decode(&req); decodeErr != nil {
					return
				}
				requests <- req
				_, _ = conn.Write([]byte(`{"type":"ok","ok":true}` + "\n"))
				if req.Type != RequestAttach {
					return
				}
				_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				var next Request
				if decodeErr := json.NewDecoder(reader).Decode(&next); decodeErr == nil {
					requests <- next
				}
			}(conn)
		}
	}()

	client := Client{
		Root:        root,
		ManagerPath: "/tmp/middleman-pty-manager",
	}
	attachment, err := client.Attach(context.Background(), session, 120, 30)
	require.NoError(err)
	defer attachment.Close()
	require.NoError(attachment.Write([]byte("hello")))

	require.Eventually(func() bool { return len(requests) == 2 }, time.Second, 10*time.Millisecond)
	first := <-requests
	second := <-requests
	assert.Equal(RequestAttach, first.Type)
	assert.Equal(token, first.Token)
	assert.Equal(RequestInput, second.Type)
	assert.Equal(token, second.Token)
	assert.Equal([]byte("hello"), second.Data)
	assert.Len(connections, 1)
}

func readUntil(t *testing.T, output <-chan []byte, needle string) string {
	t.Helper()

	deadline := time.After(2 * time.Second)
	var builder strings.Builder
	for {
		select {
		case chunk, ok := <-output:
			if !ok {
				return builder.String()
			}
			builder.Write(chunk)
			if strings.Contains(builder.String(), needle) {
				return builder.String()
			}
		case <-deadline:
			require.New(t).Failf(
				"timed out waiting for output",
				"wanted %q in %q", needle, builder.String(),
			)
		}
	}
}

func waitOwnerReady(
	t *testing.T,
	done <-chan error,
	client Client,
	session string,
) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var lastErr error
	for {
		if err := client.Ping(context.Background(), session); err == nil {
			return
		} else {
			lastErr = err
		}
		select {
		case err := <-done:
			require.New(t).NoError(err)
			require.New(t).Fail("owner exited before it became ready")
		case <-tick.C:
		case <-deadline:
			require.New(t).NoError(lastErr)
			require.New(t).Fail("owner did not become ready")
		}
	}
}
