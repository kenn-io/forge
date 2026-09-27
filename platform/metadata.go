package platform

import (
	"fmt"
	"regexp"
	"strings"
)

type Metadata struct {
	Kind               Kind
	Label              string
	DefaultHost        string
	AllowNestedOwner   bool
	LowercaseRepoNames bool
}

var validKindRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var builtInMetadata = map[Kind]Metadata{
	KindBitbucket: {Kind: KindBitbucket, Label: "Bitbucket", DefaultHost: DefaultBitbucketHost},
	KindGitHub: {
		Kind:               KindGitHub,
		Label:              "GitHub",
		DefaultHost:        DefaultGitHubHost,
		AllowNestedOwner:   false,
		LowercaseRepoNames: true,
	},
	KindGitLab: {
		Kind:             KindGitLab,
		Label:            "GitLab",
		DefaultHost:      DefaultGitLabHost,
		AllowNestedOwner: true,
	},
	KindForgejo: {
		Kind:             KindForgejo,
		Label:            "Forgejo",
		DefaultHost:      DefaultForgejoHost,
		AllowNestedOwner: false,
	},
	KindGitea: {
		Kind:             KindGitea,
		Label:            "Gitea",
		DefaultHost:      DefaultGiteaHost,
		AllowNestedOwner: false,
	},
}

func NormalizeKind(raw string) (Kind, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(raw)))
	if kind == "" {
		return KindGitHub, nil
	}
	switch kind {
	case "gh":
		return KindGitHub, nil
	case "gl":
		return KindGitLab, nil
	case "fj":
		return KindForgejo, nil
	case "tea":
		return KindGitea, nil
	case KindGitHub, KindGitLab, KindForgejo, KindGitea, KindBitbucket:
		return kind, nil
	}
	if !validKindRe.MatchString(string(kind)) {
		return "", fmt.Errorf("unsupported platform %q", raw)
	}
	return kind, nil
}

func MetadataFor(kind Kind) (Metadata, bool) {
	kind, err := NormalizeKind(string(kind))
	if err != nil {
		return Metadata{}, false
	}
	meta, ok := builtInMetadata[kind]
	return meta, ok
}

func DefaultHost(kind Kind) (string, bool) {
	meta, ok := MetadataFor(kind)
	if !ok || meta.DefaultHost == "" {
		return "", false
	}
	return meta.DefaultHost, true
}

func HostOrDefault(kind Kind, host string) (string, bool) {
	host = strings.TrimSpace(host)
	if host != "" {
		return host, true
	}
	return DefaultHost(kind)
}

func AllowsNestedOwner(kind Kind) bool {
	meta, ok := MetadataFor(kind)
	if !ok {
		return true
	}
	return meta.AllowNestedOwner
}

func LowercaseRepoNames(kind Kind) bool {
	meta, ok := MetadataFor(kind)
	return ok && meta.LowercaseRepoNames
}

// MergeRequestHeadRef returns the ref used to store a merge request's
// head commit on the given platform: GitLab serves
// refs/merge-requests/<n>/head, while GitHub, Forgejo, and Gitea serve
// refs/pull/<n>/head. Bitbucket uses a local ref populated from the source branch.
// Unknown platforms fall back to the refs/pull form.
func MergeRequestHeadRef(kind Kind, number int) string {
	normalized, err := NormalizeKind(string(kind))
	if err == nil && normalized == KindGitLab {
		return fmt.Sprintf("refs/merge-requests/%d/head", number)
	}
	if err == nil && normalized == KindBitbucket {
		return fmt.Sprintf("refs/pull-requests/%d/from", number)
	}
	return fmt.Sprintf("refs/pull/%d/head", number)
}
