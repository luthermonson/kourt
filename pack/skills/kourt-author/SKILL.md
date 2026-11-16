---
name: kourt-author
description: Write a kourt plan — a Markdown test for a Kubernetes controller that is judged against the committed, ordered, revision-stamped beads timeline of what the controller actually did, not against polled live state. Use when asked to write, translate (from Chainsaw, kuttl, Ginkgo) or review a controller/operator e2e test for a k3s-on-beads cluster. Teaches the light-cone sections (INTENT / SUBTREE / GIVEN / WHEN / EXPECT / THROUGHOUT / NEVER / SETTLE / EVIDENCE), the `<kind>/<name-glob> ×N <what>` matcher grammar, the partial-order keywords (then / after / finally / concurrent with), which assertions the gate evaluates versus reports SKIPPED, and the fail-closed rule. Self-contained; the only tool it needs is the `kourt` binary for `kourt lint`.
---

# Author a kourt plan

You are writing a test whose oracle is a **timeline**, not an end state. The
substrate (k3s on kine on beads) commits every object write as a bead
mutation with a global, monotonic **revision**, and models `ownerReferences`
as parent-child edges. A plan says what the controller must have caused, in
what partial order, how many times, and what it must not have touched. The
binary (`kourt test`) applies the inputs, captures the revision window
`(A, B]`, and runs the deterministic gate. The `kourt-judge` agent reads
the rest.

The grammar is `PLAN-FORMAT.md` in the kourt repo (locked). This skill is
how to *think* in it.

## 1. Think in the light cone

Pick the stimulus (WHEN). Then:

| Cone | Section | Ask |
|---|---|---|
| **Scope** | SUBTREE | Which object(s) own everything the controller should touch? `clusters/demo`, `machinedeployments/demo-md`. "Elsewhere" is everything not owned by these. |
| **FUTURE** | EXPECT | What must the controller cause, grouped by phase, with hard edges only where causality truly forces order? |
| **Invariants** | THROUGHOUT | What counts must hold across the whole window? Exactly N creates, zero deletes, no new ReplicaSet/MachineSet on a plain scale. |
| **ELSEWHERE** | NEVER | `elsewhere changed except <background>` plus the explicit forbiddens (`machines/demo-cp-* deleted`). |
| **PAST** | GIVEN | What must already hold before A is taken? Apply it here; the binary settles before taking A. |

Write INTENT first, one paragraph, as the truth the plan establishes. If you
cannot state it, you do not have a test yet.

## 2. Build the FUTURE cone as a partial order

Controllers run concurrently; a strict sequence is almost always wrong.

- Group related matchers under `[labels]`: `[apply]`, `[controlplane]`,
  `[workers]`, `[nodes]`, `[ready]`.
- Inside a group, lines interleave freely. Add an edge only where causality
  forces it: `[nodes] after controlplane`, `[scale] then`, `[ready] finally`,
  `[workers] concurrent with controlplane`.
- Do not draw an edge from a parent's *update* to its children's creates
  when the parent's status writes trail the children (Deployment status
  after Pods; MachineDeployment status after Machines). The honest edge is
  usually ReplicaSet/MachineSet → children, or group → group.
- Use `created-by <owner>/<glob>` to assert the ownership edge:
  `machines/demo-md-* ×2 created-by machinesets/demo-md-*`.
- Use `×N` for exact counts and `×N+` for lower bounds. Two new pods is
  `×2`, "at least one reconcile" is `×1+ updated`.
- Prefer globs for generated names (`demo-md-*`); never guess hashes.

## 3. State field semantics — they are SKIPPED today, and that is fine

`phase Pending→Running`, `condition Ready=True`, `.status.nodeRef set`,
`.spec.replicas 1→3` **parse** and are reported **SKIPPED** (value decode is
a later increment). Write them anyway: they are the narrative judge's input
and the roadmap. Do not fake a field claim with a count. A plan whose
assertions are *all* field-level fails closed — keep at least one structural
line (`created`, `deleted`, `created-by`, `×0 created`) per plan.

Provenance claims (`by: <actor>`, "the ONLY actor…") are SKIPPED too: every
event is stamped `kine` until managedFields provenance lands. The ownership
edge (`created-by`) is the causality claim you can make today.

## 4. Pin the invariants

```
- machines/demo-* ×3 created
- machines/demo-* ×0 deleted
- machinesets/demo-md-* ×1 created      # exactly one; a rollout would make two
- machinedeployments/demo-md ×1+ updated
```

`×0` is how you assert absence. With the lossy snapshot source (see 7) a
count is a lower bound: `×N` passing is weak, `×N+` passing proves little.

## 5. Fence ELSEWHERE — and name the background honestly

```
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*
```

A live cluster always has background churn: node status (`nodes/*`), node
leases, apiserver peer leases, EndpointSlices, Events. **Except them
explicitly.** Nodes have no ownerReference to their Machine, so for a CAPI
subtree they are always elsewhere. An unexpected key in a failure's
evidence is information: decide whether it is background (add an except)
or a finding (keep failing). Never except `*/*`.

Add plain negatives for things that are bugs even inside the subtree:
`machines/demo-cp-* deleted`, `machinedeployments/demo-md deleted`.

## 6. Settle on the subtree

```
## SETTLE
- quiescence 15s on the subtree
- timeout 5m
```

B is taken when no write to a subtree object has landed for the quiescence
window. Hitting `timeout` is a gate **failure**: a controller that never
goes quiet is a finding, not a flake.

## 7. Know the gate's boundary

Evaluated now: create/update/delete matching by kind + name-glob, exact and
at-least counts, the then/after/finally/concurrent edges over revisions,
`created-by` through owner edges, `elsewhere` via SUBTREE, quiescence,
revision citation. SKIPPED (visible, never green-washed): field-level and
provenance matchers.

Timeline source matters. With the beads events journal enabled the window
is exact. Without it the binary falls back to a **lossy snapshot** (each
object's create and last write only) and says so in the verdict notes.

## 8. Fail closed

- A plan with no structural EXPECT / THROUGHOUT / NEVER line is a lint
  error and a gate failure.
- An EXPECT event that never arrives before SETTLE is a failure.
- A failure-scenario plan ("invalid CR creates no children") asserts the
  absence: `machines/bad-* ×0 created` and `NEVER machinesets/bad-* created`;
  the gate fails if children appear.

## 9. Run `kourt lint` before anything else

```
kourt lint plans/            # exit 0 = every plan parses and asserts something structural
```

It lists each SKIPPED line (fine, expected), each prose line (the judge's
input), each malformed directive (fix it), and each missing manifest.

## 10. Translating an existing test

From Chainsaw / kuttl `assert:` files or Ginkgo `Eventually(...).Should(...)`:
1. Each asserted end state becomes a matcher for the **event that produces
   it** (`created`, `created-by`, `updated`), grouped by phase.
2. Each `Consistently` / "never" becomes THROUGHOUT (`×0`) or NEVER.
3. Each hard-coded timeout becomes nothing; SETTLE covers the run.
4. Add what the original could not say: exact creates/deletes, the
   `elsewhere` fence, delete + recreate with a new identity as two events.

## Checklist

- [ ] INTENT is one paragraph stating the truth established.
- [ ] SUBTREE names the ownership root(s).
- [ ] EXPECT uses groups; edges only where causality forces order.
- [ ] Counts use `×N` where the number is known, `×0` for absence.
- [ ] NEVER has `elsewhere changed except ...` with specific background refs.
- [ ] Field / provenance claims are present and understood to be SKIPPED.
- [ ] SETTLE has a quiescence window and a real timeout.
- [ ] `kourt lint` exits 0.
