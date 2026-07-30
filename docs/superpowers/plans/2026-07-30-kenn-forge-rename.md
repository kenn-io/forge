# Kenn Forge Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the maintained product and source tree to Kenn Forge, ship the `kenn-forge` CLI and `go.kenn.io/forge` module, and migrate persisted user state into the new identity.

**Approved spec/design:** `docs/superpowers/specs/2026-07-30-kenn-forge-rename-design.md`

**Architecture:** A repository-local Go codemod performs deterministic path and content rewrites and enforces a narrow legacy-name allowlist. Runtime compatibility is isolated in one config migration boundary that moves the old default home, renames product-specific data files in default or custom data directories, and rewrites known built-in config values. A frontend bootstrap migration transfers browser storage before application modules initialize.

**Tech Stack:** Go 1.26, Cobra, BurntSushi TOML, gofrs/flock, SQLite, Svelte 5, TypeScript, Vite+, Vitest, Bun, Rust/Cargo.

## Global Constraints

- Product copy is “Kenn Forge”; the primary CLI is `kenn-forge`; the Go module is `go.kenn.io/forge`.
- New environment variables use `KENN_FORGE_*`; no legacy command or environment fallback is permitted.
- The default home is `~/.kenn/forge/`; only the old default home moves, while explicit config and data directory choices remain authoritative.
- The GitHub repository rename is deferred; do not change the current Git remote or perform external repository mutations.
- Shipped migration files are immutable. One new forward migration renames live `middleman_*` schema objects to `forge_*`; old schema names remain only in migration history and reversible migration statements.
- Landed dated design and implementation-plan artifacts remain historical records and are excluded from mechanical prose rewriting.
- Mechanical edits must be made by `tools/renameforge`; manual edits are limited to migration semantics and compile/test fixes that the declarative mappings cannot express safely.
- Use `bun`, never npm. Invoke frontend tooling through the checked-in Vite+ binary.
- Never amend commits, change branches, bypass hooks, or use `--no-verify`.

---

### Task 1: Deterministic Rename Codemod

**Files:**
- Create: `tools/renameforge/main.go`
- Create: `tools/renameforge/rename.go`
- Create: `tools/renameforge/mappings.go`
- Create: `tools/renameforge/rename_test.go`

**Interfaces:**
- Consumes: repository root plus the NUL-delimited tracked path list from `git ls-files -z`.
- Produces: `func Rewrite(root string, paths []string, checkOnly bool) (Report, error)`, `func RenderSchemaRename(objects []SchemaObject) (upSQL, downSQL []byte, err error)`, deterministic path/content/schema mappings, symlink-safe path moves including binary files, and a CLI with apply, `--write-schema-migration`, and `--check` modes. Check mode also renders and compares migration 44 and rejects incompatible flag combinations.

- [ ] **Step 1: Write focused failing tests for the codemod engine**

Create table-driven tests covering ordered content rewrites, longest-path-first moves, symlink target rewriting, binary-content skipping with path movement, destination-collision errors, rerun determinism before staging, audit allowlisting, self-hosted module-import rewriting, incompatible flags, migration drift detection, and reversible schema-migration rendering. Use a temporary root and explicit path/object slices so tests exercise owned rewrite logic rather than Git behavior.

```go
func TestRewriteAppliesCanonicalMappings(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		wantPath string
		wantBody string
	}{
		{
			name: "go module",
			path: "cmd/middleman/main.go",
			body: "import \"go.kenn.io/middleman/internal/config\"\n",
			wantPath: "cmd/kenn-forge/main.go",
			wantBody: "import \"go.kenn.io/forge/internal/config\"\n",
		},
		{
			name: "environment and prose",
			path: "README.md",
			body: "Middleman reads MIDDLEMAN_GITHUB_TOKEN.\n",
			wantPath: "README.md",
			wantBody: "Kenn Forge reads KENN_FORGE_GITHUB_TOKEN.\n",
		},
	}
	// Materialize each case, call Rewrite, and compare path and bytes.
}
```

- [ ] **Step 2: Run the codemod tests and confirm they fail**

Run: `go test ./tools/renameforge -shuffle=on`

Expected: FAIL because `Rewrite`, `Report`, and the mapping declarations do not exist.

- [ ] **Step 3: Implement the codemod and declarative mappings**

Implement stable rewrites with these ordered canonical cases before the generic case:

```go
var contentRules = []Rule{
	{Old: "go.kenn.io/middleman", New: "go.kenn.io/forge"},
	{Old: "@middleman/ui", New: "@kenn-forge/ui"},
	{Old: "@middleman/github-app-ui", New: "@kenn-forge/github-app-ui"},
	{Old: "middleman-github-app", New: "kenn-forge-github-app"},
	{Old: "middleman-openapi", New: "kenn-forge-openapi"},
	{Old: "MIDDLEMAN", New: "KENN_FORGE"},
	{Old: "Middleman", New: "Kenn Forge"},
}
```

Handle lowercase forms by syntax category rather than a blind identifier-breaking substitution: camel-case identifiers become `forge...`, double-underscore globals become `__kenn_forge...`, package scopes and slugs become `kenn-forge`, current SQL identifiers become `forge_*`, and current user-facing lowercase prose becomes `kenn forge` only where the mapping declares it. Skip shipped `internal/db/migrations/**` content and landed dated files under `docs/superpowers/specs/` and `docs/superpowers/plans/` other than this rename's spec/plan. Preserve executable bits and symlink identity.

The CLI obtains tracked files with `git ls-files -z`, preflights all source/destination collisions and readability before mutation, recognizes destinations already moved while the Git index still names their source, applies all path moves before content rewrites, and reports changed, moved, skipped-binary, and allowlisted counts. `--write-schema-migration` snapshots the version-43 `sqlite_schema`, renders ordered table renames plus drop/recreate statements for old-named triggers and indexes, migrates `__middleman_unknown__` and `__middleman_recovery_pending__` values, and writes the single `000044_rename_schema_to_forge.{up,down}.sql` pair through staged files so a partial pair is never published. `--check` performs no writes, compares migration 44 to freshly rendered output, and exits nonzero for drift, an unapplied canonical rewrite, or a legacy occurrence outside the explicit allowlist. Reject `--check` combined with `--write-schema-migration`.

- [ ] **Step 4: Run codemod unit tests and formatting**

Run: `gofmt -w tools/renameforge/*.go`

Run: `go test ./tools/renameforge -shuffle=on`

Expected: PASS.

- [ ] **Step 5: Context-sync and commit the codemod**

Run the repository-local `context-sync --commit` workflow, then the required commit workflow.

```bash
git add tools/renameforge
git commit -m "build: make the Kenn Forge rename reproducible"
```

### Task 2: Apply the Canonical Source Rename

**Files:**
- Rename: `cmd/middleman/` to `cmd/kenn-forge/`
- Rename: `cmd/middleman-github-app/` to `cmd/kenn-forge-github-app/`
- Rename: `cmd/middleman-openapi/` to `cmd/kenn-forge-openapi/`
- Rename: repository-local skill paths containing `middleman` to their `kenn-forge` equivalents
- Modify mechanically: all tracked maintained source, tests, manifests, scripts, fixtures, current docs/context, CI, Docker, build, generated API, Rust files, and current SQL selected by the codemod
- Preserve: `internal/db/migrations/000001_*` through `000043_*` and landed dated design/plan artifacts
- Create: `internal/db/migrations/000044_rename_schema_to_forge.up.sql`
- Create: `internal/db/migrations/000044_rename_schema_to_forge.down.sql`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Consumes: `tools/renameforge` from Task 1.
- Produces: `kenn-forge`, `kenn-forge-github-app`, and `kenn-forge-openapi` command trees; `go.kenn.io/forge/...` imports; `@kenn-forge/ui` workspace imports; `KENN_FORGE_*` runtime configuration; renamed build/package/Rust identities; `forge_*` live SQLite objects and current queries.

- [ ] **Step 1: Capture the pre-rename inventory**

Run: `go run ./tools/renameforge --check`

Expected: nonzero with a categorized report of unapplied paths and content. Save no generated report file.

- [ ] **Step 2: Apply the codemod once**

Run: `go run ./tools/renameforge`

Expected: tracked files and paths are rewritten with no collisions. The tool source retains only its declared legacy mapping strings.

- [ ] **Step 3: Prove the codemod is idempotent**

Run: `go run ./tools/renameforge`

Expected: zero changed and zero moved files even before staging, proving interrupted or partially indexed runs resume safely.

- [ ] **Step 4: Write a failing upgrade test for schema identity**

Build representative version-1, version-2, version-3, and version-43 databases with parent and child rows. Upgrade each through `db.Open`, and query `sqlite_schema`. Assert that bootstrap detection still recognizes the old schema/version metadata, all rows survive, `PRAGMA integrity_check` is `ok`, `PRAGMA foreign_key_check` is empty, live tables/triggers/index names and SQL contain no legacy product token, and both workspace sentinels become their `__forge_*` forms.

Run: `go test ./internal/db -run TestSchemaIdentityMigration -shuffle=on`

Expected: FAIL because migration 44 does not exist and current SQL still uses legacy schema names.

- [ ] **Step 5: Generate and apply the single forward schema migration**

Run: `go run ./tools/renameforge --write-schema-migration`

Expected: creates exactly the `000044` up/down pair without modifying migrations 1–43. The up migration renames all live `middleman_*` tables to `forge_*`, renames old-named triggers/indexes by recreate, and migrates both workspace sentinel values without losing dependent rows. The down migration reverses those exact operations.

Run: `go run ./tools/renameforge --check`

Expected: PASS only when the checked-in migration pair exactly matches fresh rendering and all residual old-name occurrences fit the explicit allowlist.

Run: `go test ./internal/db -run 'TestSchemaIdentityMigration|TestLegacyMigrationUpgrade' -shuffle=on`

Expected: PASS with clean integrity and foreign-key checks.

Run: `go run ./tools/migrationhistorycheck`

Expected: PASS, confirming migrations 1–43 are unchanged and the branch introduces only migration 44.

- [ ] **Step 6: Regenerate dependency and API artifacts through project tooling**

Run: `bun install --frozen-lockfile --ignore-scripts`

Run: `go mod tidy`

Run: `make api-generate`

Run: `cargo check --manifest-path rust/pty-manager/Cargo.toml`

Expected: package locks, generated OpenAPI clients, and Rust metadata refer only to canonical Kenn Forge identities outside approved legacy boundaries.

- [ ] **Step 7: Use the required Svelte analysis workflow on mechanically changed components**

Load `svelte-code-writer` and `svelte-core-bestpractices`, run their prescribed formatter/analyzer over changed `.svelte` files, and apply only fixes required by the rename. Do not redesign components.

- [ ] **Step 8: Run compile-oriented validation**

Run: `go test ./... -short -shuffle=on`

Run: `./node_modules/.bin/vp run frontend-check`

Run: `cargo test --manifest-path rust/pty-manager/Cargo.toml`

Expected: PASS. Fix mechanical misses by adding or correcting codemod mappings first, rerunning the tool, and then rerunning these commands; do not hand-edit repeated rename patterns.

- [ ] **Step 9: Context-sync and commit the mechanical rename**

Run the repository-local `context-sync --commit` workflow and required commit workflow.

```bash
git add -A
git commit -m "refactor: establish the Kenn Forge product identity"
```

### Task 3: Migrate Filesystem State on First Use

**Files:**
- Create: `internal/config/legacy_migration.go`
- Create: `internal/config/legacy_migration_test.go`
- Create: `cmd/kenn-forge/legacy_migration_e2e_test.go`
- Modify: `internal/config/config.go`
- Modify: config-loading call sites under `cmd/kenn-forge/`, `cmd/kenn-forge-github-app/`, `internal/cli/`, and `tools/devephemeral/`
- Modify: `internal/runtimelock/paths.go` only if a narrow helper is needed to probe legacy lock liveness
- Modify: `internal/docs/ignore.go`
- Modify: `internal/docs/ignore_test.go`
- Modify: generated agent-context ownership-marker handling and its existing tests

**Interfaces:**
- Consumes: old default home `~/.config/middleman/`, new default home `~/.kenn/forge/`, an explicitly selected config path, and the legacy runtime lock in the resolved data directory.
- Produces: `func LoadOrCreate(path string) (*Config, error)` for create-on-use commands and `func LoadExisting(path string) (*Config, error)` for non-creating first invocations; a legacy lock held at its original stable pathname; resumable per-entry home migration; `middleman.db` to `forge.db` data-file migration in default or custom data directories; rebased path-valued config; `.middlemanignore` to `.kenn-forgeignore` migration; preserved recognition of the old generated-agent-context marker.

- [ ] **Step 1: Write failing table-driven migration tests**

Cover these observable cases with real temporary directories:

```go
func TestLoadOrCreateMigratesLegacyState(t *testing.T) {
	tests := []struct {
		name string
		setup func(t *testing.T, oldHome, newHome string)
		check func(t *testing.T, oldHome, newHome string, cfg *Config, err error)
	}{
		{name: "fresh install creates new home"},
		{name: "default home moves with database and auth state"},
		{name: "custom data directory stays in place while database file is renamed"},
		{name: "absolute token and private key paths under old home are rebased"},
		{name: "empty destination is adopted"},
		{name: "nonempty old and new homes fail without merging"},
		{name: "active legacy daemon fails without moving files"},
		{name: "second legacy lock acquisition remains blocked during home movement"},
		{name: "cross device rename stages validates and publishes"},
		{name: "prepared marker resumes before publish"},
		{name: "staged copy resumes before validation"},
		{name: "validated stage resumes before publish"},
		{name: "published marker resumes legacy source cleanup"},
	}
	// Each case sets HOME and clears KENN_FORGE_HOME before calling LoadOrCreate.
}
```

Also assert that an explicit config path does not relocate its directory, known `MIDDLEMAN_*` values in that config become `KENN_FORGE_*`, every path-valued field beneath the old home is rebased while external custom paths remain unchanged, an existing `forge.db` plus `middleman.db` is a conflict, an active daemon in a custom `data_dir` blocks all movement, the held legacy lock is never unlinked, and failed staged-copy validation preserves the source. Inject a `migrationOps` value with per-entry rename, copy, validation, and marker-write functions so `EXDEV` and every interruption phase are deterministic rather than dependent on the host filesystem.

Add Docs ignore tests that migrate a lone `.middlemanignore` in place, preserve its contents, and fail when both old and new names exist. Add an agent-context test that passes the fixed old ownership marker through the existing generated-file update path and proves it is still recognized as app-owned.

- [ ] **Step 2: Run focused tests and confirm failure**

Run: `go test ./internal/config -run 'TestLoadOrCreate|TestLegacyMigration' -shuffle=on`

Expected: FAIL because the migration boundary does not exist and defaults still use the old home and filenames.

- [ ] **Step 3: Implement the resumable home move**

Resolve enough of the legacy config to find its effective `data_dir`, acquire and hold the legacy flock at its original pathname before moving or renaming anything, and release it only after migration succeeds or fails. Never move or unlink the held lock file. Add the public loading boundaries with this shape:

```go
func LoadOrCreate(path string) (*Config, error) {
	return loadWithMigration(path, true)
}

func LoadExisting(path string) (*Config, error) {
	return loadWithMigration(path, false)
}
```

`loadWithMigration` first identifies the existing legacy config without creating a new file, decodes the legacy `data_dir`, and holds the old-named flock in that directory. It then migrates the default home when applicable, creates a config only when requested, rewrites known config values, loads the canonical config, migrates data filenames, and releases the legacy lock.

Use `.kenn-forge-migration.json` with explicit `prepared`, `staged`, `validated`, and `published` phases. Move entries other than the held legacy lock from the old home into the new home. Prefer per-entry `os.Rename`; on injected or real `EXDEV`, copy into a sibling staging directory, preserving directory/file modes and symlink targets, compare type/mode/size/SHA-256 or symlink target for every copied entry, publish with rename, record `published`, and only then remove the corresponding old entry. On restart, resume only a marker whose recorded source and destination exactly match the canonical old and new homes. Recover a publish that completed before the phase write by validating the destination against the recorded source rather than treating it as an unrelated collision.

- [ ] **Step 4: Implement config and data-file rewriting**

Atomically rewrite known built-in config values while preserving unrelated bytes and file mode. Rebase every absolute `token_file`, GitHub App `private_key_path`, and other path-valued config field beneath the old home to the corresponding new-home path; preserve paths outside the old home. Rename `middleman.db` to `forge.db` within the resolved data directory, including custom directories, while holding the legacy runtime lock. Remove stale legacy runtime metadata after the database rename but leave the old lock file in place. If both database names exist, return a conflict with both exact paths.

Change defaults to `KENN_FORGE_HOME`, `~/.kenn/forge/`, `forge.db`, `kenn-forge.lock`, and `kenn-forge.run.json`. Route every production first-load path through `LoadOrCreate` or `LoadExisting`, including docs list/removal, daemon discovery, agent-hook installation, GitHub App commands, control commands, and development tools. Preserve commands such as docs list that intentionally do not create a fresh config by using `LoadExisting`. Leave plain `Load` available only for side-effect-free reloads after startup. Migrate each registered Docs folder's root ignore file in place, and retain the exact old generated-agent-context ownership marker as a legacy input.

- [ ] **Step 5: Add a real CLI/database migration test**

Create a subprocess-style test that builds an old default home with a real version-43 SQLite database and stored repository row, starts the renamed CLI on an ephemeral loopback port, waits for its normal readiness endpoint, reads the migrated repository through the generated API client, and terminates only the process created by the test. Assert the old home is gone, `forge.db` is present, and the API returns the pre-migration row. This uniquely proves command entry, database opening, and server serving after migration.

- [ ] **Step 6: Run focused and integration validation**

Run: `gofmt -w internal/config cmd/kenn-forge cmd/kenn-forge-github-app internal/cli tools/devephemeral`

Run: `go test ./internal/config ./internal/runtimelock ./cmd/kenn-forge ./cmd/kenn-forge-github-app -shuffle=on`

Run: `go test ./... -short -shuffle=on`

Expected: PASS, including existing custom-config and daemon-collision behavior.

- [ ] **Step 7: Context-sync and commit filesystem migration**

Run the repository-local `context-sync --commit` workflow and required commit workflow.

```bash
git add internal/config internal/runtimelock cmd/kenn-forge cmd/kenn-forge-github-app internal/cli tools/devephemeral
git commit -m "feat: migrate existing state into Kenn Forge"
```

### Task 4: Transfer Browser-Persisted State Before App Initialization

**Files:**
- Create: `frontend/src/lib/utils/kennForgeStorageMigration.ts`
- Create: `frontend/src/lib/utils/kennForgeStorageMigration.test.ts`
- Create: `frontend/src/kenn-forge-storage-migration.browser.svelte.ts`
- Create: `frontend/src/runStorageMigration.ts`
- Modify: `frontend/src/main.ts`

**Interfaces:**
- Consumes: any same-origin `localStorage` or `sessionStorage` key containing the legacy lowercase product token.
- Produces: `migrateKennForgeStorage(storage: Storage): void`; a side-effect bootstrap module imported before `App.svelte` that runs against both storage areas, transfers values to the `kenn-forge` key, and removes the legacy key only after the new value exists.

- [ ] **Step 1: Write failing storage migration tests**

```ts
describe("migrateKennForgeStorage", () => {
  it("moves static and dynamic legacy keys", () => {
    localStorage.setItem("middleman-sidebar", "collapsed");
    localStorage.setItem("middleman-workspace-active-tab:ws-1", "shell");

    migrateKennForgeStorage(localStorage);

    expect(localStorage.getItem("kenn-forge-sidebar")).toBe("collapsed");
    expect(localStorage.getItem("kenn-forge-workspace-active-tab:ws-1")).toBe("shell");
    expect(localStorage.getItem("middleman-sidebar")).toBeNull();
  });
});
```

Add cases proving an existing new key wins, unrelated keys remain untouched, a failed write leaves the old key intact, and one failing key does not prevent independent keys from migrating. Add a Vitest browser test that seeds real browser `localStorage` and `sessionStorage`, resets modules, dynamically imports `runStorageMigration.ts`, verifies both side effects, and then dynamically imports a storage-reading store to prove it observes the transferred value.

- [ ] **Step 2: Run the focused test and confirm failure**

Run: `./node_modules/.bin/vp test run frontend/src/lib/utils/kennForgeStorageMigration.test.ts`

Run: `./node_modules/.bin/vp test --project browser frontend/src/kenn-forge-storage-migration.browser.svelte.ts`

Expected: FAIL because the migration function does not exist.

- [ ] **Step 3: Implement pure migration and bootstrap ordering**

Snapshot storage keys before mutation. For each key containing `middleman`, derive the target with `replaceAll("middleman", "kenn-forge")`. Preserve an existing target value. Remove the old key only after the target exists or a write succeeds. Catch errors per key because browsers may deny individual storage operations.

Have `runStorageMigration.ts` call the pure function for both `window.localStorage` and `window.sessionStorage` at module evaluation. Make it the first side-effect import in `main.ts`, before the static `App.svelte` import, so store modules observe new keys during initialization.

- [ ] **Step 4: Run Svelte-aware checks and the full frontend suite**

Use the required Svelte skills for the changed bootstrap/component graph.

Run: `./node_modules/.bin/vp test run frontend/src/lib/utils/kennForgeStorageMigration.test.ts`

Run: `./node_modules/.bin/vp test`

Run: `./node_modules/.bin/vp run frontend-check`

Run: `make frontend`

Expected: PASS.

- [ ] **Step 5: Context-sync and commit browser migration**

Run the repository-local `context-sync --commit` workflow and required commit workflow.

```bash
git add frontend/src/main.ts frontend/src/runStorageMigration.ts frontend/src/kenn-forge-storage-migration.browser.svelte.ts frontend/src/lib/utils/kennForgeStorageMigration.*
git commit -m "feat: preserve browser preferences through the rename"
```

### Task 5: Final Audit, Documentation Consistency, and Release Build

**Files:**
- Modify: `tools/renameforge/mappings.go` only for evidence-backed missed mechanical categories
- Modify: current docs/context/skills only when the audit finds a maintained product identity missed by Task 2
- Modify: generated artifacts only through their owning generator

**Interfaces:**
- Consumes: completed source and runtime/browser migrations.
- Produces: clean codemod audit, fully verified `kenn-forge` build, and context that consistently describes the renamed product while retaining narrow legacy migration/schema evidence.

- [ ] **Step 1: Run the codemod audit and inspect every exception**

Run: `go run ./tools/renameforge --check`

Run: `rg -n --hidden -S 'middleman|Middleman|MIDDLEMAN' -g '!node_modules' -g '!.git'`

Expected: codemod check exits zero. Every raw search result is one of: codemod mapping input, immutable or reversible migration SQL, filesystem/browser migration input, legacy-input fixture, migration documentation, or a landed dated design/plan artifact. No live SQLite schema object or current SQL query may retain the old name. Fix repeated mechanical misses in mappings and rerun the codemod rather than editing occurrences individually.

- [ ] **Step 2: Run final generated-artifact and source validation**

Run: `make api-generate`

Run: `git diff --exit-code -- frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated packages/ui/src/api/generated`

Run: `make test`

Run: `make vet`

Run: `make lint`

Run: `make rust-test`

Run: `./node_modules/.bin/vp test`

Run: `./node_modules/.bin/vp run frontend-check`

Expected: PASS with no generated drift.

- [ ] **Step 3: Build the renamed embedded application**

Run: `make build`

Run: `./kenn-forge version`

Run: `go build -o ./tmp/kenn-forge-github-app ./cmd/kenn-forge-github-app && ./tmp/kenn-forge-github-app --help`

Run: `go build -o ./tmp/kenn-forge-openapi ./cmd/kenn-forge-openapi && ./tmp/kenn-forge-openapi --help`

Expected: the primary version output and both helper usage banners use their canonical binary names; no legacy binary is produced.

- [ ] **Step 4: Run context sync for the completed rename**

Invoke `context-sync --commit`. Update mapped context only where the completed code moved anchors or where current product naming would otherwise mislead future work. Do not rewrite immutable historical migration evidence.

- [ ] **Step 5: Commit any final audit corrections**

If the final audit or context sync changed files, use the required commit workflow:

```bash
git add -A
git commit -m "docs: align Kenn Forge development context"
```

If there is no final diff, do not create an empty commit. Do not push, open a pull request, rename the GitHub repository, or monitor CI unless separately requested.
