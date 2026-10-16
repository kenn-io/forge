import { untrack } from "svelte";
import { createSubscriber } from "svelte/reactivity";
import { canonicalProvider, type ProviderRouteRef } from "../api/provider-routes.js";
import type { PullDetail } from "../api/types.js";
import { pullDetailMatchesRef } from "../components/detail/detail-match.js";
import type { OwnedAppRuntime } from "./runtime.js";
import { createCoreActionVisitReporter, prVisitKey, type CoreAction } from "./telemetry.js";

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
      key: prVisitKey(ref),
      action:
        visible && supported && pullDetailMatchesRef(detail, ref)
          ? { event: "pr_viewed", properties: { provider } }
          : undefined,
    };
  });
}
