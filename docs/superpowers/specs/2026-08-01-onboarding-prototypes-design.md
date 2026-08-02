# Onboarding Prototype Gallery Design

## Goal

Explore three distinct ways to get a new maintainer from an installed,
authenticated `gh` CLI to configured repositories, a useful pull-request view,
and a first workspace. The output is a contained, interactive mockup gallery,
not a production onboarding implementation.

## Selected direction

Prototype A, the focused linear setup path, is the direction selected for a
production design pass. Preserve its five-step activation sequence and clear
handoff into the regular application. The other prototypes remain useful
references for resumability and durable help, but are not the primary first-run
experience.

## Research signals

- [GitHub Desktop's getting-started guide](https://docs.github.com/en/desktop/overview/getting-started-with-github-desktop)
  uses a concrete sequence: authenticate, configure basics, then create, add, or
  clone a repository before introducing pull-request workflows.
- [Tailscale's quickstart](https://tailscale.com/docs/how-to/quickstart)
  makes setup progress observable. Devices appear in the browser as they are
  authenticated, and the flow ends with an explicit handoff into the regular
  admin console.
- [Linear's Start Guide](https://linear.app/docs/start-guide) separates a short
  orientation from configuration, offers a safe demo before setup, and routes
  different users to task-oriented follow-up material.
- [Sentry's project setup walkthrough](https://docs.sentry.io/product/sentry-basics/integrate-frontend/create-new-project/)
  pairs a focused setup task with a tailored configuration guide, then ends at
  the first useful product surface rather than at "setup complete."

Together these suggest that Kenn Forge should treat the first visible pull
request and first launchable workspace as activation milestones. Repository
configuration alone is not the finish line.

## Shared activation model

All three prototypes use the same five milestones:

1. Detect `gh` and confirm its authenticated GitHub identity.
2. Choose a small set of repositories from `gh api user/repos` results.
3. Persist those repositories and make first-sync progress visible.
4. Open a real pull request from the synced result set.
5. Create or open a workspace from that pull request.

The prototypes use generic synthetic data and local component state. They do
not call APIs, persist settings, or alter normal application routes.

## Prototype A: Focused setup wizard

A temporary full-width setup surface removes the normal maintainer navigation
until the user reaches a useful PR. A compact progress rail keeps the five
milestones visible. The main step detects `gh`, presents a searchable
multi-select repository list, shows first-sync progress, and hands off into a
small PR list with a highlighted workspace action.

This is the clearest and fastest route for a brand-new installation. Its cost
is that experienced users cannot freely explore the app until they exit or
complete the flow.

## Prototype B: Activation checklist in the app shell

The normal Kenn Forge top bar and pull-request layout appear immediately. A
persistent setup rail occupies one side and advances as the user completes
real actions. Repository selection happens inline; the main pane changes from
an explanatory empty state to a populated PR list, then points at the existing
workspace action.

This preserves product context and makes onboarding resumable. It is less
focused than the wizard and asks a new user to parse more of the interface at
once.

## Prototype C: Task-oriented start guide

A "Start here" page behaves like concise product documentation inside the app.
It explains the three tasks that matter, shows detected command status and
expected results beside each task, and includes a live preview of the PR and
workspace surfaces. It remains available as durable help after activation.

This is the best bridge between in-app and docs-based onboarding and the least
intrusive option for expert users. It relies more on user initiative, so the
path is easier to abandon before activation.

## Gallery presentation

The hidden `/design-system` route gains an onboarding section near the top.
Three labelled tabs switch between browser-like prototype frames. Each tab
also states the idea, strength, and trade-off so the comparison remains useful
without implementation notes. Interactions are deliberately shallow and
reversible: selecting repositories, advancing mock progress, and switching
guide steps.

The gallery inherits Kenn Forge's theme tokens and system typography. The
color strategy is restrained: neutral application chrome, one blue action and
selection accent, and semantic green/warning status only. Layouts target a
desktop maintainer working in the normal app shell; the gallery itself remains
usable at narrow widths, but these are desktop workflow explorations rather
than mobile designs.

## Accessibility and verification

- Prototype switching uses a labelled tablist with selected state.
- All mock controls are keyboard-reachable buttons, checkboxes, or links.
- Progress is communicated with labels and icons, not color alone.
- Reduced-motion users receive no decorative transitions.
- A focused jsdom component test covers the gallery's variant switching.
- Svelte autofix, frontend type/check tooling, the focused component test, and
  a browser inspection of all three variants verify the artifact.
