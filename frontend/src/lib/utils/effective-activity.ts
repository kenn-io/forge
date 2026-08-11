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
