---
name: litecone-judge
description: Judge a controller run from its beads timeline — read a litecone run file (the ordered, revision-stamped events of a window plus ownership edges) and the gate's verdict, then reason over the stimulus's light cone (PAST causes, FUTURE effects, ELSEWHERE that must not have moved) to write a narrative verdict that cites event ids and revisions. Use when a litecone run has produced run.json + verdict.json, when asked to explain a gate failure, or when asked whether a controller "did the right thing for the right reason". Never re-runs the gate; never asserts anything it cannot cite.
---

# Judge a litecone run

You are the narrative half of the oracle. The deterministic half — the
`litecone` gate — has already judged ORDER, COUNTS and OWNERSHIP-SUBTREE
containment and written two files:

- `<plan>.run.json` — the captured window: `range {from,to}`, `source`
  (`journal` exact, or `snapshot` lossy), `events[]` sorted by revision, and
  `owners{key -> [owner chain, nearest first]}`.
- `<plan>.verdict.json` — per-line `pass | fail | skip` with `reason` and
  `evidence[] {event_id, rev, op, key}`. Lines marked `skip` are the field-
  level matchers (`phase A→B`, `condition X=val`, `.status.x set`, `<field>
  old→new`), the provenance claims (`by: <actor>`), the ordering edges whose
  side matched nothing, and the prose. The gate reports them as
  "PASSED structural checks; N assertions SKIPPED (field-decode / provenance
  pending)" — never as green. **Those are yours.**

Your output is a verdict a maintainer can act on: pass/fail with a finding
list, every claim pinned to an event id and revision. "Did everything right
but emitted 400 redundant status writes" is a finding. "Timed out waiting
for condition" is not.

## 1. Read the window as a light cone

Fix the stimulus: the WHEN apply(s). Everything in the window with a
revision just after A is its FUTURE cone. Walk it:

1. **Root first.** Find the events on the plan's root object(s) (the
   `outside <root>` of NEVER, or the EXPECT line-1 object). Their revisions
   anchor the story.
2. **Follow ownership outward.** For every other event, look up
   `owners[key]`. An owner chain that reaches the root puts the event
   inside the FUTURE cone; an empty or unrelated chain puts it in
   ELSEWHERE. The gate already failed `outside` if ELSEWHERE moved; you
   explain *what* moved and whether it is background (leases, events,
   endpointslices) or a real side effect.
3. **Order within the cone.** Child events should follow parent events:
   Deployment update → ReplicaSet update → Pod creates. A child event
   *before* its parent's event is a finding even when the gate passed
   (the plan may not have asserted that order).
4. **Counts and churn.** How many updates per object? Many updates to one
   object in a short window is reconcile churn; alternating patterns are
   flapping. Cite the revision run.

## 2. Judge the prose lines

For each `skip` line in verdict.json, decide **pass / fail / cannot judge**
and say why:

- Field semantics ("becomes Ready", "status.replicas == 3") are not decoded
  in the run file (values are k8s protobuf). If you have cluster read
  access, verify against the live object *and say that it is live state,
  not the timeline*. If not, mark **cannot judge** — never infer a field
  value from the existence of an update event.
- Ordering prose ("before", "then") you *can* judge from revisions.
- "No flapping" you can judge from per-object update counts and spacing.

## 3. Weigh the source

`source: snapshot` means the window was reconstructed from each bead's
create and last-write revisions. Then:

- a count is a **lower bound**; `== N` passing is weak evidence, `<= N`
  passing is no evidence;
- ordering between two objects' *last* writes is observable, intermediate
  writes are not;
- an object written again after B hides its in-window write entirely.

Say this plainly in the verdict when it affects a conclusion. `source:
journal` is exact: say that too.

## 4. Right result, wrong reason

The gate cannot see the actor (every write is stamped `kine` today). You
reason about cause from **shape**: a ReplicaSet update that precedes Pod
creates in the same subtree is the deployment controller doing its job; a
Pod create with *no* ReplicaSet event before it in the window is suspicious
(someone else created it, or the window missed the cause). Flag it.

## 5. Write the verdict

```
VERDICT: PASS | FAIL | PASS WITH FINDINGS
Plan: <name>   Window: A..B   Source: journal|snapshot (lossy)

Gate: <passed>/<failed>/<skipped>  (verbatim from verdict.json)

Narrative lines:
  L<n> "<text>"  -> pass | fail | cannot judge: <why, with citations>

Findings (ordered by severity):
  1. <what happened> — evidence: seq:123 rev=4567 update pods/default/x; ...
     why it matters: ...

ELSEWHERE: quiet | moved: <keys> (background | side effect)
Causal story: <3-6 sentences, revisions in order>
```

Rules: every factual sentence carries at least one `event id rev=N`.
Separate what the timeline shows from what you infer. Do not soften a gate
FAIL; you may add context, you may not overturn it. Do not re-run the gate
or modify the run file.

## 6. Where the files are

In a `mol-litecone-*` formula run the step before you recorded them in the
root bead notes as `run_file:` and `verdict_file:` (and `report:` for the
JUnit). Read those paths; do not guess. Standalone, they are under
`.litecone/<plan-name>.run.json` and `.litecone/<plan-name>.verdict.json`
next to where `litecone test` ran.
