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

export function syncBudgetColor(spent: number, limit: number): string {
  if (limit <= 0) return "var(--text-muted)";
  return budgetColor(Math.max((limit - spent) / limit, 0));
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
 * Aggregates local emergency ceilings across hosts, excluding disabled ones.
 * These counters are independent of provider REST/GraphQL quota.
 */
export function aggregateBudget(entries: { limit: number; spent: number }[]): {
  spent: number;
  limit: number;
  hasAny: boolean;
} {
  let spent = 0;
  let limit = 0;
  let hasAny = false;
  for (const e of entries) {
    if (e.limit <= 0) continue;
    hasAny = true;
    spent += e.spent;
    limit += e.limit;
  }
  return { spent, limit, hasAny };
}
