package roborevapi

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/apiclient/roborev"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	gitenv "go.kenn.io/kit/git/env"
)

func (s *Handlers) ListRoborevConfiguredRepositories(
	ctx context.Context,
	_ *struct{},
) (*itemapi.RoborevConfiguredRepositoriesOutput, error) {
	repositories, complete, err := (*s.RoborevRepositories).configuredRepositorySnapshot(ctx)
	if err != nil {
		return nil, httpapi.ServiceUnavailable("roborev repository configuration unavailable")
	}
	if repositories == nil {
		repositories = []itemapi.RoborevConfiguredRepositoryResponse{}
	}
	return &itemapi.RoborevConfiguredRepositoriesOutput{Body: itemapi.RoborevConfiguredRepositoriesResponse{
		Repositories: repositories,
		Complete:     complete,
	}}, nil
}

const (
	RoborevHookProbeWorkers   = 4
	RoborevProbeRetryCooldown = 30 * time.Second
	RoborevInventoryMaxBytes  = 2 << 20
	roborevHookMaxBytes       = 64 << 10
	roborevRefreshTimeout     = 2 * time.Minute
	roborevGitProbeTimeout    = 5 * time.Second
)

type RoborevTrackedRepository struct {
	RootPath string `json:"root_path"`
	Identity string `json:"identity"`
}

type roborevRepositoryInventory struct {
	Repos      jsontext.Value `json:"repos"`
	TotalCount *int           `json:"total_count"` // Total review jobs, not repositories.
}

type RoborevRepositoryProbeDeps struct {
	Now               func() time.Time
	LoadInventory     func(context.Context) ([]RoborevTrackedRepository, error)
	ResolveHookPath   func(context.Context, string) (string, error)
	InspectHook       func(string) (bool, error)
	OnWaitForInFlight func()
}

type roborevCheckoutProbeState struct {
	repository RoborevTrackedRepository
	definitive bool
	installed  bool
	retryAfter time.Time
}

type RoborevRepositoryProbe struct {
	mu                  sync.Mutex
	lifecycleCtx        context.Context
	knownHosts          []projects.KnownPlatformHost
	deps                RoborevRepositoryProbeDeps
	inventoryLoaded     bool
	inventoryErr        error
	inventoryRetryAfter time.Time
	checkouts           map[string]roborevCheckoutProbeState
	inFlight            chan struct{}
	generation          uint64
}

func NewRoborevRepositoryProbe(
	lifecycleCtx context.Context,
	endpoint string,
	knownHosts []projects.KnownPlatformHost,
) *RoborevRepositoryProbe {
	client := &http.Client{Timeout: 2 * time.Second}
	return newRoborevRepositoryProbeWithContextAndDeps(lifecycleCtx, knownHosts, RoborevRepositoryProbeDeps{
		Now:             time.Now,
		LoadInventory:   LoadRoborevRepositoryInventory(client, endpoint),
		ResolveHookPath: resolveRoborevHookPath,
		InspectHook:     InspectRoborevPostCommitHook,
	})
}

func NewRoborevRepositoryProbeWithDeps(
	knownHosts []projects.KnownPlatformHost,
	deps RoborevRepositoryProbeDeps,
) *RoborevRepositoryProbe {
	return newRoborevRepositoryProbeWithContextAndDeps(context.Background(), knownHosts, deps)
}

func newRoborevRepositoryProbeWithContextAndDeps(
	lifecycleCtx context.Context,
	knownHosts []projects.KnownPlatformHost,
	deps RoborevRepositoryProbeDeps,
) *RoborevRepositoryProbe {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &RoborevRepositoryProbe{
		lifecycleCtx: lifecycleCtx,
		knownHosts:   slices.Clone(knownHosts),
		deps:         deps,
		checkouts:    make(map[string]roborevCheckoutProbeState),
	}
}

func (p *RoborevRepositoryProbe) ConfiguredRepositories(
	ctx context.Context,
) ([]itemapi.RoborevConfiguredRepositoryResponse, error) {
	repositories, _, err := p.configuredRepositorySnapshot(ctx)
	return repositories, err
}

func (p *RoborevRepositoryProbe) configuredRepositorySnapshot(
	ctx context.Context,
) ([]itemapi.RoborevConfiguredRepositoryResponse, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	for {
		p.mu.Lock()
		if wait := p.inFlight; wait != nil {
			if p.deps.OnWaitForInFlight != nil {
				p.deps.OnWaitForInFlight()
			}
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}

		now := p.deps.Now()
		if !p.inventoryLoaded && p.inventoryErr != nil && now.Before(p.inventoryRetryAfter) {
			err := p.inventoryErr
			p.mu.Unlock()
			return nil, false, err
		}
		if !p.needsProbeLocked(now) {
			configured := p.configuredLocked()
			complete := p.completeLocked()
			p.mu.Unlock()
			return configured, complete, nil
		}
		wait := make(chan struct{})
		generation := p.generation
		p.inFlight = wait
		p.mu.Unlock()

		go p.runRefresh(now, wait, generation)
		select {
		case <-wait:
			continue
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

func (p *RoborevRepositoryProbe) runRefresh(
	now time.Time, wait chan struct{}, generation uint64,
) {
	ctx, cancel := context.WithTimeout(p.lifecycleCtx, roborevRefreshTimeout)
	defer cancel()
	_ = p.refresh(ctx, now, generation)

	p.mu.Lock()
	if p.inFlight == wait {
		close(wait)
		p.inFlight = nil
	}
	p.mu.Unlock()
}

func (p *RoborevRepositoryProbe) needsProbeLocked(now time.Time) bool {
	if !p.inventoryLoaded {
		return p.inventoryErr == nil || !now.Before(p.inventoryRetryAfter)
	}
	for _, state := range p.checkouts {
		if !state.definitive && !now.Before(state.retryAfter) {
			return true
		}
	}
	return false
}

func (p *RoborevRepositoryProbe) refresh(
	ctx context.Context, now time.Time, generation uint64,
) error {
	p.mu.Lock()
	if generation != p.generation {
		p.mu.Unlock()
		return nil
	}
	loaded := p.inventoryLoaded
	p.mu.Unlock()
	if !loaded {
		repositories, err := p.deps.LoadInventory(ctx)
		p.mu.Lock()
		if generation != p.generation {
			p.mu.Unlock()
			return nil
		}
		if err != nil {
			p.inventoryErr = err
			p.inventoryRetryAfter = now.Add(RoborevProbeRetryCooldown)
			p.mu.Unlock()
			return err
		}
		p.inventoryLoaded = true
		p.inventoryErr = nil
		for _, repository := range repositories {
			if projects.ParseRemoteURLWithKnownPlatforms(repository.Identity, p.knownHosts) == nil {
				continue
			}
			key := roborevCheckoutKey(repository)
			p.checkouts[key] = roborevCheckoutProbeState{repository: repository}
		}
		p.mu.Unlock()
	}

	p.mu.Lock()
	due := make([]RoborevTrackedRepository, 0, len(p.checkouts))
	for _, state := range p.checkouts {
		if !state.definitive && !now.Before(state.retryAfter) {
			due = append(due, state.repository)
		}
	}
	p.mu.Unlock()
	p.probeCheckouts(ctx, due, generation)
	return nil
}

type roborevHookPathResult struct {
	repository RoborevTrackedRepository
	path       string
	err        error
}

func (p *RoborevRepositoryProbe) probeCheckouts(
	ctx context.Context,
	repositories []RoborevTrackedRepository,
	generation uint64,
) {
	jobs := make(chan RoborevTrackedRepository)
	results := make(chan roborevHookPathResult, len(repositories))
	workers := min(RoborevHookProbeWorkers, len(repositories))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for repository := range jobs {
				path, err := p.deps.ResolveHookPath(ctx, repository.RootPath)
				results <- roborevHookPathResult{repository: repository, path: path, err: err}
			}
		})
	}
	go func() {
		for _, repository := range repositories {
			jobs <- repository
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	resolved := make(map[string][]RoborevTrackedRepository)
	for result := range results {
		if result.err != nil {
			p.recordCheckoutResult(result.repository, false, false, p.deps.Now(), generation)
			continue
		}
		resolved[result.path] = append(resolved[result.path], result.repository)
	}
	for path, matching := range resolved {
		installed, err := p.deps.InspectHook(path)
		completedAt := p.deps.Now()
		for _, repository := range matching {
			p.recordCheckoutResult(repository, installed, err == nil, completedAt, generation)
		}
	}
}

func (p *RoborevRepositoryProbe) recordCheckoutResult(
	repository RoborevTrackedRepository,
	installed bool,
	definitive bool,
	now time.Time,
	generation uint64,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if generation != p.generation {
		return
	}
	state := p.checkouts[roborevCheckoutKey(repository)]
	state.installed = installed
	state.definitive = definitive
	if !definitive {
		state.retryAfter = now.Add(RoborevProbeRetryCooldown)
	}
	p.checkouts[roborevCheckoutKey(repository)] = state
}

// Invalidate clears every cached inventory and checkout decision. A generation
// fence prevents a refresh that started before invalidation from publishing
// stale results after it completes.
func (p *RoborevRepositoryProbe) Invalidate() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.generation++
	if p.inFlight != nil {
		close(p.inFlight)
		p.inFlight = nil
	}
	p.inventoryLoaded = false
	p.inventoryErr = nil
	p.inventoryRetryAfter = time.Time{}
	p.checkouts = make(map[string]roborevCheckoutProbeState)
	p.mu.Unlock()
}

func (p *RoborevRepositoryProbe) configuredLocked() []itemapi.RoborevConfiguredRepositoryResponse {
	configured := make(map[string]itemapi.RoborevConfiguredRepositoryResponse)
	for _, state := range p.checkouts {
		if !state.definitive || !state.installed {
			continue
		}
		identity := projects.ParseRemoteURLWithKnownPlatforms(state.repository.Identity, p.knownHosts)
		if identity == nil {
			continue
		}
		ref := itemapi.RoborevConfiguredRepositoryResponse{
			Provider:     identity.Platform,
			PlatformHost: identity.Host,
			RepoPath:     identity.Owner + "/" + identity.Name,
			Owner:        identity.Owner,
			Name:         identity.Name,
		}
		configured[ref.Provider+"|"+ref.PlatformHost+"/"+ref.RepoPath] = ref
	}
	result := make([]itemapi.RoborevConfiguredRepositoryResponse, 0, len(configured))
	for _, ref := range configured {
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.PlatformHost != b.PlatformHost {
			return a.PlatformHost < b.PlatformHost
		}
		return a.RepoPath < b.RepoPath
	})
	return result
}

func (p *RoborevRepositoryProbe) completeLocked() bool {
	if !p.inventoryLoaded {
		return false
	}
	for _, state := range p.checkouts {
		if !state.definitive {
			return false
		}
	}
	return true
}

func roborevCheckoutKey(repository RoborevTrackedRepository) string {
	return repository.RootPath + "\x00" + repository.Identity
}

func LoadRoborevRepositoryInventory(
	client *http.Client,
	endpoint string,
) func(context.Context) ([]RoborevTrackedRepository, error) {
	return func(ctx context.Context) ([]RoborevTrackedRepository, error) {
		request, err := roborev.NewListReposRequest(ctx, strings.TrimRight(endpoint, "/"), &roborev.ListReposRequestOptions{})
		if err != nil {
			return nil, fmt.Errorf("build roborev repository request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("roborev repository inventory unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("roborev repository inventory returned status %d", response.StatusCode)
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, RoborevInventoryMaxBytes+1))
		if err != nil || len(content) > RoborevInventoryMaxBytes {
			return nil, errors.New("decode roborev repository inventory")
		}
		var inventory roborevRepositoryInventory
		if err := json.Unmarshal(content, &inventory); err != nil || inventory.Repos == nil || inventory.TotalCount == nil {
			return nil, errors.New("decode roborev repository inventory")
		}
		var repositories []RoborevTrackedRepository
		if err := json.Unmarshal(inventory.Repos, &repositories); err != nil {
			return nil, errors.New("decode roborev repository inventory")
		}
		if repositories == nil {
			repositories = []RoborevTrackedRepository{}
		}
		for i := range repositories {
			repositories[i].RootPath = strings.TrimSpace(repositories[i].RootPath)
			repositories[i].Identity = strings.TrimSpace(repositories[i].Identity)
			if repositories[i].RootPath == "" {
				return nil, errors.New("decode roborev repository inventory")
			}
		}
		return repositories, nil
	}
}

func resolveRoborevHookPath(ctx context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, roborevGitProbeTimeout)
	defer cancel()
	command := procutil.CommandContext(
		ctx,
		"git",
		"-C",
		root,
		"rev-parse",
		"--path-format=absolute",
		"--git-path",
		"hooks/post-commit",
	)
	command.Env = gitenv.StripAll(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", errors.New("resolve roborev hook path")
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", errors.New("resolve roborev hook path")
	}
	return path, nil
}

func InspectRoborevPostCommitHook(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, roborevHookMaxBytes))
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(strings.ToLower(string(content)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# roborev post-commit hook") {
			return true, nil
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if hasShellCommandPrefix(line, "roborev post-commit") ||
			hasShellCommandPrefix(line, "\"$roborev\" post-commit") ||
			hasShellCommandPrefix(line, "roborev enqueue") ||
			hasShellCommandPrefix(line, "\"$roborev\" enqueue") {
			return true, nil
		}
	}
	return false, nil
}

func hasShellCommandPrefix(line, prefix string) bool {
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	if len(line) == len(prefix) {
		return true
	}
	switch line[len(prefix)] {
	case ' ', '\t', '>', '|', '&', ';':
		return true
	default:
		return false
	}
}
