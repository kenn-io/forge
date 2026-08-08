import { Cause, Effect } from "effect";
import { notifyInitialRouteChange } from "./lib/stores/router.svelte.js";
import "./app.css";
import { mountApplication } from "./lib/app/mount.js";
import { makeAppRuntime } from "./lib/app/runtime.js";
import { prepareFrontendReload, retireFrontendReload } from "./lib/utils/frontendReloadGuard.js";

const currentFrontendEntrypoint = new URL(import.meta.url);

function frontendEntrypoints(document: Document, baseUrl: string): URL[] {
  return Array.from(document.querySelectorAll<HTMLScriptElement>('script[type="module"][src]'), (script) => {
    return new URL(script.src, baseUrl);
  });
}

const reloadIfFrontendChanged = Effect.fn("Main.reloadIfFrontendChanged")(function* () {
  const response = yield* Effect.tryPromise({
    try: (signal) =>
      window.fetch(window.location.href, {
        cache: "no-store",
        headers: { Accept: "text/html" },
        signal,
      }),
    catch: (cause) => (cause instanceof Error ? cause : new Error("Frontend update check failed")),
  });
  if (!response.ok) return;

  const html = yield* Effect.tryPromise({
    try: () => response.text(),
    catch: (cause) => (cause instanceof Error ? cause : new Error("Frontend update response could not be read")),
  });
  const latestDocument = new DOMParser().parseFromString(html, "text/html");
  const latestEntrypoints = frontendEntrypoints(latestDocument, response.url);
  if (latestEntrypoints.some((entrypoint) => entrypoint.pathname === currentFrontendEntrypoint.pathname)) return;

  const latestEntrypoint = latestEntrypoints.at(-1);
  if (!latestEntrypoint) return;
  if (!prepareFrontendReload(window.sessionStorage, currentFrontendEntrypoint.pathname, latestEntrypoint.pathname)) {
    return;
  }
  yield* Effect.sync(() => window.location.reload());
});

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

const runtime = makeAppRuntime();

runtime.runCommand(
  Effect.try({
    try: () => retireFrontendReload(window.sessionStorage, currentFrontendEntrypoint.pathname),
    catch: (cause) => cause,
  }).pipe(Effect.catch((error) => Effect.sync(() => console.warn("Could not clear completed frontend reload", error)))),
  { operation: "retire completed frontend reload", safeContext: {}, onFailure: () => {} },
);

// A browser tab can outlive a server update and request a content-hashed lazy
// chunk that the new binary no longer embeds. Reload only when the server's
// current HTML points to a different frontend, preserving transient retries.
runtime.runCommand(
  Effect.scoped(
    Effect.acquireRelease(
      Effect.sync(() => {
        const listener = () => {
          runtime.runCommand(
            reloadIfFrontendChanged().pipe(
              Effect.catch((error) => Effect.sync(() => console.warn("Could not check for a frontend update", error))),
            ),
            { operation: "check for a frontend update", safeContext: {}, onFailure: () => {} },
          );
        };
        window.addEventListener("vite:preloadError", listener);
        return listener;
      }),
      (listener) => Effect.sync(() => window.removeEventListener("vite:preloadError", listener)),
    ).pipe(Effect.andThen(Effect.never)),
  ),
  { operation: "observe frontend preload failures", safeContext: {}, onFailure: () => {} },
);

mountApplication(target, runtime, (cause) => {
  console.error("Frontend application Effect failed", Cause.pretty(cause));
});
runtime.runMicrotask(notifyInitialRouteChange, {
  operation: "publish initial route",
  safeContext: {},
});
