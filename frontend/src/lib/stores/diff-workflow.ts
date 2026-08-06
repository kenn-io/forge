import { Context, Layer } from "effect";
import type { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import type { GeneratedApi } from "../api/generated-api.js";
import type { DiffResponseWire, FilesResponseWire } from "../api/types.js";
import { makeLatestSharedRead, type LatestSharedRead } from "../effect/latest-shared-read.js";

export interface ProviderDiffRead {
  readonly diff: DiffResponseWire;
  readonly files: FilesResponseWire | null;
}

export type DiffReadError = ApiProblemError | TransientTransportError;

export class DiffWorkflow extends Context.Service<
  DiffWorkflow,
  LatestSharedRead<ProviderDiffRead, DiffReadError, GeneratedApi>
>()("kenn-forge/DiffWorkflow") {}

export const DiffWorkflowLive =
  Layer.effect(DiffWorkflow)(makeLatestSharedRead<ProviderDiffRead, DiffReadError, GeneratedApi>());
