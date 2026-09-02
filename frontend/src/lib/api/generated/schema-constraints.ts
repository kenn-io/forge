/**
 * This file was auto-generated from internal/apiclient/spec/openapi.json.
 * Do not make direct changes to the file.
 */

export const schemaConstraints = {
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
  RepositoryDescriptor: {
    snapshot_revision: { minimum: 0 },
  },
  Snapshot: {
    generation: { minimum: 0 },
  },
  WorkspaceLaunchPull: {
    snapshot_revision: { minimum: 1 },
  },
  WorkspaceLaunchSpec: {
    item_number: { minimum: 1 },
  },
} as const;
