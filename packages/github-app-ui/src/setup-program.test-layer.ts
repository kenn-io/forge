import { Context, Effect, Layer, Ref } from "effect";
import { SetupFlowFetch, SetupFormSubmit, type SetupFlowError, type SetupFlowView } from "./setup-program.js";

export class SetupProbe extends Context.Service<
  SetupProbe,
  {
    readonly submits: Ref.Ref<number>;
    readonly seconds: Array<number>;
    readonly failures: Array<SetupFlowError>;
    readonly flows: Array<SetupFlowView>;
    readonly recordSeconds: (seconds: number) => void;
    readonly recordFailure: (failure: SetupFlowError) => void;
    readonly recordFlow: (flow: SetupFlowView) => void;
  }
>()("kenn-forge/github-app/testing/SetupProbe") {}

const probeLayer = Layer.effect(SetupProbe)(
  Effect.gen(function* () {
    const submits = yield* Ref.make(0);
    const seconds: Array<number> = [];
    const failures: Array<SetupFlowError> = [];
    const flows: Array<SetupFlowView> = [];
    return {
      submits,
      seconds,
      failures,
      flows,
      recordSeconds: (value: number) => {
        seconds.push(value);
      },
      recordFailure: (failure: SetupFlowError) => {
        failures.push(failure);
      },
      recordFlow: (flow: SetupFlowView) => {
        flows.push(flow);
      },
    };
  }),
);

const submitLayer = Layer.effect(SetupFormSubmit)(
  Effect.gen(function* () {
    const probe = yield* SetupProbe;
    return {
      submit: () => Ref.update(probe.submits, (count) => count + 1),
    };
  }),
);

const submitWithProbe = Layer.provideMerge(submitLayer, probeLayer);

const validFetchLayer = Layer.succeed(SetupFlowFetch)({
  load: Effect.succeed({
    action: "https://example.invalid/settings/apps/new",
    manifest: JSON.stringify({
      name: "kenn-forge-test",
      default_permissions: {
        contents: "read",
        pull_requests: "write",
      },
    }),
    name: "kenn-forge-test",
    host: "example.invalid",
  }),
});

const invalidFetchLayer = Layer.succeed(SetupFlowFetch)({
  load: Effect.succeed({
    action: "https://example.invalid/settings/apps/new",
    manifest: JSON.stringify({ default_permissions: { contents: 7 } }),
    name: "kenn-forge-test",
    host: "example.invalid",
  }),
});

export const SetupBrowserTest = Layer.mergeAll(validFetchLayer, submitWithProbe);
export const SetupBrowserInvalidTest = Layer.mergeAll(invalidFetchLayer, submitWithProbe);
