import { getContext } from "svelte";
import type { NavigateCallback, HostStateAccessors, StoreInstances, UIConfig, SidebarAccessors } from "./types.js";
import type { RoborevClient } from "./api/roborev/client.js";

export const NAVIGATE_KEY = Symbol("kenn-forge-navigate");
export const WORKSPACE_DELETED_KEY = Symbol("kenn-forge-workspace-deleted");
export const STORES_KEY = Symbol("kenn-forge-stores");
export const UI_CONFIG_KEY = Symbol("kenn-forge-ui-config");
export const SIDEBAR_KEY = Symbol("kenn-forge-sidebar");
export const HOST_STATE_KEY = Symbol("kenn-forge-host-state");

export function getNavigate(): NavigateCallback {
  return getContext(NAVIGATE_KEY);
}
export function getStores(): StoreInstances {
  return getContext(STORES_KEY);
}
export function getUIConfig(): UIConfig {
  return getContext(UI_CONFIG_KEY);
}
export function getSidebar(): SidebarAccessors {
  return getContext(SIDEBAR_KEY);
}
export function getHostState(): HostStateAccessors {
  return getContext(HOST_STATE_KEY);
}

export const ROBOREV_CLIENT_KEY = Symbol("roborev-client");
export function getRoborevClient(): RoborevClient | undefined {
  return getContext(ROBOREV_CLIENT_KEY);
}
