# Detail Card Action Visibility

## Problem

The kit-ui `CommentCard` migration made edit, copy, and delete controls permanently visible in PR and issue detail timelines. The previous interaction kept this secondary chrome quiet until the user interacted with its card.

## Interaction contract

- On hover-capable devices, card action icons are hidden at rest and revealed when the card is hovered or contains keyboard focus.
- On non-hover devices, action icons remain visible so touch users can discover them.
- A focused action remains visible, and the reveal uses the existing fast opacity transition.
- Commit SHAs remain visible because they are identity metadata, not actions, even though they occupy the same kit card header region.

## Scope

Apply the behavior at the `EventTimeline` boundary. Do not change kit-ui's general `Card` or `CommentCard` contract, and do not alter compact-row or inline-reply controls that have their own interaction rules.

## Verification

Add focused coverage for the timeline-owned CSS contract, including the commit-card exemption and the touch fallback. Run the EventTimeline unit suite plus the UI package checks.
