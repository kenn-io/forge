<script lang="ts">
  import CalendarIcon from "@lucide/svelte/icons/calendar";
  import ClockIcon from "@lucide/svelte/icons/clock-3";
  import FlagIcon from "@lucide/svelte/icons/flag";
  import UserIcon from "@lucide/svelte/icons/user-round";
  import XIcon from "@lucide/svelte/icons/x";
  import { ActionButton, Chip } from "@middleman/ui";
  import type { KataTaskDetail } from "../../api/kata/taskTypes.js";
  import DatePicker from "../shared/DatePicker.svelte";
  import TypeaheadTrigger, { type TypeaheadOption } from "../shared/TypeaheadTrigger.svelte";

  interface Props {
    issue: KataTaskDetail;
    ownerOptions: TypeaheadOption[];
    onPatchMetadata: (uid: string, patch: Record<string, unknown>) => boolean | Promise<boolean>;
    onAssignOwner: (uid: string, owner: string) => boolean | Promise<boolean>;
    onUnassignOwner: (uid: string) => boolean | Promise<boolean>;
    onSetPriority: (uid: string, priority: number | null) => boolean | Promise<boolean>;
    onAddLabel: (uid: string, label: string) => boolean | Promise<boolean>;
    onRemoveLabel: (uid: string, label: string) => void | Promise<void>;
  }

  let {
    issue,
    ownerOptions,
    onPatchMetadata,
    onAssignOwner,
    onUnassignOwner,
    onSetPriority,
    onAddLabel,
    onRemoveLabel,
  }: Props = $props();

  type PropertyKey = "scheduled" | "due" | "priority";

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
  let addingLabel = $state(false);
  let editingLabels = $state(false);
  let labelDraft = $state("");
  let trackedUID = $state<string | null>(null);

  $effect(() => {
    if (issue.issue.uid === trackedUID) return;
    trackedUID = issue.issue.uid;
    activeProperty = null;
    scheduledDraft = "";
    dueDraft = "";
    addingLabel = false;
    editingLabels = false;
    labelDraft = "";
  });

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

  function ownerLabel(): string {
    return issue.issue.owner ?? "Unassigned";
  }

  function priorityLabel(): string {
    const priority = issue.issue.priority;
    return priority === undefined || priority === null ? "No priority" : `P${priority}`;
  }

  function openProperty(property: PropertyKey): void {
    activeProperty = property;
    if (property === "scheduled") {
      scheduledDraft = issue.issue.metadata.scheduled_on ?? "";
    } else if (property === "due") {
      dueDraft = issue.issue.metadata.deadline_on ?? "";
    }
  }

  async function patchScheduled(value: string): Promise<void> {
    const property = "scheduled";
    scheduledDraft = value;
    const ok = await onPatchMetadata(uid(), { scheduled_on: value === "" ? null : value });
    if (ok && activeProperty === property) {
      activeProperty = null;
    }
  }

  async function patchDue(value: string): Promise<void> {
    const property = "due";
    dueDraft = value;
    const ok = await onPatchMetadata(uid(), { deadline_on: value === "" ? null : value });
    if (ok && activeProperty === property) {
      activeProperty = null;
    }
  }

  async function updateOwner(value: string | null): Promise<boolean> {
    const owner = value?.trim() ?? "";
    const ok = owner
      ? await onAssignOwner(uid(), owner)
      : await onUnassignOwner(uid());
    if (ok) {
      activeProperty = null;
    }
    return ok;
  }

  async function updatePriority(value: string): Promise<void> {
    const property = "priority";
    const priority = value === "" ? null : Number(value);
    const ok = await onSetPriority(uid(), priority);
    if (ok && activeProperty === property) {
      activeProperty = null;
    }
  }

  async function submitLabel(): Promise<void> {
    const label = labelDraft.trim();
    if (!label) {
      addingLabel = false;
      return;
    }
    const ok = await onAddLabel(uid(), label);
    if (ok) {
      labelDraft = "";
      addingLabel = false;
    }
  }

  function toggleLabelEditing(): void {
    editingLabels = !editingLabels;
    if (!editingLabels) {
      labelDraft = "";
      addingLabel = false;
    }
  }

  function handleLabelKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      void submitLabel();
    } else if (event.key === "Escape") {
      event.preventDefault();
      labelDraft = "";
      addingLabel = false;
    }
  }
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
        clearable
        onEscape={() => {
          activeProperty = null;
        }}
        onchange={(value) => {
          void patchScheduled(value);
        }}
      />
    </div>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit scheduled" onclick={() => openProperty("scheduled")}>
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
        clearable
        onEscape={() => {
          activeProperty = null;
        }}
        onchange={(value) => {
          void patchDue(value);
        }}
      />
    </div>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit due date" onclick={() => openProperty("due")}>
      <ClockIcon size={13} strokeWidth={1.8} />
      <span>Due</span>
      <strong>{dueLabel()}</strong>
    </button>
  {/if}

  <div class="property-pill property-pill--typeahead">
    <UserIcon size={13} strokeWidth={1.8} />
    <TypeaheadTrigger
      ariaLabel="Owner"
      options={ownerOptions}
      selected={issue.issue.owner ?? null}
      allowClear
      allowCustom
      clearLabel="Unassigned"
      triggerPrefix="Owner"
      triggerAriaLabel={`Owner: ${ownerLabel()}`}
      placeholder="Owner..."
      emptyLabel="Enter an owner"
      onChange={updateOwner}
    />
  </div>

  {#if activeProperty === "priority"}
    <label class="property-pill property-pill--editing">
      <FlagIcon size={13} strokeWidth={1.8} />
      <span>Priority</span>
      <select
        aria-label="Priority"
        value={issue.issue.priority === undefined || issue.issue.priority === null
          ? ""
          : String(issue.issue.priority)}
        onchange={(event) => {
          void updatePriority(event.currentTarget.value);
        }}
      >
        {#each priorityOptions as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
    </label>
  {:else}
    <button type="button" class="property-pill" aria-label="Edit priority" onclick={() => openProperty("priority")}>
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
                  size="sm"
                  tone="muted"
                  uppercase={false}
                  interactive
                  class="kata-label-chip"
                  ariaLabel={`Remove label ${label.label}`}
                  title={`Remove label ${label.label}`}
                  onclick={() => {
                    void onRemoveLabel(uid(), label.label);
                  }}
                >
                  {label.label}
                  <XIcon size={11} strokeWidth={2.2} aria-hidden="true" />
                </Chip>
              {:else}
                <Chip size="sm" tone="muted" uppercase={false} class="kata-label-chip">
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
      bind:value={labelDraft}
      onkeydown={handleLabelKeydown}
      onblur={() => {
        void submitLabel();
      }}
    />
  {:else}
    <div class="label-actions">
      <ActionButton size="sm" surface="outline" label="Add label" onclick={() => { addingLabel = true; }} />
      {#if issue.labels.length > 0}
        <ActionButton
          size="sm"
          surface="outline"
          label={editingLabels ? "Done" : "Edit labels"}
          ariaLabel={editingLabels ? "Done editing labels" : undefined}
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

  .property-pill--typeahead :global(.typeahead) {
    min-width: 136px;
  }

  .property-pill--typeahead :global(.typeahead-trigger),
  .property-pill--typeahead :global(.typeahead-input) {
    min-height: 28px;
    height: 28px;
    border-color: transparent;
    background: var(--bg-inset);
  }

  .property-pill--typeahead :global(.typeahead-trigger:hover) {
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
  }

  .property-pill select {
    min-height: 22px;
    min-width: 0;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
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
