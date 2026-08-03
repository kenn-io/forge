package server

import (
	"context"
	"encoding/json"
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

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/kit/git/env"
)

func (s *Server) listRoborevConfiguredRepositories(
	ctx context.Context,
	_ *struct{},
) (*roborevConfiguredRepositoriesOutput, error) {
	repositories, err := s.roborevRepositories.configuredRepositories(ctx)
	if err != nil {
		return nil, httpapi.ServiceUnavailable("roborev repository configuration unavailable")
	}
	if repositories == nil {
		repositories = []roborevConfiguredRepositoryResponse{}
	}
	return &roborevConfiguredRepositoriesOutput{Body: roborevConfiguredRepositoriesResponse{
		Repositories: repositories,
	}}, nil
}

const (
	roborevHookProbeWorkers   = 4
	roborevProbeRetryCooldown = 30 * time.Second
	roborevInventoryMaxBytes  = 2 << 20
	roborevHookMaxBytes       = 64 << 10
)

type roborevTrackedRepository struct {
	RootPath string `json:"root_path"`
	Identity string `json:"identity"`
}

type roborevRepositoryInventory struct {
	Repos      []roborevTrackedRepository `json:"repos"`
	TotalCount int                        `json:"total_count"`
}

type roborevRepositoryProbeDeps struct {
	now               func() time.Time
	loadInventory     func(context.Context) ([]roborevTrackedRepository, error)
	resolveHookPath   func(context.Context, string) (string, error)
	inspectHook       func(string) (bool, error)
	onWaitForInFlight func()
}

type roborevCheckoutProbeState struct {
	repository roborevTrackedRepository
	definitive bool
	installed  bool
	retryAfter time.Time
}

type roborevRepositoryProbe struct {
	mu                  sync.Mutex
	knownHosts          []projects.KnownPlatformHost
	deps                roborevRepositoryProbeDeps
	inventoryLoaded     bool
	inventoryErr        error
	inventoryRetryAfter time.Time
	checkouts           map[string]roborevCheckoutProbeState
	inFlight            chan struct{}
}

func newRoborevRepositoryProbe(
	endpoint string,
	knownHosts []projects.KnownPlatformHost,
) *roborevRepositoryProbe {
	client := &http.Client{Timeout: 2 * time.Second}
	return newRoborevRepositoryProbeWithDeps(knownHosts, roborevRepositoryProbeDeps{
		now:             time.Now,
		loadInventory:   loadRoborevRepositoryInventory(client, endpoint),
		resolveHookPath: resolveRoborevHookPath,
		inspectHook:     inspectRoborevPostCommitHook,
	})
}

func newRoborevRepositoryProbeWithDeps(
	knownHosts []projects.KnownPlatformHost,
	deps roborevRepositoryProbeDeps,
) *roborevRepositoryProbe {
	if deps.now == nil {
		deps.now = time.Now
	}
	return &roborevRepositoryProbe{
		knownHosts: slices.Clone(knownHosts),
		deps:       deps,
		checkouts:  make(map[string]roborevCheckoutProbeState),
	}
}

func (p *roborevRepositoryProbe) configuredRepositories(
	ctx context.Context,
) ([]roborevConfiguredRepositoryResponse, error) {
	for {
		p.mu.Lock()
		if wait := p.inFlight; wait != nil {
			if p.deps.onWaitForInFlight != nil {
				p.deps.onWaitForInFlight()
			}
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		now := p.deps.now()
		if !p.inventoryLoaded && p.inventoryErr != nil && now.Before(p.inventoryRetryAfter) {
			err := p.inventoryErr
			p.mu.Unlock()
			return nil, err
		}
		if !p.needsProbeLocked(now) {
			configured := p.configuredLocked()
			p.mu.Unlock()
			return configured, nil
		}
		p.inFlight = make(chan struct{})
		p.mu.Unlock()

		err := p.refresh(ctx, now)

		p.mu.Lock()
		close(p.inFlight)
		p.inFlight = nil
		configured := p.configuredLocked()
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return configured, nil
	}
}

func (p *roborevRepositoryProbe) needsProbeLocked(now time.Time) bool {
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

func (p *roborevRepositoryProbe) refresh(ctx context.Context, now time.Time) error {
	p.mu.Lock()
	loaded := p.inventoryLoaded
	p.mu.Unlock()
	if !loaded {
		repositories, err := p.deps.loadInventory(ctx)
		p.mu.Lock()
		if err != nil {
			p.inventoryErr = err
			p.inventoryRetryAfter = now.Add(roborevProbeRetryCooldown)
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
	due := make([]roborevTrackedRepository, 0, len(p.checkouts))
	for _, state := range p.checkouts {
		if !state.definitive && !now.Before(state.retryAfter) {
			due = append(due, state.repository)
		}
	}
	p.mu.Unlock()
	p.probeCheckouts(ctx, now, due)
	return nil
}

type roborevHookPathResult struct {
	repository roborevTrackedRepository
	path       string
	err        error
}

func (p *roborevRepositoryProbe) probeCheckouts(
	ctx context.Context,
	now time.Time,
	repositories []roborevTrackedRepository,
) {
	jobs := make(chan roborevTrackedRepository)
	results := make(chan roborevHookPathResult, len(repositories))
	workers := min(roborevHookProbeWorkers, len(repositories))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for repository := range jobs {
				path, err := p.deps.resolveHookPath(ctx, repository.RootPath)
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

	resolved := make(map[string][]roborevTrackedRepository)
	for result := range results {
		if result.err != nil {
			p.recordCheckoutResult(result.repository, false, false, now)
			continue
		}
		resolved[result.path] = append(resolved[result.path], result.repository)
	}
	for path, matching := range resolved {
		installed, err := p.deps.inspectHook(path)
		for _, repository := range matching {
			p.recordCheckoutResult(repository, installed, err == nil, now)
		}
	}
}

func (p *roborevRepositoryProbe) recordCheckoutResult(
	repository roborevTrackedRepository,
	installed bool,
	definitive bool,
	now time.Time,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.checkouts[roborevCheckoutKey(repository)]
	state.installed = installed
	state.definitive = definitive
	if !definitive {
		state.retryAfter = now.Add(roborevProbeRetryCooldown)
	}
	p.checkouts[roborevCheckoutKey(repository)] = state
}

func (p *roborevRepositoryProbe) configuredLocked() []roborevConfiguredRepositoryResponse {
	configured := make(map[string]roborevConfiguredRepositoryResponse)
	for _, state := range p.checkouts {
		if !state.definitive || !state.installed {
			continue
		}
		identity := projects.ParseRemoteURLWithKnownPlatforms(state.repository.Identity, p.knownHosts)
		if identity == nil {
			continue
		}
		ref := roborevConfiguredRepositoryResponse{
			Provider:     identity.Platform,
			PlatformHost: identity.Host,
			RepoPath:     identity.Owner + "/" + identity.Name,
			Owner:        identity.Owner,
			Name:         identity.Name,
		}
		configured[ref.Provider+"|"+ref.PlatformHost+"/"+ref.RepoPath] = ref
	}
	result := make([]roborevConfiguredRepositoryResponse, 0, len(configured))
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

func roborevCheckoutKey(repository roborevTrackedRepository) string {
	return repository.RootPath + "\x00" + repository.Identity
}

func loadRoborevRepositoryInventory(
	client *http.Client,
	endpoint string,
) func(context.Context) ([]roborevTrackedRepository, error) {
	return func(ctx context.Context) ([]roborevTrackedRepository, error) {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			strings.TrimRight(endpoint, "/")+"/api/repos",
			nil,
		)
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
		var inventory roborevRepositoryInventory
		reader := io.LimitReader(response.Body, roborevInventoryMaxBytes+1)
		if err := json.NewDecoder(reader).Decode(&inventory); err != nil {
			return nil, errors.New("decode roborev repository inventory")
		}
		return inventory.Repos, nil
	}
}

func resolveRoborevHookPath(ctx context.Context, root string) (string, error) {
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

func inspectRoborevPostCommitHook(path string) (bool, error) {
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
		if strings.HasPrefix(line, "# roborev post-commit hook") ||
			strings.HasPrefix(line, "roborev post-commit") ||
			strings.HasPrefix(line, "\"$roborev\" post-commit") ||
			strings.HasPrefix(line, "roborev enqueue") ||
			strings.HasPrefix(line, "\"$roborev\" enqueue") {
			return true, nil
		}
	}
	return false, nil
}
