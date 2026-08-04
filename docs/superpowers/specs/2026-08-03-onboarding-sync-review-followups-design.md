# Onboarding Sync Review Follow-ups Design

## Goal

Prevent first-run onboarding from completing against stale sync status and ensure an onboarding flow that remains mounted advances when repository configuration changes elsewhere in the application.

## Sync Baseline

A triggered sync may begin before the server publishes its running state. The sync store therefore compares idle responses with the last completion timestamp from before the trigger. If no local sync status has loaded, the store must fetch `/sync/status` before posting `/sync` and use that response as the authoritative baseline.

After the trigger, the existing centralized status application path continues to accept completion only when the server reports `running: true` or an idle `last_run_at` advances beyond the known baseline. An unchanged historical timestamp remains stale. This preserves fast-sync handling without relying on client wall-clock time.

If the pre-trigger status request fails or has no usable status, an idle timestamp alone is not proof that the new run completed. The store remains optimistically running until it observes server-side running; normal trigger errors still restore the previous local state.

## Mounted Onboarding Transition

`OnboardingFlow` derives whether repositories are configured from the settings store. While the flow is mounted in the repository phase, a transition from no configured repositories to configured repositories must move the flow to the sync phase and call the existing idempotent sync starter.

The transition is edge-triggered and phase-guarded. Initial configured state remains handled by initialization and `onMount`; subsequent settings updates start sync once without re-triggering after later reactive updates.

## Testing

- Add a sync-store regression where local status starts as `null`, the pre-trigger fetch establishes a historical baseline, and the immediate post-trigger idle response repeats that timestamp. The store must remain running and must not notify completion listeners.
- Add an onboarding component regression where configuration changes after mount. The repository picker must yield to the sync phase and `triggerSync` must run exactly once.
- Run focused tests first, followed by the full frontend unit suite, Svelte diagnostics, formatting, and the affected frontend browser suite only if the component behavior requires a real browser boundary.
