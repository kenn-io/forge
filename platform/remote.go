package platform

import (
	"net/url"
	"strings"

	gitremote "go.kenn.io/kit/git/remote"
)

// DefaultCloneURL is the HTTPS clone URL for a repository route when the
// provider reported none. Bitbucket Data Center serves HTTP clones under its
// /scm/ prefix; every other provider serves them at the route itself.
func DefaultCloneURL(kind Kind, host, repoPath string) string {
	repoPath = strings.Trim(repoPath, "/")
	if kind == KindBitbucket && !strings.EqualFold(strings.TrimSpace(host), DefaultBitbucketHost) {
		return "https://" + host + "/scm/" + repoPath + ".git"
	}
	return "https://" + host + "/" + repoPath + ".git"
}

// RemoteRepoPath returns the provider's repository route from a Git URL.
// Data Center's HTTP clone prefix is transport syntax, not a project name.
func RemoteRepoPath(kind Kind, host, remoteURL string) string {
	return gitremote.RemoteRepoPath(repositoryRemote(kind, host, remoteURL))
}

// ValidateRemoteHost matches Git transport authority to the configured provider.
// Data Center can expose SSH on a different port from its HTTP API.
func ValidateRemoteHost(kind Kind, host, remoteURL string) error {
	return gitremote.ValidateRemoteHost(host, repositoryRemote(kind, host, remoteURL))
}

// ValidateRemoteIdentity verifies a clone URL against a canonical provider route.
func ValidateRemoteIdentity(ref RepoRef, remoteURL string) error {
	return gitremote.ValidateRemoteIdentity(gitremote.Identity{Host: ref.Host, Owner: ref.Owner, Name: ref.Name}, repositoryRemote(ref.Platform, ref.Host, remoteURL))
}

func repositoryRemote(kind Kind, host, remoteURL string) string {
	if kind != KindBitbucket || strings.EqualFold(host, DefaultBitbucketHost) {
		return remoteURL
	}
	u, err := url.Parse(remoteURL)
	if err != nil || u.Host == "" {
		if !strings.Contains(remoteURL, "://") && strings.Contains(remoteURL, "@") {
			prefix, path, ok := strings.Cut(remoteURL, ":")
			if ok {
				u, err = url.Parse("ssh://" + prefix + "/" + path)
			}
		}
		if err != nil || u == nil || u.Host == "" {
			return remoteURL
		}
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if strings.HasPrefix(u.Path, "/scm/") && strings.Count(strings.Trim(u.Path, "/"), "/") == 2 {
			u.Path = strings.TrimPrefix(u.Path, "/scm")
			u.RawPath = ""
		}
	case "ssh", "git+ssh", "ssh+git":
		authority, err := url.Parse("https://" + host)
		if err == nil && strings.EqualFold(u.Hostname(), authority.Hostname()) {
			u.Host = host
		}
	}
	return u.String()
}
