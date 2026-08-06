import { Context, Effect, Layer, Option, Ref, Semaphore } from "effect";
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
import {
  emitWorkspaceCommand,
  InvalidEmbeddingAcknowledgement,
  invokeProjectAction,
  type ProjectActionContext,
  type ProjectActionHook,
} from "../../stores/embed-config.svelte.js";

export interface RegisteredProject {
  readonly project: ProjectResponse;
  readonly acknowledgement: CommandResult;
}

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

export interface NewWorktreeCommand {
  readonly key: string;
  readonly action: ProjectActionHook;
  readonly context: ProjectActionContext;
}

type ProjectIntakeCommand =
  | ({ readonly _tag: "RegisterExisting" } & RegisterExistingProjectCommand)
  | ({ readonly _tag: "Clone" } & CloneProjectCommand);

export type ProjectMutationFailure = ProjectIntakeFailure | InvalidEmbeddingAcknowledgement | CommandQueueClosed;
export type NewWorktreeFailure = InvalidEmbeddingAcknowledgement | CommandQueueClosed;

interface RetainedCommand {
  readonly key: string;
}

interface RetainedCommandQueue<Input extends RetainedCommand, Output, Error> {
  readonly accept: (
    input: Input,
  ) => Effect.Effect<Effect.Effect<Output, Error | CommandQueueClosed>, CommandQueueClosed>;
  readonly forget: (key: string) => Effect.Effect<void>;
  readonly forgetIfCurrent: (
    key: string,
    acknowledgement: Effect.Effect<Output, Error | CommandQueueClosed>,
  ) => Effect.Effect<void>;
  readonly retained: (key: string) => Effect.Effect<Option.Option<Effect.Effect<Output, Error | CommandQueueClosed>>>;
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
      retained: (key) => Ref.get(retained).pipe(Effect.map((current) => Option.fromNullishOr(current.get(key)))),
      forget: (key) =>
        Ref.update(retained, (current) => {
          if (!current.has(key)) return current;
          const next = new Map(current);
          next.delete(key);
          return next;
        }),
      forgetIfCurrent: (key, acknowledgement) =>
        Ref.update(retained, (current) => {
          if (current.get(key) !== acknowledgement) return current;
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
    ) => Effect.Effect<Effect.Effect<RegisteredProject, ProjectMutationFailure>, CommandQueueClosed>;
    readonly acceptClone: (
      command: CloneProjectCommand,
    ) => Effect.Effect<Effect.Effect<RegisteredProject, ProjectMutationFailure>, CommandQueueClosed>;
    readonly acceptNewWorktree: (
      command: NewWorktreeCommand,
    ) => Effect.Effect<Effect.Effect<CommandResult, NewWorktreeFailure>, CommandQueueClosed>;
    readonly retainedNewWorktree: (
      key: string,
    ) => Effect.Effect<Option.Option<Effect.Effect<CommandResult, NewWorktreeFailure>>>;
    readonly forgetProject: (key: string) => Effect.Effect<void>;
    readonly forgetNewWorktree: (
      key: string,
      acknowledgement: Effect.Effect<CommandResult, NewWorktreeFailure>,
    ) => Effect.Effect<void>;
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
    const worktrees = yield* makeRetainedCommandQueue(
      "project worktree mutations",
      Effect.fn("ProjectMutationWorkflow.executeNewWorktree")(function* (command: NewWorktreeCommand) {
        return yield* invokeProjectAction(command.action, command.context);
      }),
    );

    const acknowledgeProject = (
      command: RegisterExistingProjectCommand | CloneProjectCommand,
      project: ProjectResponse,
    ) => {
      const payload: Record<string, unknown> = { projectId: project.id };
      if (command.hostKey) payload.hostKey = command.hostKey;
      return emitWorkspaceCommand("project-registered", payload).pipe(
        Effect.map((acknowledgement) => ({ project, acknowledgement })),
      );
    };

    const acceptProject = (
      command: ProjectIntakeCommand,
    ): Effect.Effect<Effect.Effect<RegisteredProject, ProjectMutationFailure>, CommandQueueClosed> =>
      intake
        .accept(command)
        .pipe(
          Effect.map((committed) => committed.pipe(Effect.flatMap((project) => acknowledgeProject(command, project)))),
        );

    return {
      acceptRegisterExisting: (command: RegisterExistingProjectCommand) =>
        acceptProject({ ...command, _tag: "RegisterExisting" }),
      acceptClone: (command: CloneProjectCommand) => acceptProject({ ...command, _tag: "Clone" }),
      acceptNewWorktree: worktrees.accept,
      retainedNewWorktree: worktrees.retained,
      forgetProject: intake.forget,
      forgetNewWorktree: worktrees.forgetIfCurrent,
    };
  }),
);

export function projectMutationFailureMessage(failure: ProjectMutationFailure, fallback: string): string {
  switch (failure._tag) {
    case "ApiProblemError":
    case "InvalidExternalPayload":
    case "InvalidProjectIntake":
    case "TransientTransportError":
      return projectIntakeFailureMessage(failure);
    case "CommandQueueClosed":
      return "The project request stopped before it completed.";
    case "InvalidEmbeddingAcknowledgement":
      return fallback;
  }
}

export function projectMutationKey(
  kind: "register" | "clone",
  hostKey: string | undefined,
  values: readonly string[],
): string {
  return JSON.stringify([kind, hostKey ?? null, ...values.map((value) => value.trim())]);
}

export function newWorktreeMutationKey(projectId: string, hostKey?: string): string {
  return JSON.stringify(["new-worktree", hostKey ?? null, projectId]);
}
