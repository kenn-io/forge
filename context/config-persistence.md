# Config Persistence Invariants

Use this document when adding or changing config fields that kenn-forge saves
back to TOML.

## Saving and loading

- `Save` writes only the hand-maintained `configFile` subset; a `Config` field missing from it
  loads fine but is silently dropped on the next save (`internal/config/config.go::configFile`).
- Every new persisted field or section must be wired into `Config`, `configFile`, and the `Save`
  initializer, with a save/load round-trip test using a non-default value
  (`internal/config/config_test.go::TestPullRequestsConfigRoundTrip`).
- When zero is meaningful, store the value as optional so TOML `omitempty` cannot turn an
  explicit zero into the default; the round-trip test must cover zero (`internal/config/config.go::Terminal`).
- Startup env/flag overlays are transient: reload keeps them, and saves validate effective values
  but write the file's own values for overridden fields so pending file edits survive
  (`internal/config/runtime_overrides.go::Config.restoreRuntimeFileValues`).
- Never make users hand-edit config after a field changes type: older text `platform_repo_id`
  values convert on load and startup saves the file once
  (`internal/config/legacy_repository_ids.go::upgradeRepositoryIDs`).
- Legacy database migration moves into the effective runtime `data_dir`, including
  `KENN_FORGE_DATA_DIR`; an empty winning override fails before moving anything
  (`internal/config/legacy_migration.go::migrateLegacyDatabase`).

## Settings writes

- Whole-file settings mutations must hold `configReloadMu` before `cfgMu` while applying and
  saving, or the watcher can restore a stale snapshot between writes
  (`internal/server/settings_handlers.go::updateSettings`).
- Partial settings request objects use pointer fields and merge only fields that were present;
  reusing persisted value structs collapses omission into zero values.
- Whole-list sections (`agents`, `quick_actions`) replace the list, so unrelated edits must
  retain each agent target's `disable_forge_tools` choice, including built-in defaults
  (`frontend/src/lib/components/settings/AgentSettings.svelte::serializeDrafts`).
- Detail settings writes replace the whole section, so the UI must send every Detail field.
- Activity item/event defaults distinguish omitted lists (all selected) from empty lists (none);
  preserve empty lists through settings writes and TOML saves (`internal/config/config.go::Activity`).
- New hub-owned settings fields must also cross the spoke request mapping; omitted
  generated-client lists become null and fail hub validation
  (`internal/server/spokeapi/provider_sources.go::providerSettingsRequestBody`).
- Repository presets store only named custom definitions; `Global` is derived and must never be
  serialized. Members persist provider, host, provider-verified repository ID, and a last-known
  route; presets change only through dedicated atomic endpoints (`internal/config/config.go::RepoPreset`).

## Fleet

- Omitted `fleet.role` means `hub`; an explicit `spoke` role and its hub binding must survive
  whole-file saves together (`internal/config/config.go::Fleet.RoleOrDefault`).
- `fleet.members` is hub-only metadata keyed by stable node ID and canonical HTTPS origin.
  Enrollment never changes the startup-bound role; only `fleet prepare-spoke` saves spoke role,
  after matching the durable seal, and abort may reset an unactivated spoke (`internal/config/config.go::Fleet.Validate`).
- Ordinary fleet mutations persist against the boot-pinned role, origin, and hub binding;
  preparation carries forward the hub binding written by join. Reload drift cannot redefine either
  (`internal/server/settingsapi/settings_routes.go::Handlers.mutatePersistedFleetChecked`).
- Unsupported fleet fields fail config loading. Do not add migration parsing or derive spoke
  identity from removed display metadata; operators remove the fields and enroll spokes explicitly
  (`internal/config/config.go::rejectUnsupportedConfigKeys`).
- Enrollment records, preparation seals, and federation bearers live in 0600 atomic JSON stores
  under `data_dir`, never TOML; settings and CLI output must never expose bearers or seals
  (`internal/federationauth/store.go::DefaultStorePath`).
- A spoke's settings response combines hub-owned provider policy with spoke-owned execution
  policy. Each write targets exactly one owner; mixed-owner requests fail before either side
  changes (`internal/server/settings_handlers.go::Server.updateSettings`).
- Spoke-local writes commit without hub I/O; a slow or failed hub (bounded by `fleet.peer_timeout`)
  only omits hub-owned fields and never reports the committed write as failed.
- `fleet setup` never moves `data_dir` or writes enrollment-owned spoke role or binding; failed
  setup restores the previous config before restarting the previous service
  (`internal/fleetsetup/setup.go::Runner.Apply`).

## Field contracts

- `airplane_mode` is a false-by-default, node-local preference applied without restart; spoke
  settings never overwrite it with the hub's value (`internal/server/settingsapi/settings_handlers.go::SplitSettingsUpdate`).
- `activity.use_workspace_activity_for_recency` is one global PR, Issue, and Activity opt-in,
  intentionally false by default and hot-reloaded (`internal/server/streamapi/server.go::PullConfigSnapshot`).
- `detail.initial_timeline_entry_limit` is a global PR/issue preference; omitted or zero means 50,
  explicit values must stay within 10-250 on load and settings writes (`internal/config/config.go::Detail`).
- `detail.collapse_single_line_breaks` is a false-by-default timeline opt-in for CommonMark soft
  breaks in comments and markdown commit bodies. Descriptions always use soft breaks; it never
  changes plain text, Docs mode, or editor previews (`frontend/src/lib/utils/markdown.ts::getMarked`).
- `detail.render_commit_messages_as_markdown` is a false-by-default opt-in that renders timeline
  commit bodies as comment markdown instead of plain text.
- `sync_budget_per_hour` is hub-owned, bounded 50-15000 on load and settings writes, and never
  startup-bound: writes and reloads resize live budgets in place, keeping the window's spend
  (`internal/github/budget.go::SyncBudget.SetLimit`).
- Omitted `mcp.enabled` defaults on only for loopback-IP non-workers without a saved port;
  explicit false must survive saves, and clearing a port preserves an opt-out
  (`internal/config/config.go::Config.MCPEnabled`).
- `modes.actions` is false by default and controls presentation only; provider workflow HTTP
  access remains available. Disabling it removes both the Actions mode and the PR workflow menu
  (`internal/config/config.go::ModeVisibility`).
- `workspaces.idle_runtime_stop_hours` is a TOML-only, hot-reloaded node-local setting bounded
  0-720; zero keeps runtimes running (`internal/config/config.go::Workspaces`).
- `roborev.init_managed_clones` is a hot-reloaded, false-by-default setup policy; only the
  effective Roborev endpoint is startup-bound (`internal/config/config.go::Roborev`).
- Quick-action agent keys are validated at use time, not config load, so a missing binary never
  rejects the file; `acp_options` need only non-blank IDs and values, since only the running agent
  knows what it offers (`internal/config/config.go::Config.validateQuickActions`).
