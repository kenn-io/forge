// Stable provider repository identity.
//
// Every provider identifies a repository by a positive integer ID except
// Bitbucket Cloud, which only has a repository UUID. The API encodes that one
// identity as two flat fields (`platform_repo_id`, `bitbucket_repository_uuid`);
// app code never reads or writes those fields directly. Decode them here where
// data enters and encode them here where a request leaves, so no caller can
// check half of an identity.

export type RepositoryKey =
  | { readonly kind: "id"; readonly id: number }
  | { readonly kind: "uuid"; readonly uuid: string };

/** Flat wire encoding used by API responses (snake_case fields). */
export type RepositoryKeyWire = {
  readonly platform_repo_id?: number | null | undefined;
  readonly bitbucket_repository_uuid?: string | null | undefined;
};

/** Flat wire encoding used by repository catalog responses (Go field names). */
export type RepositoryKeyCatalogWire = {
  readonly PlatformRepoID?: number | null | undefined;
  readonly BitbucketRepositoryUUID?: string | null | undefined;
};

// Decoded keys are interned: one frozen object per identity, so reactive
// derivations that re-decode unchanged payloads keep a stable value.
const internedKeys = new Map<string, RepositoryKey>();

function intern(key: RepositoryKey): RepositoryKey {
  const canonical = repositoryKeyString(key);
  const existing = internedKeys.get(canonical);
  if (existing) return existing;
  const frozen = Object.freeze(key);
  internedKeys.set(canonical, frozen);
  return frozen;
}

export function repositoryIdKey(id: number): RepositoryKey | undefined {
  return Number.isSafeInteger(id) && id > 0 ? intern({ kind: "id", id }) : undefined;
}

export function repositoryUUIDKey(uuid: string): RepositoryKey | undefined {
  const normalized = normalizeUUID(uuid);
  return normalized ? intern({ kind: "uuid", uuid: normalized }) : undefined;
}

// A wire type must declare both identity fields; a type that only models
// `platform_repo_id` would silently drop a Bitbucket Cloud UUID.
type DeclaresBothKeyFields<T> = [NonNullable<T>] extends [never]
  ? []
  : "bitbucket_repository_uuid" extends keyof NonNullable<T>
    ? "platform_repo_id" extends keyof NonNullable<T>
      ? []
      : [missingWireField: never]
    : [missingWireField: never];

/**
 * Decodes the flat wire fields. A positive ID yields an ID key and a non-empty
 * UUID yields a UUID key; neither, or both at once, is not a verified identity.
 */
export function repositoryKeyFromWire<T extends RepositoryKeyWire>(
  wire: T | null | undefined,
  ..._declaresBothFields: DeclaresBothKeyFields<T>
): RepositoryKey | undefined {
  if (!wire) return undefined;
  return decode(wire.platform_repo_id, wire.bitbucket_repository_uuid);
}

export function repositoryKeyFromCatalog(wire: RepositoryKeyCatalogWire | null | undefined): RepositoryKey | undefined {
  if (!wire) return undefined;
  return decode(wire.PlatformRepoID, wire.BitbucketRepositoryUUID);
}

/** Encodes a key as the flat request fields; an absent key encodes to nothing. */
export function repositoryKeyToWire(key: RepositoryKey | null | undefined): {
  platform_repo_id?: number;
  bitbucket_repository_uuid?: string;
} {
  if (!key) return {};
  return key.kind === "id" ? { platform_repo_id: key.id } : { bitbucket_repository_uuid: key.uuid };
}

/**
 * Encodes a key for request schemas that require `platform_repo_id`. A UUID key
 * carries `platform_repo_id: 0`, the same encoding the server emits for it.
 */
export function repositoryKeyToRequiredWire(key: RepositoryKey): {
  platform_repo_id: number;
  bitbucket_repository_uuid?: string;
} {
  return key.kind === "id"
    ? { platform_repo_id: key.id }
    : { platform_repo_id: 0, bitbucket_repository_uuid: key.uuid };
}

/**
 * Canonical string form, used in identity keys and persisted browser state.
 * Integer keys encode as `id|<n>`, matching the format persisted before UUID
 * keys existed, so remembered selections and drafts keep resolving.
 */
export function repositoryKeyString(key: RepositoryKey): string {
  return key.kind === "id" ? `id|${key.id}` : `uuid|${key.uuid}`;
}

/**
 * Encoding for repository route refs persisted in browser storage. Integer keys
 * keep the `platformRepoId` field stored before UUID keys existed, so saved
 * selections keep resolving.
 */
export type StoredRepositoryKey = {
  readonly platformRepoId?: number | undefined;
  readonly bitbucketRepositoryUUID?: string | undefined;
};

export function repositoryKeyToStored(key: RepositoryKey | null | undefined): {
  platformRepoId?: number;
  bitbucketRepositoryUUID?: string;
} {
  if (!key) return {};
  return key.kind === "id" ? { platformRepoId: key.id } : { bitbucketRepositoryUUID: key.uuid };
}

export function repositoryKeyFromStored(stored: StoredRepositoryKey | null | undefined): RepositoryKey | undefined {
  if (!stored) return undefined;
  return decode(stored.platformRepoId, stored.bitbucketRepositoryUUID);
}

export function sameRepositoryKey(a: RepositoryKey | null | undefined, b: RepositoryKey | null | undefined): boolean {
  if (!a || !b) return !a && !b;
  if (a.kind === "id") return b.kind === "id" && a.id === b.id;
  return b.kind === "uuid" && a.uuid === b.uuid;
}

/**
 * Whether a candidate agrees with an expected key. An unknown expectation
 * matches anything; a known one requires the same key.
 */
export function repositoryKeyAllows(
  expected: RepositoryKey | null | undefined,
  candidate: RepositoryKey | null | undefined,
): boolean {
  return !expected || sameRepositoryKey(expected, candidate);
}

function decode(id: number | null | undefined, uuid: string | null | undefined): RepositoryKey | undefined {
  const idKey = typeof id === "number" ? repositoryIdKey(id) : undefined;
  const uuidKey = typeof uuid === "string" ? repositoryUUIDKey(uuid) : undefined;
  if (idKey && uuidKey) return undefined;
  return idKey ?? uuidKey;
}

function normalizeUUID(uuid: string): string {
  let value = uuid.trim().toLowerCase();
  if (value.startsWith("{") && value.endsWith("}")) value = value.slice(1, -1).trim();
  return value;
}
