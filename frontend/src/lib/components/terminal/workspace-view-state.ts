import { Semaphore } from "effect";
import { executeGeneratedApiRequest } from "../../api/generated-api.js";
import type { WorkflowTabKey } from "./terminal-layout.js";

// Keep reads and selections ordered within a workspace without blocking other hosts.
// ponytail: retain these small queues for the browser session; evict if workspace churn grows.
const requests = new Map<string, Semaphore.Semaphore>();

function workspaceRequests(workspaceId: string, hostKey?: string) {
  const key = JSON.stringify([hostKey ?? "", workspaceId]);
  let queue = requests.get(key);
  if (!queue) {
    queue = Semaphore.makeUnsafe(1);
    requests.set(key, queue);
  }
  return queue;
}

export function loadWorkspaceViewState(workspaceId: string, hostKey?: string) {
  return workspaceRequests(workspaceId, hostKey).withPermit(
    executeGeneratedApiRequest("load workspace tab", (client, signal) => {
      if (hostKey?.startsWith("devbox:")) {
        return client.DevboxesService.getDevboxWorkspaceViewState(
          { connectionId: hostKey.slice(7), id: workspaceId },
          { signal },
        );
      }
      if (hostKey) {
        return client.FleetService.getFleetWorkspaceViewState({ hostKey, id: workspaceId }, { signal });
      }
      return client.WorkspacesService.getWorkspaceViewState({ id: workspaceId }, { signal });
    }),
  );
}

export function saveWorkspaceViewState(workspaceId: string, hostKey: string | undefined, activeTab: WorkflowTabKey) {
  return workspaceRequests(workspaceId, hostKey).withPermit(
    executeGeneratedApiRequest("save workspace tab", (client, signal) => {
      const body = { active_tab: activeTab };
      if (hostKey?.startsWith("devbox:")) {
        return client.DevboxesService.updateDevboxWorkspaceViewState(
          { connectionId: hostKey.slice(7), id: workspaceId },
          body,
          { signal },
        );
      }
      if (hostKey) {
        return client.FleetService.updateFleetWorkspaceViewState({ hostKey, id: workspaceId }, body, { signal });
      }
      return client.WorkspacesService.updateWorkspaceViewState({ id: workspaceId }, body, { signal });
    }),
  );
}
