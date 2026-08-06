<script lang="ts">
  import { Effect } from "effect";
  import { getStores } from "../../context.js";
  import type { PullRequestSettings as PullRequestSettingsType } from "../../api/types.js";

  import { getAppRuntime } from "../../app/runtime-context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";

  interface Props {
    pullRequests: PullRequestSettingsType;
    onUpdate: (settings: PullRequestSettingsType) => void;
  }

  let { pullRequests, onUpdate }: Props = $props();
  const runtime = getAppRuntime();
  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  let saving = $state(false);

  type BooleanPullRequestSetting =
    | "allow_mid_stack_merges"
    | "prefer_github_native_stacks";

  function toggleSetting(key: BooleanPullRequestSetting): void {
    if (embedded || saving) return;
    const previous = pullRequests;
    const pending = {
      ...pullRequests,
      [key]: !pullRequests[key],
    };
    onUpdate(pending);
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ pull_requests: pending }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              onUpdate(previous);
              console.warn("Failed to save pull request settings:", settingsErrorMessage(failure));
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              onUpdate(settings.pull_requests);
              settingsStore.setPullRequestSettings(settings.pull_requests);
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          saving = false;
        })),
      ),
      {
        operation: "save pull request settings",
        safeContext: { setting: key },
        onFailure: () => {},
      },
    );
  }
</script>

<div class="settings-list">
  <div class="setting-row">
    <div class="setting-copy">
      <span class="setting-label">Prefer GitHub native stacks</span>
      <span class="setting-description">
        Use GitHub's read-only stack preview when available. Kenn Forge's branch-based detection remains the fallback.
      </span>
    </div>
    <button
      class={[
        "toggle-btn",
        pullRequests.prefer_github_native_stacks && "toggle-on",
      ]}
      type="button"
      disabled={saving}
      onclick={() => toggleSetting("prefer_github_native_stacks")}
      aria-label="Prefer GitHub native stacks"
      aria-pressed={pullRequests.prefer_github_native_stacks}
    >
      <span class="toggle-track"><span class="toggle-thumb"></span></span>
    </button>
  </div>

  <div class="setting-row">
    <div class="setting-copy">
      <span class="setting-label">Allow mid-stack merges</span>
      <span class="setting-description">
        When off, only the bottom unmerged branch in a stack can be merged. When on, kenn-forge warns before merging another stack member.
      </span>
    </div>
    <button
      class={["toggle-btn", pullRequests.allow_mid_stack_merges && "toggle-on"]}
      type="button"
      disabled={saving}
      onclick={() => toggleSetting("allow_mid_stack_merges")}
      aria-label="Allow mid-stack merges"
      aria-pressed={pullRequests.allow_mid_stack_merges}
    >
      <span class="toggle-track"><span class="toggle-thumb"></span></span>
    </button>
  </div>
</div>

<style>
  .settings-list { display: flex; flex-direction: column; gap: var(--space-4); }
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .toggle-btn { flex: 0 0 auto; cursor: pointer; padding: 0; background: none; }
  .toggle-btn:disabled { cursor: wait; opacity: 0.6; }
  .toggle-track { display: block; width: 36px; height: 20px; border-radius: 10px; background: var(--bg-inset); border: 1px solid var(--border-muted); position: relative; transition: background 0.15s, border-color 0.15s; }
  .toggle-on .toggle-track { background: var(--accent-blue); border-color: var(--accent-blue); }
  .toggle-thumb { display: block; width: 14px; height: 14px; border-radius: 50%; background: white; position: absolute; top: 2px; left: 2px; transition: transform 0.15s; box-shadow: var(--shadow-sm); }
  .toggle-on .toggle-thumb { transform: translateX(16px); }
</style>
