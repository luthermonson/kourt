# kourt

Causality-first testing for Kubernetes controllers, built on **k3s-on-beads**:
a real k3s cluster whose datastore (kine) stores every object as a bead in a
Dolt-versioned beads work-graph. Instead of polling live cluster state until
it matches (Chainsaw, kuttl, `Eventually(...)`), kourt **judges the
committed, ordered, revision-stamped timeline of what a controller actually
did**.

The name is relativity's light cone. Fix the stimulus — the manifest you
apply. Its **PAST cone** is what had to hold first (GIVEN). Its **FUTURE
cone** is what the controller must cause, in causal order (EXPECT). Its
**ELSEWHERE** is everything causally unrelated, which must not have moved
(NEVER). A plan states the cone in plain text; the gate checks it against
the record.

```
# PLAN capi-autoscale-up
## SUBTREE
- machinedeployments/demo-md
## WHEN
- apply manifests/pending-pods.yaml
## EXPECT
[pending]
- pods/scale-test-* ×4 created
[scale] then
- machinedeployments/demo-md updated
- machines/demo-md-* ×2 created-by machinesets/demo-md-*
[nodes] after scale
- nodes/demo-md-* ×2 created
## THROUGHOUT
- machinesets/demo-md-* ×0 created          # a scale, not a rollout
## NEVER
- elsewhere changed except pods/scale-test-*, nodes/*, leases/*, events/*
- machines/demo-cp-* changed
## SETTLE
- quiescence 20s on the subtree
- timeout 10m
```

What a poller asserts: "eventually there are two more nodes". What this
asserts additionally: the MachineSet created exactly two Machines **after**
the MachineDeployment changed, the nodes registered **after** that, no
MachineSet was created (scale is not rollout), nothing was deleted, and the
control plane and everything outside the MachineDeployment's ownership
subtree stayed untouched — each claim cited with the event id and revision
that decided it. The plan grammar is in [PLAN-FORMAT.md](PLAN-FORMAT.md).

## Why a timeline beats polling

| | apply-and-poll | kourt |
|---|---|---|
| Asserts | presence of an end state | the complete, ordered changeset |
| Side effects | invisible | `elsewhere changed` fails on any write outside the ownership subtree |
| Order / causality | not observable | hard edges over revisions (`then`, `after`, `finally`, `concurrent with`); ownership via `created-by` |
| Counts | "at least one" | exact (`×2`), bounds (`×1+`), absence (`×0`) |
| Settling | a timeout guess | quiescence on the subtree; timeout is a failure, not a flake |
| Re-judging | re-run the cluster | `kourt replay` on a recorded window, offline |

## Architecture: one repo, two faces

```
                 plan.md                      k3s-on-beads substrate
                    │                   ┌───────────────────────────────┐
   ┌────────────────▼──────────────┐    │ k3s apiserver ─► kine-beads   │
   │  kourt (Go binary)         │    │                   │ (beads lib)│
   │  ├ plan parser                │    │           dolt ◄──┤            │
   │  ├ k8s: client-go SSA apply ──┼────┼─► objects  ▲      │            │
   │  ├ beads /v0 http client ─────┼────┼────────────┴── bd serve /v0    │
   │  ├ timeline capture (A,B]     │    └───────────────────────────────┘
   │  ├ GATE: order/counts/subtree │
   │  └ exit 0/1/2, JUnit          │
   └───────────────┬───────────────┘
                   │ run.json + verdict.json
   ┌───────────────▼───────────────┐
   │  pack/ (Gas City)             │   gc sling --formula mol-kourt-test --var plan=...
   │  skills: author, judge        │   → run (binary) → judge (agent reads the skill)
   │  formulas: run, judge, test   │
   │  agents: kourt-judge       │
   └───────────────────────────────┘
```

**(A) The binary** is the deterministic oracle. Self-contained and static
(no kubectl, no bd, no bash): plan inputs go through `client-go`
(server-side apply, field manager `kourt`); the timeline comes from the
beads **/v0 HTTP API** via plain `net/http`. It cross-compiles for
linux/darwin/windows × amd64/arm64.

**(B) The pack** wraps it for a city: skills your agent reads to author plans
and to judge a run as a light cone, formulas `gc sling` runs in a worker
session, and a judge agent for the narrative verdict the gate cannot give.
See [pack/README.md](pack/README.md).

### The substrate's object model, as kourt reads it

One etcd key = one bead. `external_ref`/`title` = `/registry/<resource>/<ns>/<name>`;
`issue_type` = the resource (grouped keys are typed by their API group);
`description` = `{"v": base64(protobuf), "cr": createRev, "mr": modRev, "del": bool}`;
`ownerReferences` = beads **parent-child** edges (`GET /v0/beads/dependencies/tree?root_id=<owner>&direction=up`
walks Deployment → ReplicaSet → Pods). The global monotonic **revision**
is kine's; `A..B` windows are revision windows. Values are k8s protobuf and
are **not decoded**: the gate works at the object + revision + event level.

### Timeline sources

- **journal** — `GET /v0/beads/events?since=N`, the beads events journal:
  every committed mutation in order, with the full bead after each write.
  Exact. Requires `events-journal: true` on **every writer** (kine-beads,
  bd serve), then a restart; the server answers `409 events_journal_disabled`
  otherwise.
- **snapshot** (fallback, `lossy: true` in every verdict) — the current bead
  set, reconstructing the window from each object's `cr`/`mr`. Only an
  object's create and last write are visible; an object written again after
  B hides its in-window write. `--source journal` refuses to fall back.

### The gate

Input: the parsed plan + a `Run` (events sorted by revision, owner chains).
Output: a `Verdict` of per-line checks, each `pass | fail | skip` with a
reason and cited evidence (`event id`, `rev`, `op`, `key`).

Evaluated today, fully: create/update/delete matching by `<kind>/<name-glob>`,
exact and at-least counts, the EXPECT partial order (`then`, `after X`,
`finally`, `concurrent with X`) over matched revisions, `created-by <owner>`
through the owner chain, `elsewhere changed` via SUBTREE containment,
quiescence on the subtree, revision citation.

Parsed but reported **SKIPPED** (never silently passed): field-level
matchers (`<field> old→new`, `phase A→B`, `condition X=val`, `.status.x set`)
pending protobuf decode, and provenance matchers (`by: <actor>`) pending
managedFields provenance — every event's actor is `kine` today. A plan with
skipped lines reports `PASSED structural checks; N assertion(s) SKIPPED`,
and each skipped assertion is a `<skipped>` JUnit testcase.

**Fail-closed.** An EXPECT event that never arrives fails. A tripped NEVER
fails. A SETTLE timeout fails. A plan with nothing structural to evaluate
fails (lint rejects it first). "Nothing matched" passes only when the plan
says so (`×0`).

### Exit codes (the CI contract)

| Code | Meaning |
|---|---|
| 0 | every plan passed its structural checks (SKIPPED does not fail) |
| 1 | a gate assertion failed: EXPECT unmet, THROUGHOUT violated, NEVER tripped, SETTLE timed out |
| 2 | could not run: cluster or /v0 unreachable, plan parse/lint error, bad arguments, no plans found |

`cmd/kourt/main_test.go` pins these against fixture plans (one that must
fail with a tripped NEVER and an unmet EXPECT), including by building the
binary and checking the process exit status, and checks the JUnit marks the
failure with its evidence.

## Commands

```sh
kourt info                                  # /v0 handshake, journal on/off, head revision
kourt rev                                   # current head kine revision
kourt diff <revA> <revB> [--events] [--json]   # full changeset between two revisions (B may be `head`)
kourt record --range A..B -o run.json [--plan p.md]   # capture a window for offline replay
kourt lint <plan.md|dir> ...                # machine / skipped / prose / error per line; exit 2 on error
kourt test <plan.md|dir> ... [--cleanup] [--out-dir .kourt] [--report junit --report-path f.xml]
kourt replay <plan.md> (--run run.json | --range A..B) [--report junit ...]   # re-judge, offline with --run
```

Global flags: `--beads URL` (`$KOURT_BEADS`, default `http://127.0.0.1:8099`),
`--kubeconfig PATH` (`$KUBECONFIG`), `--source auto|journal|snapshot`,
`--json`, `-q`. Flags may follow positionals.

`kourt test` per plan: apply GIVEN and settle; `A = head`; apply WHEN;
settle on the subtree (quiescence or timeout); `B = head`; capture `(A, B]`;
judge; write `<out-dir>/<plan>.run.json` and `.verdict.json`; `--cleanup`
applies CLEANUP. Plans run sequentially; the suite exits non-zero if any
plan fails.

## Build

```sh
make build        # bin/kourt for the host (CGO_ENABLED=0, static)
make test         # unit tests incl. the exit-code contract and the pack validator
make cross        # dist/ for linux,darwin,windows × amd64,arm64
make check        # vet + test + build
goreleaser release --snapshot --clean   # same matrix via .goreleaser.yaml
```

## CI

`.github/workflows/e2e.yml` is a sample GitHub Actions job: build → lint
plans → `ci/up.sh` (dolt sql-server, kine-beads in server mode, `bd serve`,
k3s with `--datastore-endpoint` at kine) → deploy the controller →
`kourt test plans/ --report junit` → publish JUnit (`if: always()`) →
`ci/down.sh` (`if: always()`). The test step's non-zero exit is what turns
the check red. The scripts are a correct skeleton that has not yet run on a
hosted runner; the open question is whether kine-beads (beads library with
embedded Dolt linked) builds with `CGO_ENABLED=0` in server mode — on macOS
it needed `CGO_ENABLED=1 -tags gms_pure_go`.

## Repository layout

```
cmd/kourt/          the CLI (urfave/cli v2) + exit-code tests and fixtures
pkg/beads/        net/http client for the beads /v0 API
pkg/kine/         the k3s-on-beads object model (keys, {v,cr,mr,del})
pkg/timeline/     Event/Run model, journal + snapshot capture, diff, owner chains
pkg/plan/         the plan parser (PLAN-FORMAT.md)
pkg/gate/         the deterministic oracle + Suite/exit codes
pkg/report/       JUnit
pkg/k8s/          client-go server-side apply / delete
pkg/packcheck/    tests that validate pack/ (TOML, step graph, skills, doctor)
plans/examples/        capi-cluster-create, capi-autoscale-up (unit-tested), smoke-configmap, podinfo-scale
pack/                  the Gas City pack (skills, formulas, agent, orders, doctor)
ci/                    up.sh / down.sh for the e2e workflow
```

## Further reading

- [docs/agentic-judge.md](docs/agentic-judge.md) — the gc judge + remand-bead flow end to end, and the full CI workflow (boot k3s-on-beads, gate, judge, upload artifacts).
- [PLAN-FORMAT.md](PLAN-FORMAT.md) — the locked plan grammar.

## Status (2026-11-15)

- `diff`, `record`, `info`, `rev` validated against the live k3s-on-beads
  substrate (bd 1.3.4, /v0 wire revision 2) via the snapshot source; the
  journal is off on that workspace.
- `test` ran end to end against the live cluster on `plans/examples/smoke-configmap`
  (apply → subtree quiescence → capture → gate → cleanup), with JUnit.
- The gate is unit-tested against both CAPI example plans on synthetic
  timelines (pass, misordered, deleted machine, rollout-instead-of-scale).
- Field decode and provenance are the next increments; both are visible as
  SKIPPED today.
