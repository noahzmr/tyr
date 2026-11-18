# Webhooks

Tests tell a webhook how they are doing: when they start, at once when a
step fails or ends with a warning (no need to wait four hours for a burn-in
to end), and when they finish with the verdict. `tyr accept` (and so the
Tyr ISO) and `tyr stress` send them.

| Event | When | Level |
|---|---|---|
| `started` | a test starts; with the expected duration and the first health check | info |
| `step_failed` | a step failed: a stress test FAIL or ABORTED, a step that could not run, critical findings in the health check | error |
| `step_warning` | a step ended with a warning | warning |
| `pass` | finished: PASS | ok |
| `remarks` | finished: REMARKS (acceptance) or WARN (stress) | warning |
| `fail` | finished: FAIL, with the failures | error |
| `incomplete` | finished: INCOMPLETE or ABORTED | warning |

Every event carries the machine (host, vendor, model, serial number), the
profile or components, the step and its result, a summary, failures and
remarks, the duration and where the results are.

## Formats

| `format` | For | Body |
|---|---|---|
| `json` (default) | your own endpoint, n8n, Node-RED, a ticket system | the event as JSON (below) |
| `slack` | Slack, Mattermost, Rocket.Chat incoming webhooks | `{"text": …}` with an emoji per level |
| `teams` | Microsoft Teams: a *Workflows* webhook ("Post to a channel when a webhook request is received") | an Adaptive Card: coloured title, facts, failures |
| `discord` | Discord channel webhooks | `{"content": …}` (2000 characters, no mentions) |
| `gchat` | Google Chat space webhooks | `{"text": …}` |

The generic JSON:

```json
{
  "event": "fail",
  "time": "2026-11-17T13:02:11Z",
  "run": "acceptance",
  "host": "tyr-live",
  "vendor": "HPE",
  "product": "ProLiant DL360 Gen10",
  "serial": "CZ2D1234AB",
  "profile": "burnin",
  "status": "FAIL",
  "failures": ["DIMM PROC 1 DIMM 3: uncorrectable ECC error"],
  "duration": "4h02m",
  "results_dir": "/mnt/results/tyr/CZ2D1234AB-20261117T090000Z",
  "tyr_version": "1.4.0"
}
```

Tyr never prints a webhook's URL: errors, `tyr notify` and the ISO's screen
name it by scheme and host (`https://hooks.slack.com/…`), since the path of a
chat webhook is its secret.

Every request has `X-Tyr-Event: <event>`. With a `token` it carries
`Authorization: Bearer <token>`; with a `secret`, `X-Tyr-Signature:
sha256=<hex>`, the HMAC-SHA256 of the body with the secret, so the receiver
can check it came from your Tyr. A receiver that answers 5xx or 429, or
cannot be reached, is tried again after 5 and 30 seconds; a 4xx is not
retried. Delivery runs in the background and never holds up or changes a
test; at the end Tyr waits at most two minutes for the last messages.

## Settings

`webhooks.json`, mode 0600 (tokens and secrets), in `/etc/tyr` for root
(`tyr accept`, the ISO) and `~/.config/tyr` for a user; `--webhooks FILE`
names another. Edit it with `W` in the TUI or by hand:

```json
{
  "webhooks": [
    { "name": "ops", "url": "https://hooks.slack.com/services/T000/B000/XXXX", "format": "slack",
      "events": ["started", "step_failed", "fail", "incomplete"] },
    { "name": "teams", "url": "https://prod-12.westeurope.logic.azure.com:443/workflows/…", "format": "teams" },
    { "name": "inventory", "url": "https://n8n.example.com/webhook/tyr", "format": "json",
      "token": "…", "secret": "…", "headers": { "X-Team": "dc1" } }
  ]
}
```

| Field | Meaning |
|---|---|
| `url` | the webhook |
| `format` | `json`, `slack`, `teams`, `discord`, `gchat` |
| `events` | the events to send; empty or missing: all |
| `token` | sent as `Authorization: Bearer` |
| `secret` | HMAC-SHA256 signature in `X-Tyr-Signature` |
| `headers` | more request headers (file only) |
| `insecure` | do not check the certificate |
| `name` | shown in Tyr's output |

Unknown fields are rejected, so a typo cannot drop a setting silently.

**The environment adds one more webhook** - what an ISO, a container or a
CI job uses instead of a file:

| Variable | Meaning |
|---|---|
| `TYR_WEBHOOK_URL` | the webhook |
| `TYR_WEBHOOK_FORMAT` | `json` (default), `slack`, `teams`, `discord`, `gchat` |
| `TYR_WEBHOOK_EVENTS` | comma-separated events; empty: all |
| `TYR_WEBHOOK_TOKEN` | bearer token |
| `TYR_WEBHOOK_SECRET` | signature secret |
| `TYR_WEBHOOK_INSECURE` | `1`: do not check the certificate |

```bash
tyr notify                  # list the webhooks (file and environment)
tyr notify --test           # send a test notification to each, report the result
tyr notify --test --event fail   # send an example of an event to see how it looks
tyr notify --validate       # check the settings
```

`--no-webhook` on `tyr accept` and `tyr stress` sends nothing.

## In the TUI

`W` lists the webhooks; one from the environment is shown but can only be
changed there. `a` adds, `enter` edits (name, URL, format, each event on or
off, token, secret, TLS), `d` deletes, `t` sends a test notification to the
selected one, `s` saves. Paste works in every field.

## The ISO

Three ways, from fixed to flexible:

1. **Built in**: `iso/webhooks.json` becomes `/etc/tyr/webhooks.json`.
2. **From the build environment**: every `TYR_WEBHOOK_*` (and
   `TYR_NETBOX_*`) variable set when you run `make iso` goes into
   `/etc/tyr/tyr.env` in the image, which the live system loads:

   ```bash
   TYR_WEBHOOK_URL=https://hooks.slack.com/services/… TYR_WEBHOOK_FORMAT=slack make iso
   ```

3. **At boot**, without rebuilding: `e` in the boot menu and add
   `tyr.webhook=URL` (with `tyr.webhook_format=`, `tyr.webhook_events=`,
   `tyr.webhook_token=`); `tyr.webhook=0` turns notifications off.

A `webhooks.json` on the `TYR-RESULTS` stick replaces the built-in file.
With webhooks set the ISO switches DHCP on. The build checks the settings
(`tyr notify --validate`). URLs, tokens and secrets are in the image: keep
such an ISO in-house.
