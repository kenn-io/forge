# GH Auth Hostname Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Scope `gh auth token` resolution by hostname so a GHE-only user with `gh auth login --hostname ghe.example` resolves the right token without setting `MIDDLEMAN_GITHUB_TOKEN`.

**Architecture:** Replace `ghAuthToken()` in `internal/config/config.go` with `ghAuthTokenForHost(host)` that calls `gh auth token --hostname <host>` under a 5-second context timeout. Add a method `(*Config).gitHubTokenForHost(host)` that preserves the env-var-then-`gh` contract and thread it through `cfg.GitHubToken()` and `cfg.TokenForPlatformHost`. Retry bare `gh auth token` only when `--hostname github.com` is rejected by an older `gh` (detected from stderr).

**Tech Stack:** Go 1.x, `os/exec` with `context.WithTimeout`, testify, table-driven tests, PATH-based fake-`gh` shell script seam already established in `config_test.go`.

---

## File Structure

- `internal/config/config.go` — package-private `execCommand` variable, `ghAuthTokenForHost`, `(*Config).gitHubTokenForHost`, `isUnsupportedHostnameFlag`; updates to `(*Config).GitHubToken` and `(*Config).TokenForPlatformHost`.
- `internal/config/config_test.go` — extend the fake-`gh` helper to capture argv and emit controlled exit-code/stderr; add new tests for host-scoped resolution, older-`gh` retry, and timeout.

No new files. No new packages. No changes outside `internal/config/`.

---

## Task 1: Wire the test seam to a context-aware exec

**Files:**
- Modify: `internal/config/config.go` (the `var execCommand = exec.Command` line at ~1090 and `ghAuthToken` at ~1092)

The current `var execCommand = exec.Command` cannot pass a context through. Switch it to `exec.CommandContext` and update the (still-host-agnostic) `ghAuthToken` to thread `context.Background()` so existing tests keep passing while the seam is ready for Task 2. This is the smallest behavior-preserving change.

- [ ] **Step 1: Run existing config tests to capture the green baseline**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGitHubToken' -shuffle=on`
Expected: PASS for `TestGitHubToken`, `TestGitHubTokenFallsBackToGHCli`, `TestGitHubTokenPrefersEnvVarOverGHCli`, `TestGitHubTokenReturnsEmptyWhenGHCliUnavailable`.

- [ ] **Step 2: Change `execCommand` to `exec.CommandContext` and update `ghAuthToken`**

In `internal/config/config.go`, find:

```go
var execCommand = exec.Command

func ghAuthToken() string {
	out, err := execCommand("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

Replace with:

```go
var execCommand = exec.CommandContext

func ghAuthToken() string {
	out, err := execCommand(context.Background(), "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

Add `"context"` to the import block at the top of the file (alphabetical with the other stdlib imports between `"bytes"` and `"errors"`).

- [ ] **Step 3: Run the same tests, still green**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGitHubToken' -shuffle=on`
Expected: PASS. The fake `gh` script under `PATH` still gets invoked the same way; only the function variable's signature changed.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor(config): switch gh exec seam to CommandContext"
```

---

## Task 2: Introduce `ghAuthTokenForHost` with `--hostname` and timeout

**Files:**
- Modify: `internal/config/config.go` (replace `ghAuthToken`, add helpers, leave `(*Config).GitHubToken` and `(*Config).TokenForPlatformHost` alone for now)
- Test: `internal/config/config_test.go` (extend `setFakeGHCLI`, add new tests)

This task introduces the new helper and tests it directly. `GitHubToken`/`TokenForPlatformHost` keep calling the old name in this task so existing behavior is unchanged; Task 3 wires them in.

- [ ] **Step 1: Extend the fake-`gh` helper with argv capture, exit code, stderr, and sleep**

In `internal/config/config_test.go`, replace the existing `setFakeGHCLI`:

```go
func setFakeGHCLI(t *testing.T, stdout string) {
	t.Helper()
	setFakeGHCLIScript(t, fakeGHCLIOptions{Stdout: stdout})
}

type fakeGHCLIOptions struct {
	// Stdout is echoed verbatim on success (default exit 0).
	Stdout string
	// Stderr is echoed to stderr regardless of exit code.
	Stderr string
	// ExitCode is the exit status of the fake gh.
	ExitCode int
	// SleepSeconds, if >0, makes the fake sleep before exiting.
	SleepSeconds int
}

// setFakeGHCLIScript writes a fake `gh` to a temp dir and points PATH
// at it. The fake records its argv to <tempdir>/argv (newline-separated
// across invocations), then emits stdout/stderr/exit per opts. The
// path of the argv file is returned via env var FAKE_GH_ARGV so the
// caller can read it.
func setFakeGHCLIScript(t *testing.T, opts fakeGHCLIOptions) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	ghPath := filepath.Join(dir, "gh")
	script := "#!/bin/sh\n"
	script += "printf '%s\\n' \"$*\" >> \"$FAKE_GH_ARGV\"\n"
	if opts.SleepSeconds > 0 {
		script += fmt.Sprintf("sleep %d\n", opts.SleepSeconds)
	}
	if opts.Stderr != "" {
		script += "printf '%s\\n' " + shellSingleQuote(opts.Stderr) + " 1>&2\n"
	}
	if opts.Stdout != "" {
		script += "printf '%s\\n' " + shellSingleQuote(opts.Stdout) + "\n"
	}
	script += fmt.Sprintf("exit %d\n", opts.ExitCode)
	require.NoError(t, os.WriteFile(ghPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GH_ARGV", argvPath)
	return argvPath
}

// shellSingleQuote escapes s for safe inclusion inside single quotes
// in a /bin/sh script.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// readFakeGHArgv returns the recorded argv strings (one entry per
// invocation, in call order). Returns nil if no calls were made.
func readFakeGHArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	raw := strings.TrimRight(string(data), "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}
```

Add `"errors"`, `"fmt"`, and `"strings"` to the test file's import block if missing (`fmt` and `strings` are already imported elsewhere — check the existing import block at the top of `config_test.go` and add what's not there).

- [ ] **Step 2: Write the first failing test — `ghAuthTokenForHost` passes `--hostname github.com`**

Append to `config_test.go`:

```go
func TestGhAuthTokenForHostPassesHostnameFlag(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "gh-secret-github",
	})

	got := ghAuthTokenForHost("github.com")
	Assert.Equal(t, "gh-secret-github", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGhAuthTokenForHostPassesHostnameFlag' -shuffle=on`
Expected: FAIL with `undefined: ghAuthTokenForHost`.

- [ ] **Step 4: Implement `ghAuthTokenForHost` and the older-`gh` detection helper**

In `internal/config/config.go`, replace the existing `ghAuthToken` function with:

```go
// ghAuthExecTimeout bounds each gh subprocess invocation. gh auth token
// is local (no network) and returns in milliseconds; 5s is generous.
const ghAuthExecTimeout = 5 * time.Second

// ghAuthTokenForHost returns the token gh has stored for host, or "".
// On older gh that does not recognize --hostname, this falls back to
// bare `gh auth token` only when host is github.com — any other host
// returns empty without retry so the caller surfaces a missing-token
// error rather than the wrong host's token.
func ghAuthTokenForHost(host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ghAuthExecTimeout)
	defer cancel()

	out, stderr, err := runGHAuthToken(ctx, "--hostname", host)
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	if host == platformpkg.DefaultGitHubHost && isUnsupportedHostnameFlag(err, stderr) {
		out, _, err = runGHAuthToken(ctx)
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

// runGHAuthToken invokes `gh auth token` with the given extra args
// under ctx, returning stdout, stderr, and any exec error.
func runGHAuthToken(ctx context.Context, extraArgs ...string) ([]byte, []byte, error) {
	args := append([]string{"auth", "token"}, extraArgs...)
	cmd := execCommand(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.Bytes(), err
}

// isUnsupportedHostnameFlag reports whether the gh invocation failed
// specifically because the installed gh does not recognize the
// --hostname flag (cobra/pflag rejection text). Missing-binary,
// context-deadline, auth-failure, and other nonzero exits all return
// false so the caller does not retry bare.
func isUnsupportedHostnameFlag(err error, stderr []byte) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	text := string(stderr)
	return strings.Contains(text, "unknown flag: --hostname") ||
		strings.Contains(text, "unknown shorthand flag")
}
```

Delete the old `ghAuthToken` function entirely. `GitHubToken` still references `ghAuthToken` though — update it temporarily so the file compiles:

```go
func (c *Config) GitHubToken() string {
	if token := os.Getenv(c.GitHubTokenEnv); token != "" {
		return token
	}
	return ghAuthTokenForHost(platformpkg.DefaultGitHubHost)
}
```

(Task 3 will replace this with `c.gitHubTokenForHost(...)`.)

- [ ] **Step 5: Run the new test — passes**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGhAuthTokenForHostPassesHostnameFlag' -shuffle=on`
Expected: PASS.

- [ ] **Step 6: Run the existing tests — still pass**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGitHubToken' -shuffle=on`
Expected: PASS. `TestGitHubTokenFallsBackToGHCli` (which used the old `setFakeGHCLI` shape) now exercises the new helper via `GitHubToken`, and the fake's stdout still resolves the token.

- [ ] **Step 7: Add the older-`gh` retry tests**

Append to `config_test.go`:

```go
func TestGhAuthTokenForHostRetriesBareWhenOldGHRejectsHostnameFlag(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	ghPath := filepath.Join(dir, "gh")
	// Older gh rejects --hostname; bare succeeds.
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_ARGV"
case "$*" in
*--hostname*)
	printf 'unknown flag: --hostname\n' 1>&2
	exit 2
	;;
*)
	printf 'gh-secret-bare\n'
	exit 0
	;;
esac
`
	require.NoError(t, os.WriteFile(ghPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GH_ARGV", argvPath)

	got := ghAuthTokenForHost("github.com")
	Assert.Equal(t, "gh-secret-bare", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 2)
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
	Assert.Equal(t, "auth token", argv[1])
}

func TestGhAuthTokenForHostDoesNotRetryBareOnAuthFailure(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stderr:   "no oauth token",
		ExitCode: 1,
	})

	got := ghAuthTokenForHost("github.com")
	Assert.Empty(t, got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1, "should not retry bare on non-flag-rejection failure")
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}

func TestGhAuthTokenForHostDoesNotRetryBareOnGHEFlagRejection(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	ghPath := filepath.Join(dir, "gh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_ARGV"
printf 'unknown flag: --hostname\n' 1>&2
exit 2
`
	require.NoError(t, os.WriteFile(ghPath, []byte(script), 0o755))
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GH_ARGV", argvPath)

	got := ghAuthTokenForHost("ghe.example.com")
	Assert.Empty(t, got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1, "non-github.com host must not retry bare")
	Assert.Equal(t, "auth token --hostname ghe.example.com", argv[0])
}

func TestGhAuthTokenForHostReturnsEmptyWhenBinaryMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	Assert.Empty(t, ghAuthTokenForHost("github.com"))
}
```

- [ ] **Step 8: Run all the new ghAuthTokenForHost tests — pass**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGhAuthTokenForHost' -shuffle=on`
Expected: PASS.

- [ ] **Step 9: Add the timeout test**

Append to `config_test.go`:

```go
func TestGhAuthTokenForHostTimesOut(t *testing.T) {
	// Fake gh sleeps longer than the timeout, so the helper must
	// return "" once the context expires.
	setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout:       "never-reached",
		SleepSeconds: 10,
	})

	start := time.Now()
	got := ghAuthTokenForHost("github.com")
	elapsed := time.Since(start)

	Assert.Empty(t, got)
	Assert.Less(
		t, elapsed, ghAuthExecTimeout+2*time.Second,
		"helper should return shortly after timeout, took %s", elapsed,
	)
}
```

Add `"time"` to the test file's import block if it isn't already there (it should already be present).

- [ ] **Step 10: Run the timeout test**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestGhAuthTokenForHostTimesOut' -shuffle=on`
Expected: PASS. The whole test should complete within roughly the timeout (5s + small slack).

- [ ] **Step 11: Run the entire config package**

Run: `nix run nixpkgs#go -- test ./internal/config -shuffle=on`
Expected: PASS for the whole package, including the original `TestGitHubToken*` tests.

- [ ] **Step 12: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add host-scoped gh auth token helper"
```

---

## Task 3: Thread the host through `TokenForPlatformHost` and `GitHubToken`

**Files:**
- Modify: `internal/config/config.go` (`(*Config).GitHubToken` and `(*Config).TokenForPlatformHost`; add `(*Config).gitHubTokenForHost`)
- Test: `internal/config/config_test.go` (new tests for GHE-host resolution and ordering)

The helper from Task 2 is in place. Now teach `TokenForPlatformHost` to call it with the right host for GitHub-platform repos, and fold the env-var-then-`gh` contract into a single method so both `GitHubToken()` and `TokenForPlatformHost` go through it.

- [ ] **Step 1: Write the failing GHE-host test**

Append to `config_test.go`:

```go
func TestTokenForPlatformHostUsesGHWithHostnameForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-secret",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-secret", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname ghe.example.com", argv[0])
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestTokenForPlatformHostUsesGHWithHostnameForGHE' -shuffle=on`
Expected: FAIL. Today `TokenForPlatformHost` calls `c.GitHubToken()` which, after Task 2, invokes `ghAuthTokenForHost(platformpkg.DefaultGitHubHost)` — so the argv will be `auth token --hostname github.com`, not the GHE host.

- [ ] **Step 3: Add `(*Config).gitHubTokenForHost` and thread the host through**

In `internal/config/config.go`, change `(*Config).GitHubToken` and `(*Config).TokenForPlatformHost`, and add the new method.

Locate:

```go
func (c *Config) GitHubToken() string {
	if token := os.Getenv(c.GitHubTokenEnv); token != "" {
		return token
	}
	return ghAuthTokenForHost(platformpkg.DefaultGitHubHost)
}
```

Replace with:

```go
func (c *Config) GitHubToken() string {
	return c.gitHubTokenForHost(platformpkg.DefaultGitHubHost)
}

// gitHubTokenForHost resolves a github token for host. It checks the
// configured env var first, then falls back to `gh auth token
// --hostname <host>`. Internal helper; callers go through GitHubToken
// or TokenForPlatformHost.
func (c *Config) gitHubTokenForHost(host string) string {
	if token := os.Getenv(c.GitHubTokenEnv); token != "" {
		return token
	}
	return ghAuthTokenForHost(host)
}
```

Locate the github-fallback branch in `TokenForPlatformHost`:

```go
	if p == defaultPlatform {
		return c.GitHubToken()
	}
```

Replace with:

```go
	if p == defaultPlatform {
		return c.gitHubTokenForHost(h)
	}
```

(`h` is already in scope from the earlier `normalizePlatformHost(p, host)` call.)

- [ ] **Step 4: Run the new test — passes**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestTokenForPlatformHostUsesGHWithHostnameForGHE' -shuffle=on`
Expected: PASS.

- [ ] **Step 5: Add ordering tests for the GHE host**

Append to `config_test.go`:

```go
func TestTokenForPlatformHostPrefersEnvOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "ghe-from-env")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-from-env", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "env var should short-circuit gh")
}

func TestTokenForPlatformHostPrefersPlatformsEntryOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	t.Setenv("PLATFORMS_GHE_TOKEN", "ghe-from-platforms")

	cfg := &Config{
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Platforms: []PlatformConfig{
			{Type: "github", Host: "ghe.example.com", TokenEnv: "PLATFORMS_GHE_TOKEN"},
		},
	}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "")
	Assert.Equal(t, "ghe-from-platforms", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "[[platforms]] should short-circuit gh")
}

func TestTokenForPlatformHostPrefersRepoTokenEnvOverGHForGHE(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "ghe-from-gh",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	t.Setenv("REPO_GHE_TOKEN", "ghe-from-repo")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.TokenForPlatformHost("github", "ghe.example.com", "REPO_GHE_TOKEN")
	Assert.Equal(t, "ghe-from-repo", got)

	Assert.Empty(t, readFakeGHArgv(t, argvPath), "repo token_env should short-circuit gh")
}

func TestGitHubTokenInvokesGHWithGithubComHostname(t *testing.T) {
	argvPath := setFakeGHCLIScript(t, fakeGHCLIOptions{
		Stdout: "default-host-secret",
	})
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")

	cfg := &Config{GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN"}
	got := cfg.GitHubToken()
	Assert.Equal(t, "default-host-secret", got)

	argv := readFakeGHArgv(t, argvPath)
	require.Len(t, argv, 1)
	Assert.Equal(t, "auth token --hostname github.com", argv[0])
}
```

- [ ] **Step 6: Run the ordering tests**

Run: `nix run nixpkgs#go -- test ./internal/config -run 'TestTokenForPlatformHostPrefers|TestGitHubTokenInvokesGHWithGithubComHostname' -shuffle=on`
Expected: PASS for all four.

- [ ] **Step 7: Run the full config package**

Run: `nix run nixpkgs#go -- test ./internal/config -shuffle=on`
Expected: PASS for the whole package.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): scope gh auth token fallback by hostname"
```

---

## Task 4: Final verification across the repo

**Files:** none modified; this task is verification only.

Make sure nothing outside `internal/config/` regressed (no caller depends on the old host-agnostic `ghAuthToken` shape), lint is clean, and full tests pass.

- [ ] **Step 1: Confirm no other package references the removed symbol**

Run: `nix run nixpkgs#ripgrep -- -n '\bghAuthToken\b' internal cmd`
Expected: no matches (the helper was renamed to `ghAuthTokenForHost`). If any match shows up, fix it before continuing.

- [ ] **Step 2: Run `go vet`**

Run: `make vet`
Expected: clean.

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: clean. If golangci-lint flags anything in the touched files, fix it.

- [ ] **Step 4: Run the full Go test suite**

Run: `make test`
Expected: PASS. The `-shuffle=on` flag is already on the make target.

- [ ] **Step 5: Commit any lint fixes**

If Steps 2-3 required edits, commit them:

```bash
git add -p internal/config
git commit -m "chore(config): satisfy lint on host-scoped gh helper"
```

Otherwise nothing to commit; skip.

---

## Self-Review Notes

- **Spec coverage:**
  - D1 (host-scoped helper) — Task 2 Step 4 (`ghAuthTokenForHost` replaces `ghAuthToken`).
  - D2 (thread host through `TokenForPlatformHost`) — Task 3 Steps 3 and 4 (`gitHubTokenForHost`, updated `TokenForPlatformHost` branch).
  - D3 (older-`gh` retry only for `github.com`) — Task 2 Steps 4 (implementation) and 7 (`TestGhAuthTokenForHostDoesNotRetryBareOnGHEFlagRejection`).
  - D4 (5-second timeout) — Task 2 Step 4 (`ghAuthExecTimeout`) and Step 9 (`TestGhAuthTokenForHostTimesOut`).
  - D5 (narrow detection of older-`gh` case, capture stderr explicitly) — Task 2 Step 4 (`isUnsupportedHostnameFlag` checks for `*exec.ExitError` and the two cobra/pflag substrings; `runGHAuthToken` sets `cmd.Stderr = &stderr` so the rejection text is visible).
  - D6 (test seam) — Task 1 Steps 2 (the `var execCommand` change) and Task 2 Step 1 (`setFakeGHCLIScript` extends the PATH-based fake with argv capture, exit code, and stderr; no function-replacement seam introduced).
  - Architecture: `TokenForPlatformHost -> gitHubTokenForHost -> ghAuthTokenForHost` — Task 3 Step 3.
  - Risks (legacy `gh` on `github.com`) — Task 2 Step 7 explicitly tests `TestGhAuthTokenForHostRetriesBareWhenOldGHRejectsHostnameFlag`.
  - Spec test list: every named test in the spec ("Testing" section) maps to a step above.
- **Placeholder scan:** no TBDs, no "appropriate error handling", every code step has the actual code.
- **Type consistency:** `ghAuthTokenForHost(host string) string`, `(*Config).gitHubTokenForHost(host string) string`, `runGHAuthToken(ctx context.Context, extraArgs ...string) ([]byte, []byte, error)`, `isUnsupportedHostnameFlag(err error, stderr []byte) bool`, `setFakeGHCLIScript(t *testing.T, opts fakeGHCLIOptions) string`, `readFakeGHArgv(t *testing.T, path string) []string`. All names match across tasks.

---

## Out of Scope (Surfaced as Follow-Ups, Not Pulled In)

- Renaming or restructuring the rest of the token resolution chain.
- New config knobs to control the fallback.
- Equivalent CLI-fallback behavior for Forgejo / Gitea / GitLab (those providers don't ship a `gh`-style CLI today).
- Context-aware token resolution end-to-end (the helper can later accept a caller-supplied `ctx` without breaking the exported API).
- UI/API surface changes (none needed).
