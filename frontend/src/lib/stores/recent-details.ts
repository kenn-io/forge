// These snapshots are presentation only; every visit still reads fresh data.
export function createRecentDetails<T>(capacity = 10) {
  const entries = new Map<string, T>();
  return {
    remember(key: string, value: T): void {
      entries.delete(key);
      entries.set(key, value);
      if (entries.size > capacity) entries.delete(entries.keys().next().value!);
    },
    get(key: string): T | undefined {
      return entries.get(key);
    },
    delete(key: string): void {
      entries.delete(key);
    },
  };
}
