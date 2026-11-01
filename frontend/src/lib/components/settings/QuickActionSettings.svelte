<script lang="ts">
  import { Button, Chip, HarnessIcon, TextInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import SparklesIcon from "@lucide/svelte/icons/sparkles";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { Effect } from "effect";
  import type { ACPConfigOption } from "../../api/generated/models/index.js";
  import type { LaunchTarget, QuickAction } from "../../api/types.js";
  import { harnessForAgentKey } from "../terminal/agentHarness.js";
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
    acpOptions: Record<string, string>;
  }

  // The options an ACP agent offered for one action, read by starting the
  // agent once on this host. `request` names the agent and choices they were
  // read for, so a stale reply never replaces a newer one.
  interface OptionsProbe {
    request: string;
    loading: boolean;
    options: ACPConfigOption[];
    error: string;
  }

  let { quickActions, launchTargets = [], onUpdate }: Props = $props();

  const runtime = getAppRuntime();
  let nextID = 0;
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let drafts = $state<ActionDraft[]>(initialDrafts(quickActions ?? []));
  let probes = $state<Record<string, OptionsProbe>>({});
  let openOptions = $state<Record<string, boolean>>({});
  // Saved actions start collapsed to one summary line; editing happens in
  // the expanded row.
  let expanded = $state<Record<string, boolean>>({});
  let focusLabelID = $state("");

  // Mirrors the server's initial-message limit so an oversized prompt is
  // caught here instead of by a failed save.
  const MAX_PROMPT_BYTES = 64 * 1024;
  const promptEncoder = new TextEncoder();

  const agentTargets = $derived(launchTargets.filter((target) => (target.kind === "agent" || target.kind === "acp")));
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
      acpOptions: { ...action.acp_options },
    }));
  }

  function normalizeActions(configured: QuickAction[]): QuickAction[] {
    return configured.map((action) => ({
      label: action.label.trim(),
      agent: action.agent.trim().toLowerCase(),
      prompt: action.prompt,
      ...withACPOptions(action.acp_options),
    }));
  }

  // Sorted so an unchanged set of options never reads as an edit.
  function withACPOptions(options: Record<string, string> | undefined): Pick<QuickAction, "acp_options"> {
    const entries = Object.entries(options ?? {}).toSorted(([a], [b]) => a.localeCompare(b));
    return entries.length === 0 ? {} : { acp_options: Object.fromEntries(entries) };
  }

  function serializeDrafts(rows: ActionDraft[]): QuickAction[] {
    return rows.map((draft) => ({
      label: draft.label.trim(),
      agent: draft.agent.trim().toLowerCase(),
      prompt: draft.prompt,
      ...withACPOptions(draft.acpOptions),
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

  // Names the required fields a row still lacks, so a disabled Save always
  // has a visible reason.
  function missingFields(draft: ActionDraft): string[] {
    return [
      ...(draft.label.trim() === "" ? ["label"] : []),
      ...(draft.agent.trim() === "" ? ["agent"] : []),
      ...(draft.prompt.trim() === "" ? ["prompt"] : []),
    ];
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

  function isACPAgent(agent: string): boolean {
    const key = agent.trim().toLowerCase();
    return launchTargets.some((target) => target.key === key && target.kind === "acp");
  }

  function chooseAgent(draft: ActionDraft, agent: string): void {
    if (agent === draft.agent) return;
    draft.agent = agent;
    // Options belong to one agent.
    draft.acpOptions = {};
    delete probes[draft.id];
    openOptions[draft.id] = false;
  }

  function loadOptions(draft: ActionDraft): void {
    const id = draft.id;
    const targetKey = draft.agent.trim().toLowerCase();
    const choices = $state.snapshot(draft.acpOptions);
    const request = JSON.stringify([targetKey, withACPOptions(choices)]);
    const previous = probes[id];
    if (previous?.request === request && previous.error === "") return;
    // Earlier options stay choosable while the agent is read again.
    probes[id] = { request, loading: true, options: previous?.options ?? [], error: "" };
    const settle = (options: ACPConfigOption[] | undefined, error: string) => {
      const current = probes[id];
      if (current?.request !== request) return;
      probes[id] = { request, loading: false, options: options ?? current.options, error };
    };
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.probeACPOptions(targetKey, choices);
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) => Effect.sync(() => settle(undefined, settingsErrorMessage(failure))),
          onSuccess: (result) => Effect.sync(() => settle(result.options, "")),
        }),
      ),
      {
        operation: "read ACP agent options",
        safeContext: { agent: targetKey },
        onFailure: () => settle(undefined, "Could not read the agent's options."),
      },
    );
  }

  function toggleOptions(draft: ActionDraft): void {
    const open = !openOptions[draft.id];
    openOptions[draft.id] = open;
    if (open) loadOptions(draft);
  }

  function chooseOption(draft: ActionDraft, option: ACPConfigOption, value: string): void {
    const { [option.id]: _previous, ...rest } = draft.acpOptions;
    draft.acpOptions = value === "" ? rest : { ...rest, [option.id]: value };
    // A model change can replace the effort levels and other options.
    if (option.category === "model") loadOptions(draft);
  }

  function clearOption(draft: ActionDraft, id: string): void {
    const { [id]: _previous, ...rest } = draft.acpOptions;
    draft.acpOptions = rest;
  }

  function choiceOptions(option: ACPConfigOption): TypeaheadOption[] {
    return option.options.flatMap((choice): TypeaheadOption[] => {
      if (choice.group) {
        return (choice.options ?? []).flatMap((item) =>
          item.value === undefined ? [] : [{ name: item.value, label: item.name, meta: choice.name }],
        );
      }
      return choice.value === undefined ? [] : [{ name: choice.value, label: choice.name }];
    });
  }

  function optionFallback(draft: ActionDraft, option: ACPConfigOption): string {
    const value = draft.acpOptions[option.id];
    return value === undefined ? "Remembered choice" : `${value} (not offered)`;
  }

  // Saved choices for options the agent did not report this time.
  function unreportedOptions(draft: ActionDraft, probe: OptionsProbe): [string, string][] {
    if (probe.loading || probe.options.length === 0) return [];
    return Object.entries(draft.acpOptions).filter(([id]) => !probe.options.some((option) => option.id === id));
  }

  function optionsSummary(draft: ActionDraft): string {
    const entries = Object.entries(draft.acpOptions).toSorted(([a], [b]) => a.localeCompare(b));
    if (entries.length === 0) return "Remembered choices";
    return entries.map(([id, value]) => `${id}: ${value}`).join(", ");
  }

  function actionName(draft: ActionDraft): string {
    return draft.label.trim() || "New quick action";
  }

  function agentName(draft: ActionDraft): string {
    const key = draft.agent.trim().toLowerCase();
    return agentTargets.find((target) => target.key === key)?.label ?? key;
  }

  function promptPreview(draft: ActionDraft): string {
    return draft.prompt.split(/\r?\n|\r/).map((line) => line.trim()).find((line) => line !== "") ?? "";
  }

  function toggleExpanded(id: string): void {
    expanded[id] = !expanded[id];
  }

  function addAction(): void {
    const id = `action:new:${nextID++}`;
    expanded[id] = true;
    focusLabelID = id;
    drafts = [
      ...drafts,
      {
        id,
        label: "",
        agent: agentTargets.find((target) => target.available)?.key ?? "",
        prompt: "",
        acpOptions: {},
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
              probes = {};
              openOptions = {};
              expanded = {};
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
    <ul class="action-list">
      {#each drafts as draft (draft.id)}
        {@const duplicate = duplicateLabels(drafts).has(draft.label.trim().toLowerCase())}
        {@const oversized = promptTooLong(draft)}
        {@const unprintable = promptHasControlCharacters(draft)}
        {@const missing = missingFields(draft)}
        {@const invalid = duplicate || oversized || unprintable || missing.length > 0}
        {@const open = expanded[draft.id] === true}
        {@const editorID = `${draft.id}:editor`}
        {@const harness = harnessForAgentKey(draft.agent)}
        {@const optionCount = Object.keys(draft.acpOptions).length}
        {@const preview = promptPreview(draft)}
        <li class="action-row" class:action-row--open={open}>
          <button
            class="action-summary"
            type="button"
            aria-expanded={open}
            aria-controls={editorID}
            onclick={() => toggleExpanded(draft.id)}
          >
            <ChevronRightIcon class="action-chevron" size="14" strokeWidth="2" aria-hidden="true" />
            <span class="summary-label" class:summary-label--empty={draft.label.trim() === ""}>
              {actionName(draft)}
            </span>
            <span class="summary-agent">
              {#if draft.agent.trim() === ""}
                <span class="summary-placeholder">No agent</span>
              {:else}
                {#if harness}
                  <HarnessIcon {harness} size={13} decorative />
                {:else}
                  <SparklesIcon size={13} aria-hidden="true" />
                {/if}
                <span class="summary-agent-name">{agentName(draft)}</span>
              {/if}
            </span>
            <span class="summary-prompt">
              {#if preview === ""}
                <span class="summary-placeholder">No prompt</span>
              {:else}
                {preview}
              {/if}
            </span>
            <span class="summary-meta">
              {#if optionCount > 0}
                <Chip size="xs" tone="muted" uppercase={false} title={optionsSummary(draft)}>
                  {optionCount === 1 ? "1 option" : `${optionCount} options`}
                </Chip>
              {/if}
              {#if invalid}
                <span class="summary-problem">
                  <CircleAlertIcon size="13" strokeWidth="2" aria-hidden="true" />
                  Needs fixes
                </span>
              {/if}
            </span>
          </button>

          {#if open}
            <div class="action-editor" id={editorID}>
              <div class="editor-fields">
                <label class="field field--label">
                  <span class="field-label">Label</span>
                  <TextInput
                    block
                    bind:value={draft.label}
                    ariaLabel="Quick action label"
                    invalid={duplicate || missing.includes("label")}
                    disabled={saving}
                    autofocus={focusLabelID === draft.id}
                    placeholder="rebase"
                  />
                </label>
                <div class="field field--agent">
                  <span class="field-label">Agent</span>
                  <Typeahead
                    fallbackLabel="Select agent"
                    placeholder="Search agents..."
                    title="Quick action agent"
                    value={draft.agent}
                    options={agentOptions(draft)}
                    disabled={saving || agentOptions(draft).length === 0}
                    onselect={(value) => chooseAgent(draft, value)}
                  >
                    {#snippet icon(option)}
                      {@const optionHarness = harnessForAgentKey(option.name)}
                      {#if optionHarness}
                        <HarnessIcon harness={optionHarness} size={14} decorative />
                      {:else}
                        <SparklesIcon size={14} aria-hidden="true" />
                      {/if}
                    {/snippet}
                  </Typeahead>
                </div>
                <label class="field field--prompt">
                  <span class="field-label">Prompt</span>
                  <textarea
                    bind:value={draft.prompt}
                    aria-label="Quick action prompt"
                    aria-invalid={oversized || unprintable || missing.includes("prompt") || undefined}
                    disabled={saving}
                    rows="4"
                    placeholder="rebase this pull request onto main"
                  ></textarea>
                </label>
              </div>

              {#if invalid}
                <div class="field-errors" role="alert">
                  {#if missing.includes("label")}
                    <span class="field-error">A label is required.</span>
                  {/if}
                  {#if missing.includes("agent")}
                    <span class="field-error">An agent is required.</span>
                  {/if}
                  {#if missing.includes("prompt")}
                    <span class="field-error">A prompt is required.</span>
                  {/if}
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

              {#if isACPAgent(draft.agent) || optionCount > 0}
                {@const probe = probes[draft.id]}
                <div class="acp-options">
                  <button
                    class="acp-options-toggle"
                    type="button"
                    aria-expanded={openOptions[draft.id] === true}
                    disabled={saving}
                    onclick={() => toggleOptions(draft)}
                  >
                    <ChevronRightIcon size="13" strokeWidth="2" aria-hidden="true" />
                    <span class="acp-options-title">Agent options</span>
                    <span class="acp-options-summary">{optionsSummary(draft)}</span>
                  </button>
                  {#if openOptions[draft.id] && probe}
                    <div class="acp-options-body">
                      {#if probe.loading && probe.options.length === 0}
                        <p class="acp-options-note">Starting the agent to read its options...</p>
                      {:else if probe.options.length === 0 && probe.error === ""}
                        <p class="acp-options-note">This agent offers no options.</p>
                      {/if}
                      {#each probe.options as option (option.id)}
                        <div class="field acp-option">
                          <span class="field-label">{option.name || option.id}</span>
                          <Typeahead
                            title={`${option.name || option.id} for ${actionName(draft)}`}
                            fallbackLabel={optionFallback(draft, option)}
                            placeholder={`Search ${(option.name || option.id).toLowerCase()}...`}
                            value={draft.acpOptions[option.id] ?? ""}
                            options={choiceOptions(option)}
                            allowClear
                            clearLabel="Remembered choice"
                            loading={probe.loading && option.category !== "model"}
                            disabled={saving}
                            onselect={(value) => chooseOption(draft, option, value)}
                          />
                        </div>
                      {/each}
                      {#each unreportedOptions(draft, probe) as [id, value] (id)}
                        <div class="acp-option-unreported">
                          <span>{id}: {value} is not offered by this agent.</span>
                          <button type="button" class="text-action" disabled={saving} onclick={() => clearOption(draft, id)}>
                            Clear
                          </button>
                        </div>
                      {/each}
                      {#if probe.error !== ""}
                        <div class="field-errors" role="alert">
                          <span class="field-error">{probe.error}</span>
                          <button type="button" class="text-action" disabled={saving} onclick={() => loadOptions(draft)}>
                            Try again
                          </button>
                        </div>
                      {/if}
                    </div>
                  {/if}
                </div>
              {/if}

              <div class="editor-footer">
                <Button
                  size="sm"
                  tone="danger"
                  surface="soft"
                  ariaLabel={`Remove ${actionName(draft)}`}
                  disabled={saving}
                  onclick={() => removeAction(draft.id)}
                >
                  <TrashIcon size="12" strokeWidth="2" aria-hidden="true" />
                  Remove
                </Button>
              </div>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  <div class="settings-actions">
    <Button size="sm" disabled={saving} onclick={addAction}>
      <PlusIcon size="13" strokeWidth="2" aria-hidden="true" />
      Add quick action
    </Button>
    <Button
      class="save-button"
      size="sm"
      tone="info"
      surface="solid"
      ariaLabel="Save quick actions"
      busy={saving}
      disabled={!canSave}
      onclick={save}
    >
      Save
    </Button>
  </div>
</div>

<style>
  .quick-action-settings {
    /* Shared by the collapsed summary and the editor so the editor's fields
     * start under the label column. */
    --qa-row-pad-x: var(--space-5);
    --qa-chevron: 14px;
    --qa-gap: var(--space-4);

    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .intro,
  .empty {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .empty {
    padding: var(--space-6) var(--space-5);
    border: 1px dashed var(--border-muted);
    border-radius: var(--radius-md);
    text-align: center;
  }

  .action-list {
    margin: 0;
    padding: 0;
    overflow: hidden;
    list-style: none;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
  }

  .action-row + .action-row {
    border-top: 1px solid var(--border-muted);
  }

  .action-summary {
    display: grid;
    grid-template-columns:
      var(--qa-chevron)
      minmax(0, 11rem)
      minmax(0, 9rem)
      minmax(0, 1fr)
      auto;
    gap: var(--qa-gap);
    align-items: center;
    width: 100%;
    min-height: 36px;
    padding: var(--space-3) var(--qa-row-pad-x);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: 0;
    cursor: pointer;
    transition: background-color 0.12s ease;
  }

  .action-summary:hover {
    background: var(--bg-surface-hover);
  }

  .action-summary:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .action-row--open .action-summary {
    background: var(--bg-inset);
  }

  .action-summary :global(.action-chevron) {
    color: var(--text-muted);
    transition: transform 0.12s ease;
  }

  .action-row--open .action-summary :global(.action-chevron) {
    transform: rotate(90deg);
  }

  .summary-label,
  .summary-agent-name,
  .summary-prompt {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .summary-label {
    font-weight: 600;
  }

  .summary-label--empty {
    color: var(--text-muted);
    font-weight: 500;
    font-style: italic;
  }

  .summary-agent {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
    color: var(--text-secondary);
  }

  .summary-agent :global(svg),
  .summary-agent :global(.kit-harness-icon) {
    flex: none;
  }

  .summary-prompt {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .summary-placeholder {
    color: var(--text-muted);
    font-family: var(--font-sans, inherit);
    font-style: italic;
  }

  .summary-meta {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--space-4);
  }

  .summary-meta:empty {
    display: none;
  }

  .summary-problem {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
    font-weight: 500;
    white-space: nowrap;
  }

  .action-editor {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    padding:
      var(--space-5)
      var(--qa-row-pad-x)
      var(--space-5)
      calc(var(--qa-row-pad-x) + var(--qa-chevron) + var(--qa-gap));
    border-top: 1px solid var(--border-muted);
  }

  .editor-fields {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: var(--space-5);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    min-width: 0;
  }

  .field--prompt {
    grid-column: 1 / -1;
  }

  .field-label {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 500;
  }

  /* Match the kit Typeahead to TextInput's md height so the label and
   * agent controls share one baseline. */
  .field--agent,
  .acp-option {
    --typeahead-control-height: 28px;
  }

  .field :global(.kit-typeahead) {
    width: 100%;
  }

  .field textarea {
    box-sizing: border-box;
    width: 100%;
    min-width: 0;
    min-height: 5.5em;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    line-height: 1.5;
    resize: vertical;
  }

  .field textarea[aria-invalid="true"] {
    border-color: var(--accent-red);
  }

  .field-errors {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--space-1);
  }

  .field-error {
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .acp-options {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .acp-options-toggle {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    align-self: flex-start;
    max-width: 100%;
    min-width: 0;
    padding: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    text-align: left;
    background: transparent;
    border: 0;
  }

  .acp-options-toggle:hover:enabled {
    color: var(--text-primary);
  }

  .acp-options-toggle :global(svg) {
    flex: none;
    transition: transform 0.12s ease;
  }

  .acp-options-toggle[aria-expanded="true"] :global(svg) {
    transform: rotate(90deg);
  }

  .acp-options-title {
    font-weight: 500;
  }

  .acp-options-summary {
    overflow: hidden;
    color: var(--text-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .acp-options-body {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: var(--space-4) var(--space-5);
    padding-left: calc(13px + var(--space-3));
  }

  .acp-options-note,
  .acp-option-unreported,
  .acp-options-body .field-errors {
    grid-column: 1 / -1;
  }

  .acp-options-note {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .acp-option-unreported {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    color: var(--accent-amber);
    font-size: var(--font-size-xs);
  }

  .text-action {
    padding: 0;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    background: transparent;
    border: 0;
  }

  .text-action:hover:enabled {
    text-decoration: underline;
    text-underline-offset: 2px;
  }

  .editor-footer {
    display: flex;
    justify-content: flex-end;
  }

  .settings-actions {
    display: flex;
    align-items: center;
    gap: var(--space-5);
  }

  .settings-actions :global(.save-button) {
    margin-left: auto;
  }

  @media (max-width: 720px) {
    .action-summary {
      grid-template-columns: var(--qa-chevron) minmax(0, 1fr) auto;
      grid-template-areas:
        "chevron label agent"
        ". prompt prompt"
        ". meta meta";
      row-gap: var(--space-1);
    }

    .action-summary :global(.action-chevron) {
      grid-area: chevron;
    }

    .summary-label {
      grid-area: label;
    }

    .summary-agent {
      grid-area: agent;
      justify-content: flex-end;
    }

    .summary-prompt {
      grid-area: prompt;
    }

    .summary-meta {
      grid-area: meta;
      justify-content: flex-start;
    }

    .action-editor {
      padding-left: var(--qa-row-pad-x);
    }

    .editor-fields {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
