package docs

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
)

// UnsafeGitConfigError reports that a docs repository carries git
// configuration or attributes that would let the repo execute a command
// during publish (git add/commit/push). Docs folders are user data, not
// trusted code, so publish refuses these repos rather than running them.
//
// Hooks, fsmonitor, and the ext transport are neutralized unconditionally
// by safeGitConfigArgs and are therefore not part of this gate; this gate
// covers the command-bearing surfaces that cannot be safely overridden
// without breaking legitimate global auth (filters, signing programs,
// credential helpers, SSH command, external diff/textconv).
type UnsafeGitConfigError struct {
	Entries []string
}

func (e *UnsafeGitConfigError) Error() string {
	return fmt.Sprintf(
		"docs publish refuses repositories with command-bearing git config or attributes: %s. "+
			"Remove the repo-local config/attributes (for example git-lfs filters) to publish.",
		strings.Join(e.Entries, ", "),
	)
}

// assertSafeToPublish rejects a docs repo whose local config opts into
// command execution. It is called before any staging or commit so an
// untrusted repo never reaches an executing git command. Attribute-driven
// filter/diff drivers are checked separately, before git status runs, by
// assertWorktreeAttributesSafe.
func assertSafeToPublish(ctx context.Context, root string) error {
	entries, err := unsafeGitConfigEntries(ctx, root)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return &UnsafeGitConfigError{Entries: entries}
	}
	return nil
}

// assertWorktreeAttributesSafe rejects a docs repo when any worktree path
// resolves to a filter or external diff attribute. It MUST run before any
// git command that can rehash worktree content (git status, git add):
// such a command executes the globally-configured filter program the
// repo's .gitattributes selects, so the gate has to precede it rather than
// the publish set computed from git status output. Path enumeration (git
// ls-files) and attribute resolution (git check-attr) never run a filter
// program, so the gate itself is safe to call first.
func assertWorktreeAttributesSafe(ctx context.Context, root string) error {
	paths, err := worktreePaths(ctx, root)
	if err != nil {
		return err
	}
	return assertPathsAttributesSafe(ctx, root, paths)
}

// worktreePaths lists every tracked and untracked (non-ignored) path in the
// docs repo. git ls-files reads the index and directory listing only; it
// never runs a filter, so it is safe to call before the attribute gate.
func worktreePaths(ctx context.Context, root string) ([]string, error) {
	cmd, err := gitCommand(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	out, err := procutil.Output(ctx, cmd, "listing docs worktree paths")
	if err != nil {
		return nil, fmt.Errorf("listing docs worktree paths: %w", err)
	}
	var paths []string
	for path := range strings.SplitSeq(string(out), "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// assertPathsAttributesSafe rejects publish when any of the given paths
// resolves to a filter or external diff attribute. git check-attr honors
// every attribute source git itself uses (root and subdirectory
// .gitattributes, .git/info/attributes, core.attributesFile) and runs no
// filter program. Paths are fed on stdin so a large worktree cannot blow
// the command-line argument limit.
func assertPathsAttributesSafe(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	cmd, err := gitCommand(ctx, root, "check-attr", "-z", "--stdin", "filter", "diff")
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := procutil.Output(ctx, cmd, "checking docs git attributes")
	if err != nil {
		return fmt.Errorf("checking docs git attributes: %w", err)
	}
	// Output is repeated NUL-separated triples: path, attribute, value.
	fields := strings.Split(string(out), "\x00")
	var entries []string
	for i := 0; i+2 < len(fields); i += 3 {
		path, attr, value := fields[i], fields[i+1], fields[i+2]
		if (attr == "filter" || attr == "diff") && attributeValueIsDriver(value) {
			entries = append(entries, fmt.Sprintf("attribute %s %s=%s", path, attr, value))
		}
	}
	if len(entries) > 0 {
		return &UnsafeGitConfigError{Entries: entries}
	}
	return nil
}

// attributeValueIsDriver reports whether a check-attr value names a driver
// (rather than git's "no driver" sentinels), i.e. a command would run.
func attributeValueIsDriver(value string) bool {
	switch value {
	case "", "unspecified", "unset", "set":
		return false
	default:
		return true
	}
}

func unsafeGitConfigEntries(ctx context.Context, root string) ([]string, error) {
	cmd, err := gitCommand(ctx, root, "config", "--local", "--list", "-z")
	if err != nil {
		return nil, err
	}
	out, err := procutil.Output(ctx, cmd, "reading docs git config")
	if err != nil {
		// A repo with no local config still exits 0 with empty output, so a
		// non-zero exit is a real failure worth surfacing.
		return nil, fmt.Errorf("reading docs git config: %w", err)
	}
	var entries []string
	for record := range strings.SplitSeq(string(out), "\x00") {
		if record == "" {
			continue
		}
		key, value, _ := strings.Cut(record, "\n")
		if isCommandBearingConfigKey(key, value) {
			entries = append(entries, "config "+key)
		}
	}
	return entries, nil
}

// isCommandBearingConfigKey reports whether a local config entry lets git
// run an external command during status/add/commit/push. core.hooksPath,
// core.fsmonitor, and protocol.*.allow are intentionally excluded:
// safeGitConfigArgs already overrides them on every invocation.
//
// This is a denylist of the program-valued keys git honors in the publish
// command set; new program-valued keys must be added here.
func isCommandBearingConfigKey(key, value string) bool {
	k := strings.ToLower(key)
	hasValue := strings.TrimSpace(value) != ""
	switch {
	// Repo-local include directives splice arbitrary extra config (for
	// example gpg.program or core.sshCommand) into every publish git
	// command while staying invisible to the no-includes listing this
	// gate reads, and includeIf conditions (such as onbranch:) can
	// activate only after the gate has run. Reject the directive itself
	// rather than trying to expand and re-check the included content.
	case k == "include.path",
		strings.HasPrefix(k, "includeif.") && strings.HasSuffix(k, ".path"):
		return hasValue
	case k == "core.sshcommand", k == "core.gitproxy", k == "core.askpass":
		return hasValue
	case k == "credential.helper" || (strings.HasPrefix(k, "credential.") && strings.HasSuffix(k, ".helper")):
		return hasValue
	case k == "gpg.program" || (strings.HasPrefix(k, "gpg.") && strings.HasSuffix(k, ".program")):
		return hasValue
	case k == "commit.gpgsign" || k == "tag.gpgsign":
		return isGitTrue(value)
	// Push runs git-receive-pack on the remote end; for local-path remotes
	// that program executes locally, so a repo-set value is a command
	// injection. uploadpack/vcs are the fetch/remote-helper equivalents.
	case strings.HasPrefix(k, "remote.") && hasAnySuffix(k, ".receivepack", ".uploadpack", ".vcs"):
		return hasValue
	case strings.HasPrefix(k, "filter.") && hasAnySuffix(k, ".clean", ".smudge", ".process"):
		return hasValue
	case strings.HasPrefix(k, "diff.") && hasAnySuffix(k, ".command", ".textconv"):
		return hasValue
	default:
		return false
	}
}

func isGitTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
