import { Cause, Effect, Exit, Fiber } from "effect";
import type { Cause as CauseType } from "effect/Cause";
import { mount, unmount } from "svelte";
import App from "../../App.svelte";
import type { OwnedAppRuntime } from "./runtime.js";

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

export function mountApplication(
  target: HTMLElement,
  runtime: OwnedAppRuntime,
  reportFailure: (cause: CauseType<never>) => void = () => undefined,
) {
  const root = Effect.scoped(appProgram(target, runtime)).pipe(Effect.ensuring(runtime.disposeEffect));
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
