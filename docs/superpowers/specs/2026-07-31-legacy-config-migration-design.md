# Legacy Config Migration Design

## Goal

Preserve repository tracking and other user settings when an existing Middleman installation first starts as kenn-forge. The migration copies the legacy default config into the kenn-forge default home and leaves the original file intact for recovery. This design supersedes the database-only compatibility boundary in the July 30 rename design.

## Scope

This is a bounded, one-time forward migration from `~/.config/middleman/config.toml` to `~/.kenn/forge/config.toml`. It runs only when using kenn-forge's default config path without `KENN_FORGE_HOME`; explicitly selected config paths remain authoritative and are not relocated.

The migration handles two destination states:

- If the kenn-forge config does not exist, publish the migrated copy.
- If startup already created the exact generated kenn-forge default, replace that untouched default with the migrated copy. This repairs installations that have already encountered the rename regression.

Any other existing destination is user state. If it differs from the migrated legacy config, startup fails with a conflict instead of merging or overwriting it. After config and credential publication, write migration marker version `v2` in the kenn-forge home. Later startups check the marker and do not reread the legacy config, so edits to the recoverable source cannot overwrite live kenn-forge settings. Marker `v1` means the config was published by the incomplete migration but referenced credentials still need copying; completing those copies upgrades the marker to `v2`. A restart between publication and marker creation recognizes identical destinations and finishes idempotently.

The migration boundary remains only while direct upgrades from a Middleman release to kenn-forge are supported. When that upgrade path is removed from the support policy, remove the legacy config reader, marker handling, and their tests together.

## Transformation and Publication

Read the legacy config, update only product-rename values that kenn-forge owns, and preserve user settings. Known built-in `MIDDLEMAN_*` token environment names become their `KENN_FORGE_*` equivalents. Paths rooted beneath the old default home are rebased beneath the kenn-forge home; external paths and custom environment-variable names stay unchanged.

Copy every referenced token file and GitHub App private key whose effective source is beneath the old default home. Relative credential paths resolve beneath the old and new config homes; absolute paths are rebased only when contained by the old home. Preserve file contents and permissions, leave each source intact, accept an identical destination, and fail rather than overwrite conflicting credential bytes.

Write the transformed bytes to a temporary file in the destination directory, preserve the source file mode, and load the temporary config through the normal parser and validation path. Atomically rename the validated file into place. The source config is copied, never removed.

Config migration must happen before database relocation so a legacy custom `data_dir` remains authoritative when `middleman.db` is renamed to `forge.db`. The existing legacy-daemon lock and database conflict checks remain in force.

## User-Visible Data Flow

After startup loads the migrated config, `cfg.Repos` contains the legacy `[[repos]]` entries. The existing Settings endpoint returns those configured entries, and the Svelte settings view renders them without a UI-specific fallback to database rows.

## Errors

Startup fails closed with paths identifying the source and destination when:

- the destination contains a non-default, conflicting config;
- the transformed legacy config does not parse or validate; or
- a referenced credential is missing or conflicts with its destination; or
- the existing database migration detects an active Middleman daemon or conflicting database files.

No partial destination config is published after validation or write failure.

## Verification

Table-driven config tests cover a missing destination, the already-created generated default, `v1` recovery, idempotent reruns, conflicting user config/credentials, product-owned value/path rewrites, preserved custom values and file modes, and parse/validation failure. An integration test starts from a legacy config with tracked repositories and proves `LoadOrCreate` returns those repositories alongside the relocated database.

The focused config package tests run first, followed by the repository's short Go suite. No Svelte change is required because the UI already renders the settings API correctly once startup loads the right config.
