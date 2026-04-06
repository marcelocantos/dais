# Active Work Dashboard — Implementation Plan

Target: 🎯T16.1

## Overview

New MCP tool `jevons_active_work` that cross-references three signals to
produce a unified "where is active work happening" view per repo.

## Signal 1: Recent session activity

Query the `sessions` view (from the memory DB noise-filtering work):

```sql
SELECT session_id, project, session_type, substantive_msgs, last_msg
FROM sessions
WHERE session_type = 'interactive' AND substantive_msgs >= 3
  AND last_msg >= datetime('now', '-N hours')
ORDER BY last_msg DESC
```

Group by project. The `project` field encodes the repo path
(e.g. `-Users-marcelo-work-github-com-marcelocantos-jevons`). Write a
helper to decode project names back to repo paths by reversing the
dash encoding.

## Signal 2: Dirty working trees

Walk `~/work/github.com/<org>/<repo>/` (2 levels deep). For each
directory containing `.git/`, run:

- `git -C <path> status --porcelain` — changed file count
- `git -C <path> branch --show-current` — current branch

Flag repos with uncommitted changes or non-default branches (not
`master`/`main`).

Run git commands in parallel (bounded to ~8 concurrent goroutines).

## Signal 3: Open PRs

Run `gh pr list --repo <org>/<repo> --json number,title,headRefName,statusCheckRollup,state --limit 5`
for each repo that has activity from Signal 1 or Signal 2 (not all
repos — only active ones, to limit GitHub API calls). Parse JSON output.

## Output format

Unified table per repo, sorted by most recent activity:

```
Repo                        Last Session          Msgs  Working Tree    Branch       PRs
─────────────────────────────────────────────────────────────────────────────────────────
marcelocantos/jevons         2026-03-27 14:30       42   3 changed       dev          #12 ✓
marcelocantos/tern          2026-03-26 09:15       18   clean           master       -
squz/yourworld2             2026-03-25 11:00        8   1 changed       feat/xyz     #5 ⏳
```

## Tool parameters

- `hours` (number, default 72) — how far back to look for session activity
- `include_clean` (bool, default false) — include repos with no activity

## Agent assignment column

Check the agent registry (`s.registry.List()`) for agents whose
`WorkDir` matches the repo path. Show "unmanaged" for repos with no
agent.

## Implementation

### Files

| File | Changes |
|------|---------|
| `internal/mcpserver/activework.go` | **New** — handler with three signal collectors |
| `internal/mcpserver/memory.go` | Call `registerActiveWork()` from `SetMemory()` |

### Design decisions

1. **No new packages** — single MCP handler that shells out to `git`
   and `gh`. Operationally simple, doesn't warrant its own package.
2. **Project-to-repo mapping** — decode the memory DB's project
   directory name back to a filesystem path (reverse Claude Code's
   path encoding).
3. **Selective PR checks** — only query GitHub API for repos showing
   activity in Signal 1 or 2, to avoid rate limiting across 20+ repos.
4. **Registration** — `registerActiveWork()` called from `SetMemory()`
   since it depends on the memory store for Signal 1.
