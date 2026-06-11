<script lang="ts">
  import Modal from "../shared/Modal.svelte";
  import type { MessagesCapabilities } from "../../api/messages/types";
  import { hasEmptyAuthority, isLoopbackHostname } from "../../api/messages/setupURL";

  // Env var names mirror POSIX shell conventions: leading letter or
  // underscore, then letters/digits/underscores. The Go server enforces
  // the same rule via /api/v1/msgvault/configure; we validate here so users
  // see the error before a round-trip.
  const ENV_NAME_RE = /^[A-Z_][A-Z0-9_]*$/;

  // Default suggestion for first-use. Matches the project convention
  // and what the user is most likely to set in their shell.
  const DEFAULT_ENV_NAME = "MSGVAULT_API_KEY";

  interface Props {
    open: boolean;
    initialURL?: string | undefined;
    initialEnv?: string | undefined;
    onClose: () => void;
    onSave: (input: { url: string; api_key_env: string }) => Promise<MessagesCapabilities>;
  }

  let {
    open,
    initialURL = undefined,
    initialEnv = undefined,
    onClose,
    onSave,
  }: Props = $props();

  let url = $state("");
  let envName = $state("");
  let error = $state<string | null>(null);
  let saving = $state(false);
  let lastSeenOpen = $state(false);

  // Re-seed form state only on the closed->open transition so a previous
  // user's typed-but-cancelled values don't leak across re-opens, while
  // mid-edit prop changes (e.g. a capability re-probe updating
  // `initialURL` while the dialog is already open) do not silently wipe
  // the user's in-progress edits.
  // The plan only treats `undefined` as "use the default suggestion"
  // for envName; an explicit empty string is respected as-is so the
  // parent can force a cleared field when it wants to.
  $effect(() => {
    if (open && !lastSeenOpen) {
      url = initialURL ?? "";
      envName = initialEnv ?? DEFAULT_ENV_NAME;
      error = null;
      saving = false;
    }
    lastSeenOpen = open;
  });

  function validateURL(raw: string): string | null {
    // `new URL("https:///foo")` normalizes the empty authority and
    // accepts "foo" as the host, but Go's `url.Parse` (the server's
    // source of truth) returns an empty host for the same input. The
    // hasEmptyAuthority guard inspects the original input so the
    // client rejects what the server rejects, before any round-trip.
    if (hasEmptyAuthority(raw)) {
      return "URL must include a scheme and host (http/https).";
    }
    let parsed: URL;
    try {
      parsed = new URL(raw);
    } catch {
      return "URL must include a scheme and host (http/https).";
    }
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "URL must include a scheme and host (http/https).";
    }
    if (!parsed.host || !parsed.hostname) {
      return "URL must include a scheme and host (http/https).";
    }
    if (parsed.protocol === "http:" && !isLoopbackHostname(parsed.hostname)) {
      return "http URLs send the API key in cleartext; use https, or http with localhost/127.0.0.1.";
    }
    return null;
  }

  function validateEnvName(raw: string): string | null {
    if (!ENV_NAME_RE.test(raw)) {
      return "Env var name must match [A-Z_][A-Z0-9_]* (uppercase, digits, underscores).";
    }
    return null;
  }

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (saving) return;

    const trimmedURL = url.trim();
    const trimmedEnv = envName.trim();

    const urlErr = validateURL(trimmedURL);
    if (urlErr) {
      error = urlErr;
      return;
    }
    const envErr = validateEnvName(trimmedEnv);
    if (envErr) {
      error = envErr;
      return;
    }

    error = null;
    saving = true;
    try {
      await onSave({ url: trimmedURL, api_key_env: trimmedEnv });
      onClose();
    } catch (err) {
      error = err instanceof Error && err.message ? err.message : "Could not save configuration.";
    } finally {
      saving = false;
    }
  }
</script>

<Modal {open} title="Set up Messages" {onClose}>
  <!-- novalidate disables HTML5 constraint validation so handleSubmit
       always runs. Without it, real browsers reject malformed URLs
       (and empty required fields) before the form's submit listener
       fires, and the JS validator below never gets a chance to surface
       its more specific error copy. The unit tests dispatch submit
       events directly via fireEvent.submit which bypasses native
       validation anyway, so this only affects real browsers - but
       that's exactly where the dialog needs to behave correctly. -->
  <form class="setup-form" novalidate onsubmit={handleSubmit}>
    <label class="setup-field">
      <span>Message source URL</span>
      <input
        type="url"
        bind:value={url}
        placeholder="https://messages.example.com"
        required
      />
    </label>
    <label class="setup-field">
      <span>API key env var name</span>
      <input
        type="text"
        bind:value={envName}
        placeholder="MSGVAULT_API_KEY"
        required
      />
    </label>

    {#if error}
      <div role="alert" class="setup-error">{error}</div>
    {/if}

    <p class="setup-hint">
      Set the env var in your shell (e.g.
      <code>export {envName || DEFAULT_ENV_NAME}=...</code>)
      and retry setup if the connection doesn't go live immediately.
    </p>

    <div class="setup-actions">
      <button type="button" onclick={onClose} disabled={saving}>
        Cancel
      </button>
      <button type="submit" class="primary" disabled={saving}>
        {saving ? "Saving..." : "Save"}
      </button>
    </div>
  </form>
</Modal>

<style>
  .setup-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .setup-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .setup-field span {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .setup-field input {
    width: 100%;
    padding: 6px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .setup-field input:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .setup-error {
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--accent-red-soft);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .setup-hint {
    margin: 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .setup-hint code {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    background: var(--bg-surface-hover);
    padding: 1px 4px;
    border-radius: 3px;
  }

  .setup-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }

  .setup-actions button {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    cursor: pointer;
    font-size: var(--font-size-sm);
  }

  .setup-actions button:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .setup-actions button.primary {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
    color: #ffffff;
  }

  .setup-actions button.primary:hover:not(:disabled) {
    /* Slightly darker shade on hover; accent-blue is already saturated
       enough that a soft overlay reads as "pressed" rather than a
       distinct hover color. */
    filter: brightness(0.95);
  }

  .setup-actions button:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
