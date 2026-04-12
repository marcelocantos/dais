# Audit Log

Chronological record of audits, releases, documentation passes, and other
maintenance activities. Append-only — newest entries at the bottom.

## 2026-03-01 — /release v0.1.0

- **Commit**: `a765c29`
- **Outcome**: Released v0.1.0 (darwin-arm64, linux-amd64, linux-arm64). All CI jobs passed.
- **Changes**: Added `--version` and `--help-agent` flags to all binaries, `agents-guide.md`, `STABILITY.md`, release CI workflow.

## 2026-03-27 — /release v0.2.0

- **Outcome**: Released v0.2.0 (darwin-arm64, linux-amd64, linux-arm64). Major pivot to desktop-first web UI with persistent Claude PTY agents.
- **Changes**: Web chat UI, agent registry, transcript memory (FTS5), JSONL as source of truth, sqlpipe removed, MCP race fix. sqldeep query support deferred (local CGO dependency not CI-compatible yet).
- **Notes**: Pre-alpha. Many surfaces marked Fluid in STABILITY.md.
- **No Homebrew tap**: Project is desktop-only, tap not needed.

## 2026-04-12 — /release v0.3.0

- **Commit**: `pending`
- **Outcome**: Released v0.3.0 (darwin-arm64, linux-amd64, linux-arm64).
- **Changes**: Project renamed Jevon → Jevons; Grok Realtime voice bridge with adaptive VAD and transport abstraction; Claude Code session/agent management extracted to the `claudia` library (~1900 lines removed); transcript memory moved out-of-process to the `mnemo` MCP server; tern → pigeon migration; live agent terminal viewer; async fire-and-forget `jevons_agent_send`; Swift `JevonBridge` / `WebUIView`; ConnectView full-screen QR; interactive API-key prompts.
- **Workflow fixes**: `.github/workflows/release.yml`, `Makefile`, `.gitignore` all updated for the `jevon → jevons` rename (stale package paths and binary names). Linux arm64 build switched to native `ubuntu-24.04-arm` runner, dropping the `gcc-aarch64-linux-gnu` cross-compiler step.
- **STABILITY.md**: Updated to v0.3.0 snapshot — removed 4 memory MCP tools, added 6 new agent/transcript tools, documented `/ws/agent-terminal` and `/ws/voice`, removed `~/.jevons/memory.db`, added `remote` CLI section.
