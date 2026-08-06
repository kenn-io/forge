import { Cause, Effect, Exit } from "effect";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { SetupEnvironmentLive, type SetupController } from "./setup-program.js";

const target = document.getElementById("app");

if (!target) {
  throw new Error("Root element 'app' not found. Cannot mount application.");
}

let setupController: SetupController | undefined;
let setupActive = false;
mount(App, {
  target,
  props: {
    onController: (controller: SetupController, active: boolean) => {
      setupController = controller;
      setupActive = active;
    },
  },
});

if (setupController === undefined) {
  throw new Error("GitHub App setup controller was not initialized.");
}

const program = setupActive ? setupController.program : Effect.never;
const root = Effect.scoped(program.pipe(Effect.provide(SetupEnvironmentLive)));
const rootFiber = Effect.runFork(root);
rootFiber.addObserver((exit) => {
  if (Exit.isFailure(exit) && !Cause.hasInterruptsOnly(exit.cause)) {
    console.error("GitHub App setup Effect failed", Cause.pretty(exit.cause));
  }
});
window.addEventListener("pagehide", (event) => {
  if (!event.persisted) {
    rootFiber.interruptUnsafe();
  }
});
