# External PR context

Kenn Forge can show private information beside a pull request without posting
a comment. An external integration supplies a card with a summary, formatted
details, and optional action buttons. For example, a performance service can
show results and offer **Run benchmarks**.

## Configure an integration

1. Install an adapter that implements Forge's external-context JSON contract.

   The adapter must implement the protocol; pointing this at an arbitrary CLI
   that happens to print JSON is not sufficient. Adapters may call a CLI, HTTP
   API, or MCP server. Integration authors can find the
   [version 1 request and card contract](https://github.com/kenn-io/forge/blob/main/context/external-pr-context.md)
   in the repository.

2. Add the adapter to `config.toml`:

   ```toml
   [[external_context]]
   id = "performance"
   name = "Performance"
   command = ["/opt/forge-integrations/performance-context"]
   timeout = "10s"
   ```

   Give each source a unique ID. The first command argument must be an absolute
   executable path on the machine running Forge, and Forge passes any
   additional arguments directly.

3. Authenticate the adapter's CLI on that machine. It runs with the daemon's
   environment, which may differ from your terminal.

Executable configuration is available only in TOML and cannot be changed in
browser Settings. Forge reloads valid edits without a restart. The timeout
defaults to ten seconds and covers fetching context or submitting an action,
not the duration of an external job.

## Use the card

Open a pull request supported by the integration to see its card above the
description. Expand **Details** for tables and report links. When a card has
details, text boxes for actions sit inside **Details**; on cards without
details, they appear directly on the card. Other action buttons stay visible.
Choose **Refresh** to fetch the current external status. Queued work can
refresh automatically while the page is visible.

An integration can also offer advice as a **Merge** or **Hold** chip with a
short reason, shown above **Details**. Advice does not change merge rules or
the merge controls.

Results for a different commit are labeled as older results. Actions receive
the commit shown in Forge. If Forge has since synced a different head, refresh
the pull request before trying again. If an action fails, the external service
may have accepted the request before its response was lost. Refresh to check
before submitting another run.

In a fleet, the browser uses the sources configured on the node it connects to.
A hub page uses the hub's sources even when a PR workspace lives on a spoke,
and each node needs its own adapter configuration and credentials.

Cards do not change merge rules or publish provider comments or checks.
