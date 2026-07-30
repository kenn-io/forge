import { migrateKennForgeStorage } from "./lib/utils/kennForgeStorageMigration.js";

for (const storage of [window.localStorage, window.sessionStorage]) {
  try {
    migrateKennForgeStorage(storage);
  } catch (error) {
    console.warn("Could not migrate Kenn Forge browser state", error);
  }
}
