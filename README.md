# open-conduit-jiminny

An [Open Conduit](https://github.com/open-conduit/open-conduit) extension that finds calls [Jiminny](https://www.jiminny.com) has finished processing and makes each one a durable piece of Work. An agent (Claude Code, say) picks the Work up with the call's activity ID, does whatever your skill says (meeting notes, a Drive document, a review request), and Open Conduit keeps the Work until it's done.

It discovers calls by polling Jiminny on a timer. It needs no webhooks, Slack or public endpoint. It only discovers and routes calls: it never fetches transcripts or writes documents. Those jobs belong to the agent's own tools.

## How it behaves

- **Discovery.** By default, every 15 minutes, and straight away when the daemon starts, the extension lists the calls Jiminny has processed (`GET /getActivities?status=completed`) whose start time falls in the lookback window (default 14 days). It reads every page of the list.
- **One call, one Work.** The Work's correlation key is the Jiminny activity ID, so rescans, a daemon restart and calls that stay listed never start the same call twice. A completed call stays completed.
- **Recovery.** If the daemon was stopped, the next scan finds every call processed while it was down, as long as it's still inside the lookback window. Set `lookback_days` (at most 180) to the longest outage you want to cover.
- **Failed scans.** A page that fails (401, 403, 429, 5xx, a network error) publishes nothing. Open Conduit retries it with backoff, and the last complete scan stays authoritative. A daemon restart in the middle of a scan resumes the same scan from the next page.
- **Edits and ageing out.** If a call's title or activity type changes, it is recorded as history on the same Work and does not regenerate anything. A call that drops out of the window is recorded once as `core.ingress.absent` and also starts nothing.
- **What the agent gets.** The event is `jiminny.activity.processed` (`jiminny.activity.v1`):

  ```json
  {"activity_id": "…", "title": "Weekly progress", "activity_type": "Weekly", "type": "conference", "occurred_at": "2026-10-27T15:00:00Z"}
  ```

## Install

Build the package and install it into Open Conduit (Go 1.24+):

```sh
make package                                   # dist/jiminny
open-conduit extension certify dist/jiminny    # optional: the conformance suite
open-conduit extension install dist/jiminny    # takes effect at the next serve
```

Release archives for each platform are attached to [releases](../../releases), with `checksums.txt`. Unpack one and install the `jiminny/` directory it contains.

The package needs an Open Conduit host whose protocol includes reconcile-first ingress certification. That is any release after 1.2.0.

## Configure

[`examples/config.json`](examples/config.json) is a complete scope, and it passes `open-conduit config check` with the package installed (`make config-check`). It has these parts:

- **A `jiminny` connection.** Its only secret is `token`: the team API key an Admin creates under Organization Settings > Integrations > API Key. Store it with a secret reference (`keyring:…`, `env:…`, `cmd:…`) and never in the file itself.
- **A `jiminny` extension entry.** Its config fields:

  | Field | Default | Meaning |
  | --- | --- | --- |
  | `scope` | (required) | The containing scope's ID. |
  | `region` | `us` | `us` or `eu`. |
  | `interval_minutes` | 15 | Time between scans (1–1440). |
  | `lookback_days` | 14 | How far back each scan looks: the recovery horizon (1–180). |
  | `activity_types` | all | Keep only these user-set activity types, ignoring case (for example `Weekly`, `Workshop`). |
  | `title_contains` | none | Keep only calls whose title contains one of these, ignoring case. |
  | `types` | calls and meetings | Jiminny activity kinds (`conference`, `softphone`, `softphone-inbound`). |
  | `page_size` | 500 | Activities per API page. |

- **A route with handler `jiminny`.** It creates one Work, of kind `jiminny.call`, the first time a call is seen.
- **A `claude-code` binding.** `playbook: "-"` with `args: "/your-skill {key}"` starts each Work on your skill, with the activity ID as its argument and the call's details below. `cold_turns: "resume"` keeps the session across a long review wait. To send different calls to different skills, use one entry, route and binding per filter (for example, `activity_types: ["Workshop"]` to a workshop skill).
- **A `human` entry, and `human.ask` on the binding.** The skill asks for review with `human.ask`, without `options`, so that free-text corrections are accepted. The Work then waits durably for as long as it takes, and the reviewer answers from the Open Conduit TUI or CLI.

## Try it without Jiminny

`go run ./cmd/fake-jiminny URLFILE` serves two synthetic processed calls. Point an entry's `base_url` at that URL and use `fixture-token` as the token.

## Known limitations

- **Verified with fakes only.** No real Jiminny token has been used. The API behaviour comes from Jiminny's published OpenAPI spec (v1.2.0). Still to confirm against the live API: the timestamp format in responses, the list's sort order, whether `updatedAt` moves on edits, and whether `429` carries `Retry-After`.
- **The lookback is the recovery horizon.** A call that started before the window opens is never discovered. That includes a call uploaded or processed long after it happened.
- **Activities Jiminny can't build.** Activities in the response's `failed` list are skipped for that scan and retried on the next one. One that keeps failing is never discovered, and it shows up only in the extension log as a count.
- **Scan size.** Open Conduit stages at most 10,000 calls and 1,000 pages per scan, which is far more than a 14-day window of meetings normally holds.
- **Rate limit.** Jiminny allows 30 `/getActivities` requests a minute. One scan of up to 500 calls is a single request.
- **Approval surface.** Approval happens in the TUI or CLI (`open-conduit actions run … answer`), and nobody is notified that a review is waiting.
- **No duplicate-publication guard.** The extension does not stop a document being published twice. If an agent crashes after publishing and is retried, it resumes the same Claude session, but your skill should still look for an existing document for the activity ID before creating one.

## Development

`make check` runs the tests, builds the package, certifies it against the Open Conduit host the module is pinned to, and checks the example config. CI runs the same.
