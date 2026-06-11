<script lang="ts">
  interface Flow {
    action: string;
    manifest: string;
    name: string;
    host: string;
  }

  const AUTO_CONTINUE_MS = 2500;

  // The flow has exactly two browser-visible moments: confirming the
  // manifest hand-off to GitHub, and the post-creation callback. The
  // Go callback handler redirects here with ?step=done; everything
  // else is the create step. No client routing needed.
  const step: "create" | "done" = new URLSearchParams(window.location.search).get("step") === "done"
    ? "done"
    : "create";

  let flow = $state.raw<Flow | null>(null);
  let loadError = $state<string | null>(null);
  let submitted = $state(false);
  let secondsLeft = $state(Math.ceil(AUTO_CONTINUE_MS / 1000));

  let permissions = $derived.by(() => {
    if (!flow) return [];
    try {
      const manifest = JSON.parse(flow.manifest) as {
        default_permissions?: Record<string, string>;
      };
      return Object.entries(manifest.default_permissions ?? {}).sort(([a], [b]) => a.localeCompare(b));
    } catch {
      return [];
    }
  });

  function continueToGitHub(current: Flow) {
    if (submitted) return;
    submitted = true;
    // GitHub's manifest flow expects a form POST with the manifest in
    // a `manifest` field; GitHub then shows its own confirmation page
    // and redirects back to the CLI's loopback callback.
    const form = document.createElement("form");
    form.method = "post";
    form.action = current.action;
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = "manifest";
    input.value = current.manifest;
    form.appendChild(input);
    document.body.appendChild(form);
    form.submit();
  }

  async function loadFlow() {
    try {
      const resp = await fetch("./flow.json");
      if (!resp.ok) {
        throw new Error(`flow.json returned ${resp.status}`);
      }
      const loaded = (await resp.json()) as Flow;
      flow = loaded;
      // Hands-off path: continue automatically so the terminal flow
      // needs no extra click here. The countdown keeps it visible and
      // the button covers blocked pop-up-style edge cases.
      const startedAt = Date.now();
      const tick = setInterval(() => {
        const remaining = AUTO_CONTINUE_MS - (Date.now() - startedAt);
        secondsLeft = Math.max(1, Math.ceil(remaining / 1000));
        if (remaining <= 0) {
          clearInterval(tick);
          continueToGitHub(loaded);
        }
      }, 250);
    } catch (err) {
      loadError = err instanceof Error ? err.message : String(err);
    }
  }

  if (step === "create") {
    void loadFlow();
  }
</script>

{#snippet wordmark()}
  <header>
    <span class="logo" aria-hidden="true"></span>
    <span class="brand">middleman</span>
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
        GitHub handed the app's creation code back to <code>middleman-github-app</code>. The terminal is finishing
        setup: exchanging the code, saving the credentials to your middleman config, and opening GitHub's install
        page so you can choose the account your repositories live in.
      </p>
      <p>
        You can close this tab, but keep the terminal open until it reports the install step finished — if it
        prints an error instead, setup is not complete.
      </p>
    {:else if loadError !== null}
      <div class="status">
        <span class="badge err" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="22" height="22">
            <path
              fill="currentColor"
              d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06Z"
            />
          </svg>
        </span>
        <h1>This setup link is no longer active</h1>
      </div>
      <p>
        The <code>middleman-github-app create</code> command that opened this page is not running anymore. Re-run it
        in your terminal to start a fresh setup.
      </p>
      <p class="detail">{loadError}</p>
    {:else if flow}
      <h1>Create the GitHub App for middleman</h1>
      <p>
        middleman will sync <strong>{flow.host}</strong> with this app's installation tokens instead of your
        personal access token, freeing up your PAT's rate limit. Merges and comments keep using your own
        credentials, so they stay attributed to you.
      </p>

      <dl class="facts">
        <dt>App name</dt>
        <dd><code>{flow.name}</code></dd>
        <dt>Visibility</dt>
        <dd>Private to your account</dd>
        <dt>Webhooks</dt>
        <dd>Disabled — middleman polls</dd>
      </dl>

      {#if permissions.length > 0}
        <h2>Repository permissions</h2>
        <ul class="permissions">
          {#each permissions as [scope, level] (scope)}
            <li>
              <code>{scope.replaceAll("_", " ")}</code>
              <span class={["level", level === "write" && "write"]}>{level}</span>
            </li>
          {/each}
        </ul>
      {/if}

      <div class="actions">
        <button onclick={() => continueToGitHub(flow!)} disabled={submitted}>
          {submitted ? "Opening GitHub…" : "Continue to GitHub"}
        </button>
        {#if !submitted}
          <span class="countdown">continuing automatically in {secondsLeft}s</span>
        {/if}
      </div>
      <p class="detail">GitHub shows its own confirmation page before anything is created.</p>
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
