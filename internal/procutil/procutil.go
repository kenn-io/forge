package procutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/semaphore"
)

const DefaultMaxProcesses = 32

// DefaultAcquireTimeout is the maximum time a subprocess waits for limiter
// capacity before surfacing host process exhaustion.
const DefaultAcquireTimeout = 5 * time.Second

var ErrProcessLimitReached = errors.New("host process limit reached")

type Limiter struct {
	sem            *semaphore.Weighted
	acquireTimeout time.Duration
}

func NewLimiter(max int) *Limiter {
	return NewLimiterWithAcquireTimeout(max, DefaultAcquireTimeout)
}

// NewLimiterWithAcquireTimeout creates a subprocess limiter with bounded
// acquisition waits. Non-positive timeouts wait until the caller's context ends.
func NewLimiterWithAcquireTimeout(max int, acquireTimeout time.Duration) *Limiter {
	if max <= 0 {
		max = 1
	}
	return &Limiter{
		sem:            semaphore.NewWeighted(int64(max)),
		acquireTimeout: acquireTimeout,
	}
}

func (l *Limiter) TryAcquire(
	ctx context.Context, reason string,
) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if l.sem.TryAcquire(1) {
		if err := ctx.Err(); err != nil {
			l.sem.Release(1)
			return nil, err
		}
		return releaseOnce(l.sem), nil
	}

	acquireCtx := ctx
	cancel := func() {}
	if l.acquireTimeout > 0 {
		acquireCtx, cancel = context.WithTimeout(ctx, l.acquireTimeout)
	}
	defer cancel()

	if err := l.sem.Acquire(acquireCtx, 1); err != nil {
		if reason != "" {
			return nil, fmt.Errorf(
				"%w: wait for subprocess capacity %s: %w",
				ErrProcessLimitReached, reason, err,
			)
		}
		return nil, fmt.Errorf("%w: %w", ErrProcessLimitReached, err)
	}
	return releaseOnce(l.sem), nil
}

func releaseOnce(sem *semaphore.Weighted) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			sem.Release(1)
		})
	}
}

var (
	defaultLimiterMu sync.RWMutex
	defaultLimiter   = NewLimiter(DefaultMaxProcesses)
)

func TryAcquire(
	ctx context.Context, reason string,
) (func(), error) {
	defaultLimiterMu.RLock()
	limiter := defaultLimiter
	defaultLimiterMu.RUnlock()
	return limiter.TryAcquire(ctx, reason)
}

// SetDefaultLimiterForTest replaces the package default limiter and returns a
// restore function for tests that need deterministic process-capacity pressure.
func SetDefaultLimiterForTest(limiter *Limiter) func() {
	if limiter == nil {
		panic("nil procutil limiter")
	}
	defaultLimiterMu.Lock()
	previous := defaultLimiter
	defaultLimiter = limiter
	defaultLimiterMu.Unlock()
	return func() {
		defaultLimiterMu.Lock()
		defaultLimiter = previous
		defaultLimiterMu.Unlock()
	}
}

func Command(name string, arg ...string) *exec.Cmd {
	resolvedName, resolvedArgs := ResolveCommand(name, arg...)
	//nolint:forbidigo // This is the central wrapper forbidigo requires callers to use.
	cmd := exec.Command(resolvedName, resolvedArgs...)
	ConfigureBackgroundCommand(cmd)
	return cmd
}

func CommandContext(
	ctx context.Context, name string, arg ...string,
) *exec.Cmd {
	resolvedName, resolvedArgs := ResolveCommand(name, arg...)
	//nolint:forbidigo // This is the central wrapper forbidigo requires callers to use.
	cmd := exec.CommandContext(ctx, resolvedName, resolvedArgs...)
	ConfigureBackgroundCommand(cmd)
	return cmd
}

func ResolveCommand(name string, arg ...string) (string, []string) {
	return resolveCommand(name, arg)
}

func ResolveBinary(name string) string {
	if binaryNeedsPathResolution(name) {
		if resolved, ok := resolveBinaryFromPath(name); ok {
			return resolved
		}
	}
	return name
}

func binaryNeedsPathResolution(name string) bool {
	return name != "" && filepath.Base(name) == name
}

func resolveBinaryFromPath(name string) (string, bool) {
	resolved, err := exec.LookPath(name)
	if err == nil {
		if abs, absErr := filepath.Abs(resolved); absErr == nil {
			return abs, true
		}
		return resolved, true
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, candidate := range binaryPathCandidates(dir, name) {
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && isExecutableCandidate(info) {
				return candidate, true
			}
		}
	}
	return "", false
}

func CombinedOutput(
	ctx context.Context, cmd *exec.Cmd, reason string,
) ([]byte, error) {
	ConfigureBackgroundCommand(cmd)
	release, err := TryAcquire(ctx, reason)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := cmd.CombinedOutput()
	return out, WrapResourceExhaustion(err, reason)
}

func Output(
	ctx context.Context, cmd *exec.Cmd, reason string,
) ([]byte, error) {
	ConfigureBackgroundCommand(cmd)
	release, err := TryAcquire(ctx, reason)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := cmd.Output()
	return out, WrapResourceExhaustion(err, reason)
}

func Run(
	ctx context.Context, cmd *exec.Cmd, reason string,
) error {
	ConfigureBackgroundCommand(cmd)
	release, err := TryAcquire(ctx, reason)
	if err != nil {
		return err
	}
	defer release()
	return WrapResourceExhaustion(cmd.Run(), reason)
}

func IsResourceExhausted(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProcessLimitReached) ||
		errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.ENOMEM) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "resource temporarily unavailable") ||
		strings.Contains(msg, "fork failed") ||
		strings.Contains(msg, "forkpty") ||
		strings.Contains(msg, "cannot allocate memory")
}

func WrapResourceExhaustion(err error, action string) error {
	if err == nil || !IsResourceExhausted(err) {
		return err
	}
	if action == "" {
		return fmt.Errorf("%w: %v", ErrProcessLimitReached, err)
	}
	return fmt.Errorf(
		"%w while %s: %v",
		ErrProcessLimitReached, action, err,
	)
}
