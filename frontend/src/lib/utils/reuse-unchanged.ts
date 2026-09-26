function jsonValueEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
      if (!jsonValueEqual(a[i], b[i])) return false;
    }
    return true;
  }
  if (Array.isArray(b)) return false;
  const aRecord = a as Record<string, unknown>;
  const bRecord = b as Record<string, unknown>;
  const aKeys = Object.keys(aRecord);
  if (aKeys.length !== Object.keys(bRecord).length) return false;
  for (const key of aKeys) {
    if (!Object.hasOwn(bRecord, key) || !jsonValueEqual(aRecord[key], bRecord[key])) return false;
  }
  return true;
}

/**
 * Returns `next` with every item that is structurally equal to the previous
 * item of the same key replaced by that previous object. When nothing changed
 * the previous array itself is returned. Keeping identities stable lets keyed
 * lists skip re-rendering rows a refetch or poll did not change.
 */
export function reuseUnchanged<T extends object>(
  prev: readonly T[],
  next: readonly T[],
  keyOf: (item: T) => string | number,
): T[] {
  if (prev.length === 0) return [...next];
  const previousByKey = new Map<string | number, T>();
  for (const item of prev) previousByKey.set(keyOf(item), item);
  let identical = prev.length === next.length;
  const merged = next.map((item, index) => {
    const previous = previousByKey.get(keyOf(item));
    const kept = previous !== undefined && jsonValueEqual(previous, item) ? previous : item;
    if (kept !== prev[index]) identical = false;
    return kept;
  });
  return identical ? (prev as T[]) : merged;
}
