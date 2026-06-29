<script lang="ts">
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { ActionButton, SelectDropdown } from "@middleman/ui";
  import type {
    ConfigRepo,
    KataProjectRepoMapping,
  } from "@middleman/ui/api/types";
  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface Props {
    mappings?: KataProjectRepoMapping[] | undefined;
    repos: ConfigRepo[];
    onUpdate: (mappings: KataProjectRepoMapping[]) => void;
  }

  interface MappingDraft {
    id: string;
    daemonID: string;
    projectUID: string;
    repoKey: string;
  }

  interface RepoOption {
    key: string;
    label: string;
    repo: ConfigRepo;
  }

  let { mappings, repos, onUpdate }: Props = $props();

  const embedded = isEmbedded();
  let nextID = 0;
  let saving = $state(false);
  let error = $state<string | null>(null);
  // svelte-ignore state_referenced_locally
  let currentMappings = $state(normalizeMappings(mappings));
  // svelte-ignore state_referenced_locally
  let drafts = $state<MappingDraft[]>(draftsFromMappings(currentMappings));

  const repoOptions = $derived.by(() =>
    repos
      .filter((repo) => !repo.is_glob)
      .map((repo) => ({
        key: repoKey(repo.provider, repo.platform_host, repo.repo_path),
        label: repoLabel(repo),
        repo,
      }))
      .sort((left, right) => left.label.localeCompare(right.label)),
  );
  const repoOptionsByKey = $derived.by(() => new Map(repoOptions.map((option) => [option.key, option])));
  const repoSelectOptions = $derived(
    repoOptions.map((option) => ({ value: option.key, label: option.label })),
  );
  const pendingMappings = $derived(buildPendingMappings());
  const isDirty = $derived(
    JSON.stringify(pendingMappings) !== JSON.stringify(currentMappings),
  );
  const hasInvalidDraft = $derived(
    drafts.some((draft) => draft.projectUID.trim() === "" || !repoOptionsByKey.has(draft.repoKey)),
  );
  const canSave = $derived(!embedded && !saving && isDirty && !hasInvalidDraft);

  function nextDraftID(): string {
    nextID += 1;
    return `kata-project:${nextID}`;
  }

  function repoKey(provider: string, platformHost: string, repoPath: string): string {
    return `${provider}\u0000${platformHost}\u0000${repoPath}`;
  }

  function repoLabel(repo: ConfigRepo): string {
    return `${repo.provider} / ${repo.platform_host} / ${repo.repo_path}`;
  }

  function repoKeyFromMapping(mapping: KataProjectRepoMapping): string {
    return repoKey(mapping.provider, mapping.platform_host, mapping.repo_path);
  }

  function normalizeMappings(configured: KataProjectRepoMapping[] | undefined): KataProjectRepoMapping[] {
    return (configured ?? [])
      .map((mapping) => {
        const out: KataProjectRepoMapping = {
          project_uid: mapping.project_uid.trim(),
          provider: mapping.provider.trim(),
          platform_host: mapping.platform_host.trim(),
          repo_path: mapping.repo_path.trim(),
        };
        const daemonID = mapping.daemon_id?.trim() ?? "";
        if (daemonID !== "") out.daemon_id = daemonID;
        return out;
      })
      .filter((mapping) => mapping.project_uid !== "" && mapping.provider !== "" && mapping.repo_path !== "")
      .sort((left, right) =>
        `${left.daemon_id ?? ""}\u0000${left.project_uid}`.localeCompare(
          `${right.daemon_id ?? ""}\u0000${right.project_uid}`,
        ),
      );
  }

  function draftsFromMappings(configured: KataProjectRepoMapping[]): MappingDraft[] {
    return configured.map((mapping) => ({
      id: nextDraftID(),
      daemonID: mapping.daemon_id ?? "",
      projectUID: mapping.project_uid,
      repoKey: repoKeyFromMapping(mapping),
    }));
  }

  function buildPendingMappings(): KataProjectRepoMapping[] {
    const next: KataProjectRepoMapping[] = [];
    for (const draft of drafts) {
      const option = repoOptionsByKey.get(draft.repoKey);
      if (!option) continue;
      const projectUID = draft.projectUID.trim();
      if (projectUID === "") continue;
      const mapping: KataProjectRepoMapping = {
        project_uid: projectUID,
        provider: option.repo.provider,
        platform_host: option.repo.platform_host,
        repo_path: option.repo.repo_path,
      };
      const daemonID = draft.daemonID.trim();
      if (daemonID !== "") mapping.daemon_id = daemonID;
      next.push(mapping);
    }
    return normalizeMappings(next);
  }

  function addMapping(): void {
    const firstRepo = repoOptions[0]?.key ?? "";
    drafts = [
      ...drafts,
      {
        id: nextDraftID(),
        daemonID: "",
        projectUID: "",
        repoKey: firstRepo,
      },
    ];
  }

  function removeMapping(id: string): void {
    drafts = drafts.filter((draft) => draft.id !== id);
  }

  function resetDraft(): void {
    drafts = draftsFromMappings(currentMappings);
    error = null;
  }

  function rowLabel(draft: MappingDraft, index: number): string {
    return draft.projectUID.trim() || `mapping ${index + 1}`;
  }

  async function save(): Promise<void> {
    if (!canSave) return;
    saving = true;
    error = null;
    try {
      const settings = await updateSettings({ kata_projects: pendingMappings });
      const nextMappings = settings.kata_projects ?? [];
      currentMappings = normalizeMappings(nextMappings);
      drafts = draftsFromMappings(currentMappings);
      onUpdate(nextMappings);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="kata-project-mappings">
  {#if error}
    <p class="settings-error" role="alert">{error}</p>
  {/if}

  <section class="mapping-section" aria-label="Kata project repository mappings">
    <div class="mapping-section-header">
      <div>
        <h3>Manual mappings</h3>
        <p>Automatic `.kata.toml` matches are used when no row is configured here.</p>
      </div>
      <ActionButton
        size="sm"
        type="button"
        onclick={addMapping}
        disabled={embedded || saving || repoOptions.length === 0}
      >
        <PlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
        Add mapping
      </ActionButton>
    </div>

    {#if repoOptions.length === 0}
      <p class="empty-mappings">No exact watched repositories are configured.</p>
    {:else if drafts.length === 0}
      <p class="empty-mappings">No manual Kata project mappings configured.</p>
    {:else}
      <div class="mapping-table-wrap">
        <table class="mapping-table" aria-label="Kata project mappings">
          <colgroup>
            <col class="daemon-col" />
            <col class="project-col" />
            <col class="repo-col" />
            <col class="action-col" />
          </colgroup>
          <thead>
            <tr>
              <th scope="col">Daemon</th>
              <th scope="col">Project UID</th>
              <th scope="col">Repository</th>
              <th scope="col" aria-label="Mapping actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each drafts as draft, index (draft.id)}
              {@const label = rowLabel(draft, index)}
              <tr>
                <td>
                  <input
                    bind:value={draft.daemonID}
                    placeholder="Any daemon"
                    disabled={embedded || saving}
                    aria-label={`Kata project ${label} daemon ID`}
                  />
                </td>
                <td>
                  <input
                    bind:value={draft.projectUID}
                    disabled={embedded || saving}
                    aria-label={`Kata project ${label} UID`}
                  />
                </td>
                <td>
                  <SelectDropdown
                    title={`Kata project ${label} repository`}
                    value={draft.repoKey}
                    options={repoSelectOptions}
                    onchange={(value) => { draft.repoKey = value; }}
                    disabled={embedded || saving}
                  />
                </td>
                <td class="action-cell">
                  <ActionButton
                    size="sm"
                    tone="danger"
                    surface="outline"
                    type="button"
                    onclick={() => removeMapping(draft.id)}
                    disabled={embedded || saving}
                    ariaLabel={`Remove Kata project mapping ${label}`}
                    title={`Remove Kata project mapping ${label}`}
                  >
                    <TrashIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                  </ActionButton>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <div class="settings-actions">
    <ActionButton
      size="sm"
      type="button"
      onclick={resetDraft}
      disabled={!isDirty || saving}
    >
      <RotateCcwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      Reset
    </ActionButton>
    <ActionButton
      tone="info"
      surface="solid"
      type="button"
      onclick={() => void save()}
      disabled={!canSave}
    >
      Save Kata mappings
    </ActionButton>
  </div>
</div>

<style>
  .kata-project-mappings {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .mapping-section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .mapping-section-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }

  .mapping-section-header h3 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .mapping-section-header p,
  .empty-mappings {
    margin: 2px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.4;
  }

  .settings-error {
    margin: 0;
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--accent-red) 45%, var(--border-muted));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-red) 9%, var(--bg-primary));
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .mapping-table-wrap {
    overflow: visible;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  .mapping-table {
    width: 100%;
    min-width: 660px;
    border-collapse: collapse;
    table-layout: fixed;
  }

  .mapping-table th,
  .mapping-table td {
    padding: 8px;
    border-bottom: 1px solid var(--border-muted);
    vertical-align: top;
  }

  .mapping-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .mapping-table th {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-align: left;
    background: var(--bg-inset);
  }

  .mapping-table input {
    width: 100%;
    min-height: 30px;
    padding: 4px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 400;
  }

  .mapping-table input:disabled {
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .mapping-table :global(.select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .mapping-table :global(.select-dropdown-trigger) {
    height: 30px;
    font-size: var(--font-size-sm);
    font-weight: 400;
  }

  .daemon-col {
    width: 22%;
  }

  .project-col {
    width: 28%;
  }

  .repo-col {
    width: auto;
  }

  .action-col {
    width: 46px;
  }

  .action-cell {
    text-align: right;
  }

  .settings-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }

  @media (max-width: 759px) {
    .mapping-table-wrap {
      overflow-x: auto;
    }
  }
</style>
