# The agentic judge and CI flow

kourt has two layers. The **gate** (the `kourt` binary) is the deterministic
oracle: plan + committed timeline → pass/fail, as an exit code. The **judge**
(a Gas City agent) is the narrative layer: it reads the same committed run and
explains *why*, then files a remand bead for a coding agent to pick up.

This doc walks the whole path — from one command, down to a gc agent instance
processing the markdown, to a non-zero exit and a work bead.

> **Determinism boundary.** The agent is never the oracle. Pass/fail is the
> binary's exit code; no LLM is in that path. The judge's output is advisory and
> human-reviewed, and it can never overturn a gate FAIL. See the "determinism
> boundary" discussion for why this makes the agent safe to put in the loop.

## You do not need the agent to run kourt

The gate is self-contained. In CI you can stop at the binary:

```bash
kourt test plans/ --report junit --report-path junit.xml
# exit 0 = every plan passed, 1 = a gate assertion failed, 2 = could not run
```

That is the whole CI contract: the exit code is the build result. The agentic
judge is an *addition* that explains failures and files work — not a dependency.

## One command, when you do want the judge

```bash
gc sling --formula mol-kourt-test --var plan=plans/capi-flake.plan.md
```

`gc` cooks the formula into a **molecule** — a root bead plus one bead per step —
and dispatches it to a worker session. The molecule root bead is the control
bead; each step records state in its notes. The steps run in order:

### 1. `setup` — prove the preconditions
Verifies the `kourt` binary, the `/v0` endpoint (`kourt info`), and the
kubeconfig, then `kourt lint`s the plans. Nothing touches the cluster until the
plans lint clean.

### 2. `run` — apply, capture, gate (this is where the exit code is born)
Runs the oracle exactly once:

```bash
kourt test "$PLAN" --out-dir "$OUT" --report junit --report-path "$OUT/junit.xml"
EXIT=$?        # 0 passed · 1 gate failed · 2 could not run
```

The binary parses the plan's `## SECTION` markdown, applies `WHEN` through the
apiserver (client-go server-side apply), settles on the `SUBTREE`, captures the
revision window from `/v0`, runs the gate, and writes `run.json`,
`verdict.json`, and `junit.xml`. **The binary's exit code is how non-zero is
triggered** — nothing agentic decides it. The step maps it to the root-bead
notes: `0 → status: passed`, `1 → status: failed`, `2 → status: error`. In a
GitHub job that same exit code is the job's red/green.

### 3. `judge` — the gc agent processes the run (`assignee = kourt-judge`)
gc dispatches this step to a fresh worker running as the **`kourt-judge`**
agent. The agent is *told ahead of time it is judging a test*: its standing
role is the `kourt-judge` skill (`pack/skills/kourt-judge/SKILL.md`), which says
"read the window as a light cone, follow ownership outward, judge the SKIPPED
lines, cite `event id rev=N`, never overturn the gate." Its entire context is:

- the plan's `INTENT` (prose — what the controller is accused of),
- this run's `run.json` (the ordered events + owner chains),
- this run's `verdict.json` (the gate's per-line result with evidence).

Nothing else. **No live cluster, no memory of prior runs** — the judge is a
near-pure function of `(plan, timeline)`, which is what keeps it from becoming a
flake source of its own. It writes `<plan>.judgment.md` (the diagnosis) and,
when the gate failed, **files a remand bead**:

```bash
gc bd create --title "fix: <root cause>" \
  --description "<diagnosis + cited revisions + proposed fix>" \
  --label kourt-remand
gc bd dep add <remand-id> <failing-object-bead-id> --type discovered-from
```

The bead carries the evidence and links (`discovered-from`) to the actual
failing object in the graph. A coding agent later does `bd ready`, claims it,
and can traverse straight to the broken object and its timeline. Beads making
beads — the test failure becomes tracked work in the same graph as the cluster.

### 4. `finalize` — surface paths, close the root bead
Prints the artifact paths and closes the control bead with the final status.

## In a GitHub workflow

The job boots the k8s-on-beads stack, runs the gate, runs the judge **only if
it is enabled and `if: always()`** so a red gate still produces a diagnosis, and
uploads the artifact bundle. Logs are for humans; the uploaded artifacts
(`run.json`, `verdict.json`, `junit.xml`, `judgment.md`) are the durable,
machine-readable hand-off a coding agent or the remand bead points at.

```yaml
- name: boot k3s-on-beads
  run: |
    curl -L -o kine-beads \
      https://github.com/luthermonson/kine/releases/download/beads-v0.1.0/kine-beads-linux-amd64
    chmod +x kine-beads
    # start dolt sql-server (bd), kine-beads (--listen-address tcp://0.0.0.0:2379),
    # k3s (--datastore-endpoint=http://127.0.0.1:2379), bd serve (:8099)

- name: deploy capi + mirage
  run: |
    kubectl apply -k 'https://github.com/luthermonson/cluster-api-provider-mirage/config/default'
    # for the flake suite: set MIRAGE_GHOST_NODE_RATE on the mirage deployment

- name: kourt gate
  env: { KOURT_BEADS: http://127.0.0.1:8099 }
  run: kourt test plans/ --report junit --report-path junit.xml   # exit code = job result

- name: judge (optional, explains failures)
  if: always()
  run: gc sling --formula mol-kourt-judge --var plan=plans/ --var run_dir=.kourt

- uses: actions/upload-artifact@v4
  if: always()
  with: { name: kourt-run, path: .kourt/** }
```

## Summary

| Concern | Answer |
|---|---|
| Where the markdown is processed | the **binary** parses the structural sections (`run` step); the **agent** reads only `INTENT` + the captured run (`judge` step) |
| How non-zero exit is triggered | the **binary's** gate sets exit 1; the formula/CI propagate it. The agent never decides pass/fail |
| Is the agent told it is testing | yes — the `kourt-judge` skill is its standing role, and it is handed the plan intent + the run as its work |
| Where the diagnosis goes | `judgment.md` (human), a **remand bead** linked to the failing object (coding agent), both as uploaded artifacts |
