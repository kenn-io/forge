import { getContext } from "svelte";
import type {
  ForgeClient,
  ActionRegistry,
  NavigateCallback,
  EventCallback,
  PrepareRouteCallback,
  WorkspaceCommandCallback,
  WorkspaceDeletedCallback,
  HostStateAccessors,
  StoreInstances,
  UIConfig,
  SidebarAccessors,
} from "./types.js";
import type { RoborevClient } from "./api/roborev/client.js";

export const API_CLIENT_KEY = Symbol("kenn-forge-api-client");
export const ACTIONS_KEY = Symbol("kenn-forge-actions");
export const NAVIGATE_KEY = Symbol("kenn-forge-navigate");
export const EVENT_KEY = Symbol("kenn-forge-event");
export const PREPARE_ROUTE_KEY = Symbol("kenn-forge-prepare-route");
export const WORKSPACE_COMMAND_KEY = Symbol("kenn-forge-workspace-command");
export const WORKSPACE_DELETED_KEY = Symbol("kenn-forge-workspace-deleted");
export const STORES_KEY = Symbol("kenn-forge-stores");
export const UI_CONFIG_KEY = Symbol("kenn-forge-ui-config");
export const SIDEBAR_KEY = Symbol("kenn-forge-sidebar");
export const HOST_STATE_KEY = Symbol("kenn-forge-host-state");

export function getClient(): ForgeClient {
  return getContext(API_CLIENT_KEY);
}
export function getActions(): ActionRegistry {
  return getContext(ACTIONS_KEY);
}
export function getNavigate(): NavigateCallback {
  return getContext(NAVIGATE_KEY);
}
export function getEventCallback(): EventCallback {
  return getContext(EVENT_KEY);
}
export function getPrepareRoute(): PrepareRouteCallback | null {
  return getContext(PREPARE_ROUTE_KEY);
}
export function getWorkspaceCommand(): WorkspaceCommandCallback | null {
  return getContext(WORKSPACE_COMMAND_KEY);
}
export function getWorkspaceDeletedCallback(): WorkspaceDeletedCallback | null {
  return getContext(WORKSPACE_DELETED_KEY);
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
