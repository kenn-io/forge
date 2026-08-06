import { layer } from "@effect/vitest";
import { Effect, Fiber, Layer, Option } from "effect";
import { describe, expect, it, vi } from "vite-plus/test";
import { GeneratedApiLive } from "../../api/generated-api.js";
import type { ProjectActionHook } from "../../stores/embed-config.svelte.js";
import {
  newWorktreeMutationKey,
  ProjectMutationWorkflow,
  ProjectMutationWorkflowLive,
  projectMutationKey,
} from "./project-mutation-workflow.js";

const TestLayer = Layer.provide(ProjectMutationWorkflowLive, GeneratedApiLive);

it("distinguishes the self host from a fleet host named local", () => {
  expect(projectMutationKey("register", undefined, ["/srv/repo"])).not.toBe(
    projectMutationKey("register", "local", ["/srv/repo"]),
  );
  expect(newWorktreeMutationKey("prj_1")).not.toBe(newWorktreeMutationKey("prj_1", "local"));
});

describe("ProjectMutationWorkflow", () => {
  layer(TestLayer)((it) => {
    it.effect("retains an accepted worktree acknowledgement when its first waiter is interrupted", () =>
      Effect.gen(function* () {
        let startAction: (() => void) | undefined;
        let completeAction: ((result: CommandResult) => void) | undefined;
        const started = new Promise<void>((resolve) => {
          startAction = resolve;
        });
        const pending = new Promise<CommandResult>((resolve) => {
          completeAction = resolve;
        });
        const handler = vi.fn(() => {
          startAction?.();
          return pending;
        });
        const action: ProjectActionHook = {
          id: "new-worktree",
          label: "New Worktree",
          handler,
        };
        const workflow = yield* ProjectMutationWorkflow;
        const command = {
          key: "new-worktree\0local\0prj_1",
          action,
          context: { surface: "project-card", projectId: "prj_1" },
        };

        const firstAcknowledgement = yield* workflow.acceptNewWorktree(command);
        const firstWaiter = yield* Effect.forkChild(firstAcknowledgement);
        yield* Effect.promise(() => started);
        yield* Fiber.interrupt(firstWaiter);
        yield* Effect.sync(() => completeAction?.({ ok: true }));

        const retainedAcknowledgement = yield* workflow.acceptNewWorktree(command);
        const result = yield* retainedAcknowledgement;

        expect(result).toEqual({ ok: true });
        expect(handler).toHaveBeenCalledOnce();
      }),
    );

    it.effect("does not let an older owner forget a replacement worktree command", () =>
      Effect.gen(function* () {
        const firstHandler = vi.fn(() => Promise.resolve<CommandResult>({ ok: true }));
        const secondHandler = vi.fn(() => Promise.resolve<CommandResult>({ ok: true }));
        const workflow = yield* ProjectMutationWorkflow;
        const key = "new-worktree\0local\0prj_1";
        const firstAcknowledgement = yield* workflow.acceptNewWorktree({
          key,
          action: { id: "new-worktree", label: "New Worktree", handler: firstHandler },
          context: { surface: "project-card", projectId: "prj_1" },
        });
        yield* firstAcknowledgement;
        yield* workflow.forgetNewWorktree(key, firstAcknowledgement);

        const secondAcknowledgement = yield* workflow.acceptNewWorktree({
          key,
          action: { id: "new-worktree", label: "New Worktree", handler: secondHandler },
          context: { surface: "project-card", projectId: "prj_1" },
        });
        yield* workflow.forgetNewWorktree(key, firstAcknowledgement);

        const retained = yield* workflow.retainedNewWorktree(key);
        expect(Option.isSome(retained)).toBe(true);
        if (Option.isSome(retained)) expect(retained.value).toBe(secondAcknowledgement);
        yield* secondAcknowledgement;
        expect(secondHandler).toHaveBeenCalledOnce();
      }),
    );
  });
});
