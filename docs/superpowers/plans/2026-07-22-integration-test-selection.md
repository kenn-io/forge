# Integration Test Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dedicated Go integration lane execute only tests from files guarded by the `integration` build tag.

**Architecture:** Keep Go build tags responsible for compiling integration-only test files, and use a shared `TestIntegration...` naming convention plus `go test -run '^TestIntegration'` to select their top-level tests. Apply the same command shape in the Makefile and GitHub Actions so local and CI execution cannot drift.

**Tech Stack:** Go test build tags and run filters, GNU Make, GitHub Actions, gotestsum

## Global Constraints

- Do not change production code, test assertions, fixtures, or subtest names.
- Keep the ordinary Go test lane unchanged.
- Use `-shuffle=on` for every direct `go test` invocation.
- Do not add a test for Makefile or workflow configuration; verify these artifacts by enumerating tests and rendering the affected command.
- Do not execute the integration test bodies during implementation verification; enumerate their names instead.
- Do not push the rebased branch unless the user explicitly requests it.

---

### Task 1: Select Only Integration-Tagged Top-Level Tests

**Files:**
- Modify: `internal/gitclone/clone_test.go`
- Modify: `internal/gitclone/diff_test.go`
- Modify: `internal/docs/git_publish_integration_test.go`
- Modify: `internal/docs/git_pull_integration_test.go`
- Modify: `internal/github/merged_diff_test.go`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: Go's existing `integration` build constraint on the five test files.
- Produces: Top-level test names matching `^TestIntegration` and equivalent local/CI integration commands.

- [x] **Step 1: Record the failing pre-change selection behavior**

Run:

```bash
go test -tags integration ./internal/gitclone -list . -shuffle=on
```

Expected before the fix: output includes ordinary tests such as
`TestGitNetworkedResolvesTokenSourceForEachCall` as well as tagged tests such as
`TestEnsureClone`; no listed test begins with `TestIntegration`.

- [x] **Step 2: Prefix every tagged top-level test name**

In each of the five integration-tagged files, change every top-level declaration
from this form:

```go
func TestEnsureClone(t *testing.T) {
```

to this form, preserving the descriptive suffix and function body:

```go
func TestIntegrationEnsureClone(t *testing.T) {
```

Apply the same `Test` to `TestIntegration` prefix insertion to all 70 top-level
tests in those files. Do not rename helpers or `t.Run` subtests.

- [x] **Step 3: Add the same run filter to local and CI commands**

Change the Makefile target to:

```make
test-integration: ensure-embed-dir ensure-tmp-dir
	$(GOTESTSUM)=tmp/test-integration-output.json -- $(GO_TEST_P_FLAG) -tags integration ./... -run '^TestIntegration' -shuffle=on
```

Change the GitHub Actions command to:

```yaml
go tool gotestsum --format pkgname-and-test-fails --jsonfile=tmp/test-integration-output.json -- -tags integration ./... -run '^TestIntegration' -shuffle=on -parallel=2
```

- [x] **Step 4: Verify the focused selection**

Run:

```bash
go test -tags integration ./internal/gitclone ./internal/docs ./internal/github -list '^TestIntegration' -shuffle=on
```

Expected: exactly 70 listed top-level tests, all beginning with
`TestIntegration`, and no ordinary test names.

- [x] **Step 5: Inspect the actual integration command without running it**

Run:

```bash
make -n test-integration
```

Expected: the rendered gotestsum command contains both `-tags integration` and
`-run '^TestIntegration'`.

- [x] **Step 6: Confirm the ordinary lane is unchanged**

Run:

```bash
git diff -- Makefile .github/workflows/ci.yml
```

Expected: only the dedicated integration commands gain the run filter; the
ordinary Go test command remains unchanged.

- [x] **Step 7: Review and commit**

Run `git diff --check`, inspect the full diff, run the repository context-sync
commit workflow, and create one conventional commit explaining that build tags
are additive and therefore require an explicit test-name filter for the
dedicated lane.
