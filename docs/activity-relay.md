# Faster GitHub updates

With a shared **activity relay**, Kenn Forge notices GitHub changes sooner.
GitHub sends a webhook to the relay when something changes, the relay tells
each connected Forge what changed, and each Forge fetches those items from
GitHub using its own credentials.

The relay writes no activity to disk. It forwards only which repository and
item changed, and it never keeps pull request titles, comments, code, or GitHub
access tokens. Everyone can keep their existing GitHub App or token.

Use a relay for GitHub.com repositories where you can configure webhooks.
Delivery is best effort. If a Forge disconnects, falls behind during a burst,
or has too many refreshes waiting, it can miss a change even while it shows as
connected. Normal Forge syncing picks up anything missed, and GitHub delivery
delays and API limits can still delay an update.

## Connect your Forge

Before you start, ask the person running the relay for its **private feed URL**.
If the relay uses Tailscale, connect to the same private network and make sure
your account has access to the feed.

Configure the relay URL on the Forge that syncs with GitHub. For a
[federated fleet](federated-fleet.md), configure the hub; spokes receive
updates from their hub. Keep your repositories selected and syncing in Forge.
Connecting a relay does not add repositories or grant GitHub access.

1. Add the supplied URL to your Forge configuration:

   ```toml
   [relay]
   url = "https://relay.example.com"
   ```

   Use the HTTPS origin only, without `/activity` or a webhook path.

2. Restart Forge after changing this setting.

To disable the relay, remove the URL or set it to an empty string and restart
Forge.

To see whether Forge is connected and check recent changes for your
repositories, click **Relay** in the bottom bar. The button is hidden when the
relay is off. Forge reconnects on its own after an outage. The list holds the
latest 20 changes since Forge started, and an entry means a hint arrived, not
that the refresh has finished.

### GitHub Actions

When the relay receives `workflow_run` and `check_run` webhooks, it collects
Actions updates in one-minute batches. Each batch sends one checks hint per
pull request and one workflow-list hint per repository, and repeated events do
not restart the timer. Forges receive the batch rather than the individual
events.

When a hint arrives, Forge refreshes check results without another timed wait.
Forge fetches only checks, and the usual GitHub API limits still apply.

For open pull requests already in Forge, PR checks update when GitHub includes
their PR numbers in the event, while checks without a PR reference use normal
syncing. While **Actions** is open, workflow activity refreshes the selected
workflow's run list, including runs without a PR, and other repositories are
not fetched. Other pull request and issue updates pass through the relay
immediately. You do not need any extra Forge configuration.

## Run a shared relay

One small server can receive webhooks for several repositories and serve
several Forges. The relay runs as a separate `kenn-forge-relay` process.

Before you set up a relay, make sure you have:

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

Run the service under its own operating-system account, and give it a
signing-secret file that only the service and its administrator can read.
Store the secret outside the configuration file, and keep it out of command
arguments and source control.

1. Create a relay configuration file:

   ```toml
   webhook_listen = "127.0.0.1:8081"
   feed_listen = "127.0.0.1:8082"

   [sources.team]
   secret_file = "/etc/forge-relay/credentials/team"
   repository_ids = [12345]
   ```

   Replace `12345` with your repository's numeric GitHub ID, which you can look
   up with `gh api repos/team/project --jq .id`. List every repository that
   this source may report changes for. Repository names can change, while these
   IDs stay the same.

   Here, `team` is the source label used in the webhook URL. Choose 1 to 64
   lowercase letters, digits, or hyphens, starting with a letter or digit, for
   example `github-app`. Spaces and underscores are not allowed. When another
   GitHub App or webhook needs a different secret or repository list, add
   another section such as `[sources.other]`.

2. Start the service with your configuration file:

   ```sh
   tmp/kenn-forge-relay --config /path/to/relay.toml
   ```

For an unattended server, use a service manager to start the service after a
reboot and restart it if it exits.

### 2. Set up the two HTTPS addresses

Both relay ports listen only on the server itself. Route public and private
traffic separately:

| Address | Who uses it | Where it forwards |
| --- | --- | --- |
| Public webhook URL, ending in `/webhooks/github/team` | GitHub | Port `8081`, accepting only POST requests to the configured webhook paths |
| Private feed URL | Developers' Forges | Port `8082` |

Use an HTTPS reverse proxy, such as Caddy, for the public webhook URL. Keep
ports `8081` and `8082` closed to direct external connections, and configure
the public proxy to reject all other paths, including `/activity` and
`/healthz`.

Each connected Forge holds one long-lived connection to the private feed. Your
private proxy must pass streaming responses through without buffering and must
not close idle connections faster than every 20 seconds; the relay sends a
keepalive at that interval. Tailscale Serve handles both.

If your server is already enrolled in Tailscale, publish the private feed with:

```sh
sudo tailscale serve --bg --https=443 http://127.0.0.1:8082
```

Use the HTTPS URL that Tailscale prints as the Forge relay URL. Tailscale may
first ask an administrator to enable HTTPS. See the
[Tailscale Serve instructions](https://tailscale.com/docs/reference/tailscale-cli/serve).

The feed has no separate password or login. Its private network controls who
can read it. Grant developers feed access, prevent the relay from starting
connections to their machines, and leave Tailscale Funnel disabled. Confirm
these rules with real connection attempts from allowed and denied devices.

### 3. Configure GitHub webhooks

Start with one repository. In its **Settings → Webhooks**, add a webhook with
the public URL ending in `/webhooks/github/team`. Choose JSON, keep SSL
verification enabled, and enter the same secret stored on the relay. See
GitHub's [webhook setup instructions](https://docs.github.com/en/webhooks/using-webhooks/creating-webhooks).

Select these events. GitHub's settings page displays friendly names, and the
names below are also the values used when configuring hooks through its API:

```text
pull_request, pull_request_review, pull_request_review_comment,
pull_request_review_thread, issues, issue_comment, push, create,
delete, repository, workflow_run, check_run
```

Use `workflow_run` for workflow activity and `check_run` for individual check
updates. Leave `check_suite`, `status`, and `workflow_job` off; the relay
ignores them. Upgrade connected Forges **before** deploying the updated relay:
older consumers cannot read the new workflow-list hints. After that, enable the
webhook events. The relay batches the events, and Forge processes the resulting
hints without another delay.

A GitHub App can use the same webhook URL and secret. Configure its events and
repository access in the App settings. Each Forge can continue using a
different App or token to fetch repository content.

### 4. Check a real update

1. From a developer machine, request `/healthz` on the private URL. Confirm
   that the relay returns HTTP `204` with no response body, and confirm that
   the public URL returns `404` for the same path and for `/activity`.
2. Connect a Forge, restart it, and let its initial sync finish. Open a test
   pull request that Forge already tracks.
3. Change that pull request's title on GitHub. In the webhook's **Recent
   deliveries**, confirm that GitHub received HTTP `204` from the relay.
4. Keep the pull request open in Forge and wait for the title to update without
   clicking Sync. Restore the title and confirm that change updates too.
5. Open **Actions**, select a workflow, and run it on GitHub. Its run status
   should update in Forge after the relay's next one-minute batch. If the run
   belongs to a tracked open pull request, check the pull request's checks too.

A successful webhook delivery proves that the relay received the event, and
seeing the change in Forge completes the check. To measure the relay's latency,
record both times and confirm that a normal sync did not occur between them.

## Data, outages, and troubleshooting

The relay keeps no data. It passes each change to the connected Forges and
discards it. Repository IDs and timing still reveal activity in transit, so
keep request-body recording disabled in the proxy and monitoring tools.

If a Forge misses a hint, it catches up through ordinary syncing, and the relay
does not replay past hints. Pending check batches are held only in memory and
are discarded on restart. Refreshes that cannot run right away, for example
because the GitHub API budget is spent, are dropped for the same reason.

| Symptom | Check |
| --- | --- |
| GitHub reports `401` | Confirm that GitHub and the relay use the same signing secret. |
| GitHub reports `400` | Check the source's numeric repository ID list. |
| GitHub reports `404` | Check the public proxy route and source label in the URL. |
| Relay shows disconnected | Check Tailscale connectivity, access rules, and the private HTTPS URL. Forge keeps retrying with increasing delays up to 30 seconds. |
| Deliveries succeed but Forge stays stale | Confirm that the repository is selected, syncing is enabled, and Forge's GitHub credentials can read it. API limits can delay refreshes. |

GitHub [does not automatically resend failed deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).
After fixing a problem, you can redeliver an event from GitHub, and regular
Forge syncing also catches missed changes.
