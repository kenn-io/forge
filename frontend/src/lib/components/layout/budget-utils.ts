export interface HostBudgetEntry {
  remaining: number;
  limit: number;
  known: boolean;
}

export function budgetColor(ratio: number): string {
  if (ratio > 0.2) return "var(--budget-green)";
  if (ratio > 0.05) return "var(--budget-yellow)";
  return "var(--budget-red)";
}

/**
 * Returns the lowest remaining/limit ratio across known hosts.
 * Returns -1 if no hosts have known data.
 */
export function worstCaseRatio(entries: HostBudgetEntry[]): number {
  let worst = Infinity;
  let hasKnown = false;
  for (const e of entries) {
    if (!e.known || e.limit <= 0 || e.remaining < 0) continue;
    hasKnown = true;
    const ratio = e.remaining / e.limit;
    if (ratio < worst) worst = ratio;
  }
  return hasKnown ? worst : -1;
}

/**
 * Format a number compactly: 4200 → "4.2k", 5000 → "5k", 75 → "75".
 * Numbers below 1000 are returned as-is.
 */
export function formatCompact(n: number): string {
  if (n < 1000) return String(n);
  const k = n / 1000;
  return k === Math.floor(k) ? `${k}k` : `${k.toFixed(1)}k`;
}

/**
 * Aggregates budget across hosts, excluding disabled ones (limit == 0).
 * Budget data comes from SyncBudget which is independent of REST/GQL
 * rate limit observation — a host can have real budget spend even when
 * REST rate headers haven't arrived yet.
 */
export function aggregateBudget(entries: { budget_limit: number; budget_spent: number }[]): {
  spent: number;
  limit: number;
  hasAny: boolean;
} {
  let spent = 0;
  let limit = 0;
  let hasAny = false;
  for (const e of entries) {
    if (e.budget_limit <= 0) continue;
    hasAny = true;
    spent += e.budget_spent;
    limit += e.budget_limit;
  }
  return { spent, limit, hasAny };
}
