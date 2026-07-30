const legacyToken = "middle" + "man";
const canonicalToken = "kenn-forge";

export function migrateKennForgeStorage(storage: Storage): void {
  const legacyKeys = Array.from({ length: storage.length }, (_, index) => storage.key(index)).filter(
    (key): key is string => key !== null && key.includes(legacyToken),
  );
  for (const legacyKey of legacyKeys) {
    const canonicalKey = legacyKey.replaceAll(legacyToken, canonicalToken);
    if (storage.getItem(canonicalKey) === null) {
      const value = storage.getItem(legacyKey);
      if (value === null) continue;
      storage.setItem(canonicalKey, value);
    }
    if (storage.getItem(canonicalKey) !== null) storage.removeItem(legacyKey);
  }
}
