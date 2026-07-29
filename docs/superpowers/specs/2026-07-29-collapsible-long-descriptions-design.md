# Collapsible Long Descriptions

## Goal

Keep unusually long pull request and issue descriptions from dominating the detail view while preserving the full description as the default presentation.

## Scope

This change applies to provider pull request and issue detail descriptions. Kata task descriptions are a separate mode and are not part of this change.

## Behavior

- A description is considered long when its Markdown source exceeds 1,500 characters.
- Short descriptions keep their current presentation and do not show a collapse control.
- Long descriptions render fully expanded by default.
- The description header shows `Collapse` while expanded and `Expand` while compact.
- The control exposes the current state with `aria-expanded`.
- The compact state limits the description card to 320px and gives the card its own vertical scrollbar.
- Navigating to a different pull request or issue resets the description to expanded.
- Editing, copying, rendered Markdown, interactive task lists, and drag behavior remain unchanged.

## Component Design

A shared detail-description wrapper owns the long-description threshold, expanded state, header control, and compact scrolling styles. Pull and issue detail views continue to own their existing Markdown rendering and provider-specific interactions, passing the Markdown source and rendered content into the wrapper.

The wrapper keeps the threshold and compact height identical across both detail types without coupling their mutation or drag handlers. Its state is transient and per rendered item; it is not written to local storage or the URL.

## Verification

Component tests cover both pull request and issue descriptions:

- source at or below the threshold has no collapse control;
- source above the threshold has a control and starts expanded;
- collapse and expand update the accessible state and compact styling;
- changing item identity restores the expanded state.

The affected frontend test suite and Svelte checks run after the final edit.
