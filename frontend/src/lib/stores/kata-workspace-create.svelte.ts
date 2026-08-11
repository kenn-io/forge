import { isWorkspaceIdDeleted, nextWorkspaceLifecycleTick } from "./workspace-create-pending.svelte.js";

export type KataWorkspaceIdentity = Readonly<{
  daemonID: string;
  issueUID: string;
}>;

export type KataWorkspaceRef = Readonly<{
  id: string;
  status: string;
}>;

type KataWorkspaceCreateEntry = {
  key: string;
  pending: boolean;
  created: KataWorkspaceRef | null;
  createdTick: number | null;
};

let entries = $state<KataWorkspaceCreateEntry[]>([]);

function identityKey(identity: KataWorkspaceIdentity): string {
  return `${identity.daemonID}\u0000${identity.issueUID}`;
}

function entryFor(identity: KataWorkspaceIdentity): KataWorkspaceCreateEntry | undefined {
  const key = identityKey(identity);
  return entries.find((entry) => entry.key === key);
}

export function beginKataWorkspaceCreate(identity: KataWorkspaceIdentity): boolean {
  const key = identityKey(identity);
  const current = entryFor(identity);
  if (current?.pending || (current?.created && !isWorkspaceIdDeleted(current.created.id))) return false;
  entries = [...entries.filter((entry) => entry.key !== key), { key, pending: true, created: null, createdTick: null }];
  return true;
}

export function endKataWorkspaceCreate(identity: KataWorkspaceIdentity): void {
  const key = identityKey(identity);
  const current = entryFor(identity);
  if (!current?.pending) return;
  entries = current.created
    ? entries.map((entry) => (entry.key === key ? { ...entry, pending: false } : entry))
    : entries.filter((entry) => entry.key !== key);
}

export function isKataWorkspaceCreatePending(identity: KataWorkspaceIdentity): boolean {
  return entryFor(identity)?.pending ?? false;
}

export function recordKataWorkspaceCreated(identity: KataWorkspaceIdentity, ref: KataWorkspaceRef): void {
  if (isWorkspaceIdDeleted(ref.id)) return;
  const key = identityKey(identity);
  entries = [
    ...entries.filter((entry) => entry.key !== key),
    {
      key,
      pending: entryFor(identity)?.pending ?? false,
      created: ref,
      createdTick: nextWorkspaceLifecycleTick(),
    },
  ];
}

export function createdKataWorkspaceRef(identity: KataWorkspaceIdentity): KataWorkspaceRef | null {
  const created = entryFor(identity)?.created ?? null;
  return created && !isWorkspaceIdDeleted(created.id) ? created : null;
}

export function reconcileKataWorkspaceCreated(
  identity: KataWorkspaceIdentity,
  authoritative: KataWorkspaceRef | null | undefined,
  responseTick: number,
): void {
  const current = entryFor(identity);
  if (!current?.created) return;
  if (
    isWorkspaceIdDeleted(current.created.id) ||
    authoritative?.id === current.created.id ||
    (current.createdTick !== null && responseTick > current.createdTick)
  ) {
    entries = entries.filter((entry) => entry.key !== current.key);
  }
}

export function resolveKataWorkspaceRef(
  identity: KataWorkspaceIdentity,
  authoritative: KataWorkspaceRef | null | undefined,
): KataWorkspaceRef | null {
  const created = createdKataWorkspaceRef(identity);
  if (created && authoritative?.id === created.id) return authoritative;
  if (created) return created;
  if (authoritative && !isWorkspaceIdDeleted(authoritative.id)) return authoritative;
  return null;
}

export function resetKataWorkspaceCreateForTest(): void {
  entries = [];
}
