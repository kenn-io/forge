# Workspace Create-and-Launch Sequencing Design

## Problem

Creating a PR or issue workspace with a selected agent has one user intent: create the workspace and launch that agent. The workspace can become visible with an empty runtime before the workspace-ID launch queue receives the selected target. The automatic empty-workspace launcher can therefore enter the DOM even though the user has already made the choice.

## Behavior Contract

- Selecting an agent from the Create Workspace menu must prevent the automatic launcher from entering the DOM from selection through session startup.
- Plain Create Workspace keeps the existing behavior: once the workspace is ready and has no sessions, the automatic launcher opens.
- A launcher opened manually remains open until the user dismisses it or a successful launch produces a session.
- If the selected launch becomes unavailable or fails, the intent clears and the automatic launcher may open as a recovery path.

## Design

Record the optional agent target with the item-scoped create lifecycle at the moment the user selects it. This happens before the create request starts, so an early workspace publication can still resolve its PR or issue identity to a pending create-and-launch intent.

On create success, promote the item-scoped intent to the workspace-ID launch queue before publishing the created workspace reference or clearing the create lifecycle. The transition must leave no observable state in which neither the item intent nor the workspace intent exists. Existing workspace launch claiming, acknowledgement, reconciliation, and expiry remain unchanged.

The terminal view treats either form of explicit intent as a hard automatic-launcher gate. The render condition also includes the gate, so stale automatic launcher state cannot mount while an intent is pending. User-opened launcher state is not gated.

## Data Flow

1. The user selects an available agent.
2. The detail surface records `{ item identity, target key }` and starts workspace creation.
3. Any early empty workspace view sees the item-scoped intent and does not open the automatic launcher.
4. The create response supplies the workspace ID; the store promotes the target to the workspace-ID queue before publishing the workspace reference.
5. The workspace view claims and launches the selected target.
6. The intent remains authoritative until the exact session appears or existing bounded reconciliation expires.

## Failure Handling

- Create failure clears the item-scoped intent in the existing finalizer.
- Selection changes or component teardown do not discard accepted work; the shared lifecycle store remains authoritative.
- Missing or newly unavailable targets follow the existing workspace launch rejection path, including its user-visible error and recovery launcher.
- Plain creation carries no target and therefore does not create a workspace launch intent.

## Verification

- A store test covers recording an item-scoped target, promoting it to a workspace ID, and failure cleanup.
- A component regression observes DOM additions and proves the automatic launcher never mounts while either intent form is pending.
- Existing tests continue to cover manual launcher retry behavior and plain empty-workspace auto-open behavior.
- The full-stack create-and-launch test holds create and launch responses independently and records any launcher appearance across both gaps.
