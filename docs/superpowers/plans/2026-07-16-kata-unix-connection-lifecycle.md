# Kata Unix Connection Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure disposable server-side Kata clients close Unix-socket connections after each response while the cached reverse proxy retains keep-alive reuse.

**Architecture:** Keep target parsing shared, but make transport ownership explicit at disposable call sites. A small helper clones concrete `*http.Transport` values and enables `DisableKeepAlives`; task reads apply it to Unix and per-client TCP transports, health probes apply it only to owned Unix transports, and the cached proxy remains unchanged.

**Tech Stack:** Go `net/http`, Unix domain sockets, `http.Server.ConnState`, testify.

## Global Constraints

- Follow red, green, refactor: behavioral tests must fail before production code changes.
- Exercise real HTTP requests over temporary Unix sockets; do not inspect source text.
- Keep test socket paths short enough for macOS by setting `TMPDIR=/tmp` before `t.TempDir()`.
- Use synthetic daemon, project, and issue identities only.
- Do not change `kataDaemonProxyTarget` or the cached proxy's keep-alive policy.
- Do not use `CloseIdleConnections` as disposable-client cleanup.
- Run Go tests with `-shuffle=on`, without `-v` or `-count=1`.

---

### Task 1: Add Failing Disposable-Read Connection Tests

**Files:**

- Create: `internal/server/kata_unix_transport_test.go`

**Interfaces:**

- Produces: a test-only Unix HTTP server helper that counts live connections through `http.Server.ConnState` and closes cleanly through `t.Cleanup`.
- Consumes: `writeKataProxyCatalog`, `setupTestServer`, `doJSON`, and the real task-detail and project-mappings handlers.

- [ ] **Step 1: Build the real Unix test server helper**

Track `http.StateNew` as an opened connection and `http.StateClosed` or `http.StateHijacked` as a closed connection. Expose the `unix://` target and an atomic live count. Cleanup closes the server and waits for `Serve` to exit.

- [ ] **Step 2: Add the task-detail lifecycle regression**

Serve synthetic issue-detail and projects responses over the Unix socket. Coordinate the handlers so both upstream requests establish separate connections before responding, invoke `/api/v1/kata/tasks/{issue}`, require a successful response, and require the observed connection count to drain to zero within a bounded interval.

- [ ] **Step 3: Add the project-mappings lifecycle regression**

Serve a synthetic projects listing over the Unix socket, invoke `/api/v1/kata/project-mappings`, require a successful response, and require the live connection count to drain to zero.

- [ ] **Step 4: Run focused tests and verify RED**

```bash
go test ./internal/server -run 'TestKata(TaskDetail|ProjectMappings)ClosesUnixConnections' -shuffle=on
```

Expected: both tests time out waiting for zero because the abandoned transports retain idle Unix connections.

---

### Task 2: Add The Failing Health-Probe Connection Test

**Files:**

- Modify: `internal/server/kata_routes_test.go`

- [ ] **Step 1: Extend the existing Unix health test**

Attach the same `ConnState` accounting to `TestKataDaemonsEndpointHealthOverUnixSocket`. After the roster reports the daemon connected, require the probe connection to drain to zero within a bounded interval.

- [ ] **Step 2: Run the focused test and verify RED**

```bash
go test ./internal/server -run '^TestKataDaemonsEndpointHealthOverUnixSocket$' -shuffle=on
```

Expected: the test times out waiting for zero because the Unix probe transport leaves its connection idle.

---

### Task 3: Apply The Disposable Transport Policy

**Files:**

- Modify: `internal/server/kata_proxy.go`
- Modify: `internal/server/kata_task_detail.go`
- Modify: `internal/server/kata_routes.go`

**Interfaces:**

- Produces: `disposableKataDaemonTransport(http.RoundTripper) http.RoundTripper`.
- Consumes: concrete transports returned by `kataDaemonProxyTarget`, `kataDaemonProbeTarget`, and `newDefaultKataDaemonTransport`.

- [ ] **Step 1: Implement the ownership helper**

Type-assert to `*http.Transport`, clone it, set `DisableKeepAlives = true`, and return the clone. Preserve a non-concrete round tripper unchanged so the helper does not invent ownership for unknown implementations.

- [ ] **Step 2: Apply it only to disposable clients**

In `kataDaemonHTTPClient`, apply the helper after filling the nil HTTP/HTTPS transport with the per-client default. In `probeKataDaemon`, apply it only when `kataDaemonProbeTarget` returned a non-nil owned transport; leave the nil HTTP/HTTPS case sharing `http.DefaultTransport`.

- [ ] **Step 3: Run focused tests and verify GREEN**

```bash
go test ./internal/server -run 'TestKata(TaskDetail|ProjectMappings)ClosesUnixConnections|TestKataDaemonsEndpointHealthOverUnixSocket|TestKataProxyForwardsViaUnixSocket' -shuffle=on
```

Expected: PASS, including the existing cached reverse-proxy Unix test.

- [ ] **Step 4: Run the server package and full Go suite**

```bash
go test ./internal/server -shuffle=on
go test ./... -shuffle=on
```

Expected: PASS.

---

### Task 4: Commit And Verify The Live Rollout

**Files:**

- Verify only: installed middleman binary and service state.

- [ ] **Step 1: Scrub and commit the implementation**

Review only the intended Go and test changes, scan the public diff and commit message for private data, and create a separate conventional commit for the implementation without amending the design commit.

- [ ] **Step 2: Build and install current branch safely**

Keep the production Kata data untouched, build through the repository-supported install path, and confirm the installed revision contains both frontend commit `56e34a95` and the Unix transport fix.

- [ ] **Step 3: Restart middleman and reproduce the original workload**

Load the service, reconnect the browser, and observe Kata daemon CPU plus middleman and Kata Unix-socket descriptor counts across catch-up and repeated refreshes.

- [ ] **Step 4: Verify both halves independently**

Confirm CPU settles after one batched frontend revalidation and descriptor counts remain bounded. If either signal grows, stop middleman again and retain the passing code/tests without claiming the live fix.
