<script lang="ts">
  import AlarmClockIcon from "@lucide/svelte/icons/alarm-clock";
  import CalendarDaysIcon from "@lucide/svelte/icons/calendar-days";
  import CheckCircleIcon from "@lucide/svelte/icons/check-circle-2";
  import InboxIcon from "@lucide/svelte/icons/inbox";
  import LayersIcon from "@lucide/svelte/icons/layers";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import StarIcon from "@lucide/svelte/icons/star";

  import type { KataProjectSummary, KataTaskSearchFilters, KataTaskViewName } from "../../api/kata/taskTypes.js";
  import type { KataAreaSummary, KataCurrentView } from "../../stores/kata-workspace.svelte.js";

  interface Props {
    areas: KataAreaSummary[];
    projects: KataProjectSummary[];
    currentView: KataCurrentView;
    searchFilters: KataTaskSearchFilters;
    onOpenView: (name: KataTaskViewName) => void | Promise<void>;
    onOpenProject: (projectUID: string) => void | Promise<void>;
    onCreateProject: (name: string) => Promise<KataProjectSummary>;
    onRenameProject: (id: number, name: string) => Promise<void>;
  }

  let {
    areas,
    projects,
    currentView,
    searchFilters,
    onOpenView,
    onOpenProject,
    onCreateProject,
    onRenameProject,
  }: Props = $props();

  const systemViews: Array<{
    name: KataTaskViewName;
    label: string;
    icon: typeof InboxIcon;
  }> = [
    { name: "inbox", label: "Inbox", icon: InboxIcon },
    { name: "today", label: "Today", icon: StarIcon },
    { name: "upcoming", label: "Upcoming", icon: CalendarDaysIcon },
    { name: "deadlines", label: "Deadlines", icon: AlarmClockIcon },
    { name: "all", label: "All Open", icon: LayersIcon },
    { name: "logbook", label: "Logbook", icon: CheckCircleIcon },
  ];

  let creatingProject = $state(false);
  let createDraft = $state("");
  let createSaving = $state(false);
  let createError = $state<string | null>(null);
  let renamingProjectID = $state<number | null>(null);
  let renameDraft = $state("");
  let renameSaving = $state(false);
  let renameError = $state<string | null>(null);
  let createInput: HTMLInputElement | null = $state(null);
  let renameInput: HTMLInputElement | null = $state(null);

  function viewCount(name: KataTaskViewName): number | undefined {
    const inboxProject = projects.find((project) => project.metadata.role === "inbox");
    if (name === "inbox") return inboxProject?.open_count;
    if (name === "today" && currentView.name === "today" && searchFilters.scope.kind === "all") {
      return currentView.groups.reduce((sum, group) => sum + group.issues.length, 0);
    }
    return undefined;
  }

  function isProjectActive(uid: string): boolean {
    return searchFilters.scope.kind === "project" && searchFilters.scope.project_uid === uid;
  }

  function startCreatingProject(): void {
    creatingProject = true;
    createDraft = "";
    createError = null;
    queueMicrotask(() => createInput?.focus());
  }

  function cancelCreatingProject(): void {
    creatingProject = false;
    createDraft = "";
    createError = null;
  }

  async function submitCreateProject(): Promise<void> {
    const name = createDraft.trim();
    if (!name || createSaving) return;
    createSaving = true;
    createError = null;
    try {
      const project = await onCreateProject(name);
      creatingProject = false;
      createDraft = "";
      await onOpenProject(project.uid);
    } catch (err) {
      createError = err instanceof Error ? err.message : "Could not create project.";
    } finally {
      createSaving = false;
    }
  }

  function startRenamingProject(project: KataProjectSummary): void {
    renamingProjectID = project.id;
    renameDraft = project.name;
    renameError = null;
    queueMicrotask(() => renameInput?.focus());
  }

  function cancelRenamingProject(): void {
    renamingProjectID = null;
    renameDraft = "";
    renameError = null;
  }

  async function submitRenameProject(): Promise<void> {
    if (renamingProjectID === null || renameSaving) return;
    const name = renameDraft.trim();
    if (!name) {
      renameError = "Project name can't be empty.";
      return;
    }
    renameSaving = true;
    renameError = null;
    try {
      await onRenameProject(renamingProjectID, name);
      renamingProjectID = null;
      renameDraft = "";
    } catch (err) {
      renameError = err instanceof Error ? err.message : "Could not rename project.";
    } finally {
      renameSaving = false;
    }
  }
</script>

<aside class="kata-sidebar" aria-label="Kata navigation">
  <nav class="kata-nav" aria-label="System views">
    {#each systemViews as view (view.name)}
      {@const Icon = view.icon}
      {@const count = viewCount(view.name)}
      <button
        type="button"
        class:active={searchFilters.scope.kind === "all" && currentView.name === view.name}
        aria-label={count !== undefined ? `${view.label} ${count}` : view.label}
        onclick={() => {
          void onOpenView(view.name);
        }}
      >
        <span class="nav-icon"><Icon size={14} strokeWidth={1.75} /></span>
        <span class="nav-label">{view.label}</span>
        {#if count !== undefined}
          <span class="nav-count">{count}</span>
        {/if}
      </button>
    {/each}
  </nav>

  {#if areas.length > 0}
    <div class="project-groups">
      {#each areas as area (area.name)}
        <section class="project-group" aria-labelledby={`kata-area-${area.name}`}>
          <h2 id={`kata-area-${area.name}`}>{area.name}</h2>
          {#each area.projects as project (project.uid)}
            {#if renamingProjectID === project.id}
              <form
                class="project-rename-form"
                onsubmit={(event) => {
                  event.preventDefault();
                  void submitRenameProject();
                }}
              >
                <input
                  bind:this={renameInput}
                  aria-label="Rename project"
                  bind:value={renameDraft}
                  onkeydown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      void submitRenameProject();
                    } else if (event.key === "Escape") {
                      event.preventDefault();
                      cancelRenamingProject();
                    }
                  }}
                  onblur={() => {
                    if (!renameSaving) cancelRenamingProject();
                  }}
                  disabled={renameSaving}
                />
              </form>
              {#if renameError}
                <p class="sidebar-error" role="alert">{renameError}</p>
              {/if}
            {:else}
              <div class:active={isProjectActive(project.uid)} class="project-row">
                <button
                  type="button"
                  class="project-select-button"
                  onclick={() => {
                    void onOpenProject(project.uid);
                  }}
                  ondblclick={(event) => {
                    event.preventDefault();
                    startRenamingProject(project);
                  }}
                >
                  <span class="project-name">{project.name}</span>
                  <span class="project-count count">{project.open_count}</span>
                </button>
                <button
                  type="button"
                  class="project-rename-button"
                  aria-label={`Rename ${project.name}`}
                  onclick={() => startRenamingProject(project)}
                >
                  <PencilIcon size={13} strokeWidth={1.8} aria-hidden="true" />
                </button>
              </div>
            {/if}
          {/each}
        </section>
      {/each}
    </div>
  {/if}

  <div class="project-create">
    {#if creatingProject}
      <form
        class="project-create-form"
        onsubmit={(event) => {
          event.preventDefault();
          void submitCreateProject();
        }}
      >
        <input
          bind:this={createInput}
          aria-label="New project name"
          placeholder="Project name"
          bind:value={createDraft}
          onkeydown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void submitCreateProject();
            } else if (event.key === "Escape") {
              event.preventDefault();
              cancelCreatingProject();
            }
          }}
          disabled={createSaving}
        />
      </form>
      {#if createError}
        <p class="sidebar-error" role="alert">{createError}</p>
      {/if}
    {:else}
      <button type="button" class="project-create-button" onclick={startCreatingProject}>
        <PlusIcon size={13} strokeWidth={1.9} />
        <span>New project</span>
      </button>
    {/if}
  </div>
</aside>

<style>
  .kata-sidebar {
    min-width: 0;
    border-right: 1px solid var(--border-default);
    background: var(--bg-secondary);
    overflow: auto;
    padding: 12px;
  }

  .kata-nav,
  .project-group {
    display: grid;
    gap: 4px;
  }

  .kata-nav button,
  .project-select-button,
  .project-create-button {
    width: 100%;
    min-height: 30px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-secondary);
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr) auto;
    align-items: center;
    gap: 7px;
    padding: 4px 8px;
    text-align: left;
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .kata-nav button:hover,
  .project-row:hover,
  .project-create-button:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }

  .kata-nav button.active,
  .project-row.active {
    background: color-mix(in srgb, var(--accent-blue) 28%, var(--bg-secondary));
    box-shadow:
      inset 3px 0 0 var(--accent-blue),
      inset 0 0 0 1px color-mix(in srgb, var(--accent-blue) 42%, transparent);
    color: var(--text-primary);
  }

  .nav-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
  }

  .kata-nav button.active .nav-icon {
    color: var(--accent-blue);
  }

  .project-row.active .project-count,
  .kata-nav button.active .nav-count {
    color: var(--text-primary);
  }

  .kata-nav button.active .nav-label,
  .project-row.active .project-name {
    font-weight: 650;
  }

  .nav-label,
  .project-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .nav-count,
  .project-count {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }

  .project-groups {
    display: grid;
    gap: 18px;
    margin-top: 22px;
  }

  .project-create {
    margin-top: 16px;
  }

  .project-group h2 {
    margin: 0 4px 6px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .project-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 26px;
    align-items: center;
    border-radius: 6px;
  }

  .project-select-button {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .project-rename-button {
    width: 24px;
    height: 24px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: var(--text-muted);
    opacity: 0;
    cursor: pointer;
  }

  .project-row:hover .project-rename-button,
  .project-rename-button:focus-visible {
    opacity: 1;
  }

  .project-create-form input,
  .project-rename-form input {
    width: 100%;
    min-height: 30px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 5px 8px;
  }

  .project-create-form input:focus,
  .project-rename-form input:focus {
    outline: none;
    border-color: var(--accent-blue);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .sidebar-error {
    margin: 4px 6px;
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  @media (max-width: 900px) {
    .kata-sidebar {
      border-right: 0;
      border-bottom: 1px solid var(--border-default);
      max-height: 220px;
    }
  }
</style>
