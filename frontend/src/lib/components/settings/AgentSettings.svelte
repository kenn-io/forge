<script lang="ts">
  import { IconButton } from "@kenn-io/kit-ui";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import type { AgentSettings as AgentSettingsType } from "@middleman/ui/api/types";
  import { showFlash } from "@middleman/ui/stores/flash";
  import { slide } from "svelte/transition";
  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface Props {
    agents: AgentSettingsType[];
    onUpdate: (agents: AgentSettingsType[]) => void;
  }

  interface BuiltinAgent {
    key: string;
    label: string;
    binary: string;
  }

  interface AgentDraft {
    id: string;
    builtin: boolean;
    key: string;
    label: string;
    binary: string;
    args: string;
    enabled: boolean;
    expanded: boolean;
  }

  const builtins: BuiltinAgent[] = [
    { key: "codex", label: "Codex", binary: "codex" },
    { key: "claude", label: "Claude", binary: "claude" },
    { key: "gemini", label: "Gemini", binary: "gemini" },
    { key: "opencode", label: "opencode", binary: "opencode" },
    { key: "aider", label: "aider", binary: "aider" },
  ];

  let { agents, onUpdate }: Props = $props();

  const embedded = isEmbedded();
  let customID = 0;
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let drafts = $state<AgentDraft[]>(initialDrafts(agents));

  const savedAgents = $derived(normalizeAgents(agents));
  const preservedDefaultBuiltinKeys = $derived(defaultBuiltinKeys(savedAgents));
  const serializedAgents = $derived(
    serializeDrafts(drafts, preservedDefaultBuiltinKeys),
  );
  const hasInvalidDraft = $derived(drafts.some((draft) => !isDraftValid(draft)));
  const isDirty = $derived(
    JSON.stringify(serializedAgents) !== JSON.stringify(savedAgents),
  );
  const canSave = $derived(
    !embedded && !saving && isDirty && !hasInvalidDraft,
  );

  function initialDrafts(configured: AgentSettingsType[]): AgentDraft[] {
    const byKey: Record<string, AgentSettingsType | undefined> = {};
    for (const agent of normalizeAgents(configured)) {
      byKey[agent.key] = agent;
    }
    const rows = builtins.map((builtin) => {
      const configuredAgent = byKey[builtin.key];
      delete byKey[builtin.key];
      return draftFromAgent(builtin, configuredAgent);
    });
    for (const agent of Object.values(byKey)) {
      if (!agent) continue;
      rows.push(draftFromAgent(null, agent));
    }
    return rows;
  }

  function draftFromAgent(
    builtin: BuiltinAgent | null,
    agent: AgentSettingsType | undefined,
  ): AgentDraft {
    const command = agent?.command ?? [];
    const key = builtin?.key ?? agent?.key ?? "";
    const label = agent?.label ?? builtin?.label ?? key;
    const binary =
      command[0] ?? (agent?.enabled === false ? "" : builtin?.binary ?? "");
    return {
      id: builtin ? `builtin:${builtin.key}` : `custom:${key}:${customID++}`,
      builtin: builtin !== null,
      key,
      label,
      binary,
      args: stringifyArgs(command.slice(1)),
      enabled: agent?.enabled ?? true,
      expanded: builtin === null && agent === undefined,
    };
  }

  function normalizeAgents(configured: AgentSettingsType[]): AgentSettingsType[] {
    return configured
      .map((agent) => ({
        key: agent.key.trim().toLowerCase(),
        label: agent.label.trim(),
        command: [...(agent.command ?? [])],
        enabled: agent.enabled ?? true,
      }))
      .filter((agent) => agent.key !== "")
      .sort((left, right) => left.key.localeCompare(right.key));
  }

  function defaultBuiltinKeys(configured: AgentSettingsType[]): Set<string> {
    return new Set(
      configured
        .filter((agent) => isDefaultBuiltinAgent(agent))
        .map((agent) => agent.key),
    );
  }

  function isDefaultBuiltinAgent(agent: AgentSettingsType): boolean {
    const builtin = builtins.find((candidate) => candidate.key === agent.key);
    if (!builtin) return false;
    if (!agent.enabled || agent.label !== builtin.label) return false;
    const command = agent.command ?? [];
    return (
      command.length === 0 ||
      (command.length === 1 && command[0] === builtin.binary)
    );
  }

  function serializeDrafts(
    rows: AgentDraft[],
    preservedDefaultKeys = new Set<string>(),
  ): AgentSettingsType[] {
    const agentsToSave: AgentSettingsType[] = [];
    for (const draft of rows) {
      const key = draft.key.trim().toLowerCase();
      if (key === "") continue;
      const builtin = builtins.find((candidate) => candidate.key === key);
      const enabled = draft.enabled;
      const binary = draft.binary.trim();
      const args = parseArgs(draft.args);
      const label = (draft.label.trim() || builtin?.label || key);
      const command = binary === "" ? [] : [binary, ...args];

      if (draft.builtin && builtin) {
        const isDefault =
          enabled &&
          label === builtin.label &&
          binary === builtin.binary &&
          args.length === 0;
        if (isDefault && !preservedDefaultKeys.has(key)) continue;
      }

      agentsToSave.push({
        key,
        label,
        command,
        enabled,
      });
    }
    return normalizeAgents(agentsToSave);
  }

  function isDraftValid(draft: AgentDraft): boolean {
    const key = draft.key.trim();
    if (!draft.builtin && key === "") return false;
    if (draft.enabled && draft.binary.trim() === "") return false;
    return true;
  }

  function agentName(draft: AgentDraft): string {
    return draft.label.trim() || draft.key.trim() || "Custom agent";
  }

  function toggleExpanded(draft: AgentDraft): void {
    draft.expanded = !draft.expanded;
  }

  function addCustomAgent(): void {
    drafts = [
      ...drafts,
      {
        id: `custom:new:${customID++}`,
        builtin: false,
        key: "",
        label: "",
        binary: "",
        args: "",
        enabled: true,
        expanded: true,
      },
    ];
  }

  function removeCustomAgent(id: string): void {
    drafts = drafts.filter((draft) => draft.id !== id);
  }

  function resetBuiltin(draft: AgentDraft): void {
    const builtin = builtins.find((candidate) => candidate.key === draft.key);
    if (!builtin) return;
    draft.label = builtin.label;
    draft.binary = builtin.binary;
    draft.args = "";
    draft.enabled = true;
    draft.expanded = true;
  }

  async function save(): Promise<void> {
    if (!canSave) return;
    saving = true;
    try {
      const settings = await updateSettings({ agents: serializedAgents });
      const nextAgents = settings.agents ?? [];
      agents = nextAgents;
      drafts = initialDrafts(nextAgents);
      onUpdate(nextAgents);
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      saving = false;
    }
  }

  function stringifyArgs(args: string[]): string {
    return args.map(quoteArg).join(" ");
  }

  function quoteArg(arg: string): string {
    if (arg === "") return "\"\"";
    if (!/\s|["'\\]/.test(arg)) return arg;
    return `"${arg.replace(/(["\\])/g, "\\$1")}"`;
  }

  function parseArgs(input: string): string[] {
    const args: string[] = [];
    let current = "";
    let quote: "\"" | "'" | null = null;
    let escaping = false;
    let tokenStarted = false;

    for (const char of input.trim()) {
      if (escaping) {
        current += char;
        escaping = false;
        tokenStarted = true;
        continue;
      }
      if (char === "\\" && quote !== "'") {
        escaping = true;
        tokenStarted = true;
        continue;
      }
      if ((char === "\"" || char === "'") && quote === null) {
        quote = char;
        tokenStarted = true;
        continue;
      }
      if (char === quote) {
        quote = null;
        continue;
      }
      if (/\s/.test(char) && quote === null) {
        if (tokenStarted) {
          args.push(current);
          current = "";
          tokenStarted = false;
        }
        continue;
      }
      current += char;
      tokenStarted = true;
    }
    if (escaping) current += "\\";
    if (tokenStarted) args.push(current);
    return args;
  }
</script>

<div class="agent-settings">
  <div class="agent-list">
    {#each drafts as draft (draft.id)}
      <div class={["agent-row", !draft.builtin && "agent-row--custom"]}>
        <div class="agent-row-header">
          <label class="enable-field">
            <input type="checkbox" bind:checked={draft.enabled} disabled={saving} />
            <span>{agentName(draft)}</span>
          </label>

          <div class="row-actions">
            {#if draft.builtin && draft.expanded}
              <IconButton
                size="sm"
                title="Reset"
                ariaLabel={`Reset ${agentName(draft)}`}
                disabled={saving}
                onclick={() => resetBuiltin(draft)}
              >
                <RotateCcwIcon size="13" strokeWidth="2" aria-hidden="true" />
              </IconButton>
            {:else if !draft.builtin && draft.expanded}
              <IconButton
                size="sm"
                tone="danger"
                title="Remove"
                ariaLabel={`Remove ${agentName(draft)}`}
                disabled={saving}
                onclick={() => removeCustomAgent(draft.id)}
              >
                <TrashIcon size="13" strokeWidth="2" aria-hidden="true" />
              </IconButton>
            {/if}

            <IconButton
              size="sm"
              title={draft.expanded ? "Collapse" : "Edit"}
              ariaLabel={`${draft.expanded ? "Collapse" : "Edit"} ${agentName(draft)}`}
              disabled={saving}
              onclick={() => toggleExpanded(draft)}
            >
              <span class={["chevron-icon", draft.expanded && "chevron-icon--expanded"]}>
                <ChevronRightIcon size="13" strokeWidth="2" aria-hidden="true" />
              </span>
            </IconButton>
          </div>
        </div>

        {#if draft.expanded}
          <div
            class={["agent-fields", !draft.builtin && "agent-fields--custom"]}
            transition:slide={{ duration: 120 }}
          >
            {#if !draft.builtin}
              <label class="field">
                <span>Key</span>
                <input
                  type="text"
                  bind:value={draft.key}
                  aria-label="Custom agent key"
                  disabled={saving}
                  placeholder="review"
                />
              </label>
              <label class="field">
                <span>Label</span>
                <input
                  type="text"
                  bind:value={draft.label}
                  aria-label="Custom agent label"
                  disabled={saving}
                  placeholder="Review Agent"
                />
              </label>
            {/if}

            <label class="field">
              <span>Binary</span>
              <input
                type="text"
                bind:value={draft.binary}
                aria-label={`${agentName(draft)} binary`}
                disabled={saving || !draft.enabled}
                placeholder={draft.key || "agent"}
              />
            </label>

            <label class="field field--args">
              <span>Arguments</span>
              <input
                type="text"
                bind:value={draft.args}
                aria-label={`${agentName(draft)} arguments`}
                disabled={saving || !draft.enabled}
                placeholder="--flag value"
              />
            </label>
          </div>
        {/if}
      </div>
    {/each}
  </div>


  <div class="settings-actions">
    <button
      class="add-btn"
      type="button"
      disabled={saving}
      onclick={addCustomAgent}
    >
      <PlusIcon size="13" strokeWidth="2" aria-hidden="true" />
      <span>Add custom agent</span>
    </button>
    <button
      class="save-btn"
      type="button"
      aria-label="Save workspace agents"
      disabled={!canSave}
      onclick={() => void save()}
    >
      {saving ? "Saving..." : "Save"}
    </button>
  </div>
</div>

<style>
  .agent-settings {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .agent-list {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .agent-row {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding: 8px;
    border-top: 1px solid var(--border-muted);
    background: transparent;
  }

  .agent-row:first-child {
    border-top: 0;
  }

  .agent-row-header,
  .row-actions {
    display: flex;
    align-items: center;
  }

  .agent-row-header {
    justify-content: space-between;
    gap: 12px;
    min-height: 24px;
  }

  .agent-fields {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) minmax(150px, 1.2fr);
    gap: 8px;
    align-items: end;
  }

  .agent-fields--custom {
    grid-template-columns:
      minmax(88px, 0.8fr) minmax(72px, 0.7fr) minmax(96px, 0.9fr)
      minmax(96px, 1fr) minmax(128px, 1.2fr);
  }

  .enable-field,
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    min-width: 0;
  }

  .enable-field {
    align-self: center;
    flex-direction: row;
    align-items: center;
    flex: 1 1 auto;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .field span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
  }

  .field input {
    width: 100%;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  .row-actions {
    flex: 0 0 auto;
    justify-content: flex-end;
    gap: 6px;
  }

  .chevron-icon {
    display: inline-flex;
    transition: transform 120ms ease-out;
  }

  .chevron-icon--expanded {
    transform: rotate(90deg);
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

  .save-btn:hover:not(:disabled) {
    opacity: 0.9;
  }

  .save-btn:disabled,
  .add-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  @media (max-width: 900px) {
    .agent-fields,
    .agent-fields--custom {
      grid-template-columns: 1fr;
      align-items: stretch;
    }
  }
</style>
