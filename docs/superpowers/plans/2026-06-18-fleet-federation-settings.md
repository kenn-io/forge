# Fleet Federation Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Add opt-in remote fleet federation settings without disabling local workspace behavior.

**Architecture:** Add `fleet.enabled` to config and expose a focused fleet settings API shape. Gate remote peer fan-out and remote peer proxy resolution on that flag while leaving local snapshots and self routing intact. Add a Svelte settings section for federation and hide the workspace sidebar fleet indicator when the backend returns only local-host state.

**Tech Stack:** Go, Huma, TOML config, generated OpenAPI clients, Svelte 5, Vite+ Vitest.

---

### Task 1: Backend Config And Fleet Gate

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/server/fleet_hub.go`
- Modify: `internal/server/fleet_proxy.go`
- Test: `internal/server/fleet_hub_test.go`
- Test: `internal/server/fleet_proxy_test.go`

- [x] **Step 1: Write failing config tests**

Add tests in `internal/config/config_test.go` that load a config with no `fleet.enabled` and assert false, then round-trip a config with `Fleet.Enabled = true` through `Save` and `Load`.

- [x] **Step 2: Run config tests to verify failure**

Run: `go test ./internal/config -run 'Test(FleetConfigParsesAndValidates|SaveRoundTripsFleet)' -shuffle=on`

Expected: failure because `Fleet.Enabled` does not exist.

- [x] **Step 3: Implement config field**

Add `Enabled bool 'toml:"enabled,omitempty" json:"enabled"'` to `config.Fleet`. Update default config comments to show `[fleet] enabled = false`.

- [x] **Step 4: Run config tests to verify pass**

Run: `go test ./internal/config -run 'Test(FleetConfigParsesAndValidates|SaveRoundTripsFleet)' -shuffle=on`

Expected: pass.

- [x] **Step 5: Write failing server gate tests**

Add tests proving `buildFleetSnapshot(..., true)` does not fetch HTTP peers when `Fleet.Enabled` is false, does fetch when true, and `resolveFleetHostTarget` does not resolve configured remote peers when disabled while preserving self routing. Add full-stack HTTP coverage in `internal/server/e2etest` for disabled HTTP and SSH peer membership so snapshot fan-out makes zero peer calls, remote proxy routes return `notFound`, and self-host routing remains available.

- [x] **Step 6: Run server gate tests to verify failure**

Run: `go test ./internal/server -run 'TestBuildFleetSnapshot|TestResolveFleetHostTarget' -shuffle=on`

Expected: disabled tests fail because current code always includes configured peers.

- [x] **Step 7: Implement backend gate**

In `buildFleetSnapshot`, fetch HTTP and SSH peers only when `includePeers && fleetCfg.Enabled`. In `resolveFleetHostTarget`, return remote HTTP/SSH peer targets only when `cfg.Fleet.Enabled`; keep self alias behavior unchanged.

- [x] **Step 8: Run server gate tests to verify pass**

Run: `go test ./internal/server -run 'TestBuildFleetSnapshot|TestResolveFleetHostTarget' -shuffle=on`

Expected: pass.

### Task 2: Fleet Settings API

**Files:**
- Modify: `internal/server/settings_handlers.go`
- Modify: `internal/server/settings_routes.go`
- Modify: `internal/server/e2etest/settings_test.go`
- Modify: `internal/server/config_reload.go`
- Modify: `internal/server/config_reload_test.go`
- Generated: `packages/ui/src/api/generated/schema.ts`
- Generated: `internal/apiclient/generated/client.gen.go`

- [x] **Step 1: Write failing settings API tests**

Add e2e tests for `GET /api/v1/settings` returning `fleet`, `PUT /api/v1/settings/fleet` preserving peers while toggling disabled, and invalid peer inputs returning `400`.

- [x] **Step 2: Run settings API tests to verify failure**

Run: `go test ./internal/server/e2etest -run TestSettingsAPI -shuffle=on`

Expected: failure because the fleet settings route/shape does not exist.

- [x] **Step 3: Implement fleet settings shape and route**

Add `fleetSettingsResponse`, `updateFleetSettingsRequest`, `getFleetSettings`, and `updateFleetSettings` using full-config validation and rollback on save errors. Include `restart_required`, keep `GET /settings` bootstrapping the same fleet shape, and keep the existing `/settings/fleet/ssh-peers` route aligned with the same validation, persistence, rollback, and SSH restart-drift calculation.

- [x] **Step 4: Update restart-required comparison**

Include `Fleet.Enabled`, `Fleet.Key`, `Fleet.PeerTimeout`, and HTTP peers in hot-reload behavior while preserving restart-required for SSH peers and `Fleet.Sessions`.

- [x] **Step 5: Run settings API tests to verify pass**

Run: `go test ./internal/server/e2etest -run TestSettingsAPI -shuffle=on`

Expected: pass.

- [x] **Step 6: Regenerate API clients**

Run: `make api-generate`

Expected: generated schema/client files include fleet settings types and `/settings/fleet`.

### Task 3: Frontend API And Settings Panel

**Files:**
- Modify: `packages/ui/src/api/types.ts`
- Modify: `frontend/src/lib/api/settings.ts`
- Modify: `frontend/src/lib/components/settings/SettingsPage.svelte`
- Create: `frontend/src/lib/components/settings/FleetSettings.svelte`
- Test: `frontend/src/lib/components/settings/FleetSettings.test.ts`

- [x] **Step 1: Write failing frontend settings tests**

Add tests that render `FleetSettings`, show default disabled state, keep HTTP and SSH membership visible while disabled, save through `updateFleetSettings`, and surface save errors.

- [x] **Step 2: Run frontend settings tests to verify failure**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run src/lib/components/settings/FleetSettings.test.ts`

Expected: failure because `FleetSettings.svelte` and `updateFleetSettings` do not exist.

- [x] **Step 3: Implement API helper and types**

Export fleet setting types from `packages/ui/src/api/types.ts`. Add `updateFleetSettings` to `frontend/src/lib/api/settings.ts` using `PUT /settings/fleet`.

- [x] **Step 4: Implement settings component**

Create `FleetSettings.svelte` with integrated toggle, optional key, timeout, unmanaged session detail toggle, HTTP peer rows, SSH peer rows, save/reset controls, and restrained disabled-state/help text.

- [x] **Step 5: Wire into SettingsPage**

Add a Workspace nav item and render `FleetSettings` under Workspace. Update local settings state after save.

- [x] **Step 6: Run frontend settings tests and Svelte autofixer**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run src/lib/components/settings/FleetSettings.test.ts`

Run: `node node_modules/vite-plus/bin/vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/components/settings/FleetSettings.svelte`

Expected: tests pass and autofixer reports no required changes.

### Task 4: Workspace Sidebar Fleet Indicator

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Test: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`

- [x] **Step 1: Write failing sidebar test**

Update the current local-host-only test so it expects no `Fleet` status block when the snapshot contains only the self host.

- [x] **Step 2: Run sidebar test to verify failure**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run src/lib/components/terminal/WorkspaceListSidebar.test.ts`

Expected: failure because the component currently renders the fleet block for a single local host.

- [x] **Step 3: Implement hide condition**

Change `showFleetStatus` so it renders only when there is a remote host or a fleet error related to remote status. Keep remote workspace loading based on reachable remote hosts, clear or ignore stale remote host state when snapshots contain only the local host, and make disabled federation hide remote filters, remote rows, unreachable diagnostics, route affordances, and remote operation controls.

- [x] **Step 4: Run sidebar tests and Svelte autofixer**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run src/lib/components/terminal/WorkspaceListSidebar.test.ts`

Run: `node node_modules/vite-plus/bin/vp exec svelte-mcp svelte-autofixer ./frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`

Expected: tests pass and autofixer reports no required changes.

### Task 5: Final Verification

**Files:**
- All modified files

- [x] **Step 1: Run focused Go tests**

Run: `go test ./internal/config ./internal/server ./internal/server/e2etest -run 'Test(Fleet|Settings|ConfigReload|SSHFleet)' -shuffle=on`

Expected: pass.

- [x] **Step 2: Run focused frontend tests**

Run: `cd frontend && node ../node_modules/vite-plus/bin/vp test run src/lib/components/settings/FleetSettings.test.ts src/lib/components/terminal/WorkspaceListSidebar.test.ts`

Expected: pass.

- [x] **Step 3: Run API generation check**

Run: `make api-generate`

Expected: no additional generated diff after the previous generation.

- [x] **Step 4: Review diff and commit**

Run: `git status --short`, `git diff --stat`, and `git diff --check`. Stage relevant files and commit with hooks enabled.

## Final Status

Completed in commits `a5127748` and follow-up review fixes. The implementation
adds the opt-in fleet federation config, backend gates, focused settings API,
settings panel, workspace sidebar hiding behavior, generated API artifacts, and
coverage for disabled/enabled HTTP and SSH fleet behavior.

Final verification includes stage-attached config, backend gate, full-stack
settings, disabled HTTP/SSH remote snapshot/proxy, frontend settings/sidebar,
and SSH fleet tests. The full-stack settings tests exercise a real HTTP server
and temp TOML config path; snapshot tests add SQLite-backed workspace data where
needed. Svelte autofixer checks, API generation, frontend package checks,
`git diff --check`, and hook-enforced commit validation complete the pass.
