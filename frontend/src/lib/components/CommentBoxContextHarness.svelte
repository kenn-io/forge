<script lang="ts">
  import { setContext, untrack } from "svelte";
  import type { AppRuntime } from "../app/runtime.js";
  import { setAppRuntime } from "../app/runtime-context.js";

  import { STORES_KEY } from "../context.js";
  import CommentBox from "./detail/CommentBox.svelte";
  import IssueCommentBox from "./detail/IssueCommentBox.svelte";

  interface AutocompleteResponse {
    users: string[];
    references: Array<{
      kind: string;
      number: number;
      title: string;
      state: string;
    }>;
  }

  interface Props {
    runtime: AppRuntime;
    kind: "pull" | "issue";
    owner?: string;
    name?: string;
    number?: number;
    provider?: string;
    platformHost?: string | undefined;
    repoPath?: string;
    submitComment?: (owner: string, name: string, number: number, body: string) => Promise<void | boolean>;
    autocompleteResponse?: AutocompleteResponse;
    onAutocompleteQuery?: ((query: Record<string, unknown> | undefined) => void) | undefined;
  }

  const {
    runtime,
    kind,
    owner = "octo",
    name = "repo",
    number = 1,
    provider = "github",
    platformHost = "github.com",
    repoPath = `${owner}/${name}`,
    submitComment = async () => true,
  }: Props = $props();

  setAppRuntime(untrack(() => runtime));

  // Reference the props inside closures: setContext runs once at init, and
  // svelte's state_referenced_locally warning is right that a bare reference
  // would freeze the initial function values.
  setContext(STORES_KEY, {
    detail: {
      submitComment: (o: string, n: string, num: number, body: string, callbacks: { onSuccess?: () => void; onFailure?: (message: string) => void; onSettled?: () => void }) => {
        void submitComment(o, n, num, body).then((result) => {
          if (result === false) callbacks.onFailure?.("failed");
          else callbacks.onSuccess?.();
          callbacks.onSettled?.();
        });
      },
    },
    issues: {
      submitIssueComment: (o: string, n: string, num: number, body: string, callbacks: { onSuccess?: () => void; onFailure?: (message: string) => void; onSettled?: () => void }) => {
        void submitComment(o, n, num, body).then((result) => {
          if (result === false) callbacks.onFailure?.("failed");
          else callbacks.onSuccess?.();
          callbacks.onSettled?.();
        });
      },
    },
  });

</script>

{#if kind === "pull"}
  <CommentBox {provider} {platformHost} {owner} {name} {repoPath} {number} />
{:else}
  <IssueCommentBox {provider} {platformHost} {owner} {name} {repoPath} {number} />
{/if}
