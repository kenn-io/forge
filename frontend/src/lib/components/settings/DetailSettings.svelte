<script lang="ts">
  import { Effect } from "effect";
  import { Checkbox, NumberInput } from "@kenn-io/kit-ui";
  import type { DetailSettings as DetailSettingsType } from "../../api/types.js";
  import { schemaConstraints } from "../../api/generated/schema-constraints.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
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
  // The server's bounds come from the OpenAPI schema, so the input can reject
  // an out-of-range limit before any request is sent.
  const limitBounds = schemaConstraints.Detail.initial_timeline_entry_limit;
  // NumberInput clamps to min and max when the field is left. An out-of-range
  // limit must stay on screen and must not be saved as the nearest bound.
  // The draft applies only while the saved limit is unchanged, so a later
  // settings update replaces it.
  let limitDraft = $state<{ from: number; value: number | null } | null>(null);

  function wholeLimit(value: number | null): value is number {
    return value !== null
      && Number.isInteger(value)
      && value >= limitBounds.minimum
      && value <= limitBounds.maximum;
  }

  const limitValue = $derived(
    limitDraft !== null && limitDraft.from === detail.initial_timeline_entry_limit
      ? limitDraft.value
      : detail.initial_timeline_entry_limit,
  );
  const limitValid = $derived(wholeLimit(limitValue));
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

  function onLimitInput(value: number | null): void {
    limitDraft = { from: detail.initial_timeline_entry_limit, value };
  }

  function saveLimit(value: number | null): void {
    if (!wholeLimit(value)) return;
    const limit = value;
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
                limitDraft = null;
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
    <div class="number-field">
      <NumberInput
        id="initial-timeline-entry-limit"
        block
        step={10}
        value={limitValue}
        invalid={!limitValid}
        {...(limitValid ? {} : { ariaDescribedby: "initial-timeline-entry-limit-error" })}
        oninput={onLimitInput}
        onchange={saveLimit}
      />
    </div>
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

  onchange={toggleCollapseSingleLineBreaks}
  ariaLabel="Collapse single line breaks"
>
  <span class="setting-copy">
    <span class="setting-label">Collapse single line breaks</span>
    <span class="setting-description">
      Applies to comments and markdown commit bodies. Descriptions always reflow
      paragraphs to fit the pane.
    </span>
  </span>
</Checkbox>

<Checkbox
  class="toggle-row"
  bind:checked={renderCommitMessagesAsMarkdown}

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
  .number-field { width: 5.5rem; }
</style>
