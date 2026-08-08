<script lang="ts">
  import { Effect, Exit } from "effect";
  import { onDestroy } from "svelte";
  import CalendarIcon from "@lucide/svelte/icons/calendar";
  import ClockIcon from "@lucide/svelte/icons/clock-3";
  import FlagIcon from "@lucide/svelte/icons/flag";
  import UserIcon from "@lucide/svelte/icons/user-round";
  import XIcon from "@lucide/svelte/icons/x";
  import { Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { Button, Chip, SelectDropdown } from "@kenn-io/kit-ui";
  import type { KataTaskDetail } from "../../api/kata/taskTypes.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { KataCommand } from "../../features/kata/kata-command.js";
  import DatePicker from "../shared/DatePicker.svelte";

  interface Props {
    issue: KataTaskDetail;
    ownerOptions: TypeaheadOption[];
    actionsDisabled?: boolean | undefined;
    draftResetGeneration?: number | undefined;
    onPatchMetadata: (uid: string, patch: Record<string, unknown>) => KataCommand<boolean>;
    onAssignOwner: (uid: string, owner: string) => KataCommand<boolean>;
    onUnassignOwner: (uid: string) => KataCommand<boolean>;
    onSetPriority: (uid: string, priority: number | null) => KataCommand<boolean>;
    onAddLabel: (uid: string, label: string) => KataCommand<boolean>;
    onRemoveLabel: (uid: string, label: string) => KataCommand<boolean>;
  }

  let {
    issue,
    ownerOptions,
    actionsDisabled = false,
    draftResetGeneration = 0,
    onPatchMetadata,
    onAssignOwner,
    onUnassignOwner,
    onSetPriority,
    onAddLabel,
    onRemoveLabel,
  }: Props = $props();

  const runtime = getAppRuntime();

  type PropertyKey = "scheduled" | "due" | "priority";
  type PropertyDraftKey = PropertyKey | "owner" | "label";

  interface PendingDraftReset {
    key: PropertyDraftKey;
    uid: string;
    generation: number;
    ownerEditVersion?: number;
  }

  const priorityOptions = [
    { value: "", label: "No priority" },
    { value: "0", label: "P0" },
    { value: "1", label: "P1" },
    { value: "2", label: "P2" },
    { value: "3", label: "P3" },
    { value: "4", label: "P4" },
  ];

  let activeProperty = $state<PropertyKey | null>(null);
  let scheduledDraft = $state("");
  let dueDraft = $state("");
  let priorityDraft = $state("");
  let addingLabel = $state(false);
  let editingLabels = $state(false);
  let labelDraft = $state("");
  let ownerEditVersion = $state(0);
  let ownerDraft = $state("");
  let ownerEditorOpen = $state(false);
  let ownerEditorGeneration = $state(0);
  let trackedUID = $state<string | null>(null);
  let lastDraftResetGeneration = $state<number | null>(null);
  let pendingDraftReset = $state.raw<PendingDraftReset | null>(null);
  const propertyExecutions = new Map<PropertyDraftKey, AppExecution<boolean, never>>();

  $effect(() => {
    if (issue.issue.uid === trackedUID) return;
    trackedUID = issue.issue.uid;
    activeProperty = null;
    scheduledDraft = "";
    dueDraft = "";
    priorityDraft = "";
    addingLabel = false;
    editingLabels = false;
    labelDraft = "";
    ownerEditVersion = 0;
    ownerDraft = "";
    ownerEditorOpen = false;
    ownerEditorGeneration += 1;
    pendingDraftReset = null;
    lastDraftResetGeneration = draftResetGeneration;
  });

  $effect(() => {
    const nextGeneration = draftResetGeneration;
    if (lastDraftResetGeneration === null) {
      lastDraftResetGeneration = nextGeneration;
      return;
    }
    if (nextGeneration === lastDraftResetGeneration) return;
    lastDraftResetGeneration = nextGeneration;
    const pending = pendingDraftReset;
    pendingDraftReset = null;
    if (
      !pending ||
      pending.uid !== issue.issue.uid ||
      pending.generation === nextGeneration ||
      (pending.key === "owner" && pending.ownerEditVersion !== ownerEditVersion)
    ) return;
    resetDraft(pending.key);
  });

  function resetDraft(key: PropertyDraftKey): void {
    if (key === "scheduled") {
      if (activeProperty === key) activeProperty = null;
      scheduledDraft = "";
    } else if (key === "due") {
      if (activeProperty === key) activeProperty = null;
      dueDraft = "";
    } else if (key === "priority") {
      if (activeProperty === key) activeProperty = null;
      priorityDraft = "";
    } else if (key === "owner") {
      ownerDraft = "";
      ownerEditorOpen = false;
      ownerEditorGeneration += 1;
    } else {
      labelDraft = "";
      addingLabel = false;
    }
  }

  function scheduleAcceptedReset(
    key: PropertyDraftKey,
    mutationUID: string,
    generation: number,
    ownerVersion?: number,
  ): boolean {
    if (issue.issue.uid !== mutationUID) return false;
    if (draftResetGeneration !== generation) {
      if (key === "owner" && ownerVersion !== ownerEditVersion) return false;
      resetDraft(key);
      return true;
    }
    pendingDraftReset = {
      key,
      uid: mutationUID,
      generation,
      ...(ownerVersion === undefined ? {} : { ownerEditVersion: ownerVersion }),
    };
    return false;
  }

  function uid(): string {
    return issue.issue.uid;
  }

  function formatDate(value: string): string {
    const parts = value.split("-");
    if (parts.length !== 3) return value;
    const [year, month, day] = parts;
    const date = new Date(Number(year), Number(month) - 1, Number(day));
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleDateString(undefined, {
      month: "short",
      day: "numeric",
      year: new Date().getFullYear() === date.getFullYear() ? undefined : "numeric",
    });
  }

  function scheduledLabel(): string {
    const value = issue.issue.metadata.scheduled_on;
    return value ? formatDate(value) : "When";
  }

  function dueLabel(): string {
    const value = issue.issue.metadata.deadline_on;
    return value ? formatDate(value) : "No due date";
  }

  function priorityLabel(): string {
    const priority = issue.issue.priority;
    return priority === undefined || priority === null ? "No priority" : `P${priority}`;
  }

  function openProperty(property: PropertyKey): void {
    if (actionsDisabled) return;
    activeProperty = property;
    if (property === "scheduled") {
      scheduledDraft = issue.issue.metadata.scheduled_on ?? "";
    } else if (property === "due") {
      dueDraft = issue.issue.metadata.deadline_on ?? "";
    } else {
      priorityDraft = issue.issue.priority === undefined || issue.issue.priority === null
        ? ""
        : String(issue.issue.priority);
    }
  }

  function runPropertyCommand(
    key: PropertyDraftKey,
    command: KataCommand<boolean>,
    onResult: (changed: boolean) => boolean = () => false,
  ): AppExecution<boolean, never> {
    propertyExecutions.get(key)?.interrupt();
    const execution = runtime.runCommand(
      command.pipe(Effect.flatMap((changed) => Effect.sync(() => onResult(changed)))),
      {
        operation: "update Kata task property",
        safeContext: { issueUid: issue.issue.uid, property: key },
        onFailure: () => {},
      },
    );
    propertyExecutions.set(key, execution);
    return execution;
  }

  function patchScheduled(value: string): void {
    if (actionsDisabled) return;
    const property = "scheduled";
    scheduledDraft = value;
    const mutationUID = uid();
    const generation = draftResetGeneration;
    runPropertyCommand(
      property,
      onPatchMetadata(mutationUID, { scheduled_on: value === "" ? null : value }),
      (ok) => ok && scheduleAcceptedReset(property, mutationUID, generation),
    );
  }

  function patchDue(value: string): void {
    if (actionsDisabled) return;
    const property = "due";
    dueDraft = value;
    const mutationUID = uid();
    const generation = draftResetGeneration;
    runPropertyCommand(
      property,
      onPatchMetadata(mutationUID, { deadline_on: value === "" ? null : value }),
      (ok) => ok && scheduleAcceptedReset(property, mutationUID, generation),
    );
  }

  function updateOwner(value: string): boolean | Promise<boolean> {
    if (actionsDisabled) return false;
    const owner = value.trim();
    const mutationUID = uid();
    const generation = draftResetGeneration;
    const editVersion = ownerEditVersion;
    const command = owner ? onAssignOwner(mutationUID, owner) : onUnassignOwner(mutationUID);
    const execution = runPropertyCommand(
      "owner",
      command,
      (ok) => ok && scheduleAcceptedReset("owner", mutationUID, generation, editVersion),
    );
    return observePropertyCommand(execution);
  }

  function updatePriority(value: string): void {
    if (actionsDisabled) return;
    const property = "priority";
    const priority = value === "" ? null : Number(value);
    priorityDraft = value;
    const mutationUID = uid();
    const generation = draftResetGeneration;
    runPropertyCommand(
      property,
      onSetPriority(mutationUID, priority),
      (ok) => ok && scheduleAcceptedReset(property, mutationUID, generation),
    );
  }

  function submitLabel(): void {
    if (actionsDisabled) return;
    const label = labelDraft.trim();
    if (!label) {
      addingLabel = false;
      return;
    }
    const mutationUID = uid();
    const generation = draftResetGeneration;
    runPropertyCommand(
      "label",
      onAddLabel(mutationUID, label),
      (ok) => ok && scheduleAcceptedReset("label", mutationUID, generation),
    );
  }

  async function observePropertyCommand(execution: AppExecution<boolean, never>): Promise<boolean> {
    const exit = await execution.exit;
    return Exit.isSuccess(exit) && exit.value;
  }

  function toggleLabelEditing(): void {
    if (actionsDisabled) return;
    editingLabels = !editingLabels;
    if (!editingLabels) {
      labelDraft = "";
      addingLabel = false;
    }
  }

  function handleLabelKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      submitLabel();
    } else if (event.key === "Escape") {
      event.preventDefault();
      labelDraft = "";
      addingLabel = false;
    }
  }

  function trackOwnerQuery(query: string): void {
    if (query === ownerDraft) return;
    ownerDraft = query;
    ownerEditVersion += 1;
  }

  function handleOwnerFocusIn(event: FocusEvent): void {
    ownerEditorOpen = event.target instanceof HTMLInputElement && event.target.getAttribute("role") === "combobox";
  }

  function handleOwnerFocusOutCapture(event: FocusEvent): void {
    if (actionsDisabled && ownerEditorOpen) {
      event.stopPropagation();
      return;
    }
    const related = event.relatedTarget as Node | null;
    if (!(event.currentTarget as HTMLElement).contains(related)) ownerEditorOpen = false;
  }

  function removeLabel(label: string): void {
    if (actionsDisabled) return;
    runPropertyCommand("label", onRemoveLabel(uid(), label));
  }

  onDestroy(() => {
    for (const execution of propertyExecutions.values()) execution.interrupt();
    propertyExecutions.clear();
  });
</script>

<section class="property-pills" aria-label="Properties">
  {#if activeProperty === "scheduled"}
    <div class="property-pill property-pill--editing" role="group" aria-label="Edit scheduled">
      <CalendarIcon size={13} strokeWidth={1.8} />
      <span>Scheduled</span>
      <DatePicker
        class="property-date-picker"
        ariaLabel="Scheduled"
        value={scheduledDraft}
        disabled={actionsDisabled}
        clearable
        onEscape={() => {
          activeProperty = null;
        }}
        onchange={patchScheduled}
      />
    </div>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit scheduled" disabled={actionsDisabled} onclick={() => openProperty("scheduled")}>
      <CalendarIcon size={13} strokeWidth={1.8} />
      <span>Scheduled</span>
      <strong>{scheduledLabel()}</strong>
    </button>
  {/if}

  {#if activeProperty === "due"}
    <div class="property-pill property-pill--editing" role="group" aria-label="Edit due date">
      <ClockIcon size={13} strokeWidth={1.8} />
      <span>Due</span>
      <DatePicker
        class="property-date-picker"
        ariaLabel="Due"
        clearLabel="Clear due date"
        value={dueDraft}
        disabled={actionsDisabled}
        clearable
        onEscape={() => {
          activeProperty = null;
        }}
        onchange={patchDue}
      />
    </div>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit due date" disabled={actionsDisabled} onclick={() => openProperty("due")}>
      <ClockIcon size={13} strokeWidth={1.8} />
      <span>Due</span>
      <strong>{dueLabel()}</strong>
    </button>
  {/if}

  <div
    class="property-pill property-pill--typeahead"
    onfocusin={handleOwnerFocusIn}
    onfocusoutcapture={handleOwnerFocusOutCapture}
  >
    <UserIcon size={13} strokeWidth={1.8} />
    {#key ownerEditorGeneration}
    <Typeahead
      options={ownerOptions}
      value={issue.issue.owner ?? ""}
      fallbackLabel="Unassigned"
      placeholder="Owner"
      allowClear
      allowCustom
      clearLabel="Unassigned"
      triggerPrefix="Owner:"
      emptyLabel="Enter an owner"
      disabled={actionsDisabled && !ownerEditorOpen}
      onquery={trackOwnerQuery}
      onselect={updateOwner}
    />
    {/key}
  </div>

  {#if activeProperty === "priority"}
    <div class="property-pill property-pill--editing property-pill--select" role="group" aria-label="Edit priority">
      <FlagIcon size={13} strokeWidth={1.8} />
      <span>Priority</span>
      <SelectDropdown
        title="Priority"
        value={priorityDraft}
        options={priorityOptions}
        disabled={actionsDisabled}
        onchange={updatePriority}
      />
    </div>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit priority" disabled={actionsDisabled} onclick={() => openProperty("priority")}>
      <FlagIcon size={13} strokeWidth={1.8} />
      <span>Priority</span>
      <strong>{priorityLabel()}</strong>
    </button>
  {/if}
</section>

<dl class="detail-properties">
  <div>
    <dt>Project</dt>
    <dd>{issue.issue.project_name}</dd>
  </div>
  {#if issue.labels.length > 0}
    <div>
      <dt>Labels</dt>
      <dd>
        <ul class="label-list" aria-label="Labels">
          {#each issue.labels as label (label.label)}
            <li class="label-token">
              {#if editingLabels}
                <Chip
                  size="xs"
                  tone="muted"
                  uppercase={false}
                  interactive
                  class="kata-label-chip"
                  ariaLabel={`Remove label ${label.label}`}
                  title={`Remove label ${label.label}`}
                  disabled={actionsDisabled}
                  onclick={() => removeLabel(label.label)}
                >
                  {label.label}
                  <XIcon size={11} strokeWidth={2.2} aria-hidden="true" />
                </Chip>
              {:else}
                <Chip size="xs" tone="muted" uppercase={false} class="kata-label-chip">
                  {label.label}
                </Chip>
              {/if}
            </li>
          {/each}
        </ul>
      </dd>
    </div>
  {/if}
</dl>

<section class="label-editor" aria-label="Labels">
  {#if addingLabel}
    <input
      aria-label="New label"
      class="label-input"
      value={labelDraft}
      disabled={actionsDisabled}
      oninput={(event) => {
        labelDraft = event.currentTarget.value;
      }}
      onkeydown={handleLabelKeydown}
      onblur={submitLabel}
    />
  {:else}
    <div class="label-actions">
      <Button size="sm" surface="outline" label="Add label" disabled={actionsDisabled} onclick={() => { addingLabel = true; }} />
      {#if issue.labels.length > 0}
        <Button
          size="sm"
          surface="outline"
          label={editingLabels ? "Done" : "Edit labels"}
          ariaLabel={editingLabels ? "Done editing labels" : undefined}
          disabled={actionsDisabled}
          onclick={toggleLabelEditing}
        />
      {/if}
    </div>
  {/if}
</section>

<style>
  .property-pills {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 0 0 18px;
  }

  .property-pill {
    min-height: 28px;
    border: 1px solid transparent;
    border-radius: 6px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 8px;
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: 1;
  }

  button.property-pill {
    cursor: pointer;
  }

  button.property-pill:hover {
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .property-pill :global(svg) {
    color: var(--text-muted);
    flex: 0 0 auto;
  }

  .property-pill span {
    color: var(--text-muted);
  }

  .property-pill strong {
    color: var(--text-primary);
    font-weight: 600;
  }

  .property-pill--editing {
    border-color: var(--border-default);
    background: var(--bg-primary);
  }

  .property-pill--typeahead {
    padding: 0;
    gap: 4px;
    background: transparent;
  }

  .property-pill--typeahead > :global(svg) {
    margin-left: 8px;
  }

  .property-pill--typeahead :global(.kit-typeahead) {
    min-width: 136px;
  }

  .property-pill--typeahead :global(.kit-typeahead__trigger),
  .property-pill--typeahead :global(.kit-typeahead__input) {
    min-height: 28px;
    height: 28px;
    border-color: transparent;
    background: var(--bg-inset);
  }

  .property-pill--typeahead :global(.kit-typeahead__trigger:hover) {
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }

  .property-pill--select :global(.kit-select-dropdown) {
    min-width: 104px;
  }

  .property-pill--select :global(.kit-select-dropdown__trigger) {
    height: 22px;
    border-color: transparent;
    background: transparent;
    color: var(--text-primary);
  }

  .property-pill--select :global(.kit-select-dropdown__trigger:hover:not(:disabled)),
  .property-pill--select :global(.kit-select-dropdown__trigger[aria-expanded="true"]) {
    border-color: var(--border-default);
    background: var(--bg-inset);
  }

  .detail-properties {
    display: grid;
    gap: 8px;
    margin: 0 0 22px;
  }

  .detail-properties div {
    display: grid;
    grid-template-columns: 92px minmax(0, 1fr);
    gap: 12px;
  }

  .detail-properties dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .detail-properties dd {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .label-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    min-width: 0;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .label-token {
    display: inline-flex;
    align-items: center;
    max-width: 100%;
    min-width: 0;
  }

  .label-token :global(.kata-label-chip) {
    max-width: min(220px, 100%);
  }

  .label-editor {
    margin: 0 0 22px;
  }

  .label-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .label-input {
    width: min(220px, 100%);
    min-height: 30px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 5px 8px;
  }
</style>
