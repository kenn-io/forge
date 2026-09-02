<script lang="ts">
  import { Effect } from "effect";
  import { Checkbox } from "@kenn-io/kit-ui";
  import type { DetailSettings as DetailSettingsType } from "../../api/types.js";
  import { schemaConstraints } from "../../api/generated/schema-constraints.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import SettingsOwnerNotice from "./SettingsOwnerNotice.svelte";
  import type { SettingsOwner } from "./settingsOwnership.js";

  interface Props {
    detail: DetailSettingsType;
    onUpdate: (detail: DetailSettingsType) => void;
    owner?: SettingsOwner;
  }

  let { detail, onUpdate, owner = "local" }: Props = $props();
  const runtime = getAppRuntime();
  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  // The server's bounds come from the OpenAPI schema, so the input can reject
  // an out-of-range limit before any request is sent.
  const limitBounds = schemaConstraints.Detail.initial_timeline_entry_limit;
  let limitValid = $state(true);
  // Latest settings known to be persisted. Tracks the prop and advances on
  // each successful save so a queued save builds on the previous result even
  // before the parent has fed the new settings back through the prop.
  let saved = $derived(detail);
  // Every control saves on change. The checkboxes keep local checked state
  // so a failed save can flip them back to the persisted value.
  let collapseSingleLineBreaks = $derived(detail.collapse_single_line_breaks);
  let renderCommitMessagesAsMarkdown = $derived(detail.render_commit_messages_as_markdown);
  // Saves run one after another. Each builds its payload from the settings
  // known when it starts, so a checkbox click that lands while a limit save
  // is in flight neither clobbers that save nor gets dropped.
  let queue: Promise<void> = Promise.resolve();

  function validateLimit(input: HTMLInputElement): boolean {
    const limit = Number(input.value);
    limitValid =
      input.validity.valid
      && Number.isInteger(limit)
      && limit >= limitBounds.minimum
      && limit <= limitBounds.maximum;
    return limitValid;
  }

  function onLimitInput(event: Event): void {
    validateLimit(event.currentTarget as HTMLInputElement);
  }

  function saveLimit(event: Event): void {
    const input = event.currentTarget as HTMLInputElement;
    if (!validateLimit(input)) return;
    const limit = Number(input.value);
    persist((current) => (limit === current.initial_timeline_entry_limit ? null : { ...current, initial_timeline_entry_limit: limit }));
  }

  function toggleCollapseSingleLineBreaks(checked: boolean): void {
    persist((current) => (checked === current.collapse_single_line_breaks ? null : { ...current, collapse_single_line_breaks: checked }));
  }

  function toggleCommitMarkdown(checked: boolean): void {
    persist((current) =>
      checked === current.render_commit_messages_as_markdown ? null : { ...current, render_commit_messages_as_markdown: checked },
    );
  }

  function persist(build: (current: DetailSettingsType) => DetailSettingsType | null): void {
    if (embedded) return;
    queue = queue.then(() => {
      const pending = build(saved);
      if (pending === null) return;
      return save(pending);
    });
  }

  function save(pending: DetailSettingsType): Promise<void> {
    const previous = saved;
    return new Promise((resolve) => {
      runtime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* SettingsWorkflow;
          return yield* workflow.persist(() => ({ detail: pending }));
        }).pipe(
          Effect.matchEffect({
            onFailure: (failure) =>
              Effect.sync(() => {
                onUpdate(previous);
                collapseSingleLineBreaks = previous.collapse_single_line_breaks;
                renderCommitMessagesAsMarkdown = previous.render_commit_messages_as_markdown;
                showFlash(settingsErrorMessage(failure), { tone: "danger" });
              }),
            onSuccess: (settings) =>
              Effect.sync(() => {
                saved = settings.detail;
                onUpdate(settings.detail);
                settingsStore.setDetailSettings(settings.detail);
              }),
          }),
          Effect.ensuring(Effect.sync(resolve)),
        ),
        {
          operation: "save detail settings",
          safeContext: {},
          onFailure: () => {},
        },
      );
    });
  }
</script>

<SettingsOwnerNotice {owner} subject="Detail policy" />

<div class="setting-row">
  <div class="setting-copy">
    <label class="setting-label" for="initial-timeline-entry-limit">Initial timeline entries</label>
    <span class="setting-description">
      Additional entries remain available from the detail view.
    </span>
  </div>
  <div class="setting-control">
    <input
      id="initial-timeline-entry-limit"
      type="number"
      min={limitBounds.minimum}
      max={limitBounds.maximum}
      step="10"
      value={detail.initial_timeline_entry_limit}
      disabled={embedded}
      aria-invalid={!limitValid}
      aria-describedby={limitValid ? undefined : "initial-timeline-entry-limit-error"}
      oninput={onLimitInput}
      onchange={saveLimit}
    />
    {#if !limitValid}
      <span id="initial-timeline-entry-limit-error" class="setting-error" role="alert">
        Enter a whole number from {limitBounds.minimum} to {limitBounds.maximum}.
      </span>
    {/if}
  </div>
</div>

<Checkbox
  class="toggle-row"
  bind:checked={collapseSingleLineBreaks}
  disabled={embedded}
  onchange={toggleCollapseSingleLineBreaks}
  ariaLabel="Collapse single line breaks"
>
  <span class="setting-copy">
    <span class="setting-label">Collapse single line breaks</span>
    <span class="setting-description">
      Render markdown with soft line breaks: a single newline joins the paragraph
      and only a blank line starts a new one.
    </span>
  </span>
</Checkbox>

<Checkbox
  class="toggle-row"
  bind:checked={renderCommitMessagesAsMarkdown}
  disabled={embedded}
  onchange={toggleCommitMarkdown}
  ariaLabel="Render commit messages as markdown"
>
  <span class="setting-copy">
    <span class="setting-label">Render commit messages as markdown</span>
    <span class="setting-description">
      Show commit bodies in the timeline with the same markdown rendering as comments
      instead of plain text.
    </span>
  </span>
</Checkbox>

<style>
  :global(.toggle-row) { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--space-5); }
  :global(.toggle-row .kit-checkbox__box) { order: 2; margin-top: 2px; }
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .setting-control { display: flex; flex-direction: column; align-items: flex-end; gap: 4px; }
  .setting-error { color: var(--accent-red); font-size: var(--font-size-sm); }
  input { width: 5.5rem; height: 28px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-primary); color: var(--text-primary); font: inherit; font-size: var(--font-size-sm); padding: 0 8px; }
  input[aria-invalid="true"] { border-color: var(--accent-red); }
</style>
