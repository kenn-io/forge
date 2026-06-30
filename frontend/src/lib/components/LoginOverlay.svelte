<script lang="ts">
  import { loginHref } from "../api/auth-urls.js";

  interface Props {
    navigate?: (url: string) => void;
  }

  let { navigate = (url: string) => { window.location.href = url; } }: Props = $props();

  let token = $state("");

  const basePath = typeof window !== "undefined" ? (window.__BASE_PATH__ ?? "/") : "/";

  function submit(event: SubmitEvent): void {
    event.preventDefault();
    const trimmed = token.trim();
    if (trimmed === "") return;
    navigate(loginHref(basePath, trimmed));
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
    <button class="login-submit" type="submit">Sign in</button>
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
