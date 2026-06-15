package ptyowner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.kenn.io/middleman/internal/procutil"
)

type Client struct {
	Root         string
	ExePath      string
	ExeArgs      []string
	ManagerPath  string
	Command      []string
	StripEnvVars []string
	InProcess    bool
}

const (
	clientRPCTimeout       = 5 * time.Second
	startLockStaleAfter    = 30 * time.Second
	startLockRetryInterval = 25 * time.Millisecond
	ownerOutputLimit       = 64 * 1024
)

type Attachment struct {
	Output <-chan []byte
	Done   <-chan struct{}

	conn     net.Conn
	enc      *json.Encoder
	writeMu  sync.Mutex
	token    string
	exitCode func() int
	close    func()
}

func NewClient(root string, command []string) *Client {
	return &Client{
		Root:    root,
		Command: append([]string(nil), command...),
	}
}

func (c *Client) Ensure(ctx context.Context, session, cwd string) error {
	if err := c.Ping(ctx, session); err == nil {
		return nil
	} else if !isAbsentOwner(err) {
		return err
	}
	paths, err := NewSessionPaths(c.Root, session)
	if err != nil {
		return err
	}
	unlock, err := acquireStartLock(ctx, paths)
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.Ping(ctx, session); err == nil {
		return nil
	} else if !isAbsentOwner(err) {
		return err
	}
	_ = os.RemoveAll(paths.Dir)
	removeSocketDir(paths)

	exe := c.ExePath
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve middleman executable: %w", err)
		}
	}
	command := append([]string(nil), c.Command...)
	if len(command) == 0 {
		command = defaultShellCommand()
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return err
	}
	if c.InProcess {
		go func() {
			_ = RunOwner(context.Background(), Options{
				Root:         c.Root,
				Session:      session,
				Cwd:          cwd,
				Command:      command,
				StripEnvVars: c.StripEnvVars,
			})
		}()
		return c.waitReady(ctx, session, nil, nil, nil)
	}
	exe, args := c.ownerCommand(exe, session, cwd, string(commandJSON))
	cmd := procutil.Command(exe, args...)
	cmd.Env = c.ownerHelperEnvironment(os.Environ())
	detachCommand(cmd)
	stdout := newBoundedOutputBuffer(ownerOutputLimit)
	stderr := newBoundedOutputBuffer(ownerOutputLimit)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pty owner: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	return c.waitReady(ctx, session, exited, &stdout, &stderr)
}

func acquireStartLock(
	ctx context.Context,
	paths SessionPaths,
) (func(), error) {
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return nil, err
	}
	lockPath := startLockPath(paths)
	for {
		file, err := os.OpenFile(
			lockPath,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if staleStartLock(lockPath) {
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(startLockRetryInterval):
		}
	}
}

func startLockPath(paths SessionPaths) string {
	return filepath.Join(paths.Root, "."+paths.Session+".ensure.lock")
}

func staleStartLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) > startLockStaleAfter
}

func (c *Client) ownerCommand(
	defaultExe string,
	session string,
	cwd string,
	commandJSON string,
) (string, []string) {
	exe := defaultExe
	args := append([]string(nil), c.ExeArgs...)
	if c.ManagerPath == "" {
		args = append(args, "pty-owner")
	} else {
		exe = c.ManagerPath
	}
	args = append(args,
		"-root", c.Root,
		"-session", session,
		"-cwd", cwd,
		"-command-json", commandJSON,
	)
	return exe, args
}

func (c *Client) waitReady(
	ctx context.Context,
	session string,
	exited <-chan error,
	stdout, stderr *boundedOutputBuffer,
) error {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := c.Ping(ctx, session); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			return exitedOwnerError(err, stdout, stderr)
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("pty owner did not become ready: %w", lastErr)
}

func exitedOwnerError(err error, stdout, stderr *boundedOutputBuffer) error {
	msg := ""
	if stderr != nil {
		msg = strings.TrimSpace(stderr.String())
	}
	if msg == "" && stdout != nil {
		msg = strings.TrimSpace(stdout.String())
	}
	if msg == "" {
		msg = "no output"
	}
	if err != nil {
		return fmt.Errorf("pty owner exited before ready: %w: %s", err, msg)
	}
	return fmt.Errorf("pty owner exited before ready: %s", msg)
}

type boundedOutputBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newBoundedOutputBuffer(limit int) boundedOutputBuffer {
	return boundedOutputBuffer{limit: limit}
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	if b == nil || b.limit <= 0 {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *boundedOutputBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func (c *Client) HasState(session string) bool {
	paths, err := NewSessionPaths(c.Root, session)
	if err != nil {
		return false
	}
	_, err = readState(paths)
	return err == nil
}

func (c *Client) Ping(ctx context.Context, session string) error {
	conn, state, err := c.connect(ctx, session)
	if err != nil {
		return err
	}
	defer conn.Close()
	clearDeadline := applyRPCDeadline(ctx, conn)
	defer clearDeadline()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(Request{Type: RequestStatus, Token: state.Token}); err != nil {
		return err
	}
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context, session string) (Status, error) {
	conn, state, err := c.connect(ctx, session)
	if err != nil {
		return Status{}, err
	}
	defer conn.Close()
	clearDeadline := applyRPCDeadline(ctx, conn)
	defer clearDeadline()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(Request{Type: RequestStatus, Token: state.Token}); err != nil {
		return Status{}, err
	}
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return Status{}, err
	}
	if !resp.OK {
		return Status{}, errors.New(resp.Error)
	}
	return Status{
		Output: append([]byte(nil), resp.Output...),
		Title:  resp.Title,
	}, nil
}

func (c *Client) Attach(
	ctx context.Context,
	session string,
	cols int,
	rows int,
) (*Attachment, error) {
	conn, state, err := c.connect(ctx, session)
	if err != nil {
		return nil, err
	}
	clearDeadline := applyRPCDeadline(ctx, conn)
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(Request{
		Type: RequestAttach, Token: state.Token, Cols: cols, Rows: rows,
	}); err != nil {
		clearDeadline()
		conn.Close()
		return nil, err
	}
	var initial Response
	if err := dec.Decode(&initial); err != nil {
		clearDeadline()
		conn.Close()
		return nil, err
	}
	if !initial.OK {
		clearDeadline()
		conn.Close()
		return nil, errors.New(initial.Error)
	}
	clearDeadline()

	output := make(chan []byte, 64)
	done := make(chan struct{})
	var exitMu sync.Mutex
	exitCode := -1
	go func() {
		defer close(done)
		defer close(output)
		for {
			var resp Response
			if err := dec.Decode(&resp); err != nil {
				return
			}
			switch resp.Type {
			case ResponseOutput:
				chunk := append([]byte(nil), resp.Output...)
				select {
				case output <- chunk:
				case <-ctx.Done():
					return
				}
			case ResponseExit:
				if resp.ExitCode != nil {
					exitMu.Lock()
					exitCode = *resp.ExitCode
					exitMu.Unlock()
				}
				return
			}
		}
	}()

	return &Attachment{
		Output: output,
		Done:   done,
		conn:   conn,
		enc:    enc,
		token:  state.Token,
		exitCode: func() int {
			exitMu.Lock()
			defer exitMu.Unlock()
			return exitCode
		},
		close: func() { _ = conn.Close() },
	}, nil
}

func (c *Client) Stop(ctx context.Context, session string) error {
	paths, pathErr := NewSessionPaths(c.Root, session)
	if pathErr != nil {
		return pathErr
	}
	conn, state, err := c.connect(ctx, session)
	if err != nil {
		if isAbsentOwner(err) {
			cleanupAbsentOwner(paths)
			return nil
		}
		return err
	}
	defer conn.Close()
	clearDeadline := applyRPCDeadline(ctx, conn)
	defer clearDeadline()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(Request{Type: RequestStop, Token: state.Token}); err != nil {
		if isAbsentOwner(err) {
			cleanupAbsentOwner(paths)
			return nil
		}
		return err
	}
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		if isAbsentOwner(err) {
			cleanupAbsentOwner(paths)
			return nil
		}
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return c.waitStopped(ctx, session, paths.Dir)
}

func cleanupAbsentOwner(paths SessionPaths) {
	_ = os.Remove(paths.Socket)
	_ = os.RemoveAll(paths.Dir)
	removeSocketDir(paths)
}

func isAbsentOwner(err error) bool {
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, io.EOF) ||
		isStaleOwnerConnection(err)
}

func isStaleOwnerConnection(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		isPlatformStaleOwnerConnection(err)
}

func ownerHelperEnvironment(env []string) []string {
	return sessionEnvironment(env, nil)
}

func (c Client) ownerHelperEnvironment(env []string) []string {
	return sessionEnvironment(env, c.StripEnvVars)
}

func applyRPCDeadline(ctx context.Context, conn net.Conn) func() {
	deadline := time.Now().Add(clientRPCTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() {
		close(done)
		_ = conn.SetDeadline(time.Time{})
	}
}

func (c *Client) waitStopped(ctx context.Context, session, dir string) error {
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := c.Ping(ctx, session); err != nil && isStaleOwnerConnection(err) {
			_ = os.RemoveAll(dir)
			return nil
		} else if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("pty owner did not stop: %w", lastErr)
	}
	return fmt.Errorf("pty owner did not stop")
}

func (c *Client) connect(
	ctx context.Context,
	session string,
) (net.Conn, ownerState, error) {
	paths, err := NewSessionPaths(c.Root, session)
	if err != nil {
		return nil, ownerState{}, err
	}
	state, err := readState(paths)
	if err != nil {
		return nil, ownerState{}, err
	}
	var dialer net.Dialer
	network, addr, err := ownerDialTarget(state.Addr)
	if err != nil {
		return nil, ownerState{}, err
	}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, ownerState{}, err
	}
	return conn, state, nil
}

func ownerDialTarget(raw string) (string, string, error) {
	network := "tcp"
	addr := raw
	if socket, ok := strings.CutPrefix(raw, "unix://"); ok {
		network = "unix"
		addr = socket
	} else if tcpAddr, ok := strings.CutPrefix(raw, "tcp://"); ok {
		addr = tcpAddr
	} else if strings.Contains(raw, "://") {
		scheme, _, _ := strings.Cut(raw, "://")
		return "", "", fmt.Errorf("unsupported pty owner address scheme %q", scheme)
	}
	return network, addr, nil
}

func (a *Attachment) Write(data []byte) error {
	if a == nil || a.enc == nil {
		return fmt.Errorf("pty owner attachment is closed")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.enc.Encode(Request{
		Type: RequestInput, Token: a.token, Data: data,
	})
}

func (a *Attachment) Resize(cols, rows int) error {
	if a == nil || a.enc == nil {
		return fmt.Errorf("pty owner attachment is closed")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.enc.Encode(Request{
		Type: RequestResize, Token: a.token, Cols: cols, Rows: rows,
	})
}

func (a *Attachment) ExitCode() int {
	if a == nil || a.exitCode == nil {
		return -1
	}
	return a.exitCode()
}

func (a *Attachment) Close() {
	if a != nil && a.close != nil {
		a.close()
	}
}

func newToken() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
