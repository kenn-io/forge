<script lang="ts">
  // Test host mirroring App.svelte's route wiring: the workspace's route
  // emissions synchronously update the route props, exactly like the
  // router store does in the real app. The workspace's reconciler treats
  // the URL as the source of truth, so a harness whose callbacks never
  // echo would make it converge back to the stale route.
  import { untrack } from "svelte";

  import KataWorkspace from "./KataWorkspace.svelte";
  import type { KataTaskAPI, KataTaskViewName } from "../../api/kata/taskTypes.js";

  interface RouteState {
    issue?: string | null;
    view?: KataTaskViewName | null;
    scope?: string | null;
    daemon?: string | null;
  }

  interface Props {
    api?: KataTaskAPI | undefined;
    initialIssue?: string | null | undefined;
    initialView?: KataTaskViewName | null | undefined;
    initialScope?: string | null | undefined;
    initialDaemon?: string | null | undefined;
    onSelectedIssueChange?: ((uid: string | null) => void) | undefined;
    onRouteStateChange?: ((state: RouteState, options?: { replace?: boolean }) => void) | undefined;
    onOpenMessage?: ((messageId: number) => void) | undefined;
  }

  let {
    api = undefined,
    initialIssue = null,
    initialView = null,
    initialScope = null,
    initialDaemon = null,
    onSelectedIssueChange = undefined,
    onRouteStateChange = undefined,
    onOpenMessage = undefined,
  }: Props = $props();

  // The initial* props deliberately seed the mutable route state once.
  let issue = $state<string | null>(untrack(() => initialIssue) ?? null);
  let view = $state<KataTaskViewName | null>(untrack(() => initialView) ?? null);
  let scope = $state<string | null>(untrack(() => initialScope) ?? null);
  let daemon = $state<string | null>(untrack(() => initialDaemon) ?? null);

  // Simulates browser navigation (Back/Forward, palette, docs links).
  export function setRoute(next: RouteState): void {
    if ("issue" in next) issue = next.issue ?? null;
    if ("view" in next) view = next.view ?? null;
    if ("scope" in next) scope = next.scope ?? null;
    if ("daemon" in next) daemon = next.daemon ?? null;
  }

  export function route(): { issue: string | null; view: KataTaskViewName | null; scope: string | null; daemon: string | null } {
    return { issue, view, scope, daemon };
  }

  function handleSelectedIssueChange(uid: string | null): void {
    // App.svelte's openKataIssue: a null selection navigates to /kata.
    if (uid === null) {
      issue = null;
      view = null;
      scope = null;
      daemon = null;
    } else {
      issue = uid;
    }
    onSelectedIssueChange?.(uid);
  }

  function handleRouteStateChange(next: RouteState, options?: { replace?: boolean }): void {
    setRoute(next);
    if (options === undefined) {
      onRouteStateChange?.(next);
    } else {
      onRouteStateChange?.(next, options);
    }
  }
</script>

<KataWorkspace
  {api}
  selectedIssueUID={issue}
  routeViewName={view}
  routeScopeUID={scope}
  requestedDaemonId={daemon}
  onSelectedIssueChange={handleSelectedIssueChange}
  onRouteStateChange={handleRouteStateChange}
  {onOpenMessage}
/>
