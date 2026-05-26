package ptyowner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type SessionPaths struct {
	Root      string
	Session   string
	Dir       string
	Socket    string
	SocketDir string
	StatePath string
}

type ownerState struct {
	Session   string    `json:"session"`
	Addr      string    `json:"addr"`
	Token     string    `json:"token"`
	Cwd       string    `json:"cwd"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
}

func NewSessionPaths(root, session string) (SessionPaths, error) {
	if err := validateSessionName(session); err != nil {
		return SessionPaths{}, err
	}
	dir := filepath.Join(root, sessionDirName(session))
	socket := filepath.Join(root, "sock-"+sessionSocketHash(session))
	socketDir := ""
	if len(socket) > maxUnixSocketPathLen {
		socketDir = fallbackSocketDir(root, session, os.TempDir())
		socket = filepath.Join(socketDir, "sock")
	}
	return SessionPaths{
		Root:      root,
		Session:   session,
		Dir:       dir,
		Socket:    socket,
		SocketDir: socketDir,
		StatePath: filepath.Join(dir, "owner.json"),
	}, nil
}

const maxUnixSocketPathLen = 100

func fallbackSocketDir(root, session, primaryTempDir string) string {
	return fallbackSocketDirForOS(root, session, primaryTempDir, runtime.GOOS)
}

func fallbackSocketDirForOS(root, session, primaryTempDir, goos string) string {
	bases := []string{primaryTempDir}
	if goos == "darwin" {
		bases = append(bases, "/private/tmp")
	}
	bases = append(bases, "/tmp")
	for _, base := range bases {
		candidate := filepath.Join(base, "middleman-pty-"+sessionSocketHash(root+"-"+session))
		if len(filepath.Join(candidate, "sock")) <= maxUnixSocketPathLen {
			return candidate
		}
	}
	return filepath.Join("/tmp", "middleman-pty-"+sessionSocketHash(root+"-"+session))
}

func sessionDirName(session string) string {
	if strings.ContainsAny(session, `<>:"|?*`) {
		return "session-" + sessionSocketHash(session)
	}
	return session
}

func sessionSocketHash(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:])[:16]
}

func validateSessionName(session string) error {
	if session == "" {
		return fmt.Errorf("pty owner session name is empty")
	}
	if strings.Contains(session, "..") ||
		strings.ContainsAny(session, `/\`) ||
		strings.ContainsRune(session, 0) {
		return fmt.Errorf("unsafe pty owner session name %q", session)
	}
	return nil
}

func readState(paths SessionPaths) (ownerState, error) {
	data, err := os.ReadFile(paths.StatePath)
	if err != nil {
		return ownerState{}, err
	}
	var state ownerState
	if err := json.Unmarshal(data, &state); err != nil {
		return ownerState{}, err
	}
	if state.Addr == "" || state.Token == "" {
		return ownerState{}, fmt.Errorf("pty owner state is incomplete")
	}
	return state, nil
}

func writeState(paths SessionPaths, state ownerState) error {
	if err := createPrivateDir(paths.Dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(paths.Dir, ".owner-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, paths.StatePath)
}

func createPrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func removeSocketDir(paths SessionPaths) {
	if paths.SocketDir != "" {
		_ = os.RemoveAll(paths.SocketDir)
	}
}
