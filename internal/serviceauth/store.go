package serviceauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	tokenFileName = "github-user-auth.json"
	lockFileName  = ".github-user-auth.lock"
	lockRetry     = 25 * time.Millisecond
	maxStoreBytes = 64 << 10
)

type authorization struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	BrowserToken     string    `json:"browser_token"`
	Login            string    `json:"login"`
	UserID           int64     `json:"user_id"`
}

func (a *authorization) valid(ownerID int64) bool {
	return a != nil && a.AccessToken != "" && a.RefreshToken != "" &&
		a.BrowserToken != "" && a.Login != "" && a.UserID == ownerID &&
		!a.AccessExpiresAt.IsZero() && !a.RefreshExpiresAt.IsZero()
}

func (a *authorization) usable(now time.Time) bool {
	return a.AccessExpiresAt.After(now) || a.RefreshExpiresAt.After(now)
}

type storeLock struct {
	local chan struct{}
	file  *flock.Flock
}

var storeLocks sync.Map

type store struct {
	dir       string
	path      string
	ownerID   int64
	lockState *storeLock
}

func newStore(dataDir string, ownerID int64) (*store, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve service auth data directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create service auth data directory: %w", err)
	}
	lockPath := filepath.Join(dir, lockFileName)
	value, _ := storeLocks.LoadOrStore(lockPath, &storeLock{
		local: make(chan struct{}, 1),
		file:  flock.New(lockPath, flock.SetPermissions(0o600)),
	})
	return &store{
		dir:       dir,
		path:      filepath.Join(dir, tokenFileName),
		ownerID:   ownerID,
		lockState: value.(*storeLock),
	}, nil
}

func (s *store) load() (*authorization, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open service authorization: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxStoreBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read service authorization: %w", err)
	}
	if len(data) > maxStoreBytes {
		return nil, errors.New("service authorization file is too large")
	}
	var auth authorization
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, errors.New("service authorization file is malformed")
	}
	if !auth.valid(s.ownerID) {
		return nil, errors.New("service authorization file is invalid for configured owner")
	}
	return &auth, nil
}

func (s *store) save(auth *authorization) error {
	if !auth.valid(s.ownerID) {
		return errors.New("refuse to persist invalid service authorization")
	}
	data, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("encode service authorization: %w", err)
	}
	temp, err := os.CreateTemp(s.dir, ".github-user-auth-*")
	if err != nil {
		return fmt.Errorf("create service authorization temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure service authorization temporary file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write service authorization temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync service authorization temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close service authorization temporary file: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace service authorization: %w", err)
	}
	return nil
}

func (s *store) remove() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove service authorization: %w", err)
	}
	return nil
}

func (s *store) locked(ctx context.Context, operation func() error) (err error) {
	select {
	case s.lockState.local <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.lockState.local }()

	locked, err := s.lockState.file.TryLockContext(ctx, lockRetry)
	if err != nil {
		return fmt.Errorf("acquire service authorization lock: %w", err)
	}
	if !locked {
		return errors.New("service authorization lock was not acquired")
	}
	defer func() {
		if unlockErr := s.lockState.file.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release service authorization lock: %w", unlockErr))
		}
	}()
	return operation()
}
