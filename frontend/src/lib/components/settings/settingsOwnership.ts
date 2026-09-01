export type SettingsOwner = "hub" | "local";

export function settingsOwnerName(owner: SettingsOwner): string {
  return owner === "hub" ? "the fleet hub" : "this Forge";
}
