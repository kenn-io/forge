<script lang="ts">
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import { renderMarkdown } from "@middleman/ui/utils/markdown";

  import type {
    KataProjectSummary,
    KataRecurrence,
    KataTaskAPI,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskEvent,
    KataTaskGroup,
  } from "../../api/kata/taskTypes.js";
  import type { MessageLinkRef } from "../../messages/types";
  import IssueMessageLinks from "../../features/kata/IssueMessageLinks.svelte";
  import RecurrencePanel from "../recurrence/RecurrencePanel.svelte";
  import TypeaheadTrigger, { type TypeaheadOption } from "../shared/TypeaheadTrigger.svelte";
  import KataChecklistEditor from "./KataChecklistEditor.svelte";
  import KataIssueActions from "./KataIssueActions.svelte";
  import KataIssueDiscussion from "./KataIssueDiscussion.svelte";
  import KataIssueOverflowMenu from "./KataIssueOverflowMenu.svelte";
  import KataIssueProperties from "./KataIssueProperties.svelte";

  interface Props {
    issue: KataTaskDetail;
    events: KataTaskEvent[];
    currentView: { groups: KataTaskGroup[] };
    api: KataTaskAPI;
    activeDaemonId?: string | undefined;
    projects: KataProjectSummary[];
    ownerOptions: TypeaheadOption[];
    messageLinks: MessageLinkRef[];
    unlinkBusyIds: ReadonlySet<number>;
    unlinkError: string | null;
    selectedRecurrences: KataRecurrence[];
    checklistRevealed: boolean;
    onMoveIssue: (toProjectUID: string | null) => void | Promise<void>;
    onPatchMetadata: (uid: string, patch: Record<string, unknown>) => boolean | Promise<boolean>;
    onAddComment: (uid: string, body: string) => boolean | Promise<boolean>;
    onEditIssue: (uid: string, patch: KataTaskEditPatch) => boolean | Promise<boolean>;
    onAssignOwner: (uid: string, owner: string) => boolean | Promise<boolean>;
    onUnassignOwner: (uid: string) => boolean | Promise<boolean>;
    onSetPriority: (uid: string, priority: number | null) => boolean | Promise<boolean>;
    onAddLabel: (uid: string, label: string) => boolean | Promise<boolean>;
    onRemoveLabel: (uid: string, label: string) => void | Promise<void>;
    onOpenMessage?: ((link: MessageLinkRef) => void) | undefined;
    onUnlinkMessage: (link: MessageLinkRef) => void | Promise<void>;
    onRevealChecklist: () => void;
    onCreateRecurrence: () => void;
    onEditRecurrence: (recurrence: KataRecurrence) => void;
    onDeleteRecurrence: (recurrence: KataRecurrence) => void;
    onCloseIssue: (
      reason: "done" | "wontfix" | "duplicate" | "superseded",
      message: string,
    ) => boolean | Promise<boolean>;
    onReopenIssue: () => void | Promise<void>;
    onDeleteIssue: () => boolean | Promise<boolean>;
    onSelectIssue: (uid: string) => void | Promise<void>;
  }

  let {
    issue,
    events,
    currentView,
    api,
    activeDaemonId = undefined,
    projects,
    ownerOptions,
    messageLinks,
    unlinkBusyIds,
    unlinkError,
    selectedRecurrences,
    checklistRevealed,
    onMoveIssue,
    onPatchMetadata,
    onAddComment,
    onEditIssue,
    onAssignOwner,
    onUnassignOwner,
    onSetPriority,
    onAddLabel,
    onRemoveLabel,
    onOpenMessage = undefined,
    onUnlinkMessage,
    onRevealChecklist,
    onCreateRecurrence,
    onEditRecurrence,
    onDeleteRecurrence,
    onCloseIssue,
    onReopenIssue,
    onDeleteIssue,
    onSelectIssue,
  }: Props = $props();

  let editingTitle = $state(false);
  let editingBody = $state(false);
  let titleDraft = $state("");
  let bodyDraft = $state("");
  let titleInput: HTMLInputElement | null = $state(null);
  let bodyTextarea: HTMLTextAreaElement | null = $state(null);
  let cancelingTitle = $state(false);
  let lastIssueUID = $state<string | null>(null);

  const canCreateRecurrence = $derived(issue.issue.recurrence_id === undefined);
  const visibleRecurrences = $derived.by(() => {
    const attachedID = issue.issue.recurrence_id;
    if (attachedID !== undefined) {
      const attached = selectedRecurrences.find((recurrence) => recurrence.id === attachedID);
      return attached ? [attached] : [];
    }
    return selectedRecurrences;
  });

  $effect(() => {
    const uid = issue.issue.uid;
    if (uid === lastIssueUID) return;
    lastIssueUID = uid;
    editingTitle = false;
    editingBody = false;
    cancelingTitle = false;
  });

  function checklistItems() {
    return issue.issue.metadata.checklist ?? [];
  }

  function isTaskInboxProject(project: KataProjectSummary): boolean {
    return project.metadata.role === "inbox";
  }

  function moveOptions(): TypeaheadOption[] {
    return projects
      .filter((project) => project.uid !== issue.issue.project_uid)
      .filter((project) => !isTaskInboxProject(project))
      .sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }))
      .map((project) => ({
        value: project.uid,
        label: project.name,
        meta: String(project.open_count),
      }));
  }

  function currentProjectName(): string {
    const fromIssue = issue.issue.project_name.trim();
    if (fromIssue) return fromIssue;
    const project =
      projects.find((candidate) => candidate.uid === issue.issue.project_uid) ??
      projects.find((candidate) => candidate.id === issue.issue.project_id);
    return project?.name ?? issue.issue.project_uid;
  }

  function startEditingTitle(): void {
    cancelingTitle = false;
    titleDraft = issue.issue.title;
    editingTitle = true;
    queueMicrotask(() => {
      titleInput?.focus();
      titleInput?.select();
    });
  }

  async function commitTitle(): Promise<void> {
    if (cancelingTitle) {
      cancelingTitle = false;
      editingTitle = false;
      return;
    }
    const next = titleDraft.trim();
    editingTitle = false;
    if (!next || next === issue.issue.title) return;
    await onEditIssue(issue.issue.uid, { title: next });
  }

  function handleTitleKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      void commitTitle();
    } else if (event.key === "Escape") {
      event.preventDefault();
      cancelingTitle = true;
      editingTitle = false;
    }
  }

  function startEditingBody(): void {
    bodyDraft = issue.issue.body;
    editingBody = true;
    queueMicrotask(() => bodyTextarea?.focus());
  }

  async function commitBody(): Promise<void> {
    const next = bodyDraft;
    editingBody = false;
    if (next === issue.issue.body) return;
    await onEditIssue(issue.issue.uid, { body: next });
  }

  function handleBodyKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      event.preventDefault();
      editingBody = false;
    } else if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      void commitBody();
    }
  }
</script>

<section class="kata-detail" aria-label="Task detail">
  <div class="detail-heading">
    <div class="detail-heading-main">
      <div class="detail-kicker">
        <div class="crumb-project-control">
          <TypeaheadTrigger
            ariaLabel="Move issue project"
            triggerAriaLabel={`Move issue from ${currentProjectName()}`}
            options={moveOptions()}
            selected={null}
            clearLabel={currentProjectName()}
            placeholder="Move to project..."
            emptyLabel="No matching projects"
            onChange={onMoveIssue}
          />
        </div>
        <span class="crumb-sep">/</span>
        <span class="crumb-id">{issue.issue.short_id}</span>
        <span class="sr-only">{issue.issue.qualified_id}</span>
      </div>
      {#if editingTitle}
        <input
          class="title-edit"
          aria-label="Edit title"
          bind:this={titleInput}
          bind:value={titleDraft}
          onkeydown={handleTitleKeydown}
          onblur={() => {
            void commitTitle();
          }}
        />
      {:else}
        <h2 aria-label={issue.issue.title}>
          <button type="button" class="title-button" aria-label="Edit title" onclick={startEditingTitle}>
            <span>{issue.issue.title}</span>
            <PencilIcon size={13} strokeWidth={1.8} />
          </button>
        </h2>
      {/if}
    </div>
    <div class="detail-actions">
      <KataIssueOverflowMenu
        {issue}
        hasChecklist={checklistItems().length > 0 || checklistRevealed}
        hasRecurrence={!canCreateRecurrence}
        onAddChecklist={onRevealChecklist}
        onCreateRecurrence={onCreateRecurrence}
        onDeleteIssue={onDeleteIssue}
      />
      <KataIssueActions {issue} onCloseIssue={onCloseIssue} onReopenIssue={onReopenIssue} />
    </div>
  </div>

  <section class="detail-description" aria-label="Description">
    <div class="section-header">
      <h3>Description</h3>
      {#if !editingBody}
        <button type="button" class="text-button" aria-label="Edit description" onclick={startEditingBody}>
          <PencilIcon size={13} strokeWidth={1.8} />
          <span>Edit</span>
        </button>
      {/if}
    </div>
    {#if editingBody}
      <textarea
        class="body-edit"
        aria-label="Edit description"
        rows="8"
        bind:this={bodyTextarea}
        bind:value={bodyDraft}
        onkeydown={handleBodyKeydown}
      ></textarea>
      <div class="body-edit-actions">
        <span>Cmd/Ctrl+Enter saves</span>
        <div>
          <button type="button" class="ghost-button" onclick={() => { editingBody = false; }}>Cancel</button>
          <button type="button" class="accent-button" onclick={() => { void commitBody(); }}>Save</button>
        </div>
      </div>
    {:else if issue.issue.body}
      <div class="body-display markdown-body">
        {@html renderMarkdown(issue.issue.body)}
      </div>
    {:else}
      <p class="detail-body-empty">No description.</p>
    {/if}
  </section>

  <KataIssueProperties
    {issue}
    {ownerOptions}
    onPatchMetadata={onPatchMetadata}
    onAssignOwner={onAssignOwner}
    onUnassignOwner={onUnassignOwner}
    onSetPriority={onSetPriority}
    onAddLabel={onAddLabel}
    onRemoveLabel={onRemoveLabel}
  />

  <IssueMessageLinks
    links={messageLinks}
    busyIds={unlinkBusyIds}
    onOpenMessage={onOpenMessage}
    onUnlink={(link) => {
      void onUnlinkMessage(link);
    }}
  />
  {#if unlinkError}
    <p class="unlink-error" role="alert">{unlinkError}</p>
  {/if}

  <KataChecklistEditor {issue} revealed={checklistRevealed} onPatchMetadata={onPatchMetadata} onReveal={onRevealChecklist} />

  {#if visibleRecurrences.length > 0}
    <section class="recurrence-section" aria-label="Recurrence">
      <RecurrencePanel
        recurrences={visibleRecurrences}
        onCreate={onCreateRecurrence}
        onEdit={onEditRecurrence}
        onDelete={onDeleteRecurrence}
      />
    </section>
  {/if}

  {#key issue.issue.uid}
    <KataIssueDiscussion
      {issue}
      {events}
      {currentView}
      {api}
      {activeDaemonId}
      onAddComment={onAddComment}
      onEditIssue={onEditIssue}
      onSelectIssue={onSelectIssue}
    />
  {/key}
</section>

<style>
  .kata-detail {
    flex: 1 1 auto;
    min-width: 0;
    min-height: 0;
    overflow: auto;
    background: var(--bg-primary);
    padding: 18px 22px;
  }

  .detail-heading {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    margin-bottom: 14px;
  }

  .detail-heading-main {
    min-width: 0;
    flex: 1;
  }

  .detail-kicker {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    min-width: 0;
  }

  .crumb-project-control {
    width: clamp(160px, 22vw, 292px);
    min-width: 0;
  }

  .crumb-project-control :global(.typeahead-trigger),
  .crumb-project-control :global(.typeahead-input) {
    height: 22px;
    padding: 0 8px;
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    font-weight: 600;
    background: var(--bg-inset);
    border-color: var(--border-muted);
  }

  .crumb-project-control :global(.typeahead-list) {
    width: clamp(240px, 28vw, 320px);
    right: auto;
  }

  .crumb-project-control :global(.typeahead-trigger:hover) {
    background: var(--bg-surface-hover);
    border-color: var(--border-default);
  }

  .crumb-project-control :global(.typeahead-trigger > svg) {
    display: none;
  }

  .crumb-id {
    min-width: 0;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 650;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .crumb-sep {
    color: var(--text-faint);
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .detail-heading h2 {
    margin: 4px 0 0;
    font-size: var(--font-size-xl);
    line-height: 1.25;
  }

  .detail-actions {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: flex-start;
    gap: 6px;
    padding-top: 2px;
  }

  .title-button {
    width: 100%;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    display: inline-flex;
    align-items: flex-start;
    gap: 8px;
    padding: 0;
    font: inherit;
    font-weight: inherit;
    line-height: inherit;
    text-align: left;
    cursor: pointer;
  }

  .title-button :global(svg) {
    flex: 0 0 auto;
    margin-top: 0.26em;
    color: var(--text-muted);
    opacity: 0;
  }

  .title-button:hover :global(svg),
  .title-button:focus-visible :global(svg) {
    opacity: 1;
  }

  .title-edit {
    width: 100%;
    margin: 4px 0 0;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xl);
    font-weight: 650;
    line-height: 1.25;
    padding: 6px 8px;
  }

  .detail-description,
  .recurrence-section {
    margin: 0 0 18px;
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }

  .section-header h3 {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .text-button {
    min-height: 24px;
    border: 0;
    border-radius: 5px;
    background: transparent;
    color: var(--text-muted);
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 6px;
    font: inherit;
    font-size: var(--font-size-xs);
    cursor: pointer;
  }

  .text-button:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }

  .body-display {
    color: var(--text-secondary);
    line-height: 1.5;
  }

  .body-display :global(p) {
    margin: 0;
  }

  .body-display :global(p + p) {
    margin-top: 0.8em;
  }

  .body-display :global(strong) {
    color: var(--text-primary);
    font-weight: 650;
  }

  .detail-body-empty {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .body-edit {
    width: 100%;
    resize: vertical;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: 1.45;
    padding: 8px 10px;
  }

  .body-edit-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-top: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .body-edit-actions div {
    display: inline-flex;
    gap: 6px;
  }

  .ghost-button,
  .accent-button {
    min-height: 28px;
    border-radius: 6px;
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 4px 10px;
    cursor: pointer;
  }

  .ghost-button {
    border: 1px solid var(--border-default);
    background: var(--bg-primary);
    color: var(--text-secondary);
  }

  .accent-button {
    border: 1px solid var(--accent-blue);
    background: var(--accent-blue);
    color: white;
  }

  .ghost-button:disabled,
  .accent-button:disabled {
    cursor: default;
    opacity: 0.62;
  }

  .unlink-error {
    margin: 8px 0 18px;
    color: var(--color-danger-fg, #991b1b);
    font-size: var(--font-size-sm);
  }
</style>
