// Package projects holds project-registry helpers that sit between the
// generic db layer and the HTTP/Huma handlers. The package owns identity
// resolution from local repository paths and any other non-trivial logic
// that does not belong directly in the db package.
package projects

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/db"
	platformpkg "go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/procutil"
)

type KnownPlatformHost struct {
	Platform string
	Host     string
}

// ResolveIdentityFromPath runs `git remote get-url origin` in path and parses
// the result into an optional PlatformIdentity. Returns (nil, nil) when no
// origin remote is configured, when path is not a git repository, or when
// the URL does not match a recognized pattern. The contract is identity-
// only: validation that path exists and is a git repository is the caller's
// responsibility. Per the consolidation spec's Decision 7, unknown identity
// is allowed and does not block registration.
func ResolveIdentityFromPath(ctx context.Context, path string) (*db.PlatformIdentity, error) {
	return ResolveIdentityFromPathWithKnownPlatforms(ctx, path, nil)
}

func ResolveIdentityFromPathWithKnownPlatforms(
	ctx context.Context,
	path string,
	known []KnownPlatformHost,
) (*db.PlatformIdentity, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	cmd := procutil.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = abs
	cmd.Env = gitenv.StripAll(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// `git remote get-url origin` exits non-zero when origin is
			// unset; treat as a clean no-identity result.
			return nil, nil
		}
		return nil, fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseRemoteURLWithKnownPlatforms(string(out), known), nil
}

// ParseRemoteURL recognizes the common remote URL shapes (HTTPS, SSH SCP-style,
// ssh://) and extracts a PlatformIdentity. Returns nil for anything it does
// not recognize, including local paths, file:// URLs, unknown self-hosted
// hosts, and nested owner paths on platforms that do not support them.
func ParseRemoteURL(remote string) *db.PlatformIdentity {
	return ParseRemoteURLWithKnownPlatforms(remote, nil)
}

func ParseRemoteURLWithKnownPlatforms(remote string, known []KnownPlatformHost) *db.PlatformIdentity {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return nil
	}

	host, path := splitRemote(remote)
	if host == "" || path == "" {
		return nil
	}

	host = strings.ToLower(host)
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	platform, ok := resolvePlatform(host, known)
	if !ok {
		return nil
	}
	if len(parts) < 2 || (len(parts) > 2 && !platformpkg.AllowsNestedOwner(platformpkg.Kind(platform))) {
		return nil
	}
	owner, name := strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
	if owner == "" || name == "" {
		return nil
	}

	return &db.PlatformIdentity{
		Platform: platform,
		Host:     host,
		Owner:    strings.ToLower(owner),
		Name:     strings.ToLower(name),
	}
}

func resolvePlatform(host string, known []KnownPlatformHost) (string, bool) {
	for _, candidate := range known {
		if strings.EqualFold(candidate.Host, host) && strings.TrimSpace(candidate.Platform) != "" {
			return strings.ToLower(strings.TrimSpace(candidate.Platform)), true
		}
	}
	for _, kind := range []platformpkg.Kind{
		platformpkg.KindGitHub,
		platformpkg.KindGitLab,
		platformpkg.KindForgejo,
		platformpkg.KindGitea,
	} {
		defaultHost, ok := platformpkg.DefaultHost(kind)
		if ok && strings.EqualFold(defaultHost, host) {
			return string(kind), true
		}
	}
	return "", false
}

// splitRemote separates a remote URL into (host, path) without hard-coding
// any specific platform. It accepts the three common shapes:
//
//	scheme://[user@]host[:port]/path
//	user@host:path     (SCP-style)
//	host:path          (rejected — no user@ disambiguator from local paths)
func splitRemote(remote string) (host, path string) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", ""
		}
		if u.Scheme == "file" {
			return "", ""
		}
		if u.Scheme == "ssh" && u.Port() == "22" {
			return u.Hostname(), strings.TrimPrefix(u.Path, "/")
		}
		return u.Host, strings.TrimPrefix(u.Path, "/")
	}

	atIdx := strings.Index(remote, "@")
	colonIdx := strings.Index(remote, ":")
	if atIdx > 0 && colonIdx > atIdx {
		return remote[atIdx+1 : colonIdx], remote[colonIdx+1:]
	}
	return "", ""
}
