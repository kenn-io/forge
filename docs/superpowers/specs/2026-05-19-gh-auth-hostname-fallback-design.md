# Design: Scope `gh auth token` fallback by hostname

## Motivation

middleman supports multi-provider authentication. For GitHub-family hosts the
resolution chain is:

1. Repo-level `token_env` (if set on the `[[repos]]` entry).
2. `[[platforms]]` entry whose `(type, host)` matches the repo's
   `(platform, platform_host)` and has `token_env` set.
3. Provider default env var, e.g. `MIDDLEMAN_GITHUB_TOKEN`.
4. **`gh auth token`** — only reached today when the repo's host is
   `github.com` (and only via `cfg.GitHubToken()`).

The `gh auth token` invocation in `internal/config/config.go` is unscoped:

```go
func ghAuthToken() string {
    out, err := execCommand("gh", "auth", "token").Output()
    if err != nil {
        return ""
    }
    return strings.TrimSpace(string(out))
}
```

A user who has authenticated to GitHub Enterprise via
`gh auth login --hostname ghe.example` and configured a repo with
`platform_host = "ghe.example"` still has to set `MIDDLEMAN_GITHUB_TOKEN`
manually. The same user with multiple `gh` accounts (one for `github.com`,
one for `ghe.example`) silently gets whichever host `gh` happens to treat
as the default — which is rarely the GHE host. This is a common
onboarding paper-cut for GHE users.

Fix: pass the resolved hostname into `gh auth token --hostname <host>` so
each provider host resolves independently.

## Scope

In scope:

- Replace `ghAuthToken()` with a host-aware helper.
- Thread the resolved hostname through `cfg.TokenForPlatformHost` and
  `cfg.GitHubToken()`.
- Bound the subprocess at 5 seconds.
- Keep the older-`gh` (no `--hostname` flag) compatibility for `github.com`
  only.
- Test seam: make the subprocess injectable so unit tests stub `gh` without
  relying on `PATH` shenanigans.

Out of scope:

- Renaming or restructuring the rest of the token resolution chain.
- Adding new config knobs to control the fallback.
- Any platform other than GitHub. Forgejo, Gitea, and GitLab don't have a
  `gh`-style CLI fallback today and don't grow one here.
- UI surfaces. This is server-side resolution; no API or SPA change.

## Decisions

D1. **Host-scoped helper everywhere.** Replace `ghAuthToken()` with
`ghAuthTokenForHost(ctx, host)`. The exported wrapper `cfg.GitHubToken()`
becomes a thin call to `ghAuthTokenForHost(ctx, platform.DefaultGitHubHost)`.
Every call into `gh auth token` knows which host it is resolving.

D2. **Thread host through `TokenForPlatformHost`.** When the resolution
chain reaches the `gh` step for `(platform == github, host)`, call
`ghAuthTokenForHost(ctx, host)` rather than the previous host-agnostic
`cfg.GitHubToken()`. The behavior for a `github.com` repo is unchanged;
the behavior for a GHE host now reaches `gh auth token --hostname <ghe>`
instead of bare `gh auth token`.

D3. **Older-`gh` compatibility for `github.com` only.** If
`gh auth token --hostname github.com` fails specifically because the
installed `gh` does not recognize `--hostname`, retry bare `gh auth token`.
For any other host, do not retry. Other failure modes (missing binary,
context deadline, auth failure, invalid host, unrelated nonzero exit)
return empty without a bare retry.

D4. **Subprocess timeout of 5 seconds.** Wrap each subprocess in a
`context.WithTimeout(ctx, 5*time.Second)` so a hung `gh` cannot stall
startup or token refresh.

D5. **Narrow detection of the older-`gh` case.**
`isUnsupportedHostnameFlag(err, stderr)` requires both:

- `err` is `*exec.ExitError` (the subprocess actually ran and exited
  non-zero), AND
- `stderr` matches one of `unknown flag: --hostname` or
  `unknown shorthand flag` (the `cobra`/`pflag` rejection text).

Missing binary (`exec.LookPath` failure / `*exec.Error`), context deadline,
auth failure, or any other nonzero exit do not trigger the bare retry.

D6. **Injectable test seam.** Keep the `var execCommand = exec.CommandContext`
pattern. Tests stub it by replacing the variable with a fake that returns
controlled stdout/stderr/exit-code; no test depends on `PATH` for stubbing
`gh`, although we keep the existing PATH-based fakes for regression
coverage where they are clearer.

## Architecture

```
TokenForPlatformHost(platform, host, repoTokenEnv)
  -> repo token_env? -> return os.Getenv(repoTokenEnv)
  -> [[platforms]] match? -> return os.Getenv(pc.TokenEnv)
  -> provider default env? -> return os.Getenv(defaultEnv)
  -> platform == "github"? -> ghAuthTokenForHost(ctx, host)
                              -> exec.CommandContext(ctx, "gh", "auth",
                                                     "token",
                                                     "--hostname", host)
                              -> if host == "github.com" AND
                                    isUnsupportedHostnameFlag(err, stderr):
                                     retry bare exec.CommandContext(ctx, "gh",
                                                                    "auth",
                                                                    "token")
                              -> return strings.TrimSpace(stdout) or ""
  -> default: return ""

GitHubToken() = ghAuthTokenForHost(ctx, DefaultGitHubHost)
                — back-compat wrapper used by provider startup to seed the
                default github.com slot. Behavior is identical to today
                modulo the now-explicit --hostname github.com argument.
```

The 5-second timeout is created inside `ghAuthTokenForHost` so all callers
inherit it without plumbing a context through every site:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
```

Callers do not need to thread a context themselves; we are not introducing
context-aware token resolution to the rest of the codebase as part of this
change. (If the surrounding code grows context plumbing later, the helper
can accept an external context without a signature break — see "Future
work".)

## Components

- `internal/config/config.go`
  - Replace `ghAuthToken()` with `ghAuthTokenForHost(host string)` (no
    exported signature change; it's an internal helper).
  - Add `isUnsupportedHostnameFlag(err error, stderr []byte) bool`
    (file-local).
  - Change `var execCommand = exec.Command` to
    `var execCommand = exec.CommandContext`. The variable still serves as
    the test seam.
  - Update `cfg.GitHubToken()` to delegate to
    `ghAuthTokenForHost(platform.DefaultGitHubHost)`.
  - Update `cfg.TokenForPlatformHost` so the final-fallback path for
    `platform == "github"` calls `ghAuthTokenForHost(host)` instead of
    `cfg.GitHubToken()`.

- `internal/config/config_test.go`
  - Extend `setFakeGHCLI` (or add a sibling helper) so a fake `gh` can
    inspect its own argv. Add a helper that writes a richer fake `gh`
    script which emits a controlled exit code, stdout, and stderr based on
    the flags it receives.
  - New unit tests:
    - `TestTokenForPlatformHostUsesGHWithHostnameForGHE` —
      `gh --hostname ghe.example` returns the GHE token; no env var, no
      `[[platforms]]` entry.
    - `TestTokenForPlatformHostPrefersEnvOverGHForGHE` — env wins.
    - `TestTokenForPlatformHostPrefersPlatformsEntryOverGHForGHE` —
      `[[platforms]]` entry wins.
    - `TestTokenForPlatformHostPrefersRepoTokenEnvOverGHForGHE` —
      repo-level `token_env` wins.
    - `TestGitHubTokenInvokesGHWithGithubComHostname` — verifies
      `cfg.GitHubToken()` calls `gh auth token --hostname github.com`.
    - `TestGitHubTokenRetriesBareWhenOldGHRejectsHostnameFlag` — fake `gh`
      returns "unknown flag: --hostname" on `--hostname github.com`, then
      succeeds bare; helper returns the token.
    - `TestGitHubTokenDoesNotRetryBareOnAuthFailure` — fake `gh` returns
      nonzero with non-flag-rejection stderr; helper returns empty;
      assert the subprocess was called exactly once.
    - `TestGHEHostDoesNotRetryBareOnFlagRejection` — fake `gh` returns
      "unknown flag: --hostname" on `--hostname ghe.example`; helper
      returns empty; subprocess called exactly once.
    - `TestGHAuthTokenTimesOut` — fake `gh` sleeps longer than the
      configured timeout; helper returns empty in less than the timeout +
      slack.
  - Keep existing tests
    (`TestGitHubTokenFallsBackToGHCli`, `TestGitHubTokenReturnsEmptyWhenGHCliUnavailable`,
    `TestGitHubTokenPrefersEnvVarOverGHCli`). Their existing fake-`gh`
    scripts continue to work because the new helper still invokes `gh`
    via `execCommand`.

## Data flow

No persisted state changes. No DB migration. No API surface change. No
new env var or TOML key. The only behavioral difference visible to a
user is that `gh auth login --hostname ghe.example` now feeds the GHE
host's token resolution without `MIDDLEMAN_GITHUB_TOKEN` being set.

## Error handling

- Missing `gh` binary (`exec.LookPath` resolves to nothing, or the
  subprocess fails to start): return empty. Today's behavior.
- Subprocess exits non-zero with `unknown flag: --hostname` /
  `unknown shorthand flag` stderr, and host is `github.com`: retry bare.
  Today's behavior preserved.
- Same stderr but host is not `github.com`: return empty. No retry. The
  user sees the existing "no token for github host <ghe.example>" error
  at provider startup, which is the right surface.
- Subprocess context deadline exceeded (5s): return empty. Do not retry.
- Subprocess nonzero with any other stderr (auth failure, network error,
  malformed host): return empty. Do not retry.

All "return empty" paths fall through to the same missing-token error
already emitted by `collectProviderTokens`. We do not add new error
variants; the existing error already tells the user which platform/host
is unresolved and which env var would satisfy it.

## Testing

- All new tests use `t.TempDir()` for fake-`gh` scripts and `t.Setenv`
  for env-var control.
- Tests assert on subprocess invocation by writing the fake's argv to a
  side-channel file (the fake `gh` script does
  `echo "$@" > "$ARGV_FILE"`). The Go test reads that file to assert
  the expected flags were passed.
- Tests run with `-shuffle=on` (the make target already does this).
- No e2e tests against a real `gh` binary or a real GHE host. Live `gh`
  behavior is not the subject under test; the subject is middleman's
  invocation contract.

## Open questions

None. The brief constrains every meaningful design fork (host-scoped,
env-wins, repo-`token_env`-wins, github.com-only legacy fallback, 5s
timeout, stubbed subprocess).

## Future work

- If middleman grows context-aware token resolution end-to-end, the
  helper's signature is ready: `ghAuthTokenForHost(ctx context.Context,
  host string)`. The internal-only helper can accept an external context
  without a signature break.
- If a similar `gh`-style fallback ever shows up for another provider,
  we would generalize the helper and detection logic. Not part of this
  change.

## Risks

- Older `gh` users on `github.com` who do not yet have
  `MIDDLEMAN_GITHUB_TOKEN` set rely on the bare retry. The retry covers
  them. We do not break that path.
- A user with `gh auth login` only for the default host (call it
  `github.com`) and a repo configured for `ghe.example` will see "no
  token" at startup instead of getting the wrong-but-non-empty token.
  This is the correct outcome.
- The 5-second timeout could in theory cut off a slow `gh` call. In
  practice `gh auth token` is local (no network) and returns in
  milliseconds; 5s is generous. If users surface flakes we can grow it.
