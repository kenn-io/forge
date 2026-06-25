package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var ErrMissingToken = errors.New("missing provider token")

type GitHubCLIRunner func(context.Context, string) (string, error)

// GitHubAppMinter exchanges a github_app candidate (app id, private
// key path, installation id, host) for an installation access token
// and its expiry. Installation tokens live one hour; the managed
// source caches them and re-mints ahead of expiry.
type GitHubAppMinter func(context.Context, Candidate) (string, time.Time, error)

type Options struct {
	GitHubCLI GitHubCLIRunner
	GitHubApp GitHubAppMinter
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
	// App tokens are scoped to one installation candidate; a host source may
	// carry several app candidates for different owners on the same GitHub host.
	appTokens map[Candidate]githubAppTokenCache
}

type githubAppTokenCache struct {
	token string
	exp   time.Time
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
		s.appTokens = nil
	}
	s.desc = cloneDescriptor(desc)
}

func (s *ManagedSource) Invalidate() {
	s.mu.Lock()
	s.ghToken = ""
	s.ghCached = false
	s.appTokens = nil
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
	case SourceKindGitHubApp:
		return s.githubAppToken(ctx, candidate)
	default:
		return "", false, nil
	}
}

// githubAppTokenRefreshSkew re-mints installation tokens this long
// before their recorded expiry so in-flight requests never race the
// one-hour token lifetime.
const githubAppTokenRefreshSkew = 5 * time.Minute

type mutationAuthCtxKey struct{}
type githubOwnerCtxKey struct{}

// WithMutationAuth marks ctx so token resolution skips github_app
// installation tokens and resolves the user's own credential chain
// (env PAT, token file, gh CLI) instead. Mutations sent with an app
// token are attributed to "<app>[bot]" on GitHub; middleman keeps
// user-visible writes (merges, comments, state changes) on the user's
// credential so they stay attributed to the user. A host configured
// with only an app and no PAT chain fails mutation auth with a
// missing-token error rather than silently writing as the bot.
func WithMutationAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, mutationAuthCtxKey{}, true)
}

// IsMutationAuth reports whether ctx was marked by WithMutationAuth.
func IsMutationAuth(ctx context.Context) bool {
	marked, ok := ctx.Value(mutationAuthCtxKey{}).(bool)
	return ok && marked
}

// WithGitHubOwner scopes token resolution to a GitHub repository or account
// owner. GitHub App installation tokens are account-scoped, so a candidate for
// one installation account must not be used for another owner on the same host.
func WithGitHubOwner(ctx context.Context, owner string) context.Context {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return ctx
	}
	return context.WithValue(ctx, githubOwnerCtxKey{}, owner)
}

func githubOwnerFromContext(ctx context.Context) (string, bool) {
	owner, ok := ctx.Value(githubOwnerCtxKey{}).(string)
	return owner, ok && owner != ""
}

func (s *ManagedSource) githubAppToken(
	ctx context.Context,
	candidate Candidate,
) (string, bool, error) {
	// An app entry that is not installed yet cannot mint tokens; fall
	// through to the remaining candidates (PAT env vars, gh CLI) so a
	// half-configured app does not take the whole host offline.
	if candidate.InstallationID == 0 {
		return "", false, nil
	}
	// Mutations stay on the user's own credential chain so writes are
	// attributed to the user instead of the app bot.
	if IsMutationAuth(ctx) {
		return "", false, nil
	}
	if candidate.InstallationAccount != "" {
		owner, ok := githubOwnerFromContext(ctx)
		if !ok || !strings.EqualFold(owner, candidate.InstallationAccount) {
			return "", false, nil
		}
	}
	cacheKey := canonicalCandidate(candidate)
	s.mu.Lock()
	minter := s.options.GitHubApp
	cached := s.appTokens[cacheKey]
	s.mu.Unlock()
	if minter == nil {
		return "", false, nil
	}
	if cached.token != "" && time.Until(cached.exp) > githubAppTokenRefreshSkew {
		return cached.token, true, nil
	}
	token, exp, err := minter(ctx, candidate)
	if err != nil {
		// Surface mint failures instead of silently degrading to the
		// PAT chain: the app exists precisely because the PAT budget
		// is exhausted, and a quiet fallback would hide broken keys.
		return "", false, fmt.Errorf(
			"mint github app installation token (%s): %w", candidate.SafeString(), err,
		)
	}
	if token == "" {
		return "", true, nil
	}
	s.mu.Lock()
	if s.appTokens == nil {
		s.appTokens = make(map[Candidate]githubAppTokenCache)
	}
	s.appTokens[cacheKey] = githubAppTokenCache{token: token, exp: exp}
	s.mu.Unlock()
	return token, true, nil
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
