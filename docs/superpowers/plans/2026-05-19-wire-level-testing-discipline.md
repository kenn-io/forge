# Wire-Level HTTP Testing Discipline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document a forward-looking discipline for wire-level HTTP tests in middleman, with three worked examples (SSE contract pin, mutation-guard 415, branch-conflict 409 migration) that establish the shape new tests should take.

**Architecture:** Three concrete tests in the existing black-box packages (`internal/server/e2etest/`, `internal/server/apitest/`, `internal/server/workspacetest/`), plus a new "HTTP testing discipline" section in `context/testing.md` and one cross-reference bullet in `CLAUDE.md`. No production code changes; no migration of existing tests beyond the one converted example.

**Tech Stack:** Go 1.24, testify (require/assert), `net/http`, `net/http/httptest` (recorder + real socket), the generated Go API client in `internal/apiclient/generated/`, the existing `EventHub` in `internal/server/`.

---

### Task 1: SSE Contract Pin Paving Test in e2etest/

**Spec reference:** Worked example 1.

**Files:**
- Create: `internal/server/e2etest/sse_contract_test.go`

This test exercises the SSE endpoint over a real socket via `httptest.NewServer`. It pre-broadcasts a `sync_status` event before starting the server, then asserts that the first frame on the wire carries the cached payload, with the response headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`) intact. It does not assert `Connection` (hop-by-hop and absent under HTTP/2).

- [ ] **Step 1: Write the test file**

Create `internal/server/e2etest/sse_contract_test.go` with the following contents:

```go
package e2etest

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/server"
)

// TestSSEContractPinDeliversCachedSyncStatusFrame is a paving wire-level test:
// it exercises the /api/v1/events endpoint through a real httptest.NewServer
// socket, which is the only transport that faithfully simulates flushed
// streaming I/O. The in-process recorder buffers writes until the handler
// returns, so a recorder-based test would not catch middleware that strips
// or rewrites streaming response headers, nor would it observe the SSE frame
// boundary.
//
// The test pins the visible contract for a single cached sync_status event:
// status 200, the streaming Content-Type, Cache-Control: no-cache, and the
// first SSE frame's event/data lines. Pre-broadcasting through the hub
// before the server starts removes the broadcast / read race a non-cached
// first event would introduce.
func TestSSEContractPinDeliversCachedSyncStatusFrame(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	srv.Hub().Broadcast(server.Event{
		Type: "sync_status",
		Data: map[string]bool{"running": false},
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api/v1/events")
	require.NoError(err)
	defer resp.Body.Close()

	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal("no-cache", resp.Header.Get("Cache-Control"))

	eventType, dataLine := readFirstSSEFrame(t, resp.Body)
	require.Equal("sync_status", eventType)

	var payload map[string]bool
	require.NoError(json.Unmarshal([]byte(dataLine), &payload))
	assert.Equal(map[string]bool{"running": false}, payload)
}

// readFirstSSEFrame reads from r until it sees a blank-line terminator,
// returning the event type and the bytes after "data: " on the data line.
// The SSE wire format is one or more "field: value" lines terminated by a
// blank line.
func readFirstSSEFrame(t *testing.T, r io.Reader) (string, string) {
	t.Helper()
	scanner := bufio.NewScanner(r)
	var eventType, data string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = rest
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			data = rest
		}
		if line == "" && eventType != "" {
			return eventType, data
		}
	}
	require.FailNow(t, "did not read a complete SSE frame")
	return "", ""
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/e2etest -run TestSSEContractPinDeliversCachedSyncStatusFrame -shuffle=on
```

Expected: PASS.

- [ ] **Step 3: Verify the test fails when the contract regresses**

Apply a temporary change in `internal/server/server.go` to break the contract — for example, change `w.Header().Set("Cache-Control", "no-cache")` to `w.Header().Set("Cache-Control", "max-age=300")` in `handleSSE`. Re-run the test:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/e2etest -run TestSSEContractPinDeliversCachedSyncStatusFrame -shuffle=on
```

Expected: FAIL with a Cache-Control mismatch. Then revert the change in `server.go` and re-run; expected PASS.

This is a verification step that confirms the test pins the contract on the wire. Do not commit the broken state. After reverting, confirm `git status` shows only the new test file.

- [ ] **Step 4: Commit**

```sh
git add internal/server/e2etest/sse_contract_test.go
git commit -m "test: pin SSE response contract over a real socket

The recorder transport buffers writes until the handler returns and
collapses SSE frame boundaries, so it cannot catch middleware that adds,
drops, or rewrites streaming response headers like Cache-Control. Add a
paving example in e2etest/ that exercises /api/v1/events through
httptest.NewServer and pins the response status, streaming content type,
and the first cached sync_status frame on the wire."
```

---

### Task 2: Mutation Guard 415 Paving Test in apitest/

**Spec reference:** Worked example 2.

**Files:**
- Create: `internal/server/apitest/mutation_guard_test.go`

This test deliberately violates a precondition the generated client always satisfies (a `Content-Type` other than `application/json` on a mutating route). The generated client cannot construct this request, so the test uses a raw `http.Request` sent through `srv.ServeHTTP` via `httptest.NewRecorder`. The full middleware chain runs; a handler-internal test calling the handler directly would never trigger the mutation guard.

- [ ] **Step 1: Write the test file**

Create `internal/server/apitest/mutation_guard_test.go` with the following contents:

```go
package apitest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMutationGuardRejectsNonJSONContentTypeWithProblemBody is a paving
// wire-level test: it covers the path where the test deliberately violates
// a precondition the generated client always satisfies. The generated
// client sets Content-Type: application/json automatically on mutation
// requests, so the only way to exercise the CSRF/Content-Type guard from
// a wire-level test is to construct a raw http.Request.
//
// The request still flows through srv.ServeHTTP, which means the full
// middleware chain runs. A handler-internal test that called the handler
// function directly would never trigger the guard at all, because the
// guard short-circuits the request before handler dispatch.
//
// The asserted shape is the wire response: status 415, the JSON error
// envelope writeError produces, and the response Content-Type header
// writeJSON sets.
func TestMutationGuardRejectsNonJSONContentTypeWithProblemBody(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sync",
		bytes.NewReader(nil),
	)
	req.Header.Set("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusUnsupportedMediaType, resp.StatusCode)
	assert.Equal("application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body["error"], "Content-Type must be application/json")
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/apitest -run TestMutationGuardRejectsNonJSONContentTypeWithProblemBody -shuffle=on
```

Expected: PASS.

- [ ] **Step 3: Run in short mode to confirm it stays in the fast tier**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/apitest -run TestMutationGuardRejectsNonJSONContentTypeWithProblemBody -short -shuffle=on
```

Expected: PASS (apitest/ does not skip in short mode).

- [ ] **Step 4: Commit**

```sh
git add internal/server/apitest/mutation_guard_test.go
git commit -m "test: pin mutation guard 415 response shape on the wire

Handler-internal tests that call mutation handlers directly never reach
the CSRF / Content-Type middleware, so a regression where the guard
stops short-circuiting non-JSON bodies (or drops the JSON error envelope)
would slip past. Add a paving example in apitest/ that sends a raw POST
with text/plain through srv.ServeHTTP and pins status 415, the
application/json response header, and the writeError body shape."
```

---

### Task 3: Branch-Conflict 409 Migration in workspacetest/

**Spec reference:** Worked example 3.

**Files:**
- Modify: `internal/server/workspacetest/fixtures_test.go`
- Create: `internal/server/workspacetest/issue_workspace_conflict_test.go`

This task migrates the shape (not the coverage) of `TestWorkspaceCreateIssueBranchConflictReturnsTyped409` from `package server` to `package workspacetest`, parsing the response through the generated `ErrorModel` type. The original in-package test stays in place — the goal is the side-by-side worked example, not coverage replacement.

The migrated test reuses the existing `setupWorkspaceServerFixture` (which already provides a real git remote and clone). It needs a small additional helper to seed an issue, since today only PRs are seeded.

- [ ] **Step 1: Add the seedIssue helper to workspacetest fixtures**

In `internal/server/workspacetest/fixtures_test.go`, add the helper. Insert it after the existing `seedPROnHost` function so related seed helpers cluster together:

```go
func seedIssue(
	t *testing.T, database *db.DB,
	owner, name string, number int, state string,
) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(
		ctx, db.GitHubRepoIdentity("github.com", owner, name),
	)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, name, number),
		Title:          fmt.Sprintf("Test Issue #%d", number),
		Author:         "testuser",
		State:          state,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if state == "closed" {
		issue.ClosedAt = &now
	}
	issueID, err := database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	return issueID
}
```

The existing imports in `fixtures_test.go` already cover everything this helper needs (`fmt`, `testing`, `time`, `db`, `require`).

- [ ] **Step 2: Add a testGitSHA helper to workspacetest fixtures**

The migrated test reads the SHA of `refs/heads/main` from the remote to set up the conflicting `middleman/issue-7` ref. Add a helper alongside `runGit` in `fixtures_test.go`. Insert it after `runGit`:

```go
func testGitSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	cmd.Env = append(
		gitenv.StripAll(os.Environ()),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
```

The existing imports already cover everything this helper needs (`os/exec`, `os`, `strings`, `gitenv`, `require`).

- [ ] **Step 3: Write the migrated test file**

Create `internal/server/workspacetest/issue_workspace_conflict_test.go` with the following contents:

```go
package workspacetest

import (
	"net/http"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/apiclient/generated"
)

// TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient is a
// black-box migration of TestWorkspaceCreateIssueBranchConflictReturnsTyped409
// (still in internal/server/api_test.go). The original asserts the same
// behavior using a package-local rawProblemDetail struct; this version
// decodes through generated.ErrorModel so a regression that drifts the
// 409 response shape away from the published OpenAPI contract fails this
// test.
//
// The migration is not a transport change — both shapes go through
// srv.ServeHTTP via the recorder transport. It is a package-boundary
// change: this file lives in package workspacetest, cannot reach into
// package server's unexported helpers, and uses the generated apiclient
// instead of the in-package doJSON helper. workspacetest/ is the natural
// home because setupWorkspaceServerFixture already wires up a real git
// remote, which apitest/ does not.
func TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	// Pre-create the branch the handler would otherwise allocate, so
	// the next workspace request hits the typed conflict path.
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	runGit(
		t,
		fixture.bare,
		"update-ref",
		"refs/heads/middleman/issue-7",
		mainSHA,
	)

	resp, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget", 7,
		generated.CreateIssueWorkspaceInputBody{},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode(), string(resp.Body))

	problem := resp.ApplicationproblemJSONDefault
	require.NotNil(problem, "default error model must be populated for 409")

	require.NotNil(problem.Type)
	assert.Equal(
		"urn:middleman:error:issue-workspace-branch-conflict",
		*problem.Type,
	)
	require.NotNil(problem.Status)
	assert.EqualValues(http.StatusConflict, *problem.Status)
	require.NotNil(problem.Detail)
	assert.NotEmpty(*problem.Detail)

	require.NotNil(problem.Errors)
	locations := map[string]any{}
	for _, e := range *problem.Errors {
		if e.Location == nil {
			continue
		}
		locations[*e.Location] = e.Value
	}
	assert.Equal("middleman/issue-7", locations["body.git_head_ref"])
	assert.Equal(
		"middleman/issue-7-2",
		locations["body.suggested_git_head_ref"],
	)
}
```

- [ ] **Step 4: Run the migrated test**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/workspacetest -run TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient -shuffle=on
```

Expected: PASS.

- [ ] **Step 5: Confirm the migrated test skips in short mode**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server/workspacetest -run TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient -short -shuffle=on
```

Expected: PASS with no test executed (workspacetest's `setupWorkspaceServerFixture` already calls `t.Skip("workspace e2e tests skipped in short mode")`). The new test inherits that skip; no additional skip directive is needed.

- [ ] **Step 6: Confirm the original in-package test still passes**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./internal/server -run TestWorkspaceCreateIssueBranchConflictReturnsTyped409 -shuffle=on
```

Expected: PASS. The point of the migration is to ship a side-by-side example, not to replace coverage.

- [ ] **Step 7: Commit**

```sh
git add internal/server/workspacetest/fixtures_test.go \
        internal/server/workspacetest/issue_workspace_conflict_test.go
git commit -m "test: migrate branch-conflict 409 example through generated client

The existing in-package test decodes the 409 problem body into a
package-local rawProblemDetail struct, which means a future change that
drifts the response away from the published OpenAPI contract would still
pass. Add a sibling test in workspacetest/ that parses the same response
through generated.ErrorModel via the generated apiclient. The original
test stays in place; this is a worked example of the package-boundary
shape, not a coverage replacement."
```

---

### Task 4: Document the Discipline

**Spec reference:** "Where the doctrine lives" and acceptance criteria.

**Files:**
- Modify: `context/testing.md`
- Modify: `CLAUDE.md`

This task adds the doctrine itself: the operational definition of "wire-level," when to choose each shape, the fault-injection exception, and the bug-class table. It cross-references the discipline from `CLAUDE.md` so the Testing section there points readers to it.

- [ ] **Step 1: Append the HTTP testing discipline section to context/testing.md**

Open `context/testing.md`. Insert the following section between the existing `## Race test runtime` section and the existing `## Related context` section. The section sits as a peer of the existing topical sections (Live GraphQL validation, Provider work, Race test runtime) and the SQLite Fixtures / Sleep And Timer Tests subsections under Race test runtime.

Add this new top-level section before `## Related context`:

```markdown
## HTTP testing discipline

A test of user-visible HTTP behavior is **wire-level** when both of the
following hold:

1. The request flows through `srv.ServeHTTP`, so every middleware the
   production server installs runs against the test request.
2. Assertions read the response a client would actually observe: status
   code, response headers, and response body bytes. The handler
   function's return value is not consulted.

Two transports satisfy this definition:

- **In-process via `httptest.NewRecorder`** is the default for
  request / response tests. Used by `internal/server/apitest/` and the
  in-package `doJSON` helper. Fast, no port allocation, deterministic.
  Fires every middleware. Does not faithfully simulate streaming I/O:
  there is no `net.Conn` behind the recorder, the recorder buffers
  writes until the handler returns, and `Flush` on the wrapped writer
  does not push bytes toward an attached reader.
- **Real socket via `httptest.NewServer`** is required for streaming,
  hijack, long-lived, or `Flush`-sensitive endpoints. Used by
  `internal/server/e2etest/` and the in-package `TestSSE_*` tests in
  `internal/server/server_test.go`.

Direct handler-function calls (for example, `s.handleSSE(w, r)`) are not
wire-level. They bypass routing and every middleware. Allow them only
when the test injects a fault into the `http.ResponseWriter` itself
(deadline failures, hijack errors, write cancellation simulated by a
wrapping writer) or otherwise probes control flow that cannot be
expressed against a real or simulated wire. The two existing tests of
this shape (`TestSSE_TerminatesOnInitialDeadlineFailure` and
`TestSSE_TerminatesOnMidStreamDeadlineFailure`) are the legitimate
exception, not a path to avoid.

For new code that ships user-visible HTTP behavior:

- Default to a wire-level test in `internal/server/apitest/` (recorder
  transport, generated client). The OpenAPI contract is what consumers
  see, and parsing through the generated types catches schema drift the
  test author would not catch with an ad-hoc struct.
- Use `internal/server/e2etest/` for any streaming, hijack, or
  `Flush`-sensitive endpoint. SSE, the roborev proxy streams, and any
  future WebSocket flow belong here. Real socket is non-negotiable
  because the recorder collapses the `Flush` timing observable.
- Use a raw `http.Request` over the recorder transport when the test
  exercises a path the generated client cannot construct, such as a
  deliberately wrong `Content-Type`, an intentionally malformed body,
  or any preflight failure that only the runtime mutation guard can
  produce. Add a comment naming the reason; this is the only signal a
  reader has that the test is intentionally not using the generated
  client.
- Direct handler-function calls are allowed only for fault injection on
  the `http.ResponseWriter`. Add a comment naming the fault being
  injected.

Handler-internal helper unit tests (URL parsing helpers, label diff
functions, capability resolution) are fine as plain function unit tests
in `package server` and are not in scope. The rule applies to tests of
user-visible HTTP behavior, not to tests of internal helpers that
compose into a handler.

The bug classes wire-level tests catch:

| Bug class | Assertion target |
|-----------|------------------|
| Time field serialization (`Z` vs `+00:00`) | Raw response body; handler-internal tests inspect `time.Time` values before marshaling. |
| Error code missing from OpenAPI doc | `apitest/` generated client surfaces unknown status variants and schema mismatches against `generated.ErrorModel`. |
| Header set in handler but stripped by middleware | `resp.Header`, not the handler's `w.Header()` before middleware ran. |
| Status code overridden by middleware | `resp.StatusCode`, not the handler's return. |
| Mutation guard short-circuits before handler dispatch | `srv.ServeHTTP` runs the full middleware chain; handler-internal tests calling the handler directly miss this entirely. |
| SSE Content-Type / Cache-Control drift | Real-socket read; the recorder does not faithfully simulate what a real client sees on a buffered stream. |

Three worked examples ship the discipline:

- `internal/server/e2etest/sse_contract_test.go` pins the SSE response
  headers and first cached `sync_status` frame on the wire.
- `internal/server/apitest/mutation_guard_test.go` sends a raw `POST`
  with `Content-Type: text/plain` and asserts the 415 response shape.
- `internal/server/workspacetest/issue_workspace_conflict_test.go`
  reproduces an in-package 409 test as a black-box example that decodes
  through `generated.ErrorModel`. The original in-package test stays in
  place.

A `make lint-wire` target is intentionally out of scope. Either it
would flag the legitimate fault-injection tests and require annotation
comments to suppress (one-time churn for low ongoing value), or it
would enforce a no-internal-imports rule already enforced informally by
Go's package boundary. A future lint can be added if there is
measurable churn around the rule.
```

- [ ] **Step 2: Add the cross-reference bullet to CLAUDE.md**

In `CLAUDE.md`, in the `### Test Guidelines` subsection under `## Testing`, add a new bullet immediately after the existing bullet that begins `- Prefer the generated Go API client in \`internal/apiclient\` for integration-style API tests`. The new bullet:

```markdown
- For HTTP tests of user-visible behavior, follow the wire-level testing discipline in `context/testing.md` — exercise the request through `srv.ServeHTTP` and assert on the response a client would observe, not the handler's return value. The discipline names when to use `internal/server/apitest/`, `internal/server/e2etest/`, and the fault-injection exception.
```

The bullet sits naturally next to the existing apiclient bullet because both speak to integration-style API tests; together they tell a reader what shape to write and how to dispatch it.

- [ ] **Step 3: Verify the docs render cleanly**

Run:

```sh
git diff --check context/testing.md CLAUDE.md
```

Expected: no whitespace errors.

Re-read both files to confirm:
- `context/testing.md` has the new `## HTTP testing discipline` section between `## Race test runtime` and `## Related context`.
- The bug-class table renders as a markdown table.
- `CLAUDE.md` has exactly one new bullet, positioned after the apiclient bullet under `### Test Guidelines`.

- [ ] **Step 4: Commit**

```sh
git add context/testing.md CLAUDE.md
git commit -m "docs: spell out wire-level HTTP testing discipline

The boundary between handler-internal tests and tests that exercise the
full middleware chain through srv.ServeHTTP has been unwritten, so bug
classes that only show up on the wire (header rewrites, status
overrides, schema drift against the OpenAPI contract, mutation guard
short-circuits) can slip through. Add an HTTP testing discipline
section to context/testing.md that names the two wire-level transports,
the fault-injection exception, when to choose each shape, and the bug
classes wire-level catches. Cross-reference it from CLAUDE.md."
```

---

### Task 5: Final Verification

**Spec reference:** Acceptance criteria.

- [ ] **Step 1: Run the full Go test suite**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./... -shuffle=on
```

Expected: all tests pass. The three new tests and the unchanged original branch-conflict test all show up in their respective packages.

- [ ] **Step 2: Run the short-mode test suite**

Run:

```sh
nix shell 'nixpkgs#go' --command go test ./... -short -shuffle=on
```

Expected: all tests pass; the mutation-guard test in `apitest/` runs; the SSE contract test in `e2etest/` runs (e2etest has no package-level short skip); the workspacetest migration is skipped by `setupWorkspaceServerFixture`'s existing `t.Skip`.

- [ ] **Step 3: Run go vet**

Run:

```sh
nix shell 'nixpkgs#go' --command go vet ./...
```

Expected: clean (no findings).

- [ ] **Step 4: Run the linter**

Run:

```sh
nix shell 'nixpkgs#golangci-lint' --command golangci-lint run
```

Expected: clean. If any new finding appears in the new test files, fix it in a new commit.

- [ ] **Step 5: Confirm `git status` is clean**

Run:

```sh
git status
```

Expected: working tree clean. All four commits (Tasks 1-4) on the current branch.

---

## Self-Review

**Spec coverage:**
- "Operational definition" — covered by the new `## HTTP testing discipline` section in `context/testing.md` (Task 4 Step 1).
- "When to write what shape" — same section, the per-shape guidance bullet list.
- "Bug classes" — same section, the bug-class table.
- "Where the doctrine lives" — Tasks 4 Step 1 (testing.md) and Step 2 (CLAUDE.md).
- Worked example 1, SSE contract pin — Task 1.
- Worked example 2, mutation guard 415 — Task 2.
- Worked example 3, branch-conflict 409 migration — Task 3.
- "Lint target" — explicitly out of scope, noted in the testing.md section.
- "File touchpoints" — every touchpoint in the spec is created or modified by an explicit step.
- Acceptance criteria — Task 5 covers the full-suite, short-mode, vet, and lint gates.

**Placeholder scan:** no "TBD", "TODO", "implement later", "similar to Task N", or hand-wave steps. Every code block is complete and self-contained.

**Type consistency:**
- `generated.CreateIssueWorkspaceInputBody{}` matches the type at `internal/apiclient/generated/client.gen.go:279`.
- `generated.ErrorModel` matches `internal/apiclient/generated/client.gen.go:391`.
- `ErrorModel.Errors` is `*[]ErrorDetail` (per the generated source), so the test dereferences with `*problem.Errors` and ranges over the slice — matches the code in Task 3 Step 3.
- `ErrorDetail.Location` is `*string`, so the test guards with `if e.Location == nil { continue }` — matches the code.
- `server.Event{Type: ..., Data: ...}` matches `internal/server/event_hub.go:9`.
- `srv.Hub().Broadcast(...)` matches `internal/server/server.go:198` (`func (s *Server) Hub() *EventHub`).
- `setupTestServer(t)` returns `(*server.Server, *db.DB)` in both `internal/server/apitest/fixtures_test.go:26` and `internal/server/e2etest/fixtures_test.go:23`. Both task tests use the same name and signature.
- `setupWorkspaceServerFixture` in `internal/server/workspacetest/fixtures_test.go:36` returns `workspaceServerFixture`, which exposes `server`, `client`, `database`, `bare`, `remote` — all referenced consistently in Task 3.
- `runGit` is package-local to `workspacetest/fixtures_test.go:161` and to `internal/server/api_test.go:11466`; the test in Task 3 uses the workspacetest version (no cross-package import).
