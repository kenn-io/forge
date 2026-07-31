# Legacy Config Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Copy the legacy config and credential files it references so Kenn Forge starts with the same repositories and authentication.

**Approved spec/design:** `docs/superpowers/specs/2026-07-31-legacy-config-migration-design.md`

**Architecture:** On default-home startup, read and validate the legacy config, update renamed values and old-home paths, copy referenced token files and GitHub App keys, publish the config, then relocate the database. Marker `v1` recovers the initially shipped config-only migration; `v2` means config and credentials are complete.

**Tech Stack:** Go filesystem APIs, the existing TOML config loader, and `testify`.

## Constraints

- Leave legacy source files intact.
- Never replace a conflicting Kenn Forge config or credential file.
- Do not migrate custom config paths or `KENN_FORGE_HOME` installations.
- Preserve repository settings, credential contents, and file permissions.

### Task 1: Reproduce the Empty Settings Regression

**Files:**
- Test: `internal/config/legacy_migration_test.go`
- Test: `internal/config/legacy_migration_integration_test.go`

- [x] Add a failing `LoadOrCreate` test proving legacy `[[repos]]` entries replace the untouched generated config.
- [x] Add destination-conflict, invalid-config, and repeated-start coverage.
- [x] Extend the database integration test to combine nested `data_dir`, repository config, and schema migration.
- [x] Run the focused tests with `-shuffle=on` and confirm RED.

### Task 2: Copy Config and Credentials

**Files:**
- Create: `internal/config/legacy_config_migration.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/legacy_migration.go`

- [x] Validate and transform the legacy config before publishing it.
- [x] Copy referenced `token_file` and `private_key_path` files beneath the legacy home.
- [x] Preserve an existing identical credential and reject conflicting bytes.
- [x] Let marker `v1` finish credential copying and publish marker `v2` afterward.
- [x] Rebase nested legacy `data_dir` paths when relocating the database.

### Task 3: Verify and Commit

- [x] Run focused migration tests, the full config package, and config-package lint.
- [x] Update `context/server-runtime.md` with the startup migration order.
- [x] Run context structural validation and `git diff --check`.
- [x] Commit through repository hooks without bypasses.
