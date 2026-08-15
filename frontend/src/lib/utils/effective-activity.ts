export interface EffectiveActivity {
  at: string;
  fromWorkspace: boolean;
}

export function effectiveActivity(providerAt: string, workspaceAt?: string): EffectiveActivity {
  if (workspaceAt && Date.parse(workspaceAt) > Date.parse(providerAt)) {
    return { at: workspaceAt, fromWorkspace: true };
  }
  return { at: providerAt, fromWorkspace: false };
}

export function latestActivityAt(eventAt: string, itemAt?: string): string {
  if (itemAt && Date.parse(itemAt) > Date.parse(eventAt)) {
    return itemAt;
  }
  return eventAt;
}
