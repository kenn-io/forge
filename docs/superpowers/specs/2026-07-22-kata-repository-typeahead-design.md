# Kata Repository Typeahead

## Problem

The manual Kata project-mapping table uses `SelectDropdown` for repository
targets. The repository list can be long, but this control only supports
scrolling through the full list. Middleman already depends on kit-ui's
`Typeahead`, which provides filtering, keyboard navigation, match highlighting,
and popover positioning for this interaction.

## Decision

Replace only the repository-target `SelectDropdown` in
`KataProjectMappingsSettings` with the existing kit-ui `Typeahead`. Daemon and
other short enumerated pickers remain `SelectDropdown` controls.

Each typeahead option uses the existing provider-qualified repository key as its
stable `name`. Its searchable label includes both the configured display name
and `repo_path`, preserving the current visible option text and allowing either
form to match. The closed trigger shows the same full label so repositories with
similar display names remain distinguishable.

An empty mapping shows `Select a repository`. Opening the control focuses its
query input; typing filters locally, Arrow keys move through matches, Enter
selects the highlighted repository, and Escape closes the list. The control is
disabled under the same embedded and saving conditions as today.

## Data And Errors

Selection continues to write the repository key into `draft.repoKey`.
`buildPendingMappings` resolves that key through the existing
`repoOptionsByKey` map, so the saved provider, platform host, and repository path
are unchanged. Unavailable configured mappings remain selectable and removable.

The option source is already loaded with the mapping diagnostics, so filtering
is local and introduces no new loading, remote-search, or error state.

## Verification

A focused component test will open a new mapping's repository picker, type a
repository query, prove non-matching options are removed, select the remaining
option, and verify the existing save request still contains the full repository
identity. Existing tests continue to cover inferred selections, empty
selections, and unavailable configured mappings.

After the final edit, run Svelte analysis on the component, the focused
component test, and the full frontend test suite.

## Non-goals

- Changing kit-ui or adding another typeahead implementation.
- Adding remote repository search.
- Changing mapping persistence or API contracts.
- Converting short enumerated dropdowns that do not need filtering.
