# Kenn Forge Rename Design

## Goal

Rename the product from Middleman to Kenn Forge throughout the maintained source tree while preserving existing user data. The public CLI becomes `kenn-forge`, and the Go module becomes `go.kenn.io/forge`.

Renaming the GitHub repository is explicitly deferred. Source references may point at the future `kenn-io/kenn-forge` location, but this work must not alter the current Git remote or attempt the external repository rename.

## Canonical Identity

The renamed product uses these canonical identifiers:

| Surface | Identifier |
| --- | --- |
| Product name | Kenn Forge |
| Primary CLI and binary | `kenn-forge` |
| GitHub App helper | `kenn-forge-github-app` |
| OpenAPI helper | `kenn-forge-openapi` |
| Go module | `go.kenn.io/forge` |
| Environment prefix | `KENN_FORGE_` |
| Home override | `KENN_FORGE_HOME` |
| Default home | `~/.kenn/forge/` |
| Database file | `forge.db` |
| Runtime lock | `kenn-forge.lock` |
| Runtime metadata | `kenn-forge.run.json` |
| Daemon service identity | `kenn-forge` |
| Telemetry application identity | `kenn-forge` |

Package, workspace, Rust crate, socket, browser-storage, and build identifiers use `kenn-forge`, `kenn_forge`, or `forge` according to the syntax and surrounding convention. User-facing prose uses “Kenn Forge”; shell examples use `kenn-forge`.

Frontend-injected globals and browser-persisted keys move to a Kenn Forge namespace. The application transfers existing browser values once and then removes the legacy keys.

Historical database migrations must remain structurally valid and may not be edited. One new forward migration renames every live `middleman_*` SQLite table, trigger, and index to a `forge_*` identity, and current SQL statements use only the new names. Legacy schema names remain only inside immutable historical migrations and the new migration's reversible source-side statements.

## Compatibility Boundary

Compatibility covers persisted user data only. There is no legacy executable, command alias, environment-variable fallback, or permanent dual-read path. Users must update shell, service, and automation configuration from the old environment prefix to `KENN_FORGE_`.

An explicitly supplied `--config` path, a `KENN_FORGE_HOME` override, and an explicitly configured custom `data_dir` remain authoritative. The migration moves only the old default home. If a migrated config points at a custom data directory, that directory stays where the user placed it.

## First-Run Filesystem Migration

Before loading its default config, `kenn-forge` compares the old default home at `~/.config/middleman/` with the new default at `~/.kenn/forge/`.

When the old home exists and the new home does not, startup performs a one-shot migration:

1. Verify that no daemon represented by the old runtime state is active. If one is active, stop with an actionable error; do not terminate it.
2. Move the old home into the new location as one migration transaction.
3. Rename live product files, including the database, to their Kenn Forge names.
4. Discard stale lock and runtime metadata rather than publishing it as Kenn Forge state.
5. Rewrite only known built-in config values: an exact old default `data_dir` becomes the new default, and built-in token-variable references receive the `KENN_FORGE_` prefix.
6. Preserve the database, auth material, repository state, worktrees, clones, docs configuration, and all other user-owned state.
7. Write migration state sufficient to diagnose and safely resume an interrupted attempt.

If both homes contain data, startup must fail instead of silently merging them. A fresh installation with no old home creates the new home normally.

The normal path uses an atomic rename. A cross-filesystem layout uses a staged copy that preserves permissions and validates the staged tree before publishing the new home and removing the old tree. An interrupted staged migration must either resume safely or stop with specific recovery instructions; it must not select between two ambiguous databases.

## Rename Codemod

The broad source rename is performed by a repository-local codemod rather than a sequence of manual edits. The tool contains declarative, ordered mappings for:

- command directories, filenames, and generated/build paths;
- the Go module and all internal imports;
- executable, package, workspace, Rust crate, socket, and task names;
- environment variables and configuration examples;
- product prose and display copy;
- slug, identifier, frontend-global, and browser-storage forms; and
- exceptional targets such as `go.kenn.io/forge` and `forge.db`.

The codemod must be deterministic and safe to rerun. It rejects path collisions, applies path moves and content edits in a stable order, and finishes with a stale-name audit. Tests cover its mapping behavior, determinism, collision handling, and allowlist enforcement.

Manual changes after the codemod are limited to semantic implementation work—the filesystem and browser migrations—and compile or test fixes that cannot be expressed safely as declarative mappings.

The old product name may remain only in an explicit allowlist for codemod mappings, immutable historical migrations, the forward/reverse schema-rename migration, legacy filesystem/browser migration code, legacy-input fixtures, migration documentation, and landed dated design/plan artifacts that record the product as it existed when they were written. The allowlist must be narrow enough that newly introduced live product identifiers fail the audit.

## Maintained Source Scope

The rename covers the complete maintained repository surface:

- Go commands, module imports, runtime identity, and generated API tooling;
- frontend packages, globals, storage keys, copy, tests, and embedded output;
- Rust crates and PTY runtime naming;
- Make targets, scripts, Docker and development fixtures;
- CI and release assets;
- telemetry and daemon discovery identifiers;
- current documentation, examples, context, and repository-local skills.

Current documentation means maintained user and contributor guidance. Dated design and implementation-plan artifacts are historical records and are not rewritten solely for the rename.

Existing externally managed resources, such as a previously created GitHub App slug, are not renamed automatically. API routes remain unchanged unless a route itself contains the old product identity. This work does not redesign the UI or change product workflows.

## Failure Handling

Migration errors fail closed and identify the conflicting or incomplete path. Kenn Forge never silently combines old and new databases, kills an existing daemon, falls back to legacy environment variables, or guesses whether a custom directory should move.

The migration implementation exposes small filesystem operations behind testable boundaries so collision, active-daemon, rename, staged-copy, validation, and recovery behavior can be exercised without touching a real home directory.

## Verification

Verification includes:

- table-driven tests for default-home migration, custom data directories, destination collisions, active-daemon refusal, staged cross-filesystem behavior, and interrupted recovery;
- browser tests for one-time persisted-key transfer and cleanup;
- CLI and build tests proving all three binaries use their new identities;
- codemod tests for deterministic output, path collisions, and stale-name allowlisting;
- repository-wide stale-name auditing;
- the complete Go test suite plus applicable vet and lint checks;
- full frontend checks and Vitest tests because package names, globals, persisted keys, and displayed copy change broadly;
- Rust tests for the renamed PTY manager; and
- a final embedded-frontend build of `kenn-forge`.

The completed change leaves the source tree buildable using only Kenn Forge identifiers, with legacy names confined to the migration boundary.
