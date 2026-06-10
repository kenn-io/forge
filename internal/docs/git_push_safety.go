package docs

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
)

// pushTargetClass describes where the receive side of a push executes.
type pushTargetClass int

const (
	// pushTargetNetwork is a remote reached over a network transport;
	// the receive side runs on another machine (or a daemon the user
	// operates), not as a child process under the docs repo's control.
	pushTargetNetwork pushTargetClass = iota
	// pushTargetLocal is a filesystem path: git push spawns
	// git-receive-pack on this machine against the target repository, so
	// the target's hooks and config would execute locally. runPush must
	// neutralize them via localReceivePack.
	pushTargetLocal
)

// assertPushTargetSafe resolves the effective push URLs of the upstream
// remote (after remote.<name>.pushurl and url.*.insteadOf / pushInsteadOf
// rewrites) and rejects targets whose receive side would execute
// repo-controlled code:
//
//   - any URL resolving to a path inside the docs folder: a repository
//     shipped within the untrusted folder has attacker-controlled hooks
//     and config;
//   - remote-helper transports (schemes beyond http(s)/ssh/git, or
//     <transport>::<address> syntax): they invoke git-remote-<transport>
//     with a repo-chosen address.
//
// Local paths outside the folder stay allowed — pushing to a personal
// bare mirror is a real workflow — because runPush neutralizes the
// target's command surfaces with localReceivePack.
func assertPushTargetSafe(ctx context.Context, root, remote string) (pushTargetClass, error) {
	urls, err := remotePushURLs(ctx, root, remote)
	if err != nil {
		return 0, err
	}
	var localURLs, networkURLs []string
	for _, raw := range urls {
		c, err := classifyPushURL(root, raw)
		if err != nil {
			return 0, err
		}
		if c == pushTargetLocal {
			localURLs = append(localURLs, raw)
		} else {
			networkURLs = append(networkURLs, raw)
		}
	}
	// `git push <remote>` contacts every push URL in one invocation and
	// the --receive-pack hardening for local targets is per-invocation:
	// applied to a mixed set it would also be sent to the network remotes
	// as the remote command, where it either breaks the push or points
	// the server's hooksPath at a nonexistent directory and silently
	// disables its receive-side hooks. Refuse mixed sets instead.
	if len(localURLs) > 0 && len(networkURLs) > 0 {
		return 0, &UnsafeGitConfigError{Entries: []string{fmt.Sprintf(
			"remote %s mixes local (%s) and network (%s) push urls",
			remote, strings.Join(localURLs, ", "), strings.Join(networkURLs, ", "),
		)}}
	}
	if len(localURLs) > 0 {
		return pushTargetLocal, nil
	}
	return pushTargetNetwork, nil
}

// remotePushURLs lists every URL `git push <remote>` would contact. The
// upstream may also name a bare URL or path instead of a configured
// remote; get-url fails for those and the string itself is the one push
// target.
func remotePushURLs(ctx context.Context, root, remote string) ([]string, error) {
	cmd, err := gitCommand(ctx, root, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return nil, err
	}
	out, err := procutil.Output(ctx, cmd, "resolving docs git push url")
	if err != nil {
		return []string{remote}, nil
	}
	var urls []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			urls = append(urls, line)
		}
	}
	if len(urls) == 0 {
		return []string{remote}, nil
	}
	return urls, nil
}

func classifyPushURL(root, raw string) (pushTargetClass, error) {
	if scheme, rest, ok := strings.Cut(raw, "://"); ok {
		switch strings.ToLower(scheme) {
		case "http", "https", "ssh", "git":
			return pushTargetNetwork, nil
		case "file":
			return classifyLocalPushPath(root, raw, fileURLPath(rest))
		default:
			return 0, unsafePushTarget(raw, "remote helper transports are not allowed")
		}
	}
	if head, _, ok := strings.Cut(raw, "::"); ok && !strings.Contains(head, "/") {
		// <transport>::<address> remote-helper syntax (ext:: and friends).
		return 0, unsafePushTarget(raw, "remote helper transports are not allowed")
	}
	if gitParsesDrivePaths && hasDriveLetterPrefix(raw) {
		// Drive-letter paths (C:\docs, C:relative) would otherwise match
		// the scp-like colon rule below and dodge containment and
		// receive-pack hardening.
		return classifyLocalPushPath(root, raw, raw)
	}
	if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.Contains(raw[:colon], "/") {
		// scp-like user@host:path — ssh, receive side runs remotely.
		return pushTargetNetwork, nil
	}
	return classifyLocalPushPath(root, raw, raw)
}

// gitParsesDrivePaths mirrors git's has_dos_drive_prefix: only on Windows
// does a leading drive letter make a remote URL a filesystem path — on
// other platforms `c:/x` is an scp-like ssh URL for host `c`. A variable
// so tests can exercise the Windows branch on any host OS.
var gitParsesDrivePaths = runtime.GOOS == "windows"

func hasDriveLetterPrefix(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// fileURLPath extracts the filesystem path from the remainder of a
// file:// URL. Git accepts an empty host or "localhost"; anything else is
// returned as-is and ends up rejected by the containment check (git would
// refuse such a URL anyway).
func fileURLPath(rest string) string {
	if host, path, ok := strings.Cut(rest, "/"); ok && (host == "" || strings.EqualFold(host, "localhost")) {
		if gitParsesDrivePaths && hasDriveLetterPrefix(path) {
			// file:///C:/docs — the slash before the drive letter is URL
			// syntax, not part of the filesystem path.
			return path
		}
		return "/" + path
	}
	return rest
}

func classifyLocalPushPath(root, displayURL, p string) (pushTargetClass, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		// Relative remote URLs resolve against the repo root: gitCommand
		// runs every push with its working directory there.
		abs = filepath.Join(root, abs)
	}
	if pathWithin(resolveBestEffort(root), resolveBestEffort(abs)) {
		return 0, unsafePushTarget(displayURL, "push target resolves inside the docs folder")
	}
	return pushTargetLocal, nil
}

// resolveBestEffort canonicalizes a path through symlinks; when the leaf
// does not exist yet, the deepest existing ancestor is resolved instead so
// a symlinked parent cannot disguise where the target really lives.
func resolveBestEffort(p string) string {
	clean := filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		return r
	}
	dir := filepath.Dir(clean)
	if dir == clean {
		return clean
	}
	return filepath.Join(resolveBestEffort(dir), filepath.Base(clean))
}

func unsafePushTarget(url, why string) error {
	return &UnsafeGitConfigError{
		Entries: []string{fmt.Sprintf("push url %s (%s)", url, why)},
	}
}

// localReceivePack builds the --receive-pack command used for pushes to
// local path remotes. The pushing process's safe config overrides do not
// reach the receive side — a pre-receive hook in the target repo fires
// despite -c core.hooksPath on the push (verified empirically) — so the
// overrides are pinned on the receive-pack invocation itself: hooks are
// redirected to an empty dir, fsmonitor stays off, denyCurrentBranch
// keeps its safe default of refuse so the target cannot opt into worktree
// updates (push-to-checkout / updateInstead would run target hooks and
// filters), and auto-gc is disabled so no further git child processes run
// under the target's config.
func localReceivePack() (string, error) {
	hooksDir, err := emptyHooksDir()
	if err != nil {
		return "", err
	}
	return "git -c core.hooksPath='" + hooksDir + "'" +
		" -c core.fsmonitor=false" +
		" -c receive.denyCurrentBranch=refuse" +
		" -c receive.autogc=false receive-pack", nil
}
