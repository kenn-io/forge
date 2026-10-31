import { initMarkdownImageViewer } from "@kenn-io/kit-ui";
import { initMarkdownMermaidRendering } from "@kenn-io/kit-ui/utils/markdown-mermaid";
import { Cause, Effect, Exit, Fiber } from "effect";
import type { Cause as CauseType } from "effect/Cause";
import { mount, unmount } from "svelte";
import App from "../../App.svelte";
import { pushModalFrame } from "../stores/keyboard/modal-stack.svelte.js";
import { initMarkdownVideoPlayers } from "../utils/markdown-video-player.svelte.js";
import { observeMarkdownVideoPreviews } from "../utils/markdown-video-preview.js";
import type { OwnedAppRuntime } from "./runtime.js";
import { startSessionDurationReporting } from "./session-duration.js";
import { startRuntimeTelemetry } from "./telemetry.js";

function renderApplicationFailure(target: HTMLElement): void {
  const alert = target.ownerDocument.createElement("section");
  alert.className = "app-root-failure";
  alert.setAttribute("role", "alert");

  const heading = target.ownerDocument.createElement("h1");
  heading.textContent = "Kenn Forge could not start";
  const guidance = target.ownerDocument.createElement("p");
  guidance.textContent = "Reload this page to try again. Check the browser console if the problem continues.";
  alert.append(heading, guidance);
  target.replaceChildren(alert);
}

export const appProgram = (target: HTMLElement, runtime: OwnedAppRuntime) =>
  Effect.acquireRelease(
    Effect.sync(() => mount(App, { target, props: { runtime } })),
    (application) => Effect.promise(() => unmount(application)),
  ).pipe(Effect.andThen(Effect.never));

// Markdown images, Mermaid diagrams, and videos expand into kit-ui's
// MediaViewer, which pages through every displayed one on the page.
const openMediaViewerFrame = () => pushModalFrame("media-viewer", []);

const observeMarkdownImageExpansion = (target: HTMLElement) =>
  Effect.acquireRelease(
    Effect.sync(() =>
      initMarkdownImageViewer(target, {
        selector: ".markdown-body img, .doc-markdown img",
        onViewerOpen: openMediaViewerFrame,
      }),
    ),
    (controller) => Effect.sync(() => controller.disconnect()),
  ).pipe(Effect.andThen(Effect.never));

const observeMarkdownVideoPlayers = (target: HTMLElement) =>
  Effect.acquireRelease(
    Effect.sync(() => initMarkdownVideoPlayers(target, { onViewerOpen: openMediaViewerFrame })),
    (controller) => Effect.sync(() => controller.disconnect()),
  ).pipe(Effect.andThen(Effect.never));

const observeMarkdownMermaidRendering = (target: HTMLElement) =>
  Effect.acquireRelease(
    Effect.sync(() => initMarkdownMermaidRendering(target, { onViewerOpen: openMediaViewerFrame })),
    (controller) => Effect.sync(() => controller.disconnect()),
  ).pipe(Effect.andThen(Effect.never));

const reportAppOpens = (runtime: OwnedAppRuntime) =>
  Effect.acquireRelease(
    Effect.sync(() => {
      const reporter = startRuntimeTelemetry(runtime);
      let sessions: ReturnType<typeof startSessionDurationReporting> | undefined;
      const unsubscribe = reporter.onQuietChange((quiet) => {
        if (quiet) sessions?.pause();
        else if (sessions) sessions.resume();
        else
          sessions = startSessionDurationReporting((duration) => {
            reporter.reportVisit(
              { event: "session_ended", properties: { surface: "web", duration_bucket: duration } },
              { keepalive: true },
            );
          });
      });
      return () => {
        reporter.stop();
        unsubscribe();
        sessions?.stop();
      };
    }),
    (stop) => Effect.sync(stop),
  ).pipe(Effect.andThen(Effect.never));

export function mountApplication(
  target: HTMLElement,
  runtime: OwnedAppRuntime,
  reportFailure: (cause: CauseType<never>) => void = () => undefined,
) {
  const imageExpansion = runtime.runCommand(Effect.scoped(observeMarkdownImageExpansion(target)), {
    operation: "observe markdown images",
    safeContext: {},
    onFailure: () => {},
  });
  const videoPlayers = runtime.runCommand(Effect.scoped(observeMarkdownVideoPlayers(target)), {
    operation: "observe markdown videos",
    safeContext: {},
    onFailure: () => {},
  });
  const mermaidRendering = runtime.runCommand(Effect.scoped(observeMarkdownMermaidRendering(target)), {
    operation: "observe markdown diagrams",
    safeContext: {},
    onFailure: () => {},
  });
  const videoPreviews = runtime.runCommand(Effect.scoped(observeMarkdownVideoPreviews(target)), {
    operation: "preview markdown videos",
    safeContext: {},
    onFailure: () => {},
  });
  const appOpens = runtime.runCommand(Effect.scoped(reportAppOpens(runtime)), {
    operation: "report app opens",
    safeContext: {},
    onFailure: () => {},
  });
  const root = Effect.scoped(appProgram(target, runtime)).pipe(
    Effect.ensuring(Effect.sync(imageExpansion.interrupt)),
    Effect.ensuring(Effect.sync(videoPlayers.interrupt)),
    Effect.ensuring(Effect.sync(mermaidRendering.interrupt)),
    Effect.ensuring(Effect.sync(videoPreviews.interrupt)),
    Effect.ensuring(Effect.sync(appOpens.interrupt)),
    Effect.ensuring(runtime.disposeEffect),
  );
  const rootFiber = Effect.runFork(root);
  rootFiber.addObserver((exit) => {
    if (Exit.isFailure(exit) && !Cause.hasInterruptsOnly(exit.cause)) {
      renderApplicationFailure(target);
      reportFailure(exit.cause);
    }
  });
  return {
    rootFiber,
    interrupt: () => rootFiber.interruptUnsafe(),
    dispose: Fiber.interrupt(rootFiber),
  };
}
