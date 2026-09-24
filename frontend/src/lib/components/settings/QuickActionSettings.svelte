<script lang="ts">
  import { IconButton, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { Effect } from "effect";
  import type { LaunchTarget, QuickAction } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";

  interface Props {
    quickActions?: QuickAction[] | undefined;
    launchTargets?: LaunchTarget[];
    onUpdate: (quickActions: QuickAction[]) => void;
  }

  interface ActionDraft {
    id: string;
    label: string;
    agent: string;
    prompt: string;
  }

  let { quickActions, launchTargets = [], onUpdate }: Props = $props();

  const runtime = getAppRuntime();
  let nextID = 0;
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let drafts = $state<ActionDraft[]>(initialDrafts(quickActions ?? []));

  // Mirrors the server's initial-message limit so an oversized prompt is
  // caught here instead of by a failed save.
  const MAX_PROMPT_BYTES = 64 * 1024;
  const promptEncoder = new TextEncoder();

  const agentTargets = $derived(launchTargets.filter((target) => target.kind === "agent"));
  const savedActions = $derived(normalizeActions(quickActions ?? []));
  const serializedActions = $derived(serializeDrafts(drafts));
  const hasInvalidDraft = $derived(drafts.some((draft) => !isDraftValid(draft)));
  const hasDuplicateLabel = $derived(duplicateLabels(drafts).size > 0);
  const isDirty = $derived(JSON.stringify(serializedActions) !== JSON.stringify(savedActions));
  const canSave = $derived(!saving && isDirty && !hasInvalidDraft && !hasDuplicateLabel);

  function initialDrafts(configured: QuickAction[]): ActionDraft[] {
    return normalizeActions(configured).map((action) => ({
      id: `action:${nextID++}`,
      label: action.label,
      agent: action.agent,
      prompt: action.prompt,
    }));
  }

  function normalizeActions(configured: QuickAction[]): QuickAction[] {
    return configured.map((action) => ({
      label: action.label.trim(),
      agent: action.agent.trim().toLowerCase(),
      prompt: action.prompt,
    }));
  }

  function serializeDrafts(rows: ActionDraft[]): QuickAction[] {
    return rows.map((draft) => ({
      label: draft.label.trim(),
      agent: draft.agent.trim().toLowerCase(),
      prompt: draft.prompt,
    }));
  }

  function promptBytes(prompt: string): number {
    return promptEncoder.encode(prompt.replaceAll("\r\n", "\n").replaceAll("\r", "\n")).length;
  }

  function promptTooLong(draft: ActionDraft): boolean {
    return promptBytes(draft.prompt) > MAX_PROMPT_BYTES;
  }

  // Mirrors the server's printable-text rule: letters, marks, numbers,
  // punctuation, symbols, the ASCII space, and line breaks. Tabs and other
  // control characters are refused here instead of by a failed save.
  const UNPRINTABLE = /[^\p{L}\p{M}\p{N}\p{P}\p{S} \n]/u;

  function promptHasControlCharacters(draft: ActionDraft): boolean {
    return UNPRINTABLE.test(draft.prompt.replaceAll("\r\n", "\n").replaceAll("\r", "\n"));
  }

  function isDraftValid(draft: ActionDraft): boolean {
    return (
      draft.label.trim() !== ""
      && draft.agent.trim() !== ""
      && draft.prompt.trim() !== ""
      && !promptTooLong(draft)
      && !promptHasControlCharacters(draft)
    );
  }

  function duplicateLabels(rows: ActionDraft[]): Set<string> {
    const seen = new Set<string>();
    const duplicates = new Set<string>();
    for (const draft of rows) {
      const key = draft.label.trim().toLowerCase();
      if (key === "") continue;
      if (seen.has(key)) duplicates.add(key);
      seen.add(key);
    }
    return duplicates;
  }

  function agentOptions(draft: ActionDraft): TypeaheadOption[] {
    const options: TypeaheadOption[] = agentTargets.map((target) => ({
      name: target.key,
      label: target.label,
      ...(target.available ? {} : { meta: target.disabled_reason || "Not available" }),
    }));
    const current = draft.agent.trim().toLowerCase();
    if (current !== "" && !options.some((option) => option.name === current)) {
      options.unshift({
        name: current,
        label: current,
        meta: "Not a configured agent",
      });
    }
    return options.sort((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base" }));
  }

  function actionName(draft: ActionDraft): string {
    return draft.label.trim() || "New quick action";
  }

  function addAction(): void {
    drafts = [
      ...drafts,
      {
        id: `action:new:${nextID++}`,
        label: "",
        agent: agentTargets.find((target) => target.available)?.key ?? "",
        prompt: "",
      },
    ];
  }

  function removeAction(id: string): void {
    drafts = drafts.filter((draft) => draft.id !== id);
  }

  function save(): void {
    if (!canSave) return;
    const actionsToSave = serializedActions;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ quick_actions: actionsToSave }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              const next = settings.quick_actions ?? [];
              quickActions = next;
              drafts = initialDrafts(next);
              onUpdate(next);
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            saving = false;
          }),
        ),
      ),
      {
        operation: "save workspace quick actions",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }
</script>

<div class="quick-action-settings">
  <p class="intro">
    A quick action creates the workspace for the current pull request or issue,
    launches the chosen agent, and sends the prompt as its first message.
  </p>

  {#if drafts.length === 0}
    <p class="empty">No quick actions configured.</p>
  {:else}
    <div class="action-list">
      {#each drafts as draft (draft.id)}
        {@const duplicate = duplicateLabels(drafts).has(draft.label.trim().toLowerCase())}
        {@const oversized = promptTooLong(draft)}
        {@const unprintable = promptHasControlCharacters(draft)}
        <div class="action-row">
          <div class="action-fields">
            <label class="field field--label">
              <span>Label</span>
              <input
                type="text"
                bind:value={draft.label}
                aria-label="Quick action label"
                aria-invalid={duplicate || undefined}
                disabled={saving}
                placeholder="Rebase"
              />
            </label>
            <div class="field field--agent">
              <span>Agent</span>
              <Typeahead
                fallbackLabel="Select agent"
                placeholder="Search agents..."
                title="Quick action agent"
                value={draft.agent}
                options={agentOptions(draft)}
                disabled={saving || agentOptions(draft).length === 0}
                onselect={(value) => {
                  draft.agent = value;
                }}
              />
            </div>
            <div class="field field--remove">
              <span aria-hidden="true">&nbsp;</span>
              <IconButton
                size="sm"
                tone="danger"
                title="Remove"
                ariaLabel={`Remove ${actionName(draft)}`}
                disabled={saving}
                onclick={() => removeAction(draft.id)}
              >
                <TrashIcon size="13" strokeWidth="2" aria-hidden="true" />
              </IconButton>
            </div>
            <label class="field field--prompt">
              <span>Prompt</span>
              <textarea
                bind:value={draft.prompt}
                aria-label="Quick action prompt"
                aria-invalid={oversized || unprintable || undefined}
                disabled={saving}
                rows="3"
                placeholder="rebase this pull request onto main"
              ></textarea>
            </label>
            <!-- Errors live on their own grid row so the control row keeps a
                 uniform height and the remove button stays aligned. -->
            {#if duplicate || oversized || unprintable}
              <div class="field-errors" role="alert">
                {#if duplicate}
                  <span class="field-error">Labels must be unique.</span>
                {/if}
                {#if oversized}
                  <span class="field-error">Prompts must not exceed 64 KiB.</span>
                {/if}
                {#if unprintable}
                  <span class="field-error">Prompts may only contain printable text and line breaks.</span>
                {/if}
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <div class="settings-actions">
    <button class="add-btn" type="button" disabled={saving} onclick={addAction}>
      <PlusIcon size="13" strokeWidth="2" aria-hidden="true" />
      <span>Add quick action</span>
    </button>
    <button
      class="save-btn"
      type="button"
      aria-label="Save quick actions"
      disabled={!canSave}
      onclick={() => void save()}
    >
      {saving ? "Saving..." : "Save"}
    </button>
  </div>
</div>

<style>
  .quick-action-settings {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .intro,
  .empty {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .action-list {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .action-row {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    padding: 8px;
    border-top: 1px solid var(--border-muted);
  }

  .action-row:first-child {
    border-top: 0;
  }

  /* Explicit areas: the control row is label + agent + remove, then the
   * prompt, then any errors. Narrow layouts reflow by area, never by
   * auto-placement, so the remove button always shares the label's row. */
  .action-fields {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) minmax(140px, 1fr) auto;
    grid-template-areas:
      "label agent remove"
      "prompt prompt prompt"
      "errors errors errors";
    gap: 8px;
    align-items: start;
  }

  .field--label {
    grid-area: label;
  }

  .field--agent {
    grid-area: agent;
  }

  .field--remove {
    grid-area: remove;
  }

  .field-errors {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    grid-area: errors;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    min-width: 0;
  }

  .field--prompt {
    grid-area: prompt;
  }

  .field > span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
  }

  .field input,
  .field textarea {
    width: 100%;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  /* The agent picker is a kit control; size the label input to the same
   * control height so the two fields line up on one row. */
  .field input {
    box-sizing: border-box;
    height: var(--kit-control-height, 26px);
    padding: 0 8px;
  }

  .field :global(.kit-typeahead) {
    width: 100%;
  }

  .field textarea {
    resize: vertical;
  }

  .field-error {
    color: var(--accent-red);
    font-size: var(--font-size-xs);
    text-transform: none;
  }

  .settings-actions {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .add-btn,
  .save-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 28px;
    padding: 5px 10px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    font-weight: 500;
  }

  .add-btn {
    color: var(--text-secondary);
    border: 1px solid var(--border-muted);
  }

  .add-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .save-btn {
    margin-left: auto;
    color: white;
    background: var(--accent-blue);
  }

  .save-btn:hover:enabled {
    opacity: 0.9;
  }

  .save-btn:disabled,
  .add-btn:disabled {
    opacity: var(--opacity-disabled);
    cursor: not-allowed;
  }

  @media (max-width: 900px) {
    .action-fields {
      grid-template-columns: 1fr auto;
      grid-template-areas:
        "label remove"
        "agent agent"
        "prompt prompt"
        "errors errors";
    }
  }
</style>
