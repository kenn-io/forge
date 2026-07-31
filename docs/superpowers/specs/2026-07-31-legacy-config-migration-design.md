# Legacy Config Migration Design

## Goal

Preserve repository tracking and other user settings when an existing Middleman installation first starts as Kenn Forge. The migration copies the legacy default config into the Kenn Forge default home and leaves the original file intact for recovery. This design supersedes the database-only compatibility boundary in the July 30 rename design.

## Scope

This is a bounded, one-time forward migration from `~/.config/middleman/config.toml` to `~/.kenn/forge/config.toml`. It runs only when using Kenn Forge's default config path without `KENN_FORGE_HOME`; explicitly selected config paths remain authoritative and are not relocated.

The migration handles two destination states:

- If the Kenn Forge config does not exist, publish the migrated copy.
- If startup already created the exact generated Kenn Forge default, replace that untouched default with the migrated copy. This repairs installations that have already encountered the rename regression.

Any other existing destination is user state. If it differs from the migrated legacy config, startup fails with a conflict instead of merging or overwriting it. After publication, write a migration marker in the Kenn Forge home. Later startups check the marker and do not reread the legacy config, so edits to the recoverable source cannot overwrite live Kenn Forge settings. A restart between config publication and marker creation recognizes the identical migrated destination and finishes the marker step idempotently.

The migration boundary remains only while direct upgrades from a Middleman release to Kenn Forge are supported. When that upgrade path is removed from the support policy, remove the legacy config reader, marker handling, and their tests together.

## Transformation and Publication

Read the legacy config, update only product-rename values that Kenn Forge owns, and preserve unrelated user choices. Known built-in `MIDDLEMAN_*` token environment names become their `KENN_FORGE_*` equivalents. Absolute paths rooted beneath the old default home are rebased beneath the Kenn Forge home; external paths and custom environment-variable names stay unchanged.

Write the transformed bytes to a temporary file in the destination directory, preserve the source file mode, and load the temporary config through the normal parser and validation path. Atomically rename the validated file into place. The source config is copied, never removed.

Config migration must happen before database relocation so a legacy custom `data_dir` remains authoritative when `middleman.db` is renamed to `forge.db`. The existing legacy-daemon lock and database conflict checks remain in force.

## User-Visible Data Flow

After startup loads the migrated config, `cfg.Repos` contains the legacy `[[repos]]` entries. The existing Settings endpoint returns those configured entries, and the Svelte settings view renders them without a UI-specific fallback to database rows.

## Errors

Startup fails closed with paths identifying the source and destination when:

- the destination contains a non-default, conflicting config;
- the transformed legacy config does not parse or validate; or
- the existing database migration detects an active Middleman daemon or conflicting database files.

No partial destination config is published after validation or write failure.

## Verification

Table-driven config tests cover a missing destination, the already-created generated default, idempotent reruns, conflicting user config, product-owned value/path rewrites, preservation of custom values and file mode, and parse/validation failure. An integration test starts from a legacy config with tracked repositories and proves `LoadOrCreate` returns those repositories alongside the relocated database.

The focused config package tests run first, followed by the repository's short Go suite. No Svelte change is required because the UI already renders the settings API correctly once startup loads the right config.
