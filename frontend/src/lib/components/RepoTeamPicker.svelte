<script lang="ts">
  import { SelectDropdown, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy, onMount } from "svelte";
  import { getAppRuntime } from "../app/runtime-context.js";
  import type { AppExecution } from "../app/runtime.js";
  import {
    SettingsWorkflow,
    settingsErrorMessage,
    type GitHubTeam,
    type GitHubTeamRepository,
  } from "../stores/settings-workflow.js";
  import {
    selectionForTeamRepositories,
    type RepoPresetCatalogEntry,
    type RepoTeamOrg,
  } from "../stores/repo-presets.js";
  import { parseRepoFilterValue } from "../stores/filter.svelte.js";
  import DialogButton from "./shared/DialogButton.svelte";
  import Modal from "./shared/Modal.svelte";

  interface Props {
    orgs: readonly RepoTeamOrg[];
    catalog: readonly RepoPresetCatalogEntry[];
    // False while configured repository options are still loading; a team
    // matched against a partial catalog would yield a partial selection.
    catalogReady: boolean;
    onClose: () => void;
    onApply: (selection: string) => void;
  }

  let { orgs, catalog, catalogReady, onClose, onApply }: Props = $props();

  const runtime = getAppRuntime();

  type Load<T> =
    | { status: "idle" }
    | { status: "loading" }
    | { status: "failed"; message: string }
    | { status: "loaded"; value: T };

  const orgKey = (org: RepoTeamOrg) => `${org.platform_host}/${org.owner}`;

  // svelte-ignore state_referenced_locally
  let orgValue = $state(orgs.length === 1 ? orgKey(orgs[0]!) : "");
  let teamSlug = $state("");
  let teams = $state.raw<Load<readonly GitHubTeam[]>>({ status: "idle" });
  let teamRepos = $state.raw<Load<readonly GitHubTeamRepository[]>>({ status: "idle" });
  let teamsExecution: AppExecution<void, never> | null = null;
  let reposExecution: AppExecution<void, never> | null = null;

  onDestroy(() => {
    teamsExecution?.interrupt();
    reposExecution?.interrupt();
  });

  const selectedOrg = $derived(orgs.find((org) => orgKey(org) === orgValue));
  const orgOptions = $derived<SelectDropdownOption[]>([
    ...(selectedOrg ? [] : [{ value: "", label: "Choose an organization", disabled: true }]),
    ...orgs.map((org) => ({
      value: orgKey(org),
      label: orgs.some((other) => other !== org && other.owner.toLowerCase() === org.owner.toLowerCase())
        ? `${org.owner} (${org.platform_host})`
        : org.owner,
    })),
  ]);
  const teamOptions = $derived.by((): SelectDropdownOption[] => {
    if (teams.status === "loading") return [{ value: "", label: "Loading teams…", disabled: true }];
    if (teams.status !== "loaded") return [{ value: "", label: "Choose a team", disabled: true }];
    return [
      ...(teamSlug ? [] : [{ value: "", label: "Choose a team", disabled: true }]),
      ...teams.value.map((team) => ({ value: team.slug, label: team.name || team.slug })),
    ];
  });
  const teamName = $derived.by(() => {
    if (teams.status !== "loaded") return teamSlug;
    const team = teams.value.find((candidate) => candidate.slug === teamSlug);
    return team?.name || teamSlug;
  });
  const selection = $derived(
    teamRepos.status === "loaded" && selectedOrg && catalogReady
      ? selectionForTeamRepositories(teamRepos.value, selectedOrg.platform_host, catalog)
      : undefined,
  );
  const matchCount = $derived(parseRepoFilterValue(selection).length);

  // Each read replaces the one before it; a reply from a replaced read is dropped.
  function loadTeams(org: RepoTeamOrg): void {
    teamsExecution?.interrupt();
    reposExecution?.interrupt();
    teamSlug = "";
    teamRepos = { status: "idle" };
    teams = { status: "loading" };
    let execution: AppExecution<void, never> | undefined;
    execution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.listGitHubTeams(org.platform_host, org.owner);
      }).pipe(
        Effect.match({
          onFailure: (failure) => {
            if (teamsExecution !== execution) return;
            teams = { status: "failed", message: settingsErrorMessage(failure) };
          },
          onSuccess: (value) => {
            if (teamsExecution !== execution) return;
            teams = { status: "loaded", value };
          },
        }),
      ),
      {
        operation: "list GitHub teams",
        safeContext: { host: org.platform_host },
        onFailure: () => {},
      },
    );
    teamsExecution = execution;
  }

  function loadTeamRepos(org: RepoTeamOrg, slug: string): void {
    reposExecution?.interrupt();
    teamRepos = { status: "loading" };
    let execution: AppExecution<void, never> | undefined;
    execution = runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.listGitHubTeamRepositories(org.platform_host, org.owner, slug);
      }).pipe(
        Effect.match({
          onFailure: (failure) => {
            if (reposExecution !== execution) return;
            teamRepos = { status: "failed", message: settingsErrorMessage(failure) };
          },
          onSuccess: (value) => {
            if (reposExecution !== execution) return;
            teamRepos = { status: "loaded", value };
          },
        }),
      ),
      {
        operation: "list GitHub team repositories",
        safeContext: { host: org.platform_host },
        onFailure: () => {},
      },
    );
    reposExecution = execution;
  }

  function chooseOrg(value: string): void {
    orgValue = value;
    const org = orgs.find((candidate) => orgKey(candidate) === value);
    if (org) loadTeams(org);
  }

  function chooseTeam(slug: string): void {
    teamSlug = slug;
    if (selectedOrg && slug) loadTeamRepos(selectedOrg, slug);
  }

  // A single configured organization needs no choice, so its teams load at once.
  onMount(() => {
    if (selectedOrg) loadTeams(selectedOrg);
  });

  function apply(): void {
    if (selection === undefined) return;
    onApply(selection);
  }
</script>

<Modal
  open
  title="Select repositories from a GitHub team"
  width={460}
  frameId="select-repositories-from-github-team"
  {onClose}
>
  <div class="team-picker-content">
    <p class="team-picker-note">
      Shows teams and repositories your GitHub user credential can see.
    </p>
    <div class="team-picker-field">
      <span class="team-picker-label" aria-hidden="true">Organization</span>
      <SelectDropdown
        class="team-picker-select"
        title="Organization"
        value={orgValue}
        options={orgOptions}
        onchange={chooseOrg}
      />
    </div>
    <div class="team-picker-field">
      <span class="team-picker-label" aria-hidden="true">Team</span>
      <SelectDropdown
        class="team-picker-select"
        title="Team"
        value={teamSlug}
        options={teamOptions}
        onchange={chooseTeam}
        disabled={teams.status !== "loaded" || teams.value.length === 0}
      />
    </div>

    {#if teams.status === "failed"}
      <p class="team-picker-error" role="alert">{teams.message}</p>
    {:else if teams.status === "loaded" && teams.value.length === 0}
      <p class="team-picker-note" role="status">
        No teams in {selectedOrg?.owner} are visible to this credential. If teams exist, check that it can read
        organization members: read:org for a classic token, Members read for a fine-grained one.
      </p>
    {:else if teamRepos.status === "failed"}
      <p class="team-picker-error" role="alert">{teamRepos.message}</p>
    {:else if teamRepos.status === "loading"}
      <p class="team-picker-note" role="status">Loading team repositories…</p>
    {:else if teamRepos.status === "loaded" && !catalogReady}
      <p class="team-picker-note" role="status">Loading configured repositories…</p>
    {:else if teamRepos.status === "loaded"}
      <p class="team-picker-note" role="status">
        {#if matchCount === 0}
          None of the repositories this credential can see in {teamName} are configured in Forge. Add them in
          Settings, or choose another team.
        {:else}
          {matchCount} configured {matchCount === 1 ? "repository" : "repositories"} from {teamName} will replace the
          current selection. You can edit it before saving.
        {/if}
      </p>
    {/if}
  </div>

  {#snippet footer()}
    <DialogButton onclick={onClose}>Cancel</DialogButton>
    <DialogButton tone="primary" disabled={selection === undefined} onclick={apply}>
      Use selection
    </DialogButton>
  {/snippet}
</Modal>

<style>
  .team-picker-content {
    display: grid;
    gap: var(--space-3);
  }

  .team-picker-field {
    display: grid;
    gap: var(--space-1);
  }

  .team-picker-label {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  :global(.team-picker-select) {
    width: 100%;
    --kit-select-dropdown-max-width: none;
  }

  .team-picker-note,
  .team-picker-error {
    margin: 0;
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .team-picker-note {
    color: var(--text-secondary);
  }

  .team-picker-error {
    color: var(--accent-red);
  }
</style>
