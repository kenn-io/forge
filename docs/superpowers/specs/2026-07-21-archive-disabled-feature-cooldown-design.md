# Archive Disabled-Feature Cooldown Design

## Goal

Route archive inventory, maintenance, and hydration through the same per-repository issue or merge-request cooldown used by other background sync lanes. A disabled response must permit at most one provider probe per 24 hours while leaving the unaffected repository feature eligible for archive work.

## Architecture

Archive admission becomes feature-aware by receiving the archive item type alongside the provider request cost. The syncer maps that item type to the existing repository feature key, reserves the existing shared probe before provider-budget admission, and releases or renews that reservation when the archive provider operation completes.

Admission exposes a distinct feature-deferred outcome rather than representing the cooldown as provider-budget exhaustion. This keeps repository-wide budget state separate from a single disabled feature and lets archive scheduling preserve work without reporting a successful lookup.

No database schema change or second cooldown store is introduced. The existing in-memory cooldown remains the single authority for all background lanes.

## Data Flow

1. Inventory, maintenance, or hydration requests admission with its issue or merge-request item type.
2. The syncer attempts to reserve the shared repository-feature probe before checking archive rate and provider-work budgets.
3. If the feature is still cooling down, admission returns its retry deadline as an explicit feature deferral without starting provider work.
4. If admitted, archive executes one provider operation and completes admission with the operation error.
5. A repository-feature-disabled result renews the shared 24-hour cooldown. Success or a non-disabled failure releases the reservation.

## Scheduling Semantics

- A deferred hydration stays pending and receives an item-level retry deadline without incrementing its failure attempt count. It is never committed as `present`.
- A deferred inventory or maintenance page leaves its scan generation and cursor unchanged.
- A pre-call feature deferral may be skipped within the same provider-host worker pass so another repository or the unaffected item type can proceed.
- A provider call that discovers a disabled feature still ends the current bounded worker pass; the next pass observes the newly established cooldown and skips that scope without another provider request.
- Provider-budget admission remains repository-wide and retains its existing durable budget-wait behavior.

## Error Classification

GitHub archive inventory converts wrapped HTTP 410 responses containing the provider's definitive "issues are disabled" or "pull requests are disabled" messages into `repository_feature_disabled` errors before generic archive transport mapping. Hydration completion applies the same shared classifier so canonical live item sync errors renew the same gate.

## Testing

Focused archive integration coverage will use the real archive service, syncer admission, and SQLite state to prove:

- repeated disabled issue inventory work performs one provider request while merge-request inventory can still advance;
- disabled hydration performs one provider request, remains pending with the cooldown retry deadline, and does not become `present`;
- advancing the clock past the cooldown permits one new probe;
- non-disabled failures continue through existing retry handling;
- existing provider-budget and preemption behavior remains unchanged.
