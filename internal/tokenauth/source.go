package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

var ErrMissingToken = errors.New("missing provider token")

type GitHubCLIRunner func(context.Context, string) (string, error)

type Options struct {
	GitHubCLI GitHubCLIRunner
}

type Source interface {
	Token(context.Context) (string, error)
	Invalidate()
	Descriptor() Descriptor
}

type ManagedSource struct {
	mu       sync.Mutex
	desc     Descriptor
	options  Options
	ghToken  string
	ghCached bool
}

func NewManagedSource(desc Descriptor, options Options) *ManagedSource {
	return &ManagedSource{desc: cloneDescriptor(desc), options: options}
}

func (s *ManagedSource) Descriptor() Descriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDescriptor(s.desc)
}

func (s *ManagedSource) Update(desc Descriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.desc.EqualSource(desc) {
		s.ghToken = ""
		s.ghCached = false
	}
	s.desc = cloneDescriptor(desc)
}

func (s *ManagedSource) Invalidate() {
	s.mu.Lock()
	s.ghToken = ""
	s.ghCached = false
	s.mu.Unlock()
}

func (s *ManagedSource) Token(ctx context.Context) (string, error) {
	desc := s.Descriptor()
	if len(desc.Candidates) == 0 {
		return "", missingTokenError(desc)
	}
	for _, candidate := range desc.Candidates {
		token, used, err := s.tokenFromCandidate(ctx, candidate)
		if err != nil {
			return "", err
		}
		if used && token != "" {
			RegisterKnownSecret(token)
			return token, nil
		}
	}
	return "", missingTokenError(desc)
}

func (s *ManagedSource) tokenFromCandidate(
	ctx context.Context,
	candidate Candidate,
) (string, bool, error) {
	switch candidate.Kind {
	case SourceKindEnv:
		return strings.TrimSpace(os.Getenv(candidate.EnvName)), true, nil
	case SourceKindFile:
		data, err := os.ReadFile(candidate.FilePath)
		if err != nil {
			return "", false, fmt.Errorf("read token file %s: %w", candidate.FilePath, err)
		}
		return strings.TrimSpace(string(data)), true, nil
	case SourceKindGitHubCLI:
		return s.githubCLIToken(ctx, candidate.Host)
	default:
		return "", false, nil
	}
}

func (s *ManagedSource) githubCLIToken(
	ctx context.Context,
	host string,
) (string, bool, error) {
	s.mu.Lock()
	if s.ghCached {
		token := s.ghToken
		s.mu.Unlock()
		return token, true, nil
	}
	runner := s.options.GitHubCLI
	s.mu.Unlock()
	if runner == nil {
		return "", true, nil
	}
	token, err := runner(ctx, host)
	if err != nil {
		return "", false, fmt.Errorf("github cli token for %s: %w", host, err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", true, nil
	}
	s.mu.Lock()
	s.ghToken = token
	s.ghCached = true
	s.mu.Unlock()
	return token, true, nil
}

func missingTokenError(desc Descriptor) error {
	return fmt.Errorf(
		"%w for %s host %s via %s",
		ErrMissingToken, desc.Key.Platform, desc.Key.Host, desc.SafeString(),
	)
}

type SourceSet struct {
	mu      sync.Mutex
	options Options
	sources map[Key]*ManagedSource
}

func NewSourceSet(options Options) *SourceSet {
	return &SourceSet{options: options, sources: make(map[Key]*ManagedSource)}
}

func (s *SourceSet) Upsert(desc Descriptor) *ManagedSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sources[desc.Key]; ok {
		existing.Update(desc)
		return existing
	}
	src := NewManagedSource(desc, s.options)
	s.sources[desc.Key] = src
	return src
}

func (s *SourceSet) Get(key Key) (*ManagedSource, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[key]
	return src, ok
}

// ProbeToken resolves desc with this set's options without mutating live sources.
func (s *SourceSet) ProbeToken(ctx context.Context, desc Descriptor) (string, error) {
	if s == nil {
		return NewManagedSource(desc, Options{}).Token(ctx)
	}
	s.mu.Lock()
	options := s.options
	s.mu.Unlock()
	return NewManagedSource(desc, options).Token(ctx)
}

func (s *SourceSet) Keys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]Key, 0, len(s.sources))
	for key := range s.sources {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b Key) int {
		if cmp := strings.Compare(a.Platform, b.Platform); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Host, b.Host)
	})
	return keys
}

func cloneDescriptor(desc Descriptor) Descriptor {
	desc.Candidates = append([]Candidate(nil), desc.Candidates...)
	return desc
}
