import { untrack } from "svelte";
import { createSubscriber } from "svelte/reactivity";
import { canonicalProvider, type ProviderRouteRef } from "../api/provider-routes.js";
import type { PullDetail } from "../api/types.js";
import { pullDetailMatchesRef } from "../components/detail/detail-match.js";
import type { OwnedAppRuntime } from "./runtime.js";
import {
  createCoreActionVisitReporter,
  createSearchOutcome,
  prVisitKey,
  type CoreAction,
  type ErrorKind,
} from "./telemetry.js";

const subscribeVisibility = createSubscriber((update) => {
  document.addEventListener("visibilitychange", update);
  return () => document.removeEventListener("visibilitychange", update);
});

/** Keep presentation selection local and wait for a visible browser tab before dispatch. */
export function trackCoreActionVisit(
  runtime: Pick<OwnedAppRuntime, "runCommand">,
  visit: () => { key: string | null; action: CoreAction | undefined },
): void {
  const report = createCoreActionVisitReporter(runtime);
  $effect(() => {
    subscribeVisibility();
    const { key, action } = visit();
    const visible = document.visibilityState === "visible";
    untrack(() => report(key, visible ? action : undefined));
  });
}

export function trackPRVisit(
  runtime: Pick<OwnedAppRuntime, "runCommand">,
  visit: () => { ref: (ProviderRouteRef & { number: number }) | null; detail: PullDetail | null; visible: boolean },
): void {
  trackCoreActionVisit(runtime, () => {
    const { ref, detail, visible } = visit();
    const provider = canonicalProvider(ref?.provider ?? "");
    const supported =
      provider === "github" ||
      provider === "gitlab" ||
      provider === "forgejo" ||
      provider === "gitea" ||
      provider === "bitbucket";
    return {
      key: visible ? prVisitKey(ref) : null,
      action:
        visible && supported && pullDetailMatchesRef(detail, ref)
          ? { event: "pr_viewed", properties: { provider } }
          : undefined,
    };
  });
}

/** Request results settle when accepted; local fields settle when committed. */
export function trackEmptySearch(
  runtime: Pick<OwnedAppRuntime, "runCommand">,
  search: () => { count: number; settled: boolean; failed: boolean; visible: boolean },
  local: () => boolean = () => false,
): ((query: string) => void) & { commit: () => void } {
  const outcome = createSearchOutcome(runtime);
  let attempt = $state<number>();
  let committed = false;
  let normalized = "";
  $effect(() => {
    const state = search();
    const id = attempt;
    if (!state.visible) {
      outcome.reject(id);
      return;
    }
    if (state.failed || !state.settled || (local() && !committed)) return;
    untrack(() => outcome.settle(id, state.count));
  });
  return Object.assign(
    (query: string) => {
      if (query.trim() === normalized) return;
      normalized = query.trim();
      committed = false;
      outcome.start(query);
      attempt = outcome.current();
    },
    {
      commit: () => {
        committed = true;
        const state = search();
        if (state.visible && state.settled && !state.failed) outcome.settle(attempt, state.count);
      },
    },
  );
}

/** Report each visible error presentation once per workflow. */
export function trackCoreActionError(
  runtime: Pick<OwnedAppRuntime, "runCommand">,
  state: () => { key: string | null; kind: ErrorKind | null; visible: boolean },
): void {
  trackCoreActionVisit(runtime, () => {
    const { key, kind, visible } = state();
    const presenting = visible && key !== null && kind !== null;
    return {
      key: presenting ? key : null,
      action: presenting ? { event: "error_shown", properties: { kind } } : undefined,
    };
  });
}
