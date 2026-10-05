# External PR context

Kenn Forge can show private information beside a pull request without posting
a comment. An external integration supplies a card with a summary, formatted
details, and optional action buttons. For example, a performance service can
show results and offer **Run benchmarks**.

## Configure an integration

Install an adapter that implements Forge's external-context JSON contract,
then add it to `config.toml`:

```toml
[[external_context]]
id = "performance"
name = "Performance"
command = ["/opt/forge-integrations/performance-context"]
timeout = "10s"
```

Give each source a unique ID. The first command argument must be an absolute
executable path on the machine running Forge. Additional arguments are passed
directly. Authenticate the adapter's CLI on that machine; it runs with the
daemon's environment, which may differ from your terminal.

Executable configuration is available only in TOML. Browser Settings cannot
change it. Forge reloads valid edits without a restart. The timeout defaults
to ten seconds and covers fetching context or submitting an action, not the
duration of an external job.

The adapter must implement the protocol; pointing this at an arbitrary CLI
that happens to print JSON is not sufficient. Integration authors can find
the [version 1 request and card contract](https://github.com/kenn-io/forge/blob/main/context/external-pr-context.md)
in the repository. Adapters may call a CLI, HTTP API, or MCP
server.

## Use the card

Open a PR supported by the integration. Its card appears above the
description. Click a card's summary to show details and any text boxes for
actions; other action buttons and **Refresh** stay visible. Choose **Refresh**
to fetch the current external status.
Queued work can refresh automatically while the page is visible.

Results for a different commit are labeled as older results. Actions receive
the commit shown in Forge; if Forge has since synced a different head, refresh
the PR before trying again. An action failure may mean the external service
accepted the request before its response was lost. Refresh to check before
submitting another run.

In a fleet, the browser uses the sources configured on the node it connects
to. A hub page uses the hub's sources even when a PR workspace lives on a
spoke. Each node needs its own adapter configuration and credentials.

Cards do not change merge rules or publish provider comments or checks.
