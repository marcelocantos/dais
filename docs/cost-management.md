# Cost Management — Design Note

Status: idea (not scheduled)

## Goal

Jevon manages Claude Code workers. Those workers consume tokens that
cost real money. Jevon should track, report, and eventually govern
that spend — per worker, per session, per time window — so the user
stays informed and protected from runaway costs.

## Prior art: ccusage

[ccusage](https://github.com/ryoppippi/ccusage) is a Node.js CLI that
reads Claude Code's local JSONL transcripts and produces usage reports.

### How Claude Code stores usage data

Claude Code writes one JSONL file per conversation under
`~/.claude/projects/`. Each line is a JSON object containing:

| Field | Purpose |
|---|---|
| `timestamp` | ISO 8601 event time |
| `sessionId` | Groups lines into a conversation |
| `message.model` | e.g. `claude-sonnet-4-20250514` |
| `message.usage.input_tokens` | Prompt tokens |
| `message.usage.output_tokens` | Completion tokens |
| `message.usage.cache_creation_input_tokens` | Tokens written to cache |
| `message.usage.cache_read_input_tokens` | Tokens read from cache |
| `costUSD` | Pre-calculated cost (optional, added by newer Claude Code) |
| `requestId` / `message.id` | Deduplication keys |

### What ccusage does well

- Aggregates by day / week / month / session / 5-hour billing block.
- Per-model cost breakdown.
- Fetches model pricing from LiteLLM's open pricing dataset.
- Handles tiered pricing (different rates above 200k context).
- Deduplicates via request/message IDs.
- Offers an MCP server (`@ccusage/mcp`) for exposing data to agents.

### Limitations relevant to Jevon

1. **Offline only** — reads local JSONL files after the fact. No
   real-time streaming, no ability to gate or throttle live workers.
2. **Single-machine** — assumes `~/.claude/` is local. Jevon workers
   may eventually run on remote hosts.
3. **Pricing is approximate** — sourced from LiteLLM's community JSON,
   not Anthropic's billing API. The pre-calculated `costUSD` field
   (when present) is more authoritative.
4. **No policy enforcement** — purely observational. Can't pause a
   worker that's burning through budget.
5. **Node.js dependency** — not suitable for embedding in jevond (Go).

## Design sketch for Jevon

### Layer 1: Collection

jevond already manages Claude Code subprocesses. Each worker's JSONL
is available on disk. Approach:

- **Tail the JSONL** in real-time (fsnotify or periodic poll) for each
  active worker. Parse usage fields as they appear.
- **Prefer `costUSD`** when present; fall back to token-count × pricing
  table (maintain a Go-native pricing table, seeded from LiteLLM or
  Anthropic docs, updateable via config).
- **Store aggregated usage in SQLite** (jevond already uses SQLite for
  learning/memory). Schema sketch:

  ```
  usage_events(
    id          INTEGER PRIMARY KEY,
    worker_id   TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    timestamp   TEXT NOT NULL,
    model       TEXT NOT NULL,
    input_tokens      INTEGER,
    output_tokens     INTEGER,
    cache_create_tokens INTEGER,
    cache_read_tokens   INTEGER,
    cost_usd    REAL,
    request_id  TEXT UNIQUE  -- dedup
  )
  ```

### Layer 2: Reporting

Expose via the existing web UI and jevond's API:

- Real-time cost ticker per worker and aggregate.
- Breakdown by model, session, time window.
- Historical charts (daily/weekly/monthly).
- Surface in the active-work dashboard (🎯T16.1) when that ships.

### Layer 3: Governance (future)

- **Budgets**: per-worker and global spend caps (hourly / daily /
  monthly). Configurable via web UI or jevond config.
- **Actions on breach**: warn → throttle (add delay between requests)
  → pause worker → kill worker. Escalation configurable.
- **Alerts**: push notification or Slack message when spend crosses
  thresholds (50%, 80%, 100% of budget).

### Non-goals (for now)

- Replacing ccusage — it's fine for ad-hoc CLI queries.
- Querying Anthropic's billing API — no public API exists; rely on
  local JSONL data.
- Multi-machine aggregation — single-host jevond is the current scope.

## Open questions

1. Should jevond expose its own MCP resource for cost data (letting
   workers self-monitor their spend)?
2. Is the 5-hour billing block relevant for Max subscribers, or should
   Jevon focus on API-key billing only?
3. How to handle workers that use `--model` overrides (different cost
   profiles within the same session)?

## References

- ccusage: https://github.com/ryoppippi/ccusage
- LiteLLM pricing data: https://github.com/BerriAI/litellm (model_prices_and_context_window.json)
- JSONL format: inferred from ccusage source; no official Anthropic docs
