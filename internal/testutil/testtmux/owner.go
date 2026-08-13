// Package testtmux owns private tmux servers started by Go tests.
package testtmux

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

const (
	startTokenLength = 12
	cleanupTimeout   = 2 * time.Second
)

var runNamePattern = regexp.MustCompile(
	`^run\.([1-9][0-9]*)\.([0-9a-f]{12})\.([A-Za-z0-9]{6,32})$`,
)

type processIdentity struct {
	pid        int
	startToken string
}

type registeredServer struct {
	tmuxPath string
	socket   string
}

// Owner tracks private tmux servers for one test binary.
type Owner struct {
	root       string
	runDir     string
	mu         sync.Mutex
	servers    map[string]registeredServer
	cleanup    sync.Once
	cleanupErr error
}

type ownerMarker struct {
	PID          int    `json:"pid"`
	ProcessStart string `json:"process_start"`
	StartToken   string `json:"start_token"`
}

// New creates an owner beneath a stable, per-user temporary root. It reaps
// stale owners before publishing the new run.
func New() (*Owner, error) {
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	uid := current.Uid
	if uid == "" || strings.ContainsAny(uid, `/\\`) {
		return nil, fmt.Errorf("invalid current user ID %q", uid)
	}
	return newAt(filepath.Join(tmuxRootBase(), "kenn-forge-tmux-"+uid))
}

func newAt(root string) (*Owner, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("tmux test root must be absolute: %s", root)
	}
	root = filepath.Clean(root)
	if err := prepareRoot(root); err != nil {
		return nil, err
	}
	if err := reapStale(root); err != nil {
		return nil, err
	}
	start, err := processStart(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("identify tmux test owner: %w", err)
	}
	identity := processIdentity{
		pid:        os.Getpid(),
		startToken: tokenForStart(start),
	}
	nonce, err := randomToken(6)
	if err != nil {
		return nil, fmt.Errorf("create tmux test run nonce: %w", err)
	}
	runDir := filepath.Join(root, fmt.Sprintf(
		"run.%d.%s.%s", identity.pid, identity.startToken, nonce,
	))
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create tmux test run: %w", err)
	}
	marker := ownerMarker{
		PID:          identity.pid,
		ProcessStart: start,
		StartToken:   identity.startToken,
	}
	content, err := json.Marshal(marker)
	if err != nil {
		_ = os.Remove(runDir)
		return nil, fmt.Errorf("encode tmux test owner: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "owner.json"), content, 0o600); err != nil {
		_ = os.Remove(runDir)
		return nil, fmt.Errorf("write tmux test owner: %w", err)
	}
	return &Owner{
		root:    root,
		runDir:  runDir,
		servers: make(map[string]registeredServer),
	}, nil
}

// Command registers a private socket before returning a tmux command prefix.
func (o *Owner) Command(t testing.TB, tmuxPath string) []string {
	t.Helper()
	nonce, err := randomToken(6)
	require.NoError(t, err)
	serverDir := filepath.Join(o.runDir, "server-"+nonce)
	require.NoError(t, os.Mkdir(serverDir, 0o700))
	socket := filepath.Join(serverDir, "tmux.sock")
	server := registeredServer{tmuxPath: tmuxPath, socket: socket}
	o.mu.Lock()
	o.servers[socket] = server
	o.mu.Unlock()
	t.Cleanup(func() {
		require.NoError(t, o.release(server))
	})
	return []string{tmuxPath, "-f", "/dev/null", "-S", socket}
}

// Cleanup stops every registered server and removes this owner's run.
func (o *Owner) Cleanup() error {
	o.cleanup.Do(func() {
		o.mu.Lock()
		servers := make([]registeredServer, 0, len(o.servers))
		for _, server := range o.servers {
			servers = append(servers, server)
		}
		o.mu.Unlock()

		var cleanupErrors []error
		for _, server := range servers {
			if err := o.release(server); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		processErr := killRunProcesses(o.runDir)
		if processErr != nil {
			cleanupErrors = append(cleanupErrors, processErr)
		} else if err := os.RemoveAll(o.runDir); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		_ = os.Remove(o.root)
		o.cleanupErr = errors.Join(cleanupErrors...)
	})
	return o.cleanupErr
}

func (o *Owner) release(server registeredServer) error {
	o.mu.Lock()
	_, registered := o.servers[server.socket]
	o.mu.Unlock()
	if !registered {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	command := procutil.CommandContext(
		ctx, server.tmuxPath, "-f", "/dev/null", "-S", server.socket,
		"kill-server",
	)
	_ = command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("stop tmux test server %s: %w", server.socket, ctx.Err())
	}
	if err := os.RemoveAll(filepath.Dir(server.socket)); err != nil {
		return fmt.Errorf("remove tmux test server directory: %w", err)
	}
	o.mu.Lock()
	delete(o.servers, server.socket)
	o.mu.Unlock()
	return nil
}

func prepareRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create tmux test root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect tmux test root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("refusing insecure tmux test root: %s", root)
	}
	if err := validateDirectoryOwner(info); err != nil {
		return fmt.Errorf("refusing tmux test root ownership: %w", err)
	}
	return nil
}

func reapStale(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("list tmux test root: %w", err)
	}
	var cleanupErrors []error
	for _, entry := range entries {
		identity, ok := parseRunName(entry.Name())
		if !ok || runIsLive(identity, processStart) {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		info, statErr := os.Lstat(runDir)
		if statErr != nil {
			if !errors.Is(statErr, fs.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, statErr)
			}
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm() != 0o700 || validateDirectoryOwner(info) != nil {
			cleanupErrors = append(cleanupErrors,
				fmt.Errorf("refusing insecure stale tmux test run: %s", runDir),
			)
			continue
		}
		if err := validateRunMarker(runDir, identity); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if err := killSocketsIn(runDir); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if err := killRunProcesses(runDir); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		if err := os.RemoveAll(runDir); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := reapStaleProcesses(root); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func validateRunMarker(runDir string, identity processIdentity) error {
	path := filepath.Join(runDir, "owner.json")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect stale tmux test owner %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		validateDirectoryOwner(info) != nil {
		return fmt.Errorf("refusing insecure stale tmux test owner: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read stale tmux test owner %s: %w", path, err)
	}
	var marker ownerMarker
	if err := json.Unmarshal(content, &marker); err != nil {
		return fmt.Errorf("decode stale tmux test owner %s: %w", path, err)
	}
	if marker.PID != identity.pid || marker.StartToken != identity.startToken ||
		tokenForStart(marker.ProcessStart) != identity.startToken {
		return fmt.Errorf("refusing mismatched stale tmux test owner: %s", path)
	}
	return nil
}

func killSocketsIn(runDir string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil
	}
	return filepath.WalkDir(runDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		_ = procutil.CommandContext(ctx, tmuxPath, "-S", path, "kill-server").Run()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("stop stale tmux test server %s: %w", path, ctx.Err())
		}
		return nil
	})
}

func reapStaleProcesses(root string) error {
	processes, err := tmuxProcesses()
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, process := range processes {
		socket, ok := explicitSocket(process.command)
		if !ok {
			continue
		}
		identity, runDir, ok := runForSocket(root, socket)
		if !ok || runIsLive(identity, processStart) {
			continue
		}
		if _, statErr := os.Lstat(runDir); statErr == nil ||
			!errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if err := stopProcess(process.pid, process.start); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func killRunProcesses(runDir string) error {
	processes, err := tmuxProcesses()
	if err != nil {
		return err
	}
	runPrefix := filepath.Clean(runDir) + string(filepath.Separator)
	var cleanupErrors []error
	for _, process := range processes {
		socket, ok := explicitSocket(process.command)
		if !ok || !strings.HasPrefix(filepath.Clean(socket), runPrefix) {
			continue
		}
		if err := stopProcess(process.pid, process.start); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func explicitSocket(command string) (string, bool) {
	fields := strings.Fields(command)
	for index := 0; index+1 < len(fields); index++ {
		if fields[index] == "-S" && filepath.IsAbs(fields[index+1]) {
			return filepath.Clean(fields[index+1]), true
		}
	}
	return "", false
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func tokenForStart(start string) string {
	sum := sha256.Sum256([]byte(start))
	return hex.EncodeToString(sum[:])[:startTokenLength]
}

func parseRunName(name string) (processIdentity, bool) {
	match := runNamePattern.FindStringSubmatch(name)
	if match == nil {
		return processIdentity{}, false
	}
	pid, err := strconv.Atoi(match[1])
	if err != nil || pid <= 0 {
		return processIdentity{}, false
	}
	return processIdentity{pid: pid, startToken: match[2]}, true
}

func runIsLive(
	identity processIdentity,
	lookupStart func(int) (string, error),
) bool {
	start, err := lookupStart(identity.pid)
	return err == nil && tokenForStart(start) == identity.startToken
}

func identityForSocket(root, socket string) (processIdentity, bool) {
	identity, _, ok := runForSocket(root, socket)
	return identity, ok
}

func runForSocket(root, socket string) (processIdentity, string, bool) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(socket) {
		return processIdentity{}, "", false
	}
	root = filepath.Clean(root)
	socket = filepath.Clean(socket)
	relative, err := filepath.Rel(root, socket)
	if err != nil || relative == "." || relative == ".." ||
		filepath.IsAbs(relative) {
		return processIdentity{}, "", false
	}
	parts := splitPath(relative)
	if len(parts) < 3 || parts[len(parts)-1] != "tmux.sock" {
		return processIdentity{}, "", false
	}
	identity, ok := parseRunName(parts[0])
	return identity, filepath.Join(root, parts[0]), ok
}

func splitPath(path string) []string {
	var parts []string
	for path != "." && path != string(filepath.Separator) && path != "" {
		dir, base := filepath.Split(path)
		if base == "" || base == "." || base == ".." {
			return nil
		}
		parts = append([]string{base}, parts...)
		path = filepath.Clean(dir)
	}
	return parts
}
