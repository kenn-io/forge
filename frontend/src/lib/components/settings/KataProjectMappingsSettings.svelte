<script lang="ts">
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { onMount, untrack } from "svelte";
  import { Button, Chip, SelectDropdown } from "@middleman/ui";
  import type { KataProjectRepoMapping } from "@middleman/ui/api/types";
  import { updateSettings } from "../../api/settings.js";
  import { fetchKataDaemons, type KataDaemonInfo } from "../../api/kata/daemons.js";
  import {
    getKataProjectMappings,
    type KataProjectMappingDiagnostic,
    type KataProjectMappingsResponse,
  } from "../../api/kata/workspaces.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface Props {
    mappings?: KataProjectRepoMapping[] | undefined;
    enabled?: boolean;
    onUpdate: (mappings: KataProjectRepoMapping[]) => void;
  }

  interface MappingDraft {
    id: string;
    daemonID: string;
    projectUID: string;
    repoKey: string;
  }

  type MappingRepoRef = Pick<
    KataProjectMappingsResponse["targets"][number]["repo"],
    "provider" | "platform_host" | "owner" | "name" | "repo_path"
  >;

  interface RepoOption {
    key: string;
    label: string;
    displayName: string;
    repo: MappingRepoRef;
  }

  let { mappings, enabled = true, onUpdate }: Props = $props();

  const embedded = isEmbedded();
  let nextID = 0;
  let saving = $state(false);
  let error = $state<string | null>(null);
  let daemons = $state.raw<KataDaemonInfo[]>([]);
  let selectedDaemonID = $state("");
  let diagnostics = $state.raw<KataProjectMappingsResponse | null>(null);
  let diagnosticsLoading = $state(true);
  let diagnosticsError = $state<string | null>(null);
  let diagnosticsEnabled = false;
  let previousEnabled = false;
  // svelte-ignore state_referenced_locally
  let currentMappings = $state(normalizeMappings(mappings));
  // svelte-ignore state_referenced_locally
  let drafts = $state<MappingDraft[]>(draftsFromMappings(currentMappings));

  const repoOptions = $derived.by(() => {
    const targets: RepoOption[] = (diagnostics?.targets ?? []).map((target) => ({
      key: repoKey(target.repo.provider, target.repo.platform_host, target.repo.repo_path),
      label: `${target.display_name} · ${target.repo.repo_path}`,
      displayName: target.display_name,
      repo: target.repo,
    }));
    for (const mapping of currentMappings) {
      const key = repoKeyFromMapping(mapping);
      if (targets.some((option) => option.key === key)) continue;
      targets.push({
        key,
        label: `${mapping.repo_path} · unavailable`,
        displayName: mapping.repo_path,
        repo: {
          provider: mapping.provider,
          platform_host: mapping.platform_host,
          owner: mapping.repo_path.slice(0, mapping.repo_path.lastIndexOf("/")),
          name: mapping.repo_path.slice(mapping.repo_path.lastIndexOf("/") + 1),
          repo_path: mapping.repo_path,
        },
      });
    }
    return [...new Map(targets.map((option) => [option.key, option])).values()]
      .sort((left, right) => left.label.localeCompare(right.label));
  });
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
  const daemonOptions = $derived(daemons.map((daemon) => ({ value: daemon.id, label: daemon.id })));

  onMount(() => {
    diagnosticsEnabled = true;
  });

  $effect(() => {
    if (!diagnosticsEnabled) return;
    if (!enabled) {
      diagnostics = null;
      diagnosticsError = null;
      diagnosticsLoading = false;
      previousEnabled = false;
      return;
    }
    if (!previousEnabled) {
      previousEnabled = true;
      untrack(() => void loadDiagnostics());
    }
  });

  async function loadDiagnostics(daemonID = selectedDaemonID): Promise<void> {
    diagnosticsLoading = true;
    diagnosticsError = null;
    try {
      if (daemons.length === 0) {
        daemons = await fetchKataDaemons();
      }
      const effectiveID = daemonID || daemons.find((daemon) => daemon.default)?.id || daemons[0]?.id || "";
      selectedDaemonID = effectiveID;
      diagnostics = await getKataProjectMappings(effectiveID || undefined);
    } catch (err) {
      diagnosticsError = err instanceof Error ? err.message : String(err);
      diagnostics = null;
    } finally {
      diagnosticsLoading = false;
    }
  }

  function nextDraftID(): string {
    nextID += 1;
    return `kata-project:${nextID}`;
  }

  function repoKey(provider: string, platformHost: string, repoPath: string): string {
    return `${provider}\u0000${platformHost}\u0000${repoPath}`;
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
    drafts = [
      ...drafts,
      {
        id: nextDraftID(),
        daemonID: "",
        projectUID: "",
        repoKey: "",
      },
    ];
  }

  function addOverride(project: KataProjectMappingDiagnostic): void {
    const daemonID = diagnostics?.daemon_id ?? selectedDaemonID;
    const existing = drafts.find(
      (draft) => draft.projectUID === project.project_uid && draft.daemonID === daemonID,
    );
    if (existing) return;
    const mappedRepo = project.repo
      ? repoKey(project.repo.provider, project.repo.platform_host, project.repo.repo_path)
      : "";
    drafts = [
      ...drafts,
      {
        id: nextDraftID(),
        daemonID,
        projectUID: project.project_uid,
        repoKey: repoOptionsByKey.has(mappedRepo) ? mappedRepo : "",
      },
    ];
  }

  function sourceLabel(source?: string): string {
    return ({
      manual_daemon: "Manual, daemon-specific",
      manual_global: "Manual, all daemons",
      configured_clone: "Watched clone metadata",
      registered_project: "Registered project",
      tracked_repo: "Tracked repository name",
    } as Record<string, string>)[source ?? ""] ?? "No matching repository";
  }

  function statusTone(status: string): "success" | "warning" | "danger" | "muted" {
    if (status === "mapped") return "success";
    if (status === "ambiguous" || status === "invalid") return "danger";
    if (status === "unmapped") return "warning";
    return "muted";
  }

  function statusLabel(status: string): string {
    if (status === "mapped") return "Mapped";
    if (status === "ambiguous") return "Ambiguous";
    if (status === "invalid") return "Invalid override";
    return "Unmapped";
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
      await loadDiagnostics();
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

  <section class="mapping-section" aria-label="Effective Kata project mappings">
    <div class="mapping-section-header">
      <div>
        <h3>Effective mappings</h3>
        <p>What workspace creation resolves for each project reported by Kata.</p>
      </div>
      {#if daemonOptions.length > 1}
        <div class="daemon-picker">
          <span>Daemon</span>
          <SelectDropdown
            title="Kata mapping daemon"
            value={selectedDaemonID}
            options={daemonOptions}
            onchange={(value) => { void loadDiagnostics(value); }}
            disabled={diagnosticsLoading}
          />
        </div>
      {/if}
    </div>

    {#if diagnosticsLoading}
      <p class="empty-mappings">Loading effective mappings...</p>
    {:else if diagnosticsError}
      <p class="settings-error" role="alert">{diagnosticsError}</p>
    {:else if !diagnostics || diagnostics.projects.length === 0}
      <p class="empty-mappings">This Kata daemon reports no projects.</p>
    {:else}
      <div class="mapping-table-wrap">
        <table class="mapping-table effective-table" aria-label="Effective Kata project mappings">
          <thead>
            <tr>
              <th scope="col">Kata project</th>
              <th scope="col">Repository target</th>
              <th scope="col">Resolution</th>
              <th scope="col" aria-label="Mapping actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each diagnostics.projects as project (project.project_uid)}
              {@const inferredTarget = project.repo
                ? repoOptionsByKey.get(repoKey(project.repo.provider, project.repo.platform_host, project.repo.repo_path))
                : undefined}
              <tr>
                <td>
                  <strong>{project.project_name || project.project_uid}</strong>
                  <span class="mapping-meta">{project.project_uid}</span>
                </td>
                <td>
                  {#if inferredTarget}
                    <span>{inferredTarget.displayName}</span>
                    <span class="mapping-meta">{inferredTarget.repo.repo_path}</span>
                  {:else if project.repo}
                    <span class="mapping-empty">No selectable repository</span>
                    <span class="mapping-meta">Inferred repository: {project.repo.repo_path}</span>
                  {:else}
                    <span class="mapping-empty">No inferred project</span>
                  {/if}
                </td>
                <td>
                  <Chip size="xs" tone={statusTone(project.status)} uppercase={false}>
                    {statusLabel(project.status)}
                  </Chip>
                  <span class="mapping-meta">{sourceLabel(project.source)}</span>
                </td>
                <td class="action-cell">
                  {#if !project.source?.startsWith("manual_")}
                    <Button
                      size="sm"
                      surface="outline"
                      type="button"
                      onclick={() => addOverride(project)}
                      disabled={embedded || saving || repoOptions.length === 0}
                    >
                      Add override
                    </Button>
                  {:else}
                    <span class="mapping-meta">Edit below</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="mapping-section" aria-label="Kata project repository mappings">
    <div class="mapping-section-header">
      <div>
        <h3>Manual mappings</h3>
        <p>Overrides take precedence over the effective automatic mapping shown above.</p>
      </div>
      <Button
        size="sm"
        type="button"
        onclick={addMapping}
        disabled={embedded || saving || repoOptions.length === 0}
      >
        <PlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
        Add mapping
      </Button>
    </div>

    {#if drafts.length === 0}
      <p class="empty-mappings">
        {repoOptions.length === 0
          ? "No known repository targets are available."
          : "No manual Kata project mappings configured."}
      </p>
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
              <th scope="col">Repository target</th>
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
                    title={`Kata project ${label} repository target`}
                    value={draft.repoKey}
                    options={[{ value: "", label: "Select a repository" }, ...repoSelectOptions]}
                    onchange={(value) => { draft.repoKey = value; }}
                    disabled={embedded || saving}
                  />
                </td>
                <td class="action-cell">
                  <Button
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
                  </Button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <div class="settings-actions">
    <Button
      size="sm"
      type="button"
      onclick={resetDraft}
      disabled={!isDirty || saving}
    >
      <RotateCcwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      Reset
    </Button>
    <Button
      tone="info"
      surface="solid"
      type="button"
      onclick={() => void save()}
      disabled={!canSave}
    >
      Save Kata mappings
    </Button>
  </div>
</div>

<style>
  .kata-project-mappings {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .mapping-section {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .mapping-section-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-5);
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

  .effective-table strong,
  .effective-table > tbody > tr > td > span:first-child {
    display: block;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .mapping-meta {
    display: block;
    margin-top: var(--space-1);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .mapping-empty {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .daemon-picker {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .daemon-picker :global(.kit-select-dropdown) {
    min-width: 160px;
  }

  .mapping-table :global(.kit-select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .mapping-table :global(.kit-select-dropdown__trigger) {
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
    gap: var(--space-4);
  }

  @media (max-width: 759px) {
    .mapping-table-wrap {
      overflow-x: auto;
    }
  }
</style>
