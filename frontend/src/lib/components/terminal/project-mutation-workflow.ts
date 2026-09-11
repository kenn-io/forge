import { Context, Effect, Layer, Ref, Semaphore } from "effect";
import type { Scope } from "effect/Scope";
import { GeneratedApi } from "../../api/generated-api.js";
import {
  cloneProject,
  projectIntakeFailureMessage,
  registerExistingProject,
  type ProjectIntakeFailure,
  type ProjectResponse,
} from "../../api/project-intake.js";
import { CommandQueueClosed, makeOrderedCommandQueue } from "../../effect/ordered-command-queue.js";

export interface RegisterExistingProjectCommand {
  readonly key: string;
  readonly path: string;
  readonly hostKey?: string | undefined;
}

export interface CloneProjectCommand {
  readonly key: string;
  readonly url: string;
  readonly path: string;
  readonly branch: string;
  readonly hostKey?: string | undefined;
}

type ProjectIntakeCommand =
  | ({ readonly _tag: "RegisterExisting" } & RegisterExistingProjectCommand)
  | ({ readonly _tag: "Clone" } & CloneProjectCommand);

export type ProjectMutationFailure = ProjectIntakeFailure | CommandQueueClosed;

interface RetainedCommand {
  readonly key: string;
}

interface RetainedCommandQueue<Input extends RetainedCommand, Output, Error> {
  readonly accept: (
    input: Input,
  ) => Effect.Effect<Effect.Effect<Output, Error | CommandQueueClosed>, CommandQueueClosed>;
  readonly forget: (key: string) => Effect.Effect<void>;
}

const makeRetainedCommandQueue = <Input extends RetainedCommand, Output, Error, Requirements>(
  name: string,
  execute: (input: Input) => Effect.Effect<Output, Error, Requirements>,
): Effect.Effect<RetainedCommandQueue<Input, Output, Error>, never, Requirements | Scope> =>
  Effect.gen(function* () {
    const queue = yield* makeOrderedCommandQueue(name, execute);
    const retained = yield* Ref.make<ReadonlyMap<string, Effect.Effect<Output, Error | CommandQueueClosed>>>(new Map());
    const acceptance = yield* Semaphore.make(1);

    const accept = (input: Input) =>
      acceptance.withPermit(
        Effect.gen(function* () {
          const existing = (yield* Ref.get(retained)).get(input.key);
          if (existing !== undefined) return existing;
          const acknowledgement = yield* queue.accept(input);
          yield* Ref.update(retained, (current) => new Map(current).set(input.key, acknowledgement));
          return acknowledgement;
        }),
      );

    return {
      accept,
      forget: (key) =>
        Ref.update(retained, (current) => {
          if (!current.has(key)) return current;
          const next = new Map(current);
          next.delete(key);
          return next;
        }),
    };
  });

export class ProjectMutationWorkflow extends Context.Service<
  ProjectMutationWorkflow,
  {
    readonly acceptRegisterExisting: (
      command: RegisterExistingProjectCommand,
    ) => Effect.Effect<Effect.Effect<ProjectResponse, ProjectMutationFailure>, CommandQueueClosed>;
    readonly acceptClone: (
      command: CloneProjectCommand,
    ) => Effect.Effect<Effect.Effect<ProjectResponse, ProjectMutationFailure>, CommandQueueClosed>;
    readonly forgetProject: (key: string) => Effect.Effect<void>;
  }
>()("kenn-forge/ProjectMutationWorkflow") {}

export const ProjectMutationWorkflowLive = Layer.effect(ProjectMutationWorkflow)(
  Effect.gen(function* () {
    const api = yield* GeneratedApi;
    const intake = yield* makeRetainedCommandQueue(
      "project intake mutations",
      Effect.fn("ProjectMutationWorkflow.executeIntake")(function* (command: ProjectIntakeCommand) {
        const options = command.hostKey ? { hostKey: command.hostKey } : undefined;
        const project =
          command._tag === "RegisterExisting"
            ? yield* registerExistingProject(command.path, options).pipe(Effect.provideService(GeneratedApi, api))
            : yield* cloneProject(command.url, command.path, command.branch, options).pipe(
                Effect.provideService(GeneratedApi, api),
              );
        return project;
      }),
    );
    return {
      acceptRegisterExisting: (command: RegisterExistingProjectCommand) =>
        intake.accept({ ...command, _tag: "RegisterExisting" }),
      acceptClone: (command: CloneProjectCommand) => intake.accept({ ...command, _tag: "Clone" }),
      forgetProject: intake.forget,
    };
  }),
);

export function projectMutationFailureMessage(failure: ProjectMutationFailure): string {
  switch (failure._tag) {
    case "ApiProblemError":
    case "InvalidExternalPayload":
    case "InvalidProjectIntake":
    case "TransientTransportError":
      return projectIntakeFailureMessage(failure);
    case "CommandQueueClosed":
      return "The project request stopped before it completed.";
  }
}

export function projectMutationKey(
  kind: "register" | "clone",
  hostKey: string | undefined,
  values: readonly string[],
): string {
  return JSON.stringify([kind, hostKey ?? null, ...values.map((value) => value.trim())]);
}
