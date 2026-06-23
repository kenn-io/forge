import type { components } from "@middleman/ui/api/schema";

import { apiErrorMessage, client } from "./runtime.ts";

export type HostSummary = components["schemas"]["HostSummary"];

export async function loadSnapshotHosts(): Promise<HostSummary[]> {
  const { data, error } = await client.GET("/snapshot", {
    params: { query: { include_peers: true } },
  });
  if (!data) {
    throw new Error(apiErrorMessage(error, "Couldn't load hosts."));
  }
  return (data.hosts ?? []) as HostSummary[];
}
