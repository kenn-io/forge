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

Historical database migrations must remain structurally valid and may not be edited. One new forward migration renames every live `middleman_*` SQLite table, trigger, and index to a `forge_*` identity, migrates both legacy workspace sentinels, and makes current-schema SQL use only the new names. Legacy schema names remain inside immutable historical migrations, the new migration's reversible source-side statements, and the narrow bootstrap queries required to recognize pre-`schema_migrations` version 1–3 databases.

## Compatibility Boundary

Compatibility covers the persisted SQLite database only. There is no legacy executable, command alias, environment-variable fallback, browser-storage transfer, config rewrite, or permanent dual-read path. Users must update shell, service, and automation configuration from the old environment prefix to `KENN_FORGE_`.

An explicitly supplied config with a custom `data_dir` remains authoritative: its database is renamed in place. Without an explicit config or `KENN_FORGE_HOME`, the old default database moves from `~/.config/middleman/middleman.db` to `~/.kenn/forge/forge.db`.

## First-Run Database Migration

Before server startup creates or loads Kenn Forge config, it checks for the legacy database. If `middleman.db` exists, it attempts to acquire `middleman.lock` in the same data directory. A held lock means the Middleman daemon is active, so startup fails without moving anything.

With the lock held, startup moves the SQLite main file and any WAL/SHM sidecars to the selected Kenn Forge data directory under the `forge.db` name. If `forge.db` already exists, startup reports a conflict instead of combining databases. No other files or persisted browser/config state are migrated.

Opening the relocated database runs the ordinary SQLite migration chain. Migration 44 renames its live tables, indexes, triggers, and workspace sentinel values to the Forge identity.

## Rename Codemod

The broad source rename is performed by a temporary repository-local codemod rather than a sequence of manual edits. The codemod is removed after applying and verifying the rename; it is an implementation aid, not a maintained product tool. It contains declarative, ordered mappings for:

- command directories, filenames, and generated/build paths;
- the Go module and all internal imports;
- executable, package, workspace, Rust crate, socket, and task names;
- environment variables and configuration examples;
- product prose and display copy;
- slug, identifier, frontend-global, and browser-storage forms; and
- exceptional targets such as `go.kenn.io/forge` and `forge.db`.

The codemod must be deterministic and safe to rerun. It rejects path collisions, applies path moves and content edits in a stable order, and finishes with a stale-name audit. Tests cover its mapping behavior, determinism, collision handling, and allowlist enforcement.

Manual changes after the codemod are limited to the database migration semantics and compile or test fixes that cannot be expressed safely as declarative mappings.

The old product name may remain only in an explicit allowlist for codemod mappings, immutable historical migrations, the forward/reverse schema-rename migration, legacy schema-bootstrap queries, the database relocation boundary, legacy-input fixtures, migration documentation, and landed dated design/plan artifacts that record the product as it existed when they were written. The allowlist must be narrow enough that newly introduced live product identifiers fail the audit.

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

Migration errors fail closed and identify the conflicting path. Kenn Forge never silently combines old and new databases, kills an existing daemon, or falls back to legacy environment variables.

## Verification

Verification includes:

- focused tests for default database relocation, custom data-directory renaming, destination collisions, and active-daemon refusal;
- CLI and build tests proving all three binaries use their new identities;
- codemod tests for deterministic output, path collisions, and stale-name allowlisting;
- repository-wide stale-name auditing;
- the complete Go test suite plus applicable vet and lint checks;
- full frontend checks and Vitest tests because package names, globals, persisted keys, and displayed copy change broadly;
- Rust tests for the renamed PTY manager; and
- a final embedded-frontend build of `kenn-forge`.

The completed change leaves the source tree buildable using only Kenn Forge identifiers, with legacy names confined to the migration boundary.
