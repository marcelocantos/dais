# Lua as a Unified Programmable Membrane

## Observation

The server-side Lua runtime (gopher-lua in Go) and the client-side Lua
runtime (C Lua in iOS) are not two separate systems. They are two
execution sites for a single logical layer — the programmable boundary
between state and presentation.

View scripts define the contract: given this state, produce this UI.
Where those scripts run is an optimisation decision, not an
architectural one.

## Current State

- **Server**: gopher-lua renders view trees from state, streams JSON
  to client. Used during development and as fallback.
- **Client**: C Lua 5.1.5 runs the same scripts locally against
  replicated state (via sqlpipe). Target architecture for production.
- **Scripts**: identical Lua source, same builder functions, same
  state access patterns.

The pivot from server-side to client-side rendering (🎯T9) is really
about moving execution within the membrane, not changing the membrane
itself.

## Design Constraint

All Lua state access must go through declared data dependencies:

- `query(sql)` — read from the local sqlpipe replica.
- `subscribe(sql)` — declare a live dependency; re-render when
  underlying data changes.

No side channels (direct HTTP calls, global variables, ambient state).
If a script accesses state only through these functions, its execution
location becomes a deployment decision:

| Data lives on... | Script runs on... | Why |
|---|---|---|
| Client replica | Client | No round-trip needed |
| Server only | Server | Data isn't replicated |
| Both | Either | Optimise for latency |

This constraint is cheap to maintain and keeps the option open for
automatic placement later without building infrastructure now.

## Practical Implications

1. **Don't specialise scripts by location.** A view script shouldn't
   know or care whether it runs on the server or the client. If you
   find yourself writing `if server then ... end`, the data dependency
   isn't properly declared.

2. **Keep the builder API identical.** Both runtimes must expose the
   same set of builder functions (`text`, `vstack`, `hstack`,
   `padding`, etc.). Divergence breaks location transparency.

3. **Jevon writes scripts, not locations.** When the AI agent modifies
   a view script, it writes Lua. It doesn't decide where that Lua
   runs. The runtime handles placement.

4. **Two runtimes is fine for now.** gopher-lua and C Lua implement
   the same Lua 5.1 semantics. Unifying them into a single runtime
   isn't necessary — what matters is that the scripts are portable
   between them.

## Future Possibilities

If the membrane thickens beyond views (event handlers, data transforms,
validation rules), the same principle applies: write the logic once,
declare its data dependencies, let the runtime place it. This is not
a priority, but the constraint above keeps the door open.

### Longer-term vision

The logical endpoint is a single relational expression over the entire
system — `(state, event) → (state, action)` — where an optimiser
splits the expression across client/server boundaries automatically,
monitoring traffic patterns and adaptively restructuring the flow.
sqlpipe becomes an execution strategy, not an API.

Prior art: `arr-ai/arrai` (Marcelo's earlier project) was a
relational/functional language with set-based semantics intended to
go in this direction. The ambition stalled but the thinking is
relevant. See also: Naiad/Differential Dataflow, Dedalus/Bloom,
CALM theorem.

Parked — not actionable for jevon now, but the `subscribe(sql)`
constraint preserves the option.
