# Faster GitHub updates

Kenn Forge can use a shared **activity relay** to notice GitHub changes sooner.
GitHub sends the relay a webhook, a message saying that something changed.
The relay passes that change to the connected Forges right away, and each
Forge fetches the changed items from GitHub using its own credentials.

The relay stores nothing. It passes on which repository and item changed and
never keeps pull request titles, comments, code, or GitHub access tokens.
Everyone can keep their existing GitHub App or token.

Use this for GitHub.com repositories where you can configure webhooks.
Delivery is best effort: a Forge that is disconnected, falls behind during a
burst, or has too many refreshes waiting can miss a change even while it
shows as connected. Normal Forge syncing picks up anything missed. GitHub
delivery delays and API limits can still delay an update.

## Connect your Forge

Ask the person running the relay for its **private feed URL**. If the relay
uses Tailscale, connect to the same private network and make sure your account
has access to the feed.

Add this to your Forge configuration, using the supplied URL:

```toml
[relay]
url = "https://relay.example.com"
```

Use the HTTPS origin only, without `/activity` or a webhook path. Restart
Forge after changing this setting. Remove the URL or set it to an empty
string to disable the relay.

Configure this on the Forge that syncs with GitHub. For a
[federated fleet](federated-fleet.md), that means the hub; spokes receive
updates from their hub. Keep the repositories selected and syncing in Forge.
Connecting a relay does not add repositories or grant GitHub access.

Click **Relay** in the bottom bar to see whether Forge is connected and the
recent changes received for your repositories. Forge reconnects on its own
after an outage. The list holds the latest 20 changes since Forge started; an
entry means a hint was received, not that the refresh has finished. The
button is hidden when the relay is off.

## Run a shared relay

One small server can receive webhooks for several repositories and serve
several Forges. The relay runs as a separate `kenn-forge-relay` process.

You need:

- A server with a public HTTPS address for GitHub webhooks.
- A private HTTPS address that developers can reach, such as Tailscale Serve.
- Permission to configure repository webhooks or a GitHub App's webhooks.
- A random signing secret shared between GitHub and the relay. Developers
  connecting their Forges do not need this secret.

### 1. Install and configure the service

Download a matching Linux relay archive and its checksums from
[Forge releases](https://github.com/kenn-io/forge/releases), when available.
Relay archives use the name `forge-relay_VERSION_linux_ARCH.tar.gz`.
To build from a Forge checkout instead, run:

```sh
make build-relay
```

This creates `tmp/kenn-forge-relay` for the machine doing the build and does
not need a frontend build.

Run the service under its own operating-system account. Give it a
signing-secret file that only the service and its administrator can read.
Store the secret outside the configuration file. Do not put it in command
arguments or source control.

Create a relay configuration file:

```toml
webhook_listen = "127.0.0.1:8081"
feed_listen = "127.0.0.1:8082"

[sources.team]
secret_file = "/etc/forge-relay/credentials/team"
repository_ids = [12345]
```

Replace `12345` with your repository's numeric GitHub ID. You can look it up
with `gh api repos/team/project --jq .id`. List every repository that this
source may report changes for. Names can change; these IDs stay the same.

Here, `team` is the source label used in the webhook URL. Choose 1–64
lowercase letters, digits, or hyphens, starting with a letter or digit;
for example, `github-app`. Spaces and underscores are not allowed.
Add another section, such as `[sources.other]`, when another GitHub App or webhook
needs a different secret or repository list.

Start the service with the configuration file:

```sh
tmp/kenn-forge-relay --config /path/to/relay.toml
```

For an unattended server, use a service manager to start it after a reboot
and restart it if it exits.

### 2. Set up the two HTTPS addresses

Both relay ports listen only on the server itself. Use separate public and
private routes:

| Address | Who uses it | Where it forwards |
| --- | --- | --- |
| Public webhook URL, ending in `/webhooks/github/team` | GitHub | Port `8081`, accepting only POST requests to the configured webhook paths |
| Private feed URL | Developers' Forges | Port `8082` |

Use an HTTPS reverse proxy, such as Caddy, for the public webhook URL. Keep
ports `8081` and `8082` closed to direct external connections. The public
proxy must reject all other paths, including `/activity` and `/healthz`.

Forges hold one long-lived connection each to the private feed. The private
proxy must pass streaming responses through without buffering and must not
close idle connections faster than every 20 seconds; the relay sends a
keepalive at that interval. Tailscale Serve handles both.

For a server already enrolled in Tailscale, publish the private feed with:

```sh
sudo tailscale serve --bg --https=443 http://127.0.0.1:8082
```

Use the HTTPS URL printed by Tailscale as the Forge relay URL. Tailscale
may first ask an administrator to enable HTTPS. See the
[Tailscale Serve instructions](https://tailscale.com/docs/reference/tailscale-cli/serve).

The feed has no separate password or login. Its private network controls who
can read it. Grant developers feed access, keep the relay from starting
connections to their machines, and leave Tailscale Funnel disabled. Confirm
these rules with real connection attempts from allowed and denied devices.

### 3. Configure GitHub webhooks

Start with one repository. In its **Settings → Webhooks**, add a webhook
with the public URL ending in `/webhooks/github/team`. Choose JSON, keep
SSL verification enabled, and enter the same secret stored on the relay.
See GitHub's [webhook setup instructions](https://docs.github.com/en/webhooks/using-webhooks/creating-webhooks).

Select these events. GitHub's settings page displays friendly names; the
names below are also the values used when configuring hooks through its API:

```text
pull_request, pull_request_review, pull_request_review_comment,
pull_request_review_thread, issues, issue_comment, push, create,
delete, repository
```

Do not subscribe to `check_run`, `check_suite`, `workflow_run`, `status`,
or `workflow_job`. The relay ignores these events so check updates do not
crowd out other activity. Check results still update through normal Forge
syncing.

A GitHub App can use the same webhook URL and secret. Configure its events
and repository access in the App settings. Each Forge can continue using a
different App or token to fetch repository content.

### 4. Check a real update

1. From a developer machine, request the private URL's `/healthz` path.
   A working relay returns HTTP `204` with no response body. Confirm that the
   public URL returns `404` for the same path and for `/activity`.
2. Connect a Forge, restart it, and let its initial sync finish. Open a test
   pull request that Forge already tracks.
3. Change that pull request's title on GitHub. In the webhook's **Recent
   deliveries**, confirm that GitHub received HTTP `204` from the relay.
4. Keep the pull request open in Forge and wait for the title to update
   without clicking Sync. Restore the title and check that change too.

A successful webhook delivery proves that the relay received the event.
Seeing the change in Forge completes the check. To measure the relay's
latency, record both times and check that a normal sync did not occur
between them.

## Data, outages, and troubleshooting

The relay keeps no data. Each change is passed to the connected Forges and
discarded. Repository IDs and timing still reveal activity in transit, so keep
request-body recording disabled in the proxy and monitoring tools.

A Forge that is disconnected when a change happens does not receive it later.
Ordinary syncing picks up the change on its next pass, so a relay or Forge
restart needs no recovery step. Refreshes that cannot run right away, for
example because the GitHub API budget is spent, are dropped for the same
reason.

| Symptom | Check |
| --- | --- |
| GitHub reports `401` | GitHub and the relay must use the same signing secret. |
| GitHub reports `400` | Check the source's numeric repository ID list. |
| GitHub reports `404` | Check the public proxy route and source label in the URL. |
| Relay shows disconnected | Check Tailscale connectivity, access rules, and the private HTTPS URL. Forge keeps retrying with increasing delays up to 30 seconds. |
| Deliveries succeed but Forge stays stale | Confirm the repository is selected, syncing is enabled, and Forge's GitHub credentials can read it. API limits can delay refreshes. |

GitHub [does not automatically resend failed deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).
You can redeliver an event from GitHub after fixing a problem; regular Forge
syncing also catches missed changes.
