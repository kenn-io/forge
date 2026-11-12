package platform

import (
	"net"
	"net/url"
	"strings"
)

// HeadRepoKind says where a pull request's head branch lives relative to its base repository.
type HeadRepoKind string

const (
	HeadRepoKindSameRepo HeadRepoKind = "same_repo"
	HeadRepoKindFork     HeadRepoKind = "fork"
	HeadRepoKindUnknown  HeadRepoKind = "unknown"
)

// ClassifyHeadRepo reports whether a pull request's head clone URL names its base
// repository, a fork, or a repository it cannot identify. An empty provider means
// GitHub and an empty host means the provider's default host.
func ClassifyHeadRepo(provider, platformHost, owner, name, cloneURL string) HeadRepoKind {
	kind := Kind(provider)
	if strings.TrimSpace(provider) == "" {
		kind = KindGitHub
	}
	if strings.TrimSpace(platformHost) == "" {
		platformHost = DefaultGitHubHost
		if host, ok := DefaultHost(kind); ok {
			platformHost = host
		}
	}
	headRepo := HeadRepo(string(kind), platformHost, owner, name, cloneURL)
	switch {
	case headRepo == nil:
		return HeadRepoKindSameRepo
	case *headRepo == "":
		return HeadRepoKindUnknown
	default:
		return HeadRepoKindFork
	}
}

// HeadRepo classifies a pull-request head repository against its
// base repository identity: nil means confirmed same-repo, a non-nil empty
// string means the head repository identity could not be determined from
// cloneURL, and a non-nil clone URL means the head lives in a fork.
func HeadRepo(provider, platformHost, owner, name, cloneURL string) *string {
	// A fork head must be resolved through fork-safe refs. GitHub also fills
	// head.repo.clone_url for same-repo PRs, so compare clone identities before
	// treating a non-empty URL as fork metadata.
	headRepo := CloneRepoIdentity(provider, platformHost, cloneURL)
	if headRepo == "" {
		unknown := ""
		return &unknown
	}
	baseRepo := strings.ToLower(strings.Join([]string{strings.TrimSpace(provider), PlatformHostIdentity(platformHost), strings.TrimSpace(owner), strings.TrimSpace(name)}, "/"))
	if headRepo == baseRepo {
		return nil
	}
	s := cloneURL
	return &s
}

// CloneRepoIdentity returns the lowercased provider/host/repo-path identity a clone
// URL names, or "" when the URL does not name a repository on that provider.
func CloneRepoIdentity(provider, platformHost, cloneURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	cloneURL = strings.TrimSpace(cloneURL)
	if provider == "" || cloneURL == "" {
		return ""
	}
	if !strings.Contains(cloneURL, "://") && !strings.Contains(cloneURL, "@") {
		return ""
	}
	host := cloneURLHost(cloneURL)
	if host == "" {
		return ""
	}
	repoPath := cloneRepoPath(cloneURL)
	if provider == string(KindBitbucket) && platformHost != DefaultBitbucketHost {
		if ValidateRemoteHost(KindBitbucket, platformHost, cloneURL) != nil {
			return ""
		}
		host = PlatformHostIdentity(platformHost)
		repoPath = RemoteRepoPath(KindBitbucket, platformHost, cloneURL)
	}
	if repoPath == "" {
		return ""
	}
	return strings.ToLower(provider + "/" + host + "/" + repoPath)
}

func cloneRepoPath(cloneURL string) string {
	var repoPath string
	if strings.Contains(cloneURL, "://") {
		parsed, err := url.Parse(cloneURL)
		if err != nil {
			return ""
		}
		repoPath = parsed.Path
	} else {
		beforePath, path, ok := strings.Cut(cloneURL, ":")
		if !ok || !strings.Contains(beforePath, "@") {
			return ""
		}
		repoPath = path
	}
	unescaped, err := url.PathUnescape(repoPath)
	if err != nil {
		return ""
	}
	repoPath = strings.Trim(strings.TrimSpace(unescaped), "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")
	if strings.Count(repoPath, "/") < 1 {
		return ""
	}
	return repoPath
}

func cloneURLHost(cloneURL string) string {
	if strings.Contains(cloneURL, "://") {
		parsed, err := url.Parse(cloneURL)
		if err != nil {
			return ""
		}
		return hostPortIdentity(
			parsed.Scheme, parsed.Hostname(), parsed.Port(),
		)
	}
	beforePath, _, ok := strings.Cut(cloneURL, ":")
	if !ok {
		return ""
	}
	_, host, ok := strings.Cut(beforePath, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(host)
}

// PlatformHostIdentity lowercases a provider host and drops its scheme's default port.
func PlatformHostIdentity(platformHost string) string {
	platformHost = strings.TrimSpace(platformHost)
	if platformHost == "" {
		return ""
	}
	if strings.Contains(platformHost, "://") {
		parsed, err := url.Parse(platformHost)
		if err != nil {
			return ""
		}
		return hostPortIdentity(
			parsed.Scheme, parsed.Hostname(), parsed.Port(),
		)
	}
	host, port, err := net.SplitHostPort(platformHost)
	if err == nil {
		return hostPortIdentity("https", host, port)
	}
	return strings.ToLower(platformHost)
}

func hostPortIdentity(scheme, host, port string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if port == "" || isDefaultCloneURLPort(scheme, port) {
		return host
	}
	return strings.ToLower(net.JoinHostPort(host, port))
}

func isDefaultCloneURLPort(scheme, port string) bool {
	switch strings.ToLower(scheme) {
	case "http":
		return port == "80"
	case "https":
		return port == "443"
	case "ssh":
		return port == "22"
	default:
		return false
	}
}
