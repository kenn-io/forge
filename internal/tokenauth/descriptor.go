package tokenauth

import (
	"fmt"
	"slices"
	"strings"
)

type SourceKind string

const (
	SourceKindEnv       SourceKind = "env"
	SourceKindFile      SourceKind = "file"
	SourceKindGitHubCLI SourceKind = "github_cli"
)

type Key struct {
	Platform string
	Host     string
}

func (k Key) String() string {
	return k.Platform + "\x00" + k.Host
}

// clonePlatform is the synthetic Key.Platform under which a host's git
// clone/fetch credential is registered. Git transport auth is host-scoped
// rather than provider-scoped — every provider sharing a host must present
// the same canonical credential chain — so clone auth holds one dedicated
// source per host instead of borrowing whichever provider source startup
// iteration yielded first.
const clonePlatform = "git-clone"

// CloneKey returns the SourceSet key holding host's git clone/fetch
// credential. It never collides with provider keys: clonePlatform is not a
// platform kind.
func CloneKey(host string) Key {
	return Key{Platform: clonePlatform, Host: host}
}

type Candidate struct {
	Kind     SourceKind
	EnvName  string
	FilePath string
	Host     string
}

func (c Candidate) SafeString() string {
	switch c.Kind {
	case SourceKindEnv:
		return fmt.Sprintf("env:%s", c.EnvName)
	case SourceKindFile:
		return fmt.Sprintf("file:%s", c.FilePath)
	case SourceKindGitHubCLI:
		return fmt.Sprintf("github_cli:%s", c.Host)
	default:
		return string(c.Kind)
	}
}

type Descriptor struct {
	Key        Key
	Candidates []Candidate
}

func (d Descriptor) EqualSource(other Descriptor) bool {
	return d.Key == other.Key &&
		slices.Equal(
			canonicalCandidates(d.Candidates),
			canonicalCandidates(other.Candidates),
		)
}

func (d Descriptor) SafeString() string {
	return joinCandidateStrings(d.Candidates)
}

// CanonicalSourceString returns a stable identifier for the descriptor's
// resolution chain with duplicate and field-redundant candidates removed.
// Two descriptors that resolve from the same ordered sources produce the same
// string even when their raw candidate lists differ — for example a repo-level
// override that repeats the platform fallback (env:SHARED -> env:SHARED)
// matches a provider that lists env:SHARED once. Same-host clone-token
// validation compares this; SafeString keeps the raw chain for human-readable
// error messages.
func (d Descriptor) CanonicalSourceString() string {
	return joinCandidateStrings(canonicalCandidates(d.Candidates))
}

func joinCandidateStrings(candidates []Candidate) string {
	if len(candidates) == 0 {
		return "none"
	}
	var out strings.Builder
	out.WriteString(candidates[0].SafeString())
	for _, c := range candidates[1:] {
		out.WriteString(" -> ")
		out.WriteString(c.SafeString())
	}
	return out.String()
}

func canonicalCandidates(candidates []Candidate) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	seen := make(map[Candidate]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = canonicalCandidate(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func canonicalCandidate(candidate Candidate) Candidate {
	switch candidate.Kind {
	case SourceKindEnv:
		return Candidate{Kind: candidate.Kind, EnvName: candidate.EnvName}
	case SourceKindFile:
		return Candidate{Kind: candidate.Kind, FilePath: candidate.FilePath}
	case SourceKindGitHubCLI:
		return Candidate{Kind: candidate.Kind, Host: candidate.Host}
	default:
		return candidate
	}
}
