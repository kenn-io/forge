// Extracts the USD cost from a roborev job's token_usage JSON blob.
// Returns null when the job has no reported cost.
export function parseCostUsd(tokenUsage: string | undefined): number | null {
  if (!tokenUsage) return null;
  try {
    const usage: unknown = JSON.parse(tokenUsage);
    if (
      typeof usage !== "object" ||
      usage === null ||
      !("has_cost" in usage) ||
      !("cost_usd" in usage) ||
      usage.has_cost !== true ||
      typeof usage.cost_usd !== "number"
    ) {
      return null;
    }
    return usage.cost_usd;
  } catch {
    return null;
  }
}
