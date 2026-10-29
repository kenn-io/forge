<script lang="ts">
  import { Button, Checkbox, IconButton, SearchInput, SelectDropdown } from "@kenn-io/kit-ui";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { Effect } from "effect";
  import type { AgentSettings as AgentSettingsType, LaunchTarget } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import LaunchTargetName from "../terminal/LaunchTargetName.svelte";
  import { slide } from "svelte/transition";

  interface Props {
    agents: AgentSettingsType[];
    launchTargets?: LaunchTarget[];
    onUpdate: (
      agents: AgentSettingsType[],
      launchTargets: LaunchTarget[],
    ) => void;
  }

  interface BuiltinAgent {
    key: string;
    label: string;
    binary: string;
  }

  interface AgentDraft {
    protocol: "terminal" | "acp";
    id: string;
    builtin: boolean;
    key: string;
    label: string;
    binary: string;
    args: string;
    enabled: boolean;
    disableForgeTools: boolean;
    // One NAME=value per line.
    env: string;
    expanded: boolean;
  }

  const builtins: BuiltinAgent[] = [
    { key: "codex", label: "Codex", binary: "codex" },
    { key: "claude", label: "Claude", binary: "claude" },
    { key: "pi", label: "Pi", binary: "pi" },
    { key: "gemini", label: "Gemini", binary: "gemini" },
    { key: "opencode", label: "opencode", binary: "opencode" },
    { key: "aider", label: "aider", binary: "aider" },
  ];

  let { agents, launchTargets = [], onUpdate }: Props = $props();

  const runtime = getAppRuntime();
  let customID = 0;
  let saving = $state(false);
  let checks = $state<Record<string, { command: string; status: "testing" | "passed" | "failed"; message: string }>>({});
  const testing = $derived(Object.values(checks).some((check) => check.status === "testing"));
  // svelte-ignore state_referenced_locally
  let drafts = $state<AgentDraft[]>(initialDrafts(agents));
  let filter = $state("");
  const visibleDrafts = $derived(drafts.filter((draft) => matchesFilter(draft, filter)));

  const savedAgents = $derived(normalizeAgents(agents));
  const preservedDefaultBuiltinKeys = $derived(defaultBuiltinKeys(savedAgents));
  const serializedAgents = $derived(
    serializeDrafts(drafts, preservedDefaultBuiltinKeys),
  );
  const draftProblems = $derived(
    drafts.flatMap((draft) => {
      const message = draftProblem(draft);
      return message === null ? [] : [{ id: draft.id, message }];
    }),
  );
  const hasInvalidDraft = $derived(draftProblems.length > 0);
  const isDirty = $derived(
    JSON.stringify(serializedAgents) !== JSON.stringify(savedAgents),
  );
  const canSave = $derived(
    !saving && !testing && isDirty && !hasInvalidDraft,
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
    // Sort once when drafts are built, so a row does not move while its
    // label is being edited. Rows added before saving stay at the end.
    return rows.sort((left, right) =>
      agentName(left).localeCompare(agentName(right), undefined, { sensitivity: "base", numeric: true }),
    );
  }

  function matchesFilter(draft: AgentDraft, query: string): boolean {
    const needle = query.trim().toLowerCase();
    if (needle === "") return true;
    return [draft.label, draft.key, draft.binary].some((value) => value.toLowerCase().includes(needle));
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
      protocol: agent?.protocol === "acp" ? "acp" : "terminal",
      enabled: agent?.enabled ?? true,
      disableForgeTools: agent?.disable_forge_tools ?? false,
      env: stringifyEnv(agent?.env),
      expanded: builtin === null && agent === undefined,
    };
  }

  function normalizeAgents(configured: AgentSettingsType[]): AgentSettingsType[] {
    return configured
      .map((agent) => ({
        ...(agent.protocol === "acp" ? { protocol: agent.protocol } : {}),
        ...(agent.disable_forge_tools ? { disable_forge_tools: true } : {}),
        ...(agent.env && Object.keys(agent.env).length > 0 ? { env: sortedEnv(agent.env) } : {}),
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
    if (!builtin || agent.protocol === "acp" || agent.disable_forge_tools || agent.env) return false;
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
      const env = parseEnv(draft.env).env;

      if (draft.builtin && builtin) {
        const isDefault =
          draft.protocol === "terminal" && !draft.disableForgeTools && enabled &&
          Object.keys(env).length === 0 &&
          label === builtin.label &&
          binary === builtin.binary &&
          args.length === 0;
        if (isDefault && !preservedDefaultKeys.has(key)) continue;
      }

      agentsToSave.push({
        ...(draft.protocol === "acp" ? {protocol: draft.protocol} : {}),
        ...(draft.disableForgeTools ? { disable_forge_tools: true } : {}),
        ...(Object.keys(env).length > 0 ? { env } : {}),
        key,
        label,
        command,
        enabled,
      });
    }
    return normalizeAgents(agentsToSave);
  }

  function draftProblem(draft: AgentDraft): string | null {
    const missing = [
      !draft.builtin && draft.key.trim() === "" ? "key" : null,
      draft.enabled && draft.binary.trim() === "" ? "binary" : null,
    ].filter((field) => field !== null);
    if (missing.length > 0) return `${agentName(draft)} needs a ${missing.join(" and a ")}.`;
    const { invalid } = parseEnv(draft.env);
    if (invalid !== null) return `${agentName(draft)} environment line "${invalid}" needs to be NAME=value.`;
    return null;
  }

  const envName = /^[A-Za-z_][A-Za-z0-9_]*$/;

  // parseEnv reads one NAME=value per line, skipping blank lines. invalid is
  // the first line that is not one, or a repeated name.
  function parseEnv(text: string): { env: Record<string, string>; invalid: string | null } {
    const env: Record<string, string> = {};
    for (const raw of text.split("\n")) {
      const line = raw.trim();
      if (line === "") continue;
      const separator = line.indexOf("=");
      const name = separator < 0 ? line : line.slice(0, separator);
      if (separator < 0 || !envName.test(name) || name in env) return { env, invalid: line };
      env[name] = line.slice(separator + 1);
    }
    return { env: sortedEnv(env), invalid: null };
  }

  function stringifyEnv(env: Record<string, string> | undefined): string {
    return Object.entries(sortedEnv(env ?? {}))
      .map(([name, value]) => `${name}=${value}`)
      .join("\n");
  }

  function sortedEnv(env: Record<string, string>): Record<string, string> {
    return Object.fromEntries(Object.entries(env).sort(([left], [right]) => left.localeCompare(right)));
  }

  function setEnabled(draft: AgentDraft, enabled: boolean): void {
    draft.enabled = enabled;
    // A built-in saved disabled with no command has an empty binary; give it
    // the default back so turning it on does not leave an unsavable row.
    if (!enabled || !draft.builtin || draft.binary.trim() !== "") return;
    const builtin = builtins.find((candidate) => candidate.key === draft.key);
    if (builtin) draft.binary = builtin.binary;
  }

  function agentName(draft: AgentDraft): string {
    return draft.label.trim() || draft.key.trim() || "Custom agent";
  }

  function toggleExpanded(draft: AgentDraft): void {
    draft.expanded = !draft.expanded;
  }

  function addCustomAgent(): void {
    filter = "";
    drafts = [
      ...drafts,
      {
        id: `custom:new:${customID++}`,
        builtin: false,
        key: "",
        label: "",
        binary: "",
        args: "",
        protocol: "terminal",
        enabled: true,
        disableForgeTools: false,
        env: "",
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
    draft.protocol = "terminal";
    draft.label = builtin.label;
    draft.binary = builtin.binary;
    draft.args = "";
    draft.env = "";
    draft.enabled = true;
    draft.expanded = true;
  }

  function save(): void {
    if (!canSave) return;
    const agentsToSave = serializedAgents;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        for (const draft of drafts) {
          if (draft.protocol !== "acp" || !draft.enabled) continue;
          const previous = savedAgents.find((agent) => agent.key === draft.key.trim().toLowerCase());
          const unchanged = previous?.protocol === "acp" && previous.enabled !== false &&
            launchKey(previous.command ?? [], previous.env ?? {}) === commandKey(draft);
          if (!unchanged && currentCheck(draft)?.status !== "passed" && !(yield* checkAgent(draft))) return;
        }
        return yield* workflow.persist(() => ({ agents: agentsToSave }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              if (!settings) return;
              const nextAgents = settings.agents ?? [];
              const nextLaunchTargets = settings.launch_targets ?? [];
              agents = nextAgents;
              launchTargets = nextLaunchTargets;
              drafts = initialDrafts(nextAgents);
              onUpdate(nextAgents, nextLaunchTargets);
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          saving = false;
        })),
      ),
      {
        operation: "save workspace agents",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  // commandKey identifies what an ACP check tested: the command and the
  // environment it ran with.
  function commandKey(draft: AgentDraft): string {
    return launchKey([draft.binary.trim(), ...parseArgs(draft.args)], parseEnv(draft.env).env);
  }

  function launchKey(command: string[], env: Record<string, string>): string {
    return JSON.stringify({ command, env: sortedEnv(env) });
  }

  function currentCheck(draft: AgentDraft) {
    const check = checks[draft.id];
    return check?.command === commandKey(draft) ? check : undefined;
  }

  function checkAgent(draft: AgentDraft) {
    const command = [draft.binary.trim(), ...parseArgs(draft.args)];
    const { env } = parseEnv(draft.env);
    const key = commandKey(draft);
    return Effect.gen(function* () {
      checks[draft.id] = { command: key, status: "testing", message: "Testing ACP connection…" };
      const workflow = yield* SettingsWorkflow;
      const result = yield* workflow.testACP(command, draft.disableForgeTools, env).pipe(
        Effect.catch((failure) => Effect.succeed({ valid: false, message: settingsErrorMessage(failure) })),
      );
      checks[draft.id] = { command: key, status: result.valid ? "passed" : "failed", message: result.message };
      if (!result.valid) draft.expanded = true;
      return result.valid;
    });
  }

  function testAgent(draft: AgentDraft): void {
    if (saving || testing || !draft.binary.trim()) return;
    runtime.runCommand(checkAgent(draft), {
      operation: "test ACP connection", safeContext: {}, onFailure: () => {},
    });
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
  <SearchInput
    bind:value={filter}
    placeholder="Filter agents..."
    ariaLabel="Filter workspace agents"
    size="sm"
    block
  />
  <div class="agent-list">
    {#each visibleDrafts as draft (draft.id)}
      <div class={["agent-row", !draft.builtin && "agent-row--custom"]}>
        <div class="agent-row-header">
          <Checkbox
            class="enable-field"
            checked={draft.enabled}
            disabled={saving}
            onchange={(enabled) => setEnabled(draft, enabled)}
          >
            <LaunchTargetName
              target={{ kind: draft.protocol === "acp" ? "acp" : "agent", key: draft.key }}
              label={agentName(draft)}
              fallbackIcon
            />
          </Checkbox>

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
            class="agent-fields"
            transition:slide={{ duration: 120 }}
          >
            <div class="agent-field-group">
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

              <div class="field">
                <span>Experience</span>
                <SelectDropdown
                  title={`${agentName(draft)} experience`}
                  value={draft.protocol}
                  options={[{value: "terminal", label: "Terminal"}, {value: "acp", label: "ACP chat"}]}
                  disabled={saving}
                  onchange={(value) => { draft.protocol = value === "acp" ? "acp" : "terminal"; }}
                />
              </div>
            </div>
            <div class="agent-field-group">
              <label class="field">
                <span>{draft.protocol === "acp" ? "ACP executable" : "Binary"}</span>
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
            <label class="field">
              <span>Environment</span>
              <textarea
                class="env-input"
                bind:value={draft.env}
                aria-label={`${agentName(draft)} environment`}
                disabled={saving}
                rows="3"
                spellcheck="false"
                placeholder="NAME=value, one per line"
              ></textarea>
            </label>
            {#if draft.protocol === "acp"}
              <div class="acp-check">
                <Button disabled={saving || testing || !draft.binary.trim()} onclick={() => testAgent(draft)}>
                  {currentCheck(draft)?.status === "testing" ? "Testing…" : "Test ACP connection"}
                </Button>
                <p>Checks this host. New or changed ACP commands are tested before saving.</p>
                {#if currentCheck(draft)}
                  <p role="status" class:check-failed={currentCheck(draft)?.status === "failed"}>
                    {currentCheck(draft)?.message}
                  </p>
                {/if}
              </div>
            {/if}
          </div>
        {/if}
      </div>
    {:else}
      <p class="agent-empty">No agents match "{filter.trim()}"</p>
    {/each}
  </div>

  {#if draftProblems.length > 0}
    <ul id="agent-save-blocked" class="save-blocked">
      {#each draftProblems as problem (problem.id)}
        <li>{problem.message}</li>
      {/each}
    </ul>
  {/if}

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
      aria-describedby={draftProblems.length > 0 ? "agent-save-blocked" : undefined}
      disabled={!canSave}
      onclick={() => void save()}
    >
      {saving ? "Saving..." : "Save"}
    </button>
  </div>
</div>

<style>
  .acp-check p { margin: var(--space-3) 0 0; color: var(--text-secondary); font-size: var(--font-size-sm); overflow-wrap: anywhere; }
  .acp-check p.check-failed { color: var(--accent-red); }
  @media (pointer: coarse) { .acp-check :global(button) { min-height: var(--mobile-chrome-hit-target); } }
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

  .agent-empty {
    margin: 0;
    padding: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
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
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .agent-field-group {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 12rem), 1fr));
    gap: var(--space-4);
    align-items: start;
  }

  .field :global(.kit-select-dropdown) {
    min-width: 0;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    min-width: 0;
  }

  :global(.enable-field) {
    align-self: center;
    flex: 1 1 auto;
  }

  :global(.enable-field .kit-checkbox__label) {
    color: var(--text-primary);
    font-weight: 600;
  }

  .field span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: var(--label-transform, uppercase);
  }

  .field input,
  .field textarea {
    width: 100%;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
  }

  .env-input {
    resize: vertical;
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

  .save-blocked {
    margin: 0;
    padding: 0;
    list-style: none;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
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

</style>
