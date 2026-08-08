<script lang="ts">
  import {
    makeSetupController,
    type SetupController,
    type SetupFlowError,
    type SetupFlowView,
  } from "./setup-program.js";

  interface Props {
    onController: (controller: SetupController, active: boolean) => void;
  }

  let { onController }: Props = $props();

  // The flow has exactly two browser-visible moments: confirming the
  // manifest hand-off to GitHub, and the post-creation callback. The
  // Go callback handler redirects here with ?step=done; everything
  // else is the create step. No client routing needed.
  const step: "create" | "done" = new URLSearchParams(window.location.search).get("step") === "done"
    ? "done"
    : "create";

  let flow = $state.raw<SetupFlowView | null>(null);
  let failure = $state.raw<SetupFlowError | null>(null);
  let submitted = $state(false);
  let secondsLeft = $state(3);

  const controller = makeSetupController({
    onFlow: (loaded) => {
      failure = null;
      flow = loaded;
    },
    onSecondsLeft: (seconds) => {
      secondsLeft = seconds;
    },
    onFailure: (nextFailure: SetupFlowError) => {
      submitted = false;
      failure = nextFailure;
    },
    onSubmit: () => {
      failure = null;
      submitted = true;
    },
  });

  function continueToGitHub(): void {
    failure = null;
    controller.continue();
  }

  function registerController(): void {
    onController(controller, step === "create");
  }

  registerController();
</script>

{#snippet wordmark()}
  <header>
    <span class="logo" aria-hidden="true"></span>
    <span class="brand">kenn-forge</span>
    <span class="divider">/</span>
    <span class="context">GitHub App setup</span>
  </header>
{/snippet}

<main>
  <section class="card">
    {@render wordmark()}

    {#if step === "done"}
      <div class="status">
        <span class="badge ok" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="22" height="22">
            <path
              fill="currentColor"
              d="M13.78 4.22a.75.75 0 0 1 0 1.06l-7.25 7.25a.75.75 0 0 1-1.06 0L2.22 9.28a.75.75 0 1 1 1.06-1.06l2.72 2.72 6.72-6.72a.75.75 0 0 1 1.06 0Z"
            />
          </svg>
        </span>
        <h1>App created on GitHub</h1>
      </div>
      <p>
        GitHub handed the app's creation code back to <code>kenn-forge-github-app</code>. The terminal is finishing
        setup: exchanging the code, saving the credentials to your kenn-forge config, and opening GitHub's install
        page so you can choose the account your repositories live in.
      </p>
      <p>
        You can close this tab, but keep the terminal open until it reports the install step finished — if it
        prints an error instead, setup is not complete.
      </p>
    {:else if flow}
      <h1>Create the GitHub App for kenn-forge</h1>
      <p>
        kenn-forge will sync <strong>{flow.host}</strong> with this app's installation tokens instead of your
        personal access token, freeing up your PAT's rate limit. Merges and comments keep using your own
        credentials, so they stay attributed to you.
      </p>

      <dl class="facts">
        <dt>App name</dt>
        <dd><code>{flow.name}</code></dd>
        <dt>Visibility</dt>
        <dd>Private to your account</dd>
        <dt>Webhooks</dt>
        <dd>Disabled — kenn-forge polls</dd>
      </dl>

      {#if flow.permissions.length > 0}
        <h2>Repository permissions</h2>
        <ul class="permissions">
          {#each flow.permissions as [scope, level] (scope)}
            <li>
              <code>{scope.replaceAll("_", " ")}</code>
              <span class={["level", level === "write" && "write"]}>{level}</span>
            </li>
          {/each}
        </ul>
      {/if}

      {#if failure?._tag === "SetupFormSubmitError"}
        <p class="detail setup-error">{failure.reason}. Try again.</p>
      {/if}

      <div class="actions">
        <button onclick={continueToGitHub} disabled={submitted}>
          {submitted ? "Opening GitHub…" : "Continue to GitHub"}
        </button>
        {#if !submitted}
          <span class="countdown">continuing automatically in {secondsLeft}s</span>
        {/if}
      </div>
      <p class="detail">GitHub shows its own confirmation page before anything is created.</p>
    {:else if failure !== null}
      <div class="status">
        <span class="badge err" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="22" height="22">
            <path
              fill="currentColor"
              d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06Z"
            />
          </svg>
        </span>
        <h1>
          {failure._tag === "SetupInvalidPayload"
            ? "This setup page could not read the app manifest"
            : "This setup link is no longer active"}
        </h1>
      </div>
      {#if failure._tag === "SetupInvalidPayload"}
        <p>
          The setup data did not match the GitHub App manifest format. Re-run
          <code>kenn-forge-github-app create</code> in your terminal to start a fresh setup.
        </p>
      {:else}
        <p>
          The <code>kenn-forge-github-app create</code> command that opened this page is not running anymore. Re-run
          it in your terminal to start a fresh setup.
        </p>
      {/if}
      <p class="detail">{failure.reason}</p>
    {:else}
      <h1>Preparing the app manifest…</h1>
    {/if}
  </section>
</main>

<style>
  main {
    min-height: 100%;
    display: grid;
    place-items: center;
    padding: 24px;
  }

  .card {
    width: min(560px, 100%);
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-md);
    padding: 28px 32px 32px;
  }

  header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 24px;
    font-size: 13px;
    color: var(--text-muted);
  }

  .logo {
    width: 10px;
    height: 10px;
    border-radius: 3px;
    background: var(--accent-blue);
  }

  .brand {
    color: var(--text-secondary);
    font-weight: 600;
  }

  h1 {
    font-size: 18px;
    font-weight: 600;
    line-height: 1.3;
  }

  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-secondary);
    margin-top: 20px;
    margin-bottom: 8px;
  }

  p {
    margin-top: 10px;
    color: var(--text-secondary);
  }

  .detail {
    font-size: 12px;
    color: var(--text-muted);
  }

  .setup-error {
    color: var(--accent-red);
  }

  code {
    font-family: var(--font-mono);
    font-size: 12px;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    padding: 1px 5px;
  }

  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin-top: 16px;
    padding: 14px 16px;
    background: var(--bg-inset);
    border-radius: var(--radius-md);
    border: 1px solid var(--border-muted);
  }

  .facts dt {
    color: var(--text-muted);
    font-size: 12px;
    line-height: 22px;
  }

  .facts dd {
    color: var(--text-primary);
    font-size: 13px;
    line-height: 22px;
  }

  .permissions {
    list-style: none;
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px 16px;
  }

  .permissions li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .level {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }

  .level.write {
    color: var(--accent-blue);
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-top: 24px;
  }

  button {
    font: inherit;
    font-weight: 600;
    color: #ffffff;
    background: var(--accent-blue);
    border: none;
    border-radius: var(--radius-md);
    padding: 9px 18px;
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.7;
    cursor: default;
  }

  .countdown {
    font-size: 12px;
    color: var(--text-muted);
  }

  .status {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .badge {
    display: grid;
    place-items: center;
    width: 36px;
    height: 36px;
    border-radius: 50%;
  }

  .badge.ok {
    color: var(--accent-green);
    background: color-mix(in srgb, var(--accent-green) 14%, transparent);
  }

  .badge.err {
    color: var(--accent-red);
    background: color-mix(in srgb, var(--accent-red) 14%, transparent);
  }
</style>
