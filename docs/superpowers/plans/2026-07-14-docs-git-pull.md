# Docs Git Pull Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fast-forward-only "pull from git" action to the docs view, next to the existing commit-and-push button.

**Architecture:** A new `GitPull` operation in `internal/docs` mirrors `GitPublish`'s safety pipeline (config gate, attributes gate, upstream resolution, remote-URL classification), then runs `git fetch` + `git merge --ff-only` with divergence detected via `git merge-base --is-ancestor`. A new `POST /docs/folders/{id}/git/pull` route shares the per-folder lock with publish. The frontend adds a Download-icon button in the docs workspace header with a status notice, refreshing the tree, git status, and open doc after a pull.

**Tech Stack:** Go (huma, testify), Svelte 5, openapi-fetch typed client, Vitest (jsdom).

**Spec:** `docs/superpowers/specs/2026-07-14-docs-git-pull-design.md`

## Global Constraints

- Go tests use testify (`require` for preconditions, `assert` otherwise); never `t.Fatal`/`t.Fatalf`/`t.Error`/`t.Errorf`/`t.Fail`/`t.FailNow`. When a test has more than 3 assertions, declare `assert := assert.New(t)` (and `require := require.New(t)`) locally. Import testify assert without an alias.
- Always pass `-shuffle=on` to direct `go test` invocations. Never pass `-count=1`. Never pass `-v`.
- Integration-tagged Go tests (files starting with `//go:build integration`) run via `go test -tags integration ./internal/docs -shuffle=on` (optionally `-run <pattern>`).
- Frontend: never npm. Deps via `bun install`. Run Vitest from `frontend/` as `../node_modules/.bin/vp test <files>`.
- Any `.svelte` edit must load the `svelte-code-writer` skill first (per repo instructions).
- Regenerate API artifacts with `make api-generate` after route changes; commit the regenerated files with the route.
- Every commit goes through the `kenn:commit` skill; conventional subject that states the why; never `--amend`; never `--no-verify`; never switch branches. Work happens on the current branch `t3code/docs-pull-button`.
- No emojis in code or output. Datetimes are UTC at API boundaries (not applicable here — no timestamps in the wire types).
- The docs repo under operation is untrusted user data: every git invocation must go through `gitCommand` (safe config overrides, isolated env). Never call `exec.Command("git", ...)` directly in `internal/docs`.

---

### Task 1: Direction-labeled remote URL classification

The push-safety classifier (`classifyPushURL`) is exactly the classification pull needs for fetch URLs, but its error strings say "push url". Parameterize the direction label so pull errors read "fetch url ..." without duplicating the classifier.

**Files:**
- Modify: `internal/docs/git_push_safety.go`
- Test: `internal/docs/git_push_safety_test.go`

**Interfaces:**
- Consumes: existing `pushTargetClass`, `unsafePushTarget`, `classifyPushURL`, `fileURLPath`, `classifyLocalPushPath` in `git_push_safety.go`.
- Produces: `classifyRemoteURL(root, raw, direction string) (pushTargetClass, error)` — `direction` is `"push"` or `"fetch"` and only affects error text. Task 2 calls this with `"fetch"`.

- [ ] **Step 1: Write the failing test**

Append to `internal/docs/git_push_safety_test.go`:

```go
func TestClassifyRemoteURLLabelsDirection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()

	_, pushErr := classifyRemoteURL(root, "ext::sh -c true", "push")
	require.Error(pushErr)
	assert.Contains(pushErr.Error(), "push url ext::sh -c true")

	_, fetchErr := classifyRemoteURL(root, "ext::sh -c true", "fetch")
	require.Error(fetchErr)
	assert.Contains(fetchErr.Error(), "fetch url ext::sh -c true")

	_, insideErr := classifyRemoteURL(root, root, "fetch")
	require.Error(insideErr)
	assert.Contains(insideErr.Error(), "fetch target resolves inside the docs folder")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/docs -run TestClassifyRemoteURLLabelsDirection -shuffle=on`
Expected: FAIL to compile with `undefined: classifyRemoteURL`

- [ ] **Step 3: Rename with a direction parameter**

In `internal/docs/git_push_safety.go`:

1. Rename `classifyPushURL(root, raw string)` to `classifyRemoteURL(root, raw, direction string)` and thread `direction` through every error site inside it.
2. Rename `unsafePushTarget(url, why string)` to `unsafeRemoteTarget(direction, url, why string)`:

```go
func unsafeRemoteTarget(direction, url, why string) error {
	return &UnsafeGitConfigError{
		Entries: []string{fmt.Sprintf("%s url %s (%s)", direction, url, why)},
	}
}
```

3. Give `fileURLPath` a `direction` parameter (`fileURLPath(raw, direction string)`) and pass it to its two `unsafeRemoteTarget` calls.
4. Rename `classifyLocalPushPath(root, displayURL, p string)` to `classifyLocalRemotePath(root, displayURL, p, direction string)` and change its in-folder rejection to:

```go
return 0, unsafeRemoteTarget(direction, displayURL, direction+" target resolves inside the docs folder")
```

The push-direction output stays byte-identical to today's message (`push url <url> (push target resolves inside the docs folder)`), so no existing error text changes.

5. Update `assertPushTargetSafe` to call `classifyRemoteURL(root, raw, "push")`.
6. Update the two existing call sites in `git_push_safety_test.go` (`TestClassifyPushURLDriveLetterPaths`) from `classifyPushURL(root, raw)` to `classifyRemoteURL(root, raw, "push")`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/docs -shuffle=on`
Expected: PASS (including `TestClassifyRemoteURLLabelsDirection` and the drive-letter tests)

Also run the integration lane to confirm publish behavior is unchanged:
Run: `go test -tags integration ./internal/docs -shuffle=on`
Expected: PASS

- [ ] **Step 5: Commit**

Use the `kenn:commit` skill. Suggested subject: `refactor: label docs remote-URL safety errors by direction`. Body: the docs pull feature reuses the push classifier for fetch URLs, and a rejected fetch remote must not be reported as a "push url" problem.

---

### Task 2: GitPull backend with fetch-target safety

**Files:**
- Create: `internal/docs/git_fetch_safety.go`
- Create: `internal/docs/git_pull.go`
- Test (create): `internal/docs/git_pull_integration_test.go`

**Interfaces:**
- Consumes (all existing in `internal/docs`): `(*Registry).Lookup`, `isGitRepo`, `assertSafeToPublish`, `assertWorktreeAttributesSafe`, `currentBranch`, `currentUpstream`, `currentUpstreamPushTarget` (returns the `branch.<b>.remote` / `branch.<b>.merge` pair, which is the fetch source as much as the push target), `gitCommand`, `emptyHooksDir`, `pushTargetClass` (`pushTargetLocal`/`pushTargetNetwork`), `classifyRemoteURL` (Task 1), `NoUpstreamError`, `ErrNotAGitRepo`, `UnsafeGitConfigError`, `procutil.{Run,Output}`.
- Produces:
  - `type PullResponse struct { Branch, Upstream string; UpToDate bool; Commit, ShortCommit string }` with JSON tags `branch`, `upstream`, `up_to_date`, `commit`, `short_commit`.
  - `func (r *Registry) GitPull(ctx context.Context, folderID string) (PullResponse, error)`
  - `var ErrDiverged = errors.New(...)` (sentinel)
  - `type PullFailedError struct{ Stderr string }` with `Error() string`
  Task 3 maps these to HTTP problems.

- [ ] **Step 1: Write the failing integration tests**

Create `internal/docs/git_pull_integration_test.go`. The fixture helpers (`newGitRepo`, `newGitRepoNoUpstream`, `runGit`, `gitOutput`, `useIsolatedGitEnv`, `(*gitRepo).writeFile`) already exist in `git_publish_integration_test.go` in the same package/tag; do not duplicate them.

```go
//go:build integration

package docs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remoteCommit advances the bare fixture remote by one commit created in a
// scratch clone, returning the new remote head SHA. This simulates another
// machine pushing docs changes.
func (g *gitRepo) remoteCommit(t *testing.T, rel, body string) string {
	t.Helper()
	clone := t.TempDir()
	runGit(t, g.dir, "clone", g.remote, clone)
	runGit(t, clone, "config", "user.email", "middleman-fixture@example.invalid")
	runGit(t, clone, "config", "user.name", "Middleman Fixture")
	full := filepath.Join(clone, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	runGit(t, clone, "add", "--", rel)
	runGit(t, clone, "commit", "-m", "remote update")
	runGit(t, clone, "push", "origin", "main")
	return gitOutput(t, clone, "rev-parse", "HEAD")
}

func TestGitPullFastForwards(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	want := g.remoteCommit(t, "remote.md", "# remote\n")

	res, err := g.registry.GitPull(context.Background(), g.folderID)

	require.NoError(err)
	assert.False(res.UpToDate)
	assert.Equal(want, res.Commit)
	assert.Equal(want[:7], res.ShortCommit)
	assert.Equal("main", res.Branch)
	assert.Equal("origin/main", res.Upstream)
	assert.Equal(want, gitOutput(t, g.dir, "rev-parse", "HEAD"))
	body, readErr := os.ReadFile(filepath.Join(g.dir, "remote.md"))
	require.NoError(readErr)
	assert.Equal("# remote\n", string(body))
}

func TestGitPullUpToDate(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	g := newGitRepo(t)
	head := gitOutput(t, g.dir, "rev-parse", "HEAD")

	res, err := g.registry.GitPull(context.Background(), g.folderID)

	require.NoError(err)
	assert.True(res.UpToDate)
	assert.Equal(head, res.Commit)
	assert.Equal(head[:7], res.ShortCommit)
}

func TestGitPullRefusesDiverged(t *testing.T) {
	g := newGitRepo(t)
	g.remoteCommit(t, "remote.md", "remote\n")
	g.writeFile(t, "local.md", "local\n")
	runGit(t, g.dir, "add", "--", "local.md")
	runGit(t, g.dir, "commit", "-m", "local update")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	require.ErrorIs(t, err, ErrDiverged)
}

func TestGitPullRefusesOverwritingDirtyWorktree(t *testing.T) {
	g := newGitRepo(t)
	g.remoteCommit(t, "seed.md", "remote seed\n")
	g.writeFile(t, "seed.md", "local dirty\n")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var pullFailed *PullFailedError
	require.ErrorAs(t, err, &pullFailed)
	assert.Contains(t, pullFailed.Stderr, "overwritten")
	// The dirty local edit must survive the refused pull.
	body, readErr := os.ReadFile(filepath.Join(g.dir, "seed.md"))
	require.NoError(t, readErr)
	assert.Equal(t, "local dirty\n", string(body))
}

func TestGitPullRefusesNoUpstream(t *testing.T) {
	g := newGitRepoNoUpstream(t)

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var noUpstream *NoUpstreamError
	require.ErrorAs(t, err, &noUpstream)
	assert.Contains(t, noUpstream.SuggestedCommand, "--set-upstream-to")
}

func TestGitPullRefusesNotARepo(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: dir}})

	_, err := reg.GitPull(context.Background(), "f")

	require.ErrorIs(t, err, ErrNotAGitRepo)
}

func TestGitPullRefusesRemoteHelperURL(t *testing.T) {
	g := newGitRepo(t)
	runGit(t, g.dir, "remote", "set-url", "origin", "ext::sh -c true")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(t, err, &unsafe)
	assert.Contains(t, unsafe.Error(), "fetch url")
}

func TestGitPullRefusesInFolderRemote(t *testing.T) {
	g := newGitRepo(t)
	runGit(t, g.dir, "remote", "set-url", "origin", filepath.Join(g.dir, "inner.git"))

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(t, err, &unsafe)
	assert.Contains(t, unsafe.Error(), "fetch target resolves inside the docs folder")
}

func TestGitPullRefusesCommandBearingConfig(t *testing.T) {
	g := newGitRepo(t)
	runGit(t, g.dir, "config", "filter.lfs.clean", "lfs clean")

	_, err := g.registry.GitPull(context.Background(), g.folderID)

	var unsafe *UnsafeGitConfigError
	require.ErrorAs(t, err, &unsafe)
}
```

Add `"go.kenn.io/middleman/internal/config"` to the import block (needed by `TestGitPullRefusesNotARepo`; the existing integration file already imports it — Go allows only one import block per file, and this is a new file, so declare what this file uses: `context`, `os`, `path/filepath`, `testing`, testify `assert`/`require`, and `config`).

Note the happy-path tests fetch from a **local bare path remote** (the fixture's `t.TempDir()` remote), so they exercise the hardened `--upload-pack` branch on every run.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -tags integration ./internal/docs -run TestGitPull -shuffle=on`
Expected: FAIL to compile with `undefined: PullFailedError` / `g.registry.GitPull undefined`

- [ ] **Step 3: Implement fetch-target safety**

Create `internal/docs/git_fetch_safety.go`:

```go
package docs

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
)

// assertFetchTargetSafe is the fetch-side twin of assertPushTargetSafe: it
// resolves the fetch URLs of the upstream remote and rejects targets whose
// serving side would execute repo-controlled code. Git documents that only
// the first fetch URL is used, but classification covers every configured
// URL anyway so a future git behavior change cannot silently widen the
// attack surface; a set mixing local and network URLs is refused because
// the --upload-pack hardening for local targets is per-invocation.
func assertFetchTargetSafe(ctx context.Context, root, remote string) (pushTargetClass, error) {
	urls, err := remoteFetchURLs(ctx, root, remote)
	if err != nil {
		return 0, err
	}
	var localURLs, networkURLs []string
	for _, raw := range urls {
		c, err := classifyRemoteURL(root, raw, "fetch")
		if err != nil {
			return 0, err
		}
		if c == pushTargetLocal {
			localURLs = append(localURLs, raw)
		} else {
			networkURLs = append(networkURLs, raw)
		}
	}
	if len(localURLs) > 0 && len(networkURLs) > 0 {
		return 0, &UnsafeGitConfigError{Entries: []string{fmt.Sprintf(
			"remote %s mixes local (%s) and network (%s) fetch urls",
			remote, strings.Join(localURLs, ", "), strings.Join(networkURLs, ", "),
		)}}
	}
	if len(localURLs) > 0 {
		return pushTargetLocal, nil
	}
	return pushTargetNetwork, nil
}

// remoteFetchURLs lists the configured fetch URLs of a remote. The upstream
// may also name a bare URL or path instead of a configured remote; get-url
// fails for those and the string itself is the one fetch source.
func remoteFetchURLs(ctx context.Context, root, remote string) ([]string, error) {
	cmd, err := gitCommand(ctx, root, "remote", "get-url", "--all", remote)
	if err != nil {
		return nil, err
	}
	out, err := procutil.Output(ctx, cmd, "resolving docs git fetch url")
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

// localUploadPack builds the --upload-pack command used for fetches from
// local path remotes, the fetch analogue of localReceivePack: the fetching
// process's safe config overrides do not reach the serving side, so hooks
// are redirected to an empty dir, fsmonitor stays off, and the
// pack-objects hook is cleared so the target repo's config cannot name a
// program for git to run while serving the fetch.
func localUploadPack() (string, error) {
	hooksDir, err := emptyHooksDir()
	if err != nil {
		return "", err
	}
	return "git -c core.hooksPath='" + hooksDir + "'" +
		" -c core.fsmonitor=false" +
		" -c uploadpack.packobjectshook= upload-pack", nil
}
```

- [ ] **Step 4: Implement GitPull**

Create `internal/docs/git_pull.go`:

```go
package docs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
)

// ErrDiverged is returned when the local branch and its upstream have both
// moved: completing the pull would need a merge or rebase, which the docs
// UI refuses to perform on untrusted repo content.
var ErrDiverged = errors.New("local branch and upstream have diverged; resolve with a git client")

type PullFailedError struct {
	Stderr string
}

func (e *PullFailedError) Error() string {
	return fmt.Sprintf("git pull failed: %s", e.Stderr)
}

type PullResponse struct {
	Branch      string `json:"branch"`
	Upstream    string `json:"upstream"`
	UpToDate    bool   `json:"up_to_date"`
	Commit      string `json:"commit"`
	ShortCommit string `json:"short_commit"`
}

// GitPull fast-forwards the docs folder's branch to its upstream. It runs
// the same safety pipeline as GitPublish and never merges or rebases:
// divergence is a typed error, not a conflict state left on disk.
func (r *Registry) GitPull(ctx context.Context, folderID string) (PullResponse, error) {
	v, err := r.Lookup(folderID)
	if err != nil {
		return PullResponse{}, err
	}
	if !isGitRepo(v.Path) {
		return PullResponse{}, ErrNotAGitRepo
	}
	if err := assertSafeToPublish(ctx, v.Path); err != nil {
		return PullResponse{}, err
	}
	// The fast-forward checkout rewrites worktree files through any
	// configured filter, so the attribute gate must precede the merge just
	// as it precedes status/add in the publish flow.
	if err := assertWorktreeAttributesSafe(ctx, v.Path); err != nil {
		return PullResponse{}, err
	}
	branch, err := currentBranch(ctx, v.Path)
	if err != nil {
		return PullResponse{}, err
	}
	noUpstream := &NoUpstreamError{
		Branch:           branch,
		SuggestedCommand: fmt.Sprintf("git branch --set-upstream-to=origin/%s %s", branch, branch),
	}
	upstream, err := currentUpstream(ctx, v.Path, branch)
	if err != nil || upstream == "" {
		return PullResponse{}, noUpstream
	}
	remote, mergeRef, err := currentUpstreamPushTarget(ctx, v.Path, branch)
	if err != nil || remote == "" || mergeRef == "" {
		return PullResponse{}, noUpstream
	}
	target, err := assertFetchTargetSafe(ctx, v.Path, remote)
	if err != nil {
		return PullResponse{}, err
	}
	if stderr, err := runFetch(ctx, v.Path, remote, mergeRef, target); err != nil {
		return PullResponse{}, &PullFailedError{Stderr: stderr}
	}
	head, err := revParse(ctx, v.Path, "HEAD")
	if err != nil {
		return PullResponse{}, err
	}
	fetchHead, err := revParse(ctx, v.Path, "FETCH_HEAD")
	if err != nil {
		return PullResponse{}, err
	}
	res := PullResponse{Branch: branch, Upstream: upstream}
	upToDate, err := isAncestor(ctx, v.Path, fetchHead, head)
	if err != nil {
		return PullResponse{}, err
	}
	if upToDate {
		res.UpToDate = true
		res.Commit = head
		res.ShortCommit = head[:7]
		return res, nil
	}
	canFastForward, err := isAncestor(ctx, v.Path, head, fetchHead)
	if err != nil {
		return PullResponse{}, err
	}
	if !canFastForward {
		return PullResponse{}, ErrDiverged
	}
	if stderr, err := runFFMerge(ctx, v.Path); err != nil {
		return PullResponse{}, &PullFailedError{Stderr: stderr}
	}
	res.Commit = fetchHead
	res.ShortCommit = fetchHead[:7]
	return res, nil
}

func runFetch(ctx context.Context, root, remote, mergeRef string, target pushTargetClass) (string, error) {
	args := []string{"fetch"}
	if target == pushTargetLocal {
		uploadPack, err := localUploadPack()
		if err != nil {
			return "", err
		}
		args = append(args, "--upload-pack="+uploadPack)
	}
	args = append(args, remote, mergeRef)
	return runWithStderr(ctx, root, "fetching docs git changes", args...)
}

func runFFMerge(ctx context.Context, root string) (string, error) {
	return runWithStderr(ctx, root, "fast-forwarding docs git branch", "merge", "--ff-only", "FETCH_HEAD")
}

func runWithStderr(ctx context.Context, root, what string, args ...string) (string, error) {
	cmd, err := gitCommand(ctx, root, args...)
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, what); err != nil {
		raw := strings.TrimSpace(stderr.String())
		if raw == "" {
			raw = err.Error()
		}
		return raw, err
	}
	return "", nil
}

func revParse(ctx context.Context, root, rev string) (string, error) {
	cmd, err := gitCommand(ctx, root, "rev-parse", rev)
	if err != nil {
		return "", err
	}
	out, err := procutil.Output(ctx, cmd, "resolving docs git revision")
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isAncestor reports whether ancestor is reachable from descendant. git
// merge-base --is-ancestor signals its answer through the exit code: 0 for
// yes, 1 for no, anything else is a real failure.
func isAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	cmd, err := gitCommand(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, err
	}
	if err := procutil.Run(ctx, cmd, "checking docs git ancestry"); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git merge-base: %w", err)
	}
	return true, nil
}
```

Note on `runWithStderr`: `runPush` in `git_publish.go` has the same stderr-capture shape inline. Do NOT refactor `runPush` onto the helper in this task — keep the diff focused; a follow-up can unify them.

- [ ] **Step 5: Run the tests**

Run: `go test -tags integration ./internal/docs -run TestGitPull -shuffle=on`
Expected: PASS (9 tests)

Then the whole package, both lanes:
Run: `go test ./internal/docs -shuffle=on && go test -tags integration ./internal/docs -shuffle=on`
Expected: PASS

- [ ] **Step 6: Commit**

Use the `kenn:commit` skill. Suggested subject: `feat: fast-forward-only git pull for docs folders`. Body: docs folders could publish but never sync down remote edits; explain the ff-only decision (untrusted repo content, no merge drivers, no conflict states) and the hardened upload-pack for local-path remotes.

---

### Task 3: Pull route with error mapping

**Files:**
- Modify: `internal/server/docs_routes.go`
- Test: `internal/server/docs_git_routes_test.go`
- Regenerate: `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, `internal/apiclient/generated/`, `packages/ui/src/api/generated/schema.ts` via `make api-generate`

**Interfaces:**
- Consumes: `docs.PullResponse`, `(*docs.Registry).GitPull`, `docs.ErrDiverged`, `*docs.PullFailedError`, `*docs.NoUpstreamError`, `docs.ErrNotAGitRepo` (Task 2); existing `docsPublishLockSet`, `bodyOutput`, `problemConflict`, `problemBadRequest`, `newProblem`, `docsRegistryProblem`, `docsFolderIDInput`.
- Produces: `POST /api/v1/docs/folders/{id}/git/pull` (operation id `pull-docs-git`) returning `docs.PullResponse`; problem reasons `notGitRepo`, `noUpstream`, `diverged`, `pullFailed`, `gitOperationInProgress`, `unsafeGitConfig`. Tasks 4–5 consume the route and reasons.

- [ ] **Step 1: Write the failing route tests**

Append to `internal/server/docs_git_routes_test.go`. The file already has `newDocsGitRepo`, `runDocsGit`, `setupDocsGitRouteServer`, and `doDocsJSON`; reuse them.

```go
func (g docsGitRepo) remoteAdvance(t *testing.T, rel, body string) string {
	t.Helper()
	clone := t.TempDir()
	runDocsGit(t, g.dir, "clone", g.remote, clone)
	runDocsGit(t, clone, "config", "user.email", "middleman-fixture@example.invalid")
	runDocsGit(t, clone, "config", "user.name", "Middleman Fixture")
	runDocsGit(t, clone, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(clone, filepath.FromSlash(rel)), []byte(body), 0o644))
	runDocsGit(t, clone, "add", "--", rel)
	runDocsGit(t, clone, "commit", "-m", "remote update")
	runDocsGit(t, clone, "push", "origin", "main")
	return strings.TrimSpace(runDocsGit(t, clone, "rev-parse", "HEAD"))
}

func TestDocsGitPullEndpointFastForwards(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	want := repo.remoteAdvance(t, "remote.md", "# remote\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/pull", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.PullResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.False(body.UpToDate)
	assert.Equal(want, body.Commit)
	assert.Equal("main", body.Branch)
	assert.Equal("origin/main", body.Upstream)
	_, statErr := os.Stat(filepath.Join(repo.dir, "remote.md"))
	assert.NoError(statErr)
}

func TestDocsGitPullEndpointUpToDate(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/pull", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.PullResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.True(body.UpToDate)
	assert.NotEmpty(body.Commit)
}

func TestDocsGitPullEndpointDivergedIs409(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.remoteAdvance(t, "remote.md", "remote\n")
	repo.write(t, "local.md", "local\n")
	runDocsGit(t, repo.dir, "add", "--", "local.md")
	runDocsGit(t, repo.dir, "commit", "-m", "local update")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/pull", nil)

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), `"diverged"`)
}

func TestDocsGitPullEndpointNoUpstreamIs400(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, false)
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/pull", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), `"noUpstream"`)
	assert.Contains(rr.Body.String(), "--set-upstream-to")
}

func TestDocsGitPullEndpointHeldLockIs409(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	srv := setupDocsGitRouteServer(t, repo.dir)
	require.True(srv.docsPublishLocks.tryAcquire("f"))
	defer srv.docsPublishLocks.release("f")

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/pull", nil)

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), `"gitOperationInProgress"`)
}
```

If `strings` is not yet imported by the test file, it is (see `strings.Repeat` usages); `os`/`filepath`/`json` are too. Verify imports compile.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server -run TestDocsGitPull -shuffle=on`
Expected: FAIL — 404-shaped responses (route not registered) or compile error if `docs.PullResponse` typo'd

- [ ] **Step 3: Register the route and map errors**

In `internal/server/docs_routes.go`, after the `publish-docs-git` registration in `registerDocsAPI`:

```go
huma.Register(api, huma.Operation{
	OperationID:   "pull-docs-git",
	Method:        http.MethodPost,
	Path:          "/docs/folders/{id}/git/pull",
	DefaultStatus: http.StatusOK,
	Summary:       "Pull docs Git changes",
	Tags:          []string{"Docs"},
}, s.pullDocsGit)
```

After `publishDocsGit`, add the handler and mapper:

```go
func (s *Server) pullDocsGit(ctx context.Context, in *docsFolderIDInput) (*bodyOutput[docs.PullResponse], error) {
	if !s.docsPublishLocks.tryAcquire(in.ID) {
		return nil, problemConflict(
			CodeConflict,
			"another git operation is in flight for this folder",
			map[string]any{"reason": "gitOperationInProgress"},
		)
	}
	defer s.docsPublishLocks.release(in.ID)

	pulled, err := s.docsRegistry.GitPull(ctx, in.ID)
	if err != nil {
		return nil, docsGitPullProblem(err)
	}
	return &bodyOutput[docs.PullResponse]{Body: pulled}, nil
}

func docsGitPullProblem(err error) huma.StatusError {
	var noUpstream *docs.NoUpstreamError
	var pullFailed *docs.PullFailedError
	switch {
	case errors.Is(err, docs.ErrNotAGitRepo):
		return problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "notGitRepo"})
	case errors.Is(err, docs.ErrDiverged):
		return problemConflict(CodeConflict, err.Error(), map[string]any{"reason": "diverged"})
	case errors.As(err, &noUpstream):
		return problemBadRequest(CodeBadRequest, noUpstream.Error(), map[string]any{
			"reason":            "noUpstream",
			"branch":            noUpstream.Branch,
			"suggested_command": noUpstream.SuggestedCommand,
		})
	case errors.As(err, &pullFailed):
		return newProblem(http.StatusBadGateway, CodeUpstreamError, pullFailed.Error(), map[string]any{
			"reason": "pullFailed",
		})
	default:
		return docsRegistryProblem(err)
	}
}
```

(`docsRegistryProblem` already maps `UnsafeGitConfigError` to 400/`unsafeGitConfig` and folder-not-found to 404.)

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server -run TestDocsGit -shuffle=on`
Expected: PASS (new pull tests plus existing publish/status route tests)

- [ ] **Step 5: Regenerate API artifacts**

Run: `make api-generate`
Expected: `frontend/openapi/openapi.yaml`, `internal/apiclient/spec/openapi.json`, and `packages/ui/src/api/generated/schema.ts` gain the `pull-docs-git` operation; generated Go client updates if the Makefile target covers it. Confirm with `git status --short` that only generated artifacts changed beyond the hand-edited files.

Run: `go build ./...`
Expected: success

- [ ] **Step 6: Commit**

Use the `kenn:commit` skill. Suggested subject: `feat: expose docs git pull over the API`. Include the regenerated artifacts in the same commit. Body: the route shares publish's per-folder lock so pull and publish cannot interleave; reasons are stable codes for frontend branching.

---

### Task 4: Frontend API client and mock backend

**Files:**
- Modify: `frontend/src/lib/api/docs/types.ts`
- Modify: `frontend/src/lib/api/docs/api.ts`
- Modify: `frontend/src/lib/components/docs/docsTestBackend.ts`
- Test: `frontend/src/lib/api/docs/api.test.ts`

**Interfaces:**
- Consumes: the `pull-docs-git` operation in the regenerated `packages/ui/src/api/generated/schema.ts` (Task 3).
- Produces: `GitPullResponse` type; `DocsAPI.gitPull(folderID: string): Promise<GitPullResponse>`; error codes `diverged`, `pull_failed`, `git_operation_in_progress` on `DocsAPIError.code`. Task 5 consumes all three.

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/lib/api/docs/api.test.ts` (the file's `fakeFetch` and `problemWithDetails` helpers already exist):

```ts
test("gitPull POSTs to /git/pull and returns the parsed response", async () => {
  const { fn, calls } = fakeFetch([
    {
      status: 200,
      body: {
        branch: "main",
        upstream: "origin/main",
        up_to_date: false,
        commit: "abcdef1234567890abcdef1234567890abcdef12",
        short_commit: "abcdef1",
      },
    },
  ]);
  const api = createDocsAPI({ fetch: fn });
  const res = await api.gitPull("notes");
  expect(calls[0]!.url).toContain("/api/v1/docs/folders/notes/git/pull");
  expect(calls[0]!.init?.method).toBe("POST");
  expect(res.up_to_date).toBe(false);
  expect(res.short_commit).toBe("abcdef1");
});

test("gitPull maps server pull reasons onto frontend error codes", async () => {
  const cases: Array<{ reason: string; code: string; status: number }> = [
    { reason: "diverged", code: "diverged", status: 409 },
    { reason: "pullFailed", code: "pull_failed", status: 502 },
    { reason: "gitOperationInProgress", code: "git_operation_in_progress", status: 409 },
    { reason: "noUpstream", code: "no_upstream", status: 400 },
  ];
  for (const { reason, code, status } of cases) {
    const { fn } = fakeFetch([
      { status, body: problemWithDetails(status, "conflict", `pull refused: ${reason}`, { reason }) },
    ]);
    const api = createDocsAPI({ fetch: fn });
    const err = await api.gitPull("notes").catch((e: unknown) => e as Error & { code?: string; status?: number });
    expect(err).toBeInstanceOf(Error);
    expect((err as { code?: string }).code).toBe(code);
    expect((err as { status?: number }).status).toBe(status);
  }
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `frontend/`): `../node_modules/.bin/vp test src/lib/api/docs/api.test.ts`
Expected: FAIL — `api.gitPull is not a function` (and a TS error for the missing interface member)

- [ ] **Step 3: Implement the client**

In `frontend/src/lib/api/docs/types.ts`, after `GitPublishResponse`:

```ts
// Wire shape returned by POST /git/pull. Mirrors internal/docs.PullResponse.
export interface GitPullResponse {
  branch: string;
  upstream: string;
  up_to_date: boolean;
  commit: string;
  short_commit: string;
}
```

In `frontend/src/lib/api/docs/api.ts`:

1. Add `GitPullResponse` to the type import from `./types`.
2. Add to the `DocsAPI` interface after `gitPublish`:

```ts
  // Fast-forward the folder's branch to its upstream. Throws DocsAPIError
  // with code "diverged" when local and remote history have both moved.
  gitPull(folderID: string): Promise<GitPullResponse>;
```

3. Add the implementation after `gitPublish` in the returned object:

```ts
    async gitPull(folderID) {
      const { data, error, response } = await api.POST("/docs/folders/{id}/git/pull", {
        params: { path: { id: folderID } },
      });
      throwOnDocsError(error, response);
      return data as GitPullResponse;
    },
```

4. Extend the `switch (reason)` in `docsErrorCodeFromEnvelope` with:

```ts
      case "diverged":
        return "diverged";
      case "pullFailed":
        return "pull_failed";
      case "gitOperationInProgress":
        return "git_operation_in_progress";
```

In `frontend/src/lib/components/docs/docsTestBackend.ts` (implements `DocsAPI`, so TypeScript forces this): add `GitPullResponse` to the type imports and add after the `gitPublish` method:

```ts
    async gitPull(folderID): Promise<GitPullResponse> {
      const idx = state.findIndex((v) => v.meta.id === folderID);
      if (idx < 0) throw makeError(404, "folder_not_found", `folder not found: ${folderID}`);
      const repo = repoState[idx]!;
      if (!repo.isRepo) {
        throw makeError(400, "not_a_git_repo", "folder is not a git repository");
      }
      if (!repo.upstream) {
        throw makeError(
          400,
          "no_upstream",
          `No upstream is configured for ${repo.branch}. Run: git branch --set-upstream-to=origin/${repo.branch} ${repo.branch}`,
        );
      }
      return {
        branch: repo.branch,
        upstream: repo.upstream,
        up_to_date: true,
        commit: "0123456789abcdef0123456789abcdef01234567",
        short_commit: "0123456",
      };
    },
```

(Match the surrounding mock's actual `makeError` signature and `repoState` shape — read the neighboring `gitPublish` mock before editing.)

- [ ] **Step 4: Run the tests**

Run (from `frontend/`): `../node_modules/.bin/vp test src/lib/api/docs/api.test.ts src/lib/components/docs/docsTestBackend.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

Use the `kenn:commit` skill. Suggested subject: `feat: docs API client support for git pull`.

---

### Task 5: Pull button in the docs workspace

**Files:**
- Modify: `frontend/src/lib/components/docs/DocsWorkspace.svelte`
- Test: `frontend/src/lib/components/docs/DocsWorkspace.test.ts`

Load the `svelte-code-writer` skill before editing the `.svelte` file.

**Interfaces:**
- Consumes: `DocsAPI.gitPull` + `GitPullResponse` (Task 4); existing `loadTree`, `loadGitStatus`, `loadDoc`, `activeFolderIsRepo`, `route`, and the publish button block at the `{#if activeFolderIsRepo}` guard.
- Produces: user-visible pull button (`aria-label="Pull from git"`, `title="Pull from git"`) and a shared git notice line replacing `publishSuccess`.

- [ ] **Step 1: Write the failing tests**

Append inside the `describe("DocsWorkspace", ...)` block in `DocsWorkspace.test.ts`:

```ts
  test("pull button is hidden for non-git folders", async () => {
    const api = createMockDocsBackend({
      folders: [{ meta: { id: "x", name: "X", path: "/x" }, files: { "README.md": "# x" } }],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { queryByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    await waitFor(() => expect(queryByRole("button", { name: "Pull from git" })).toBeNull());
  });

  test("pull button pulls, reports the commit, and refreshes the tree", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const tree = vi.fn(backend.tree);
    const gitPull = vi.fn(async () => ({
      branch: "main",
      upstream: "origin/main",
      up_to_date: false,
      commit: "abcdef1234567890abcdef1234567890abcdef12",
      short_commit: "abcdef1",
    }));
    const api = { ...backend, tree, gitPull };
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    const treeCallsBefore = tree.mock.calls.length;
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Pulled to abcdef1"));
    expect(gitPull).toHaveBeenCalledWith("x");
    expect(tree.mock.calls.length).toBeGreaterThan(treeCallsBefore);
  });

  test("pull button reports an up-to-date repo", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api: backend },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Already up to date."));
  });

  test("pull failure surfaces the error in the notice line", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const gitPull = vi.fn(async () => {
      const err = new Error("local branch and upstream have diverged; resolve with a git client") as Error & {
        status?: number;
        code?: string;
      };
      err.status = 409;
      err.code = "diverged";
      throw err;
    });
    const api = { ...backend, gitPull };
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("diverged"));
  });
```

(The default mock `gitPull` from Task 4 returns `up_to_date: true`, which the third test relies on.)

- [ ] **Step 2: Run tests to verify they fail**

Run (from `frontend/`): `../node_modules/.bin/vp test src/lib/components/docs/DocsWorkspace.test.ts`
Expected: the four new tests FAIL (`Unable to find role="button" ... "Pull from git"`); existing tests PASS

- [ ] **Step 3: Implement the button and notice**

In `DocsWorkspace.svelte`:

1. Add the icon import (alphabetical position, before `FileText`):

```ts
  import Download from "@lucide/svelte/icons/download";
```

2. Replace the `publishSuccess` state (`let publishSuccess: string | null = $state(null);` near line 119) with a shared notice used by both publish and pull:

```ts
  let gitNotice: { kind: "success" | "error"; text: string } | null = $state(null);
  let pulling = $state(false);
```

3. Update `onPublishedSuccess` to set the notice:

```ts
  async function onPublishedSuccess(result: GitPublishResponse) {
    gitNotice = {
      kind: "success",
      text: `Committed and pushed ${result.files.length} ${result.files.length === 1 ? "file" : "files"} as ${result.short_commit}.`,
    };
    if (route.folder) await loadGitStatus(route.folder);
  }
```

4. Add the pull handler next to it:

```ts
  async function pullFromGit() {
    if (!route.folder || pulling) return;
    pulling = true;
    try {
      const result = await api.gitPull(route.folder);
      gitNotice = {
        kind: "success",
        text: result.up_to_date ? "Already up to date." : `Pulled to ${result.short_commit}.`,
      };
      await loadTree(route.folder);
      await loadGitStatus(route.folder);
      // The pulled commit may have rewritten the open document on disk.
      if (route.doc) await loadDoc(route.folder, route.doc);
    } catch (err) {
      gitNotice = { kind: "error", text: err instanceof Error ? err.message : "Pull failed" };
    } finally {
      pulling = false;
    }
  }
```

5. In the header, inside the existing `{#if activeFolderIsRepo}` block, add the pull button directly before the publish button:

```svelte
          {#if activeFolderIsRepo}
            <button
              type="button"
              class="list-action"
              aria-label="Pull from git"
              title="Pull from git"
              onclick={pullFromGit}
              disabled={pulling}
            >
              <Download size={14} strokeWidth={1.75} />
            </button>
            <button
              type="button"
              class="list-action"
              aria-label="Publish to git"
              title="Commit & push to git"
              onclick={() => (publishOpen = true)}
            >
              <Upload size={14} strokeWidth={1.75} />
            </button>
          {/if}
```

6. Replace the notice render block (`{#if publishSuccess}` near line 1368):

```svelte
{#if gitNotice}
  <p
    class="publish-success kit-popover-card"
    class:notice-error={gitNotice.kind === "error"}
    role="status"
  >
    {gitNotice.text}
  </p>
{/if}
```

7. Add the error modifier below the `.publish-success` rule in the style block:

```css
  .publish-success.notice-error {
    color: var(--accent-red);
  }
```

- [ ] **Step 4: Run the component tests**

Run (from `frontend/`): `../node_modules/.bin/vp test src/lib/components/docs/DocsWorkspace.test.ts src/lib/components/docs/PublishDocsDialog.test.ts`
Expected: PASS (including the existing publish-success tests against the renamed notice)

- [ ] **Step 5: Commit**

Use the `kenn:commit` skill. Suggested subject: `feat: pull-from-git button in the docs workspace`. Body: why the open doc/tree/status reload after a pull, and that the notice line is shared with publish.

---

### Task 6: Full validation

**Files:** none (verification only)

- [ ] **Step 1: Go suites**

Run: `make test-short`
Expected: PASS

Run: `go test -tags integration ./internal/docs ./internal/server -shuffle=on`
Expected: PASS

Run: `make vet && make lint`
Expected: clean

- [ ] **Step 2: Frontend full suite**

Run (from `frontend/`): `../node_modules/.bin/vp test`
Expected: PASS. If browser-lane files are missing or port 63315 is busy, a sibling worktree is running its browser lane — wait and rerun; never kill the other process.

Run: `make frontend-check`
Expected: clean (svelte-check, lint, fmt)

- [ ] **Step 3: Commit any stragglers**

If validation surfaced fixes, commit them via the `kenn:commit` skill. Otherwise nothing to commit.

- [ ] **Step 4: Visual artifact for the PR**

The feature adds visible UI. Before any PR is opened, use the `capture-playwright` skill to capture the docs header with the new pull button (real seeded backend, not mocks) so the PR description can attach it with `gh image`. Do not open or update a PR unless the user asks.
