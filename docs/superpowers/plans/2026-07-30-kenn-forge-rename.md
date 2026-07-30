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
- Shipped migration files and SQLite schema identifiers are immutable persistence history and remain narrowly allowlisted.
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
- Produces: `func Rewrite(root string, paths []string, checkOnly bool) (Report, error)`, deterministic `pathRules` and `contentRules`, symlink-safe path moves, and a CLI with apply mode plus `--check` audit mode.

- [ ] **Step 1: Write focused failing tests for the codemod engine**

Create table-driven tests covering ordered content rewrites, longest-path-first moves, symlink target rewriting, binary-file skipping, destination-collision errors, rerun determinism, and audit allowlisting. Use a temporary root and explicit path slices so tests exercise owned rewrite logic rather than Git behavior.

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

Handle lowercase forms by syntax category rather than a blind identifier-breaking substitution: camel-case identifiers become `forge...`, double-underscore globals become `__kenn_forge...`, package scopes and slugs become `kenn-forge`, and current user-facing lowercase prose becomes `kenn forge` only where the mapping declares it. Skip shipped `internal/db/migrations/**` content and persisted `middleman_*` SQL identifiers. Preserve executable bits and symlink identity.

The CLI obtains tracked files with `git ls-files -z`, applies all path moves before content rewrites, and reports changed, moved, skipped-binary, and allowlisted counts. `--check` performs no writes and exits nonzero for an unapplied canonical rewrite or a legacy occurrence outside the explicit allowlist.

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
- Modify mechanically: all tracked maintained source, tests, manifests, scripts, fixtures, docs, CI, Docker, build, generated API, and Rust files selected by the codemod
- Preserve: `internal/db/migrations/**` structural content and persisted `middleman_*` schema references

**Interfaces:**
- Consumes: `tools/renameforge` from Task 1.
- Produces: `kenn-forge`, `kenn-forge-github-app`, and `kenn-forge-openapi` command trees; `go.kenn.io/forge/...` imports; `@kenn-forge/ui` workspace imports; `KENN_FORGE_*` runtime configuration; renamed build/package/Rust identities.

- [ ] **Step 1: Capture the pre-rename inventory**

Run: `go run ./tools/renameforge --check`

Expected: nonzero with a categorized report of unapplied paths and content. Save no generated report file.

- [ ] **Step 2: Apply the codemod once**

Run: `go run ./tools/renameforge`

Expected: tracked files and paths are rewritten with no collisions. The tool source retains only its declared legacy mapping strings.

- [ ] **Step 3: Prove the codemod is idempotent**

Run: `go run ./tools/renameforge`

Expected: zero changed and zero moved files.

- [ ] **Step 4: Regenerate dependency and API artifacts through project tooling**

Run: `bun install --frozen-lockfile --ignore-scripts`

Run: `go mod tidy`

Run: `make api-generate`

Run: `cargo check --manifest-path rust/pty-manager/Cargo.toml`

Expected: package locks, generated OpenAPI clients, and Rust metadata refer only to canonical Kenn Forge identities outside approved legacy boundaries.

- [ ] **Step 5: Use the required Svelte analysis workflow on mechanically changed components**

Load `svelte-code-writer` and `svelte-core-bestpractices`, run their prescribed formatter/analyzer over changed `.svelte` files, and apply only fixes required by the rename. Do not redesign components.

- [ ] **Step 6: Run compile-oriented validation**

Run: `go test ./... -short -shuffle=on`

Run: `./node_modules/.bin/vp run frontend-check`

Run: `cargo test --manifest-path rust/pty-manager/Cargo.toml`

Expected: PASS. Fix mechanical misses by adding or correcting codemod mappings first, rerunning the tool, and then rerunning these commands; do not hand-edit repeated rename patterns.

- [ ] **Step 7: Context-sync and commit the mechanical rename**

Run the repository-local `context-sync --commit` workflow and required commit workflow.

```bash
git add -A
git commit -m "refactor: establish the Kenn Forge product identity"
```

### Task 3: Migrate Filesystem State on First Use

**Files:**
- Create: `internal/config/legacy_migration.go`
- Create: `internal/config/legacy_migration_test.go`
- Modify: `internal/config/config.go`
- Modify: config-loading call sites under `cmd/kenn-forge/`, `cmd/kenn-forge-github-app/`, `internal/cli/`, and `tools/devephemeral/`
- Modify: `internal/runtimelock/paths.go` only if a narrow helper is needed to probe legacy lock liveness

**Interfaces:**
- Consumes: old default home `~/.config/middleman/`, new default home `~/.kenn/forge/`, and an explicitly selected config path.
- Produces: `func LoadOrCreate(path string) (*Config, error)` as the production startup boundary; resumable home migration; `middleman.db` to `forge.db` data-file migration in default or custom data directories; rewritten built-in config values.

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
		{name: "nonempty old and new homes fail without merging"},
		{name: "active legacy daemon fails without moving files"},
		{name: "cross device rename stages validates and publishes"},
		{name: "published marker resumes legacy source cleanup"},
	}
	// Each case sets HOME and clears KENN_FORGE_HOME before calling LoadOrCreate.
}
```

Also assert that an explicit config path does not relocate its directory, known `MIDDLEMAN_*` values in that config become `KENN_FORGE_*`, an existing `forge.db` plus `middleman.db` is a conflict, and failed staged-copy validation preserves the source.

- [ ] **Step 2: Run focused tests and confirm failure**

Run: `go test ./internal/config -run 'TestLoadOrCreate|TestLegacyMigration' -shuffle=on`

Expected: FAIL because the migration boundary does not exist and defaults still use the old home and filenames.

- [ ] **Step 3: Implement the resumable home move**

Add `LoadOrCreate` with this order:

```go
func LoadOrCreate(path string) (*Config, error) {
	if path == DefaultConfigPath() && os.Getenv("KENN_FORGE_HOME") == "" {
		if err := migrateLegacyDefaultHome(); err != nil {
			return nil, err
		}
	}
	if err := EnsureDefault(path); err != nil {
		return nil, err
	}
	if err := rewriteLegacyConfig(path); err != nil {
		return nil, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	if err := migrateLegacyDataFiles(cfg.DataDir); err != nil {
		return nil, err
	}
	return cfg, nil
}
```

Use `.kenn-forge-migration.json` with explicit `prepared` and `published` phases. Probe the old lock before moving anything. Prefer `os.Rename`; on `EXDEV`, copy into a sibling staging directory, preserving directory/file modes and symlink targets, compare type/mode/size/SHA-256 or symlink target for every copied entry, publish with rename, record `published`, and only then remove the old tree. On restart, resume only a marker whose recorded source and destination exactly match the canonical old and new homes.

- [ ] **Step 4: Implement config and data-file rewriting**

Atomically rewrite known built-in config values while preserving unrelated bytes and file mode. Rename `middleman.db` to `forge.db` within the resolved data directory, including custom directories, only after confirming the legacy runtime lock is not held. Remove stale legacy lock/runtime files after the database rename. If both database names exist, return a conflict with both exact paths.

Change defaults to `KENN_FORGE_HOME`, `~/.kenn/forge/`, `forge.db`, `kenn-forge.lock`, and `kenn-forge.run.json`. Replace production `EnsureDefault` plus `Load` pairs with `LoadOrCreate`; leave plain `Load` available for side-effect-free reloads after startup.

- [ ] **Step 5: Run focused and integration validation**

Run: `gofmt -w internal/config cmd/kenn-forge cmd/kenn-forge-github-app internal/cli tools/devephemeral`

Run: `go test ./internal/config ./internal/runtimelock ./cmd/kenn-forge ./cmd/kenn-forge-github-app -shuffle=on`

Run: `go test ./... -short -shuffle=on`

Expected: PASS, including existing custom-config and daemon-collision behavior.

- [ ] **Step 6: Context-sync and commit filesystem migration**

Run the repository-local `context-sync --commit` workflow and required commit workflow.

```bash
git add internal/config internal/runtimelock cmd/kenn-forge cmd/kenn-forge-github-app internal/cli tools/devephemeral
git commit -m "feat: migrate existing state into Kenn Forge"
```

### Task 4: Transfer Browser-Persisted State Before App Initialization

**Files:**
- Create: `frontend/src/lib/utils/kennForgeStorageMigration.ts`
- Create: `frontend/src/lib/utils/kennForgeStorageMigration.test.ts`
- Create: `frontend/src/runStorageMigration.ts`
- Modify: `frontend/src/main.ts`

**Interfaces:**
- Consumes: any same-origin `Storage` key containing the legacy lowercase product token.
- Produces: `migrateKennForgeStorage(storage: Storage): void`; a side-effect bootstrap module imported before `App.svelte` that transfers values to the `kenn-forge` key and removes the legacy key only after the new value exists.

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

Add cases proving an existing new key wins, unrelated keys remain untouched, a failed write leaves the old key intact, and one failing key does not prevent independent keys from migrating.

- [ ] **Step 2: Run the focused test and confirm failure**

Run: `./node_modules/.bin/vp test run frontend/src/lib/utils/kennForgeStorageMigration.test.ts`

Expected: FAIL because the migration function does not exist.

- [ ] **Step 3: Implement pure migration and bootstrap ordering**

Snapshot storage keys before mutation. For each key containing `middleman`, derive the target with `replaceAll("middleman", "kenn-forge")`. Preserve an existing target value. Remove the old key only after the target exists or a write succeeds. Catch errors per key because browsers may deny individual storage operations.

Have `runStorageMigration.ts` call the pure function at module evaluation. Make it the first side-effect import in `main.ts`, before the static `App.svelte` import, so store modules observe new keys during initialization.

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
git add frontend/src/main.ts frontend/src/runStorageMigration.ts frontend/src/lib/utils/kennForgeStorageMigration.*
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

Expected: codemod check exits zero. Every raw search result is one of: codemod mapping input, filesystem/browser migration input, immutable SQLite schema identifier/current SQL reference, legacy-input fixture, or migration documentation. Fix repeated mechanical misses in mappings and rerun the codemod rather than editing occurrences individually.

- [ ] **Step 2: Run final generated-artifact and source validation**

Run: `make api-generate`

Run: `git diff --exit-code -- frontend/openapi/openapi.yaml internal/apiclient/spec/openapi.json internal/apiclient/generated packages/ui/src/api/generated`

Run: `make test-short`

Run: `make vet`

Run: `make lint`

Run: `make rust-test`

Run: `./node_modules/.bin/vp test`

Run: `./node_modules/.bin/vp run frontend-check`

Expected: PASS with no generated drift.

- [ ] **Step 3: Build the renamed embedded application**

Run: `make build`

Run: `./kenn-forge version`

Expected: the build succeeds and version output identifies `kenn-forge`; no `middleman` binary is produced.

- [ ] **Step 4: Run context sync for the completed rename**

Invoke `context-sync --commit`. Update mapped context only where the completed code moved anchors or where current product naming would otherwise mislead future work. Do not rewrite immutable historical migration evidence.

- [ ] **Step 5: Commit any final audit corrections**

If the final audit or context sync changed files, use the required commit workflow:

```bash
git add -A
git commit -m "docs: align Kenn Forge development context"
```

If there is no final diff, do not create an empty commit. Do not push, open a pull request, rename the GitHub repository, or monitor CI unless separately requested.
