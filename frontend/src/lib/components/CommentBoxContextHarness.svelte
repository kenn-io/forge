<script lang="ts">
  import { setContext } from "svelte";

  import {
    API_CLIENT_KEY,
    STORES_KEY,
  } from "../../../../packages/ui/src/context.js";
  import CommentBox from "../../../../packages/ui/src/components/detail/CommentBox.svelte";
  import IssueCommentBox from "../../../../packages/ui/src/components/detail/IssueCommentBox.svelte";

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
    kind,
    owner = "octo",
    name = "repo",
    number = 1,
    provider = "github",
    platformHost = "github.com",
    repoPath = `${owner}/${name}`,
    submitComment = async () => true,
    autocompleteResponse = { users: [], references: [] },
    onAutocompleteQuery = undefined,
  }: Props = $props();

  // Reference the props inside closures: setContext runs once at init, and
  // svelte's state_referenced_locally warning is right that a bare reference
  // would freeze the initial function values.
  setContext(STORES_KEY, {
    detail: {
      submitComment: async (o: string, n: string, num: number, body: string) =>
        (await submitComment(o, n, num, body)) !== false,
    },
    issues: {
      submitIssueComment: async (o: string, n: string, num: number, body: string) =>
        (await submitComment(o, n, num, body)) !== false,
    },
  });

  setContext(API_CLIENT_KEY, {
    GET: async (
      path: string,
      options?: {
        params?: {
          path?: Record<string, unknown>;
          query?: Record<string, unknown>;
        };
      },
    ) => {
      if (
        path === "/repo/{provider}/{owner}/{name}/comment-autocomplete"
        || path === "/host/{platform_host}/repo/{provider}/{owner}/{name}/comment-autocomplete"
      ) {
        onAutocompleteQuery?.(options?.params);
        return { data: autocompleteResponse };
      }
      return { data: undefined, error: { title: "not mocked" } };
    },
  });
</script>

{#if kind === "pull"}
  <CommentBox {provider} {platformHost} {owner} {name} {repoPath} {number} />
{:else}
  <IssueCommentBox {provider} {platformHost} {owner} {name} {repoPath} {number} />
{/if}
