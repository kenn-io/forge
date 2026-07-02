<script lang="ts">
  interface Props {
    reload?: () => void;
  }

  let { reload = () => window.location.reload() }: Props = $props();

  let token = $state("");
  let error = $state<string | null>(null);
  let submitting = $state(false);

  const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";

  // The token is POSTed as a JSON body instead of navigating to the
  // ?auth_token= bootstrap URL so it never appears in a request URI,
  // which reverse proxies commonly log.
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const trimmed = token.trim();
    if (trimmed === "" || submitting) return;
    submitting = true;
    error = null;
    try {
      const response = await fetch(`${basePath}auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: trimmed }),
      });
      if (!response.ok) {
        error = response.status === 403 ? "Invalid token" : `Sign in failed (${response.status})`;
        return;
      }
      reload();
    } catch {
      error = "Sign in failed: could not reach the server";
    } finally {
      submitting = false;
    }
  }
</script>

<div class="login-overlay" role="dialog" aria-modal="true" aria-label="Sign in to middleman">
  <form class="login-card" onsubmit={submit}>
    <h1 class="login-title">middleman</h1>
    <p class="login-hint">
      Run <code>middleman auth url</code> on the server and open the link, or paste your
      access token below.
    </p>
    <input
      class="login-input"
      type="password"
      autocomplete="off"
      placeholder="access token"
      aria-label="Access token"
      bind:value={token}
    />
    {#if error !== null}
      <p class="login-error" role="alert">{error}</p>
    {/if}
    <button class="login-submit" type="submit" disabled={submitting}>Sign in</button>
  </form>
</div>

<style>
  .login-overlay {
    position: fixed;
    inset: 0;
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--bg-primary);
  }

  .login-card {
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: min(360px, 90vw);
    padding: 24px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .login-title {
    margin: 0;
    font-size: var(--font-size-lg);
    color: var(--text-primary);
  }

  .login-hint {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .login-error {
    margin: 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .login-input {
    padding: 8px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .login-submit {
    padding: 8px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-weight: 500;
    cursor: pointer;
  }
</style>
