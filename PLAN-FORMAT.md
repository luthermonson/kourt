# The litecone plan format

> **Status: grammar LOCKED by the repo owner (2026-11-15).** The parser
> (`internal/plan`) implements exactly this. What the gate can evaluate
> today versus what it parses but reports **SKIPPED** is spelled out below;
> nothing is silently passed. `litecone lint <plan.md|dir>` tells you, per
> line, whether it is machine-checked, skipped, prose, or malformed.

A plan is a Markdown file. It states **intent and the expected light cone**
of a stimulus; it never encodes poll loops or per-assertion timeouts. The
gate judges it against the committed, ordered beads timeline of a revision
window `(A, B]`: `A` is the head revision just before WHEN, `B` the head at
SETTLE.

## Sections

```markdown
# PLAN <name>

## INTENT
One paragraph of prose: the truth this plan establishes. The narrative judge's input; the gate ignores it.

## SUBTREE
- <kind>/<name>                     # one or more ownership roots

## GIVEN
- apply <manifest.yaml>             # preconditions; applied and let settle BEFORE A is taken
- wait <duration>

## WHEN
- apply <manifest.yaml>             # the stimulus (apply | patch | delete, via client-go)

## EXPECT
[group-label] [ordering keywords]   # optional group header
- <kind>/<name-glob> [×N[+]] <what> [ordering keywords]
...

## THROUGHOUT
- <kind>/<name-glob> [×N[+]] <what> # invariants over the whole window (counts)

## NEVER
- elsewhere changed [except <kind>/<glob>, ...]   # nothing outside the SUBTREE moved
- <kind>/<name-glob> <what>                       # zero such events

## SETTLE
- quiescence <duration> on the subtree  # B is taken once no SUBTREE event lands for this long
- timeout <duration>                    # max wait; hitting it is a gate FAILURE

## EVIDENCE
Prose: what each result must cite (event id + revision). The gate attaches evidence itself.

## CLEANUP                              # extension (not in the locked grammar): teardown with --cleanup
- delete <manifest.yaml>
```

### Lexical rules

1. `# PLAN <name>` (or the first H1) names the plan. `## <WORD>` opens a
   section; only the first word matters, case-insensitively.
2. Each bullet (`-`, `*`, `+`), numbered (`1.`) or plain line inside a
   section is one line. Blank lines and fenced code blocks are skipped. A
   line wrapped in parentheses is prose.
3. A trailing `  # ...` or ` -- ...` is a comment.
4. A line that parses is **machine** (gate evaluates it), or **skipped**
   (parses, needs field decode or provenance; reported SKIPPED), or an
   **error** (starts with a `<kind>/<name>` ref but is malformed; fails
   lint and the gate). Anything else is **prose** for the narrative judge.

### Object references: `<kind>/<name-glob>`

- `kind` is the Kubernetes **resource** as it appears in the etcd key
  (`pods`, `machines`, `machinedeployments`) — equivalently the bead's
  `issue_type`, except that the kine driver types grouped keys
  (`/registry/cluster.x-k8s.io/machines/...`) by their API group, so the
  gate matches the key's resource segment first and the issue_type second.
  `nodes` also matches the apiserver's legacy `minions` key; `services`
  matches `services/specs`.
- `name-glob` matches the etcd key's **last segment** (`demo-md-*`). `*`
  matches within the segment.
- The namespace is **not** part of a reference. `pods/scale-test-*` matches
  in every namespace. (Extension: `<kind>/<ns>/<name-glob>` is accepted.)

### Counts: `×N` and `×N+`

`×N` (or ASCII `xN`) requires **exactly** N matching events in the window;
`×N+` at least N. Default `×1`. `×0` is how a plan asserts that something
did **not** happen (`machinesets/demo-md-* ×0 created`).

### `<what>`

| `<what>` | Gate today | Meaning |
|---|---|---|
| `created` | **evaluated** | a create event for the object |
| `created-by <kind>/<glob>` | **evaluated** | created, and the object's owner chain (ownerReferences → beads parent-child edges) contains an object matching the ref |
| `deleted` | **evaluated** | a delete event (kine marks `del: true`; the bead stays) |
| `updated` | **evaluated** (extension) | an update event |
| `changed` | **evaluated** (extension) | any create / update / delete |
| `<field> <old>→<new>` (also `->`) | **SKIPPED** | field transition — needs k8s-protobuf value decode |
| `phase A→B→C` | **SKIPPED** | phase trail — value decode |
| `condition <X>=<val>` | **SKIPPED** | condition — value decode |
| `.status.<path> set` | **SKIPPED** | field presence — value decode |
| `created by <actor>`, `by: <actor>`, `the ONLY actor ...` | **SKIPPED** | provenance — every event's actor is `kine` until managedFields provenance lands |

A SKIPPED line is reported on stdout, in `verdict.json`, and as a
`<skipped>` testcase in the JUnit report. It never fails CI and it never
counts as a pass. A plan whose assertions are **all** skipped fails closed
("nothing structural was evaluated").

### EXPECT: a partial order

Concurrent controllers interleave, so EXPECT is **not** a strict sequence.
Matchers with no ordering keyword may interleave freely. Hard edges come
only from keywords, on a group header or on a line:

| Keyword | Edge |
|---|---|
| `then` | every matched event of this group/line is after every matched event of the **previous** group/line |
| `after <group>` or `after <kind>/<glob>` | earliest matched revision here > latest matched revision there (a ref target matches every event in the window for that object) |
| `finally` | after every other matched EXPECT event |
| `concurrent with <group>` | the two revision spans overlap |

The gate enforces: (1) each matcher's count, (2) each declared edge over
the matched events' revisions. An edge whose side has no structural match
(a group of only field matchers, or an unmet count) is reported SKIPPED,
with the reason, rather than passed.

### NEVER and SUBTREE

`elsewhere changed` fails if any create/update/delete in the window touched
an object that is neither a SUBTREE root nor owned (transitively) by one,
unless an `except` ref matches it. A live cluster always has background
churn — node status, node leases, apiserver peer leases, EndpointSlices,
Events — **name it** in `except`; an unexpected key in the failure evidence
is information, not noise. `<kind>/<glob> <what>` lines require zero
matching events.

### SETTLE

Quiescence is judged **on the subtree**: a write counts as activity only if
its object is a root or has a root in its owner chain. The global revision
never stops moving (leases renew every few seconds), so the subtree is the
only meaningful settle signal. Exceeding `timeout` is a gate failure.

## Worked example 1: `plans/examples/capi-cluster-create`

```markdown
# PLAN capi-cluster-create

## SUBTREE
- clusters/demo

## WHEN
- apply manifests/demo-cluster.yaml

## EXPECT
[apply]
- clusters/demo created
- mirageclusters/demo created
- kubeadmcontrolplanes/demo-cp created
- machinedeployments/demo-md created

[infra] then
- mirageclusters/demo condition Ready=True                      # SKIPPED (field decode)
- clusters/demo .status.controlPlaneEndpoint set               # SKIPPED

[controlplane] after apply
- machines/demo-cp-* ×1 created-by kubeadmcontrolplanes/demo-cp
- miragemachines/demo-cp-* ×1 created
- machines/demo-cp-* phase Pending→Provisioning→Provisioned→Running   # SKIPPED

[workers] concurrent with controlplane
- machinesets/demo-md-* ×1 created-by machinedeployments/demo-md
- machines/demo-md-* ×2 created-by machinesets/demo-md-*
- miragemachines/demo-md-* ×2 created

[nodes] after controlplane
- nodes/demo-cp-* ×1 created
- nodes/demo-md-* ×2 created

[ready] finally
- clusters/demo condition Ready=True                           # SKIPPED

## THROUGHOUT
- machines/demo-* ×3 created
- machines/demo-* ×0 deleted
- machinesets/demo-md-* ×1 created

## NEVER
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*
- machines/demo-cp-* deleted
- the ONLY actor writing machines/* status is the mirage controller   # SKIPPED (provenance)

## SETTLE
- quiescence 15s on the subtree
- timeout 5m
```

Gate output for a healthy run: `PASSED structural checks; 9 assertion(s)
SKIPPED (field-decode / provenance pending)`. Note `[infra] then` is
SKIPPED (its group has only field matchers), `[nodes] after controlplane`
is evaluated (node creates vs. the control-plane machine events), and
`[workers] concurrent with controlplane` is evaluated as a span overlap.

## Worked example 2: `plans/examples/capi-autoscale-up`

```markdown
# PLAN capi-autoscale-up

## SUBTREE
- machinedeployments/demo-md

## WHEN
- apply manifests/pending-pods.yaml

## EXPECT
[pending]
- pods/scale-test-* ×4 created
- pods/scale-test-* condition PodScheduled=False                # SKIPPED

[scale] then
- machinedeployments/demo-md .spec.replicas 1→3                # SKIPPED
- machinedeployments/demo-md updated
- machinesets/demo-md-* updated
- machines/demo-md-* ×2 created-by machinesets/demo-md-*
- miragemachines/demo-md-* ×2 created

[nodes] after scale
- nodes/demo-md-* ×2 created

[bind] after nodes
- pods/scale-test-* .spec.nodeName set                         # SKIPPED
- pods/scale-test-* condition PodScheduled=True                # SKIPPED

## THROUGHOUT
- machines/demo-md-* ×2 created
- machines/demo-md-* ×0 deleted
- machinesets/demo-md-* ×0 created     # a scale, not a rollout

## NEVER
- elsewhere changed except deployments/scale-test, replicasets/scale-test-*, pods/scale-test-*, nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*
- machinedeployments/demo-md deleted
- machines/demo-cp-* changed
- kubeadmcontrolplanes/demo-cp changed

## SETTLE
- quiescence 20s on the subtree
- timeout 10m
```

If a rollout happens instead of a scale (a new MachineSet appears), the
`×0 created` invariant fails and so does `elsewhere` if the control plane
was touched — absence is asserted, never assumed.

## Both example plans are unit-tested

`internal/gate/gate_test.go` parses both files and evaluates them against
synthetic timelines: a healthy run (structural pass, N SKIPPED), a
misordered run (node before control plane; stray write elsewhere), a
Machine delete (NEVER + THROUGHOUT trip), and a rollout-instead-of-scale.

## Open questions

1. `created-by <ref>` is implemented as an **ownership** claim (owner chain
   contains the ref). If the owner meant it as an actor claim it should be
   spelled `created by <actor>` (SKIPPED, provenance).
2. `after <kind>/<glob>` as a target compares against **every** event for
   that object in the window, including trailing status writes. Groups are
   the safer target.
3. Nodes have no ownerReference to their Machine, so they are always
   ELSEWHERE for a CAPI subtree and must be excepted. Should the substrate
   project `status.nodeRef` as an edge?
4. `CLEANUP`, `updated`, `changed`, `wait`, `patch` and `<kind>/<ns>/<name>`
   are extensions beyond the locked grammar; drop or keep?
