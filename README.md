# Jevons ([why?](#about-the-name))

Remote control for [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
instances. Talk to a coordinator that manages Claude Code workers — from a
terminal, or (eventually) from your phone.

## How it works

```
  remote (TUI)  ──WebSocket──►  jevonsd  ──spawns──►  Jevons (Claude Code)
                                        ──manages──►  workers  (Claude Code)
                                   MCP ◄─────────────┘ (tool calls)
```

**jevonsd** is the coordinator daemon. It runs *Jevon* — a Claude Code
session that receives your messages and decides whether to answer directly or
delegate coding tasks to *worker* sessions. Jevons manages workers via
an in-process MCP server (no separate binary needed). Multiple clients can
connect simultaneously; messages and responses are broadcast to all.

**remote** is a terminal UI that connects to jevonsd over WebSocket. It renders
markdown responses, supports input history, and tracks unread messages when
you scroll up.

## Install

```bash
brew install marcelocantos/tap/jevons
```

Or download a binary from the
[latest release](https://github.com/marcelocantos/jevons/releases/latest)
(macOS arm64, Linux x86_64, Linux arm64), or build from source:

```bash
git clone https://github.com/marcelocantos/jevons.git
cd jevons
make jevonsd remote
```

Requires Go 1.22+ and a C compiler (CGo is needed for SQLite).

## Usage

```bash
# Start the coordinator
jevonsd --port 13705 --workdir ~/projects --model sonnet

# Connect from another terminal
remote --addr localhost:13705
```

Type a message and press Enter. Jevons will either answer directly or
spin up a Claude Code worker to handle the task. Results stream back in real
time.

### Flags

```
jevonsd:
  --port              Listen port (default 13705)
  --workdir           Default working directory for workers (default ".")
  --model             Default model for workers
  --jevons-model       Model for Jevons (default: same as --model)
  --debug             Enable debug logging
  --version           Print version and exit
  --help-agent        Print agent guide and exit

remote:
  --addr              jevonsd address (default "localhost:13705")
  --version           Print version and exit
  --help-agent        Print agent guide and exit
```

## Data

jevonsd stores its data in `~/.jevons/`:

| Path | Purpose |
|---|---|
| `jevons.db` | SQLite database (transcript, workers, raw logs) |
| `jevons/` | Jevons working directory and generated CLAUDE.md |
| `remote_history` | TUI input history |

## Agent integration

If you use an agentic coding tool, include
[`agents-guide.md`](agents-guide.md) in your project context for a detailed
reference. You can also run `jevonsd --help-agent` to get the same information.

## About the name

Jevons is named after [Jevons paradox](https://en.wikipedia.org/wiki/Jevons_paradox):
when technological progress makes a resource cheaper to use, total consumption
of that resource tends to *increase* rather than decrease. AI coding assistants
make development dramatically more efficient — so you end up doing more of it,
not less. Jevons leans into this by letting you orchestrate multiple Claude Code
sessions at once, multiplying the effect — so keep an eye on your AI bill at
the end of the month.

## Licence

Apache 2.0 — see [LICENSE](LICENSE).
