/**
 * This file was auto-generated from frontend/openapi/openapi.yaml.
 * Do not make direct changes to the file.
 */

export const schemaConstraints = {
  Assignment: {
    uid: { minimum: 0 },
  },
  Connection: {
    uid: { minimum: 0 },
  },
  CreateEnrollmentTokenInputBody: {
    expires_in_seconds: { minimum: 1, maximum: 86400 },
  },
  CreateWorktreeFromMergeRequestInputBody: {
    number: { minimum: 1 },
  },
  Detail: {
    initial_timeline_entry_limit: { minimum: 10, maximum: 250 },
  },
  DiffDescriptor: {
    snapshot_revision: { minimum: 0 },
  },
  FederationDiffDescriptorRequest: {
    pull_number: { minimum: 1 },
  },
  NeutralHost: {
    generation: { minimum: 0 },
  },
  NeutralSnapshot: {
    generation: { minimum: 0 },
  },
  RawSnapshot: {
    generation: { minimum: 0 },
  },
  Snapshot: {
    generation: { minimum: 0 },
  },
  SyncSettingsResponse: {
    budget_per_hour: { minimum: 50, maximum: 15000 },
  },
  SyncSettingsUpdate: {
    budget_per_hour: { minimum: 50, maximum: 15000 },
  },
  WorkerIdentity: {
    uid: { minimum: 0 },
  },
  WorkspaceLaunchPull: {
    snapshot_revision: { minimum: 1 },
  },
  WorkspaceLaunchSpec: {
    item_number: { minimum: 1 },
  },
} as const;
