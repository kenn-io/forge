# Product

## Register

product

## Users

Kenn Forge is for maintainers who manage pull requests, issues, reviews, CI signals, and local workspaces across a small fixed set of repositories. They are usually already fluent in their code forge and want a calmer, faster command surface than provider notifications.

Their context is task-oriented: scanning what changed, finding what needs attention, reviewing or merging work, and returning to focused development without losing state.

## Product Purpose

Kenn Forge is a local-first maintainer console for GitHub, GitLab, Forgejo, and
Gitea repositories. It syncs provider data into SQLite, serves a fast Svelte
interface, and keeps maintainers out of notification clutter while preserving
the actions they need: triage, review, comment, merge, track activity, and
manage repository settings.

Success looks like an expert user understanding the state of their repos at a glance, moving through items with keyboard navigation, and taking action without bouncing through multiple provider pages.

## Brand Personality

Expert, dense, calm, and sharp. The interface should feel deliberate and efficient: compact enough for high-signal scanning, calm enough for repeated daily use, and crisp enough that status, ownership, and next action are immediately legible.

## Anti-references

Avoid provider notification clutter, overloaded activity streams, and surfaces where everything competes for attention. Avoid excessive animation, decorative motion, generic SaaS gloss, inflated controls, and ornamental dashboard styling that slows down expert triage.

## Design Principles

- Maintain the user's scan line: prioritize compact hierarchy, stable columns, and fast recognition over decorative presentation.
- Make state speak quietly: use semantic color, typography, and placement to clarify status without turning the UI into an alert wall.
- Keep expert flow intact: keyboard navigation, predictable shortcuts, and preserved context matter more than onboarding spectacle.
- Treat overlays as shared infrastructure: dropdowns, popovers, menus, and tooltips must float above split panes, sidebars, resize handles, drawers, and other overflow-constrained containers instead of being clipped by their local layout.
- Local-first should feel trustworthy: sync, error, and data freshness states must be visible without being noisy.
- Code-forge fluency is assumed, but provider clutter is not copied.

## Accessibility & Inclusion

Keyboard navigation is a core product affordance, not a bonus. The interface should preserve visible focus states, make primary flows reachable without a pointer, avoid motion that is not tied to state, and keep reduced-motion users comfortable.

Aim for WCAG AA contrast for text and controls, with status communication that does not rely on color alone.
