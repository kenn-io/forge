package externalcontext

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
)

const (
	cacheLifetime = 5 * time.Second
	cacheCapacity = 64
	stdoutLimit   = 1024 * 1024
	stderrLimit   = 64 * 1024
)

type readKey struct {
	sourceID string
	pull     PullRequest
}

type outcome struct {
	result    ExternalContextResult
	err       error
	completed time.Time
}

type readCall struct {
	done chan struct{}
	outcome
}

// Runner owns shared reads and all subprocesses until Close cancels and drains them.
type Runner struct {
	ctx     context.Context
	cancel  context.CancelFunc
	limiter *procutil.Limiter
	mu      sync.Mutex
	sources []config.ExternalContextSource
	flights map[readKey]*readCall
	cache   map[readKey]outcome
	closed  bool
	wg      sync.WaitGroup
}

func New(sources []config.ExternalContextSource) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{ctx: ctx, cancel: cancel, limiter: procutil.NewLimiter(2)}
	runner.Update(sources)
	return runner
}

func (r *Runner) Update(sources []config.ExternalContextSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = slices.Clone(sources)
	for i := range r.sources {
		r.sources[i].Command = slices.Clone(r.sources[i].Command)
	}
	r.flights = make(map[readKey]*readCall)
	r.cache = make(map[readKey]outcome)
}

func (r *Runner) Sources() []ExternalContextSourceInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]ExternalContextSourceInfo, 0, len(r.sources))
	for _, source := range r.sources {
		result = append(result, ExternalContextSourceInfo{ID: source.ID, Name: source.Name})
	}
	return result
}

func (r *Runner) source(id string) (config.ExternalContextSource, error) {
	if r.closed {
		return config.ExternalContextSource{}, ErrClosed
	}
	for _, source := range r.sources {
		if source.ID == id {
			return source, nil
		}
	}
	return config.ExternalContextSource{}, ErrUnknownSource
}

func (r *Runner) Read(ctx context.Context, sourceID string, pull PullRequest, refresh bool) (ExternalContextResult, error) {
	if err := ctx.Err(); err != nil {
		return ExternalContextResult{}, err
	}
	key := readKey{sourceID: sourceID, pull: pull}
	r.mu.Lock()
	source, err := r.source(sourceID)
	if err != nil {
		r.mu.Unlock()
		return ExternalContextResult{}, err
	}
	call := r.flights[key]
	if call == nil {
		if cached, ok := r.cache[key]; ok && !refresh && time.Since(cached.completed) < cacheLifetime {
			r.mu.Unlock()
			return cloneResult(cached.result), cached.err
		}
		delete(r.cache, key)
		call = &readCall{done: make(chan struct{})}
		r.flights[key] = call
		r.wg.Go(func() {
			call.result, call.err = r.invoke(source, pull, "read", "", "")
			call.completed = time.Now()
			r.mu.Lock()
			// Deleting a flight invalidates it without cancelling its existing waiters.
			if r.flights[key] == call {
				delete(r.flights, key)
				r.store(key, call.outcome)
			}
			r.mu.Unlock()
			close(call.done)
		})
	}
	r.mu.Unlock()
	return wait(ctx, call)
}

func (r *Runner) Action(ctx context.Context, sourceID string, pull PullRequest, actionID, input string) (ExternalContextResult, error) {
	if err := ctx.Err(); err != nil {
		return ExternalContextResult{}, err
	}
	r.mu.Lock()
	source, err := r.source(sourceID)
	if err != nil {
		r.mu.Unlock()
		return ExternalContextResult{}, err
	}
	r.invalidate(sourceID, pull)
	call := &readCall{done: make(chan struct{})}
	r.wg.Go(func() {
		call.result, call.err = r.invoke(source, pull, "action", actionID, input)
		r.mu.Lock()
		r.invalidate(sourceID, pull)
		r.mu.Unlock()
		close(call.done)
	})
	r.mu.Unlock()
	return wait(ctx, call)
}

func wait(ctx context.Context, call *readCall) (ExternalContextResult, error) {
	select {
	case <-ctx.Done():
		return ExternalContextResult{}, ctx.Err()
	case <-call.done:
		return cloneResult(call.result), call.err
	}
}

func cloneResult(result ExternalContextResult) ExternalContextResult {
	if result.Card != nil {
		card := *result.Card
		card.Actions = slices.Clone(card.Actions)
		result.Card = &card
	}
	return result
}

func (r *Runner) invalidate(sourceID string, pull PullRequest) {
	matches := func(key readKey) bool {
		return key.sourceID == sourceID && key.pull.Provider == pull.Provider && key.pull.PlatformHost == pull.PlatformHost && key.pull.RepoKey == pull.RepoKey && key.pull.Number == pull.Number
	}
	for key := range r.cache {
		if matches(key) {
			delete(r.cache, key)
		}
	}
	for key := range r.flights {
		if matches(key) {
			delete(r.flights, key)
		}
	}
}

func (r *Runner) store(key readKey, value outcome) {
	var oldestKey readKey
	var oldest time.Time
	for k, cached := range r.cache {
		if time.Since(cached.completed) >= cacheLifetime {
			delete(r.cache, k)
		} else if oldest.IsZero() || cached.completed.Before(oldest) {
			oldestKey, oldest = k, cached.completed
		}
	}
	if len(r.cache) >= cacheCapacity {
		delete(r.cache, oldestKey)
	}
	r.cache[key] = value
}

func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Runner) invoke(source config.ExternalContextSource, pull PullRequest, operation, actionID, actionInput string) (ExternalContextResult, error) {
	timeout := 10 * time.Second
	if source.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(source.Timeout)
		if err != nil || timeout <= 0 {
			return ExternalContextResult{}, ErrInvocation
		}
	}
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	release, err := r.limiter.TryAcquire(ctx, "external context")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ExternalContextResult{}, ErrTimeout
		}
		return ExternalContextResult{}, ErrInvocation
	}
	defer release()
	if len(source.Command) == 0 {
		return ExternalContextResult{}, ErrInvocation
	}
	request := struct {
		Version     int         `json:"version"`
		Operation   string      `json:"operation"`
		PullRequest PullRequest `json:"pull_request"`
		ActionID    string      `json:"action_id,omitempty"`
		Input       string      `json:"input,omitempty"`
	}{1, operation, pull, actionID, actionInput}
	input, err := json.Marshal(request)
	if err != nil {
		return ExternalContextResult{}, ErrInvocation
	}
	cmd := procutil.CommandContext(ctx, source.Command[0], source.Command[1:]...)
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &limitedOutput{limit: stdoutLimit, cancel: cancel}
	stderr := &limitedOutput{limit: stderrLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = procutil.Run(ctx, cmd, "external context")
	var result ExternalContextResult
	switch {
	case stdout.exceeded || stderr.exceeded:
		err = ErrOutputLimit
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = ErrTimeout
	case err != nil:
		err = ErrInvocation
	default:
		result, err = decodeResult(stdout.buffer.Bytes())
	}
	if err != nil && operation == "action" && cmd.Process != nil {
		err = fmt.Errorf("%w. %s", err, "The action may have been submitted. Refresh to check its status.")
	}
	return result, err
}

type limitedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.exceeded = true
		b.cancel()
		return 0, ErrOutputLimit
	}
	return b.buffer.Write(p)
}

func decodeResult(data []byte) (ExternalContextResult, error) {
	var envelope struct {
		Card jsontext.Value `json:"card"`
	}
	if err := json.Unmarshal(data, &envelope, json.RejectUnknownMembers(true)); err != nil || len(envelope.Card) == 0 {
		return ExternalContextResult{}, ErrInvalidResponse
	}
	var result ExternalContextResult
	if err := json.Unmarshal(envelope.Card, &result.Card, json.RejectUnknownMembers(true)); err != nil {
		return ExternalContextResult{}, ErrInvalidResponse
	}
	card := result.Card
	if card == nil {
		return result, nil
	}
	switch card.Status {
	case "neutral", "pending", "success", "warning", "error":
	default:
		return ExternalContextResult{}, ErrInvalidResponse
	}
	if strings.TrimSpace(card.Summary) == "" || len(card.Summary) > 4096 || len(card.Markdown) > 512*1024 || len(card.ResultHeadSHA) > 256 || len(card.Actions) > 32 || card.RefreshAfterSeconds < 0 {
		return ExternalContextResult{}, ErrInvalidResponse
	}
	seen := make(map[string]bool, len(card.Actions))
	for _, action := range card.Actions {
		if strings.TrimSpace(action.ID) == "" || len(action.ID) > 128 || seen[action.ID] || strings.TrimSpace(action.Label) == "" || len(action.Label) > 256 || len(action.DisabledReason) > 4096 || (action.Input != nil && (len(action.Input.Placeholder) > 256 || action.Input.MaxLength < 0 || action.Input.MaxLength > 16384)) {
			return ExternalContextResult{}, ErrInvalidResponse
		}
		seen[action.ID] = true
	}
	if card.RefreshAfterSeconds > 0 && card.RefreshAfterSeconds < 5 {
		card.RefreshAfterSeconds = 5
	}
	return result, nil
}
