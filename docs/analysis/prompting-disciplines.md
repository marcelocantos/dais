# The Four Disciplines of Prompting — Integration Notes for Jevons

Source: Nate B. Jones, *"Prompting Just Split Into 4 Skills"* (YouTube,
Feb 2026). This is an analysis piece, not a plan — it extracts ideas
worth integrating into Jevons and proposes concrete hooks. Promote
anything here into `docs/plans/` or a bullseye target before acting.

## The Argument

Autonomous agents (Opus 4.6, Gemini 3.1 Pro, GPT-5.3 Codex) now run for
hours or days without check-ins. That breaks the synchronous
chat-prompt loop: you can no longer catch mistakes, supply missing
context, or course-correct in real time. Everything the human used to
do mid-conversation has to be **encoded before the agent starts**.

Under that pressure, "prompting" splits into four stacked disciplines:

| Discipline | Altitude | Horizon | Role |
|---|---|---|---|
| 1. Prompt craft | Single instruction | Seconds | Clear, well-structured inputs. Table stakes. |
| 2. Context engineering | Token window | Per-session | Curate the optimal token set (system prompts, tools, RAG, memory, `CLAUDE.md`). |
| 3. Intent engineering | Agent goals | Multi-session | Encode values, trade-offs, decision boundaries so long-runners don't optimise the wrong thing. |
| 4. Specification engineering | Org corpus | Multi-agent / indefinite | Treat every document as agent-readable; write self-contained problem statements, acceptance criteria, constraint architectures. |

Failure stakes rise with altitude: a bad prompt wastes a morning, bad
intent loses customer trust (Klarna), bad org-level specs produce the
kind of implicit-assumption mess humans call "politics."

The five primitives the piece proposes for training spec engineering:

1. **Self-contained problem statements** — the receiver has no prior
   context beyond what you wrote.
2. **Acceptance criteria** — three sentences an independent observer
   could verify against, without asking you.
3. **Constraint architecture** — musts, must-nots, preferences,
   escalation triggers.
4. **Decomposition** — break work into ~2-hour independently
   verifiable subtasks (or describe the break pattern a planner
   session should use).
5. **Evaluation design** — named-good outputs you re-run after model
   updates; the only thing standing between usable and unusable
   autonomous output.

The piece also notes the **planner/worker architecture** dominating
production: a capable model plans, decomposes, and defines acceptance
criteria; cheaper models execute. Spec quality is the quality ceiling.

## Why This Lines Up With Jevons

Jevons is already shaped like the answer to this problem:

- **Targets as first-class.** `jwork(target, scope?, model?)` treats
  the target as the reason a session exists. That is a specification
  surface by default.
- **Mobile/voice as the input channel.** The voice pipeline
  (AssemblyAI → LLM cleanup → learning memory) is the obvious place
  to turn a casual utterance into a structured spec.
- **Session registry + shadow context.** Context engineering
  infrastructure is already in flight — shadow tailers, pre-warmed
  pools, directory-scoped `CLAUDE.md` discovery.
- **Foreman emergence.** The design already anticipates planner
  sessions appearing when a scope gets busy. That is the
  planner/worker pattern — it just needs to be named and instrumented.
- **Bullseye adjacency.** Bullseye targets are desired states with
  assertions. That is a specification format the user already uses.

The gap is not architectural. It is that today Jevons accepts raw
targets ("fix the build") and routes them. The disciplines above
suggest an **enrichment layer** between "user expresses intent" and
"session activates" that turns casual intent into a complete spec.

## Integration Suggestions

### 1. Spec-aware `jwork`

Today's `jwork(target)` accepts a string. Extend the target payload to
carry the primitives explicitly:

```
jwork(
  target: string,                 // self-contained problem statement
  acceptance: string[],           // three-sentence verification criteria
  constraints: {
    musts: string[],
    must_nots: string[],
    preferences: string[],
    escalate: string[],           // conditions that require user approval
  },
  decomposition_hint?: string,    // "break by component" / "break by test"
  eval_cases?: EvalRef[],         // named good/bad examples
  scope?: string,
  model?: "sonnet" | "opus",
)
```

The string form stays — a raw `target` just means "fields unset, and
that is itself a signal about how aggressive the enrichment step
should be."

### 2. A dedicated planner session type (emergent, not declared)

The vision already says foremen emerge from targets like "coordinate
work in area X." Extend that with a **planner target** pattern: when
an incoming target lacks acceptance criteria or decomposition, the
daemon routes it first to a planner session whose target is
*"produce a spec for: <raw target>"*. The planner session interviews
the user (via the mobile app I/O channel), fills in the primitives,
and then submits the enriched target back through `jwork`.

This is the explicit embodiment of the planner/worker architecture
the piece argues is where value accrues. It also keeps the daemon dumb
— planning is a session's job, not infrastructure's.

### 3. Voice-to-spec, not just voice-to-text

The current voice pipeline does STT then LLM cleanup. Add a third
stage: **LLM specification**. Prompt the cleanup model not just to
transcribe but to structure the utterance into the primitives, flag
gaps, and optionally ask one clarifying question back through the
mobile UI before dispatch.

Concretely, swap "cleanup prompt" for a spec-completeness prompt that
returns JSON matching the enriched `jwork` payload. Empty fields
trigger a single turn of clarification; fully specified input
dispatches immediately.

### 4. Per-scope intent infrastructure

Intent engineering (discipline 3) is the thing that kept Klarna's
agent from destroying customer trust. In Jevons, intent lives per
scope (a directory, a repo, a project family). Store it alongside the
session registry:

- `scope_intent` table: goals, values, trade-off hierarchy, escalation
  triggers.
- Prepended automatically to every session activated in that scope,
  below `CLAUDE.md` but above the target.
- Editable from the mobile app (a "scope settings" screen) so the user
  can add an escalation trigger mid-session and have it apply to all
  future activations in the scope.

This is strictly richer than `CLAUDE.md` because it is structured
(JSON, not prose) and therefore enforceable — the daemon can refuse to
activate a session whose target violates the scope's `must_not` list
without even spawning `claude`.

### 5. Acceptance-criteria-aware completion

Sessions currently have "activity" (active/idle) and reaping, not
"done." Add a soft completion signal: when a session's transcript
contains an output that matches its target's acceptance criteria
(checked by a cheap evaluator session), surface it to the user for
approval. On approval, the session goes idle with a "target-achieved"
marker. This is the hook point for:

- auto-retiring bullseye targets that map to jevons targets,
- feeding eval cases back into the scope's `eval_cases`,
- measuring routing quality (did first activation achieve it?).

Note: this does not contradict the "no done state" rule in the
vision. The session still persists; the marker is just metadata.

### 6. Eval store and post-model-update replay

Evaluation design is the discipline Jevons has the least
infrastructure for. Add:

- `evals` table: `(scope, name, input, expected_signal, last_pass_at,
  last_model)`.
- An `jevals` MCP tool: list, add, run.
- A background job that re-runs evals when Anthropic ships a new
  model (detected via the Claude Code version changing in pool
  workers). Regressions show up in the dashboard as a new panel.

Ties back to (5): captured acceptance criteria become seed eval cases
automatically — the user is building the eval corpus as a side effect
of using Jevons normally.

### 7. Organisational corpus index

Discipline 4's punchline: your whole document corpus should be
agent-readable. Jevons already has `internal/discovery/` for
`~/.claude/projects/`. Extend it into a **spec corpus indexer**:

- Walk `~/work/github.com/<org>/<repo>/` (same shape the active-work
  dashboard plan already walks).
- Index `CLAUDE.md`, `docs/targets.yaml`, `docs/TODO.md`,
  `docs/architecture.md`, `README.md`.
- Expose through an MCP resource so any session, in any repo, can ask
  "what does the user believe about X?" and get an answer from the
  canonical doc rather than reconstructing it.
- Feed routing: a target mentioning "sqlpipe" without a scope should
  route to the session pool keyed on the sqlpipe repo because the
  indexer knows that repo exists.

This is the biggest lever and the cheapest — the indexer is a
read-only pass over files the user already maintains.

### 8. Mobile UI: two modes

The mobile app is the user I/O channel. Expose the disciplines in the
UI without forcing the user to think about them:

- **Chat mode** (synchronous). Current behaviour. Type, get response,
  iterate. Discipline 1.
- **Dispatch mode** (autonomous). A short guided flow driven by the
  planner session:
  1. Problem statement (free text / voice).
  2. "What does done look like?" (acceptance criteria; suggested by
     planner, user edits).
  3. Constraints review (musts/must-nots surfaced from scope intent;
     user adds ad-hoc).
  4. Dispatch.

Dispatch mode is how you make the primitives tactile without turning
the mobile app into a form-filling chore. The planner does the
heavy lifting; the user approves or edits.

## What Not To Do

- **Don't prescribe session types.** The vision is firm that types
  are emergent. "Planner" and "worker" are target patterns, not
  declared roles. The daemon routes; the target description does the
  typing.
- **Don't hardcode the primitives as required fields.** A user
  submitting `jwork("fix the build error on CI")` should still work.
  Enrichment is opportunistic, not gatekeeping.
- **Don't conflate execution safety with intent.** The doit absorption
  handles "can this command run?" Scope intent handles "should this
  work be done this way?" Keep them separate layers; both gate
  activation but on different axes.
- **Don't replicate bullseye.** If a target maps to a bullseye target,
  link to it (`bullseye_id: "T7.2"`) and let bullseye own the desired
  state. Jevons owns the session and enrichment; bullseye owns the
  assertion.

## Proposed Next Step

If any of this is worth pursuing, the cheapest high-leverage starting
points are (7) the corpus indexer and (3) voice-to-spec. Both extend
infrastructure that already exists, both pay off immediately on the
current mobile app, and both are independent of the bigger session-
model refactor in `docs/vision-v2.md`.

Consider opening a bullseye target along the lines of:

> 🎯 Jevons enriches raw targets into spec-complete payloads
> (problem statement, acceptance criteria, constraints) before
> dispatch, using a planner session pattern and the voice/text
> pipeline.

— then decompose into (3), (1), (2), (5), in roughly that order.
