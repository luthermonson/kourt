# kourt — the Gas City pack

Causality-first testing for Kubernetes controllers on k3s-on-beads, wrapped
for a city. The engine is the `kourt` binary (see the repo README); this
pack adds the three things a city needs around it:

| Mode | What | Entry |
|---|---|---|
| **skills/** (agent-read) | `kourt-author`: how to write a plan. `kourt-judge`: how to read a run's timeline as a light cone and write a cited verdict. | your coding agent reads them |
| **formulas/** (gc-dispatched) | `mol-kourt-run` (apply + capture + gate), `mol-kourt-judge` (offline replay + narrative judge), `mol-kourt-test` (run, then judge). Thin wrappers: mechanics in the binary, standards in the skills, state in the root-bead notes. | `gc sling --formula mol-kourt-test --var plan=plans/foo.plan.md` |
| **agents/** | `kourt-judge`: the narrative judge a formula's `judge` step is assigned to. | assigned by the formulas |
| **orders/** | `kourt-nightly` (cron, the whole `plans/` dir), `kourt-health` (exec, /v0 + journal probe). Both ship disabled. | `gc order run kourt-nightly` |
| **doctor/** | `cluster` (apiserver + binary reachable), `beads` (/v0 up, events journal on). | `gc doctor` |

Self-contained: no `[imports.*]`. Requirements are the `kourt` binary on
PATH, a kubeconfig, and `KOURT_BEADS` (default `http://127.0.0.1:8099`).

## Install

```toml
# city.toml (or a rig's pack.toml)
[imports.kourt]
source = "https://github.com/luthermonson/kourt.git//pack"   # or a local path
```

Then, in the rig that holds your `plans/` and manifests:

```sh
gc sling --formula mol-kourt-test --var plan=plans/            # run + judge everything
gc sling --formula mol-kourt-run  --var plan=plans/foo.plan.md # mechanics only
gc sling --formula mol-kourt-judge --var plan=plans/foo.plan.md --var range=4210..4388
gc formula show mol-kourt-test                                  # inspect vars/steps
```

Formula vars: `plan` (file or dir, default `plans`), `out_dir` (default
`.kourt`), `source` (`auto|journal|snapshot`), `cleanup` (`true|false`);
`mol-kourt-judge` takes `range=A..B` **or** `run_dir=<dir>` instead.

## What a run leaves behind

```
.kourt/
  <plan>.run.json       the captured window: ordered events + owner chains
  <plan>.verdict.json   the gate: pass/fail/skip per line, cited evidence
  <plan>.judgment.md    the judge's narrative (mol-kourt-test / -judge)
  junit.xml             one testcase per plan + one skipped case per SKIPPED assertion
  stdout.txt
```

Root-bead notes carry `plan:`, `exit:`, `run_dir:`, `report:`, `status:`
(`passed | failed | error`), and for judge runs `run_file:`, `verdict_file:`,
`judgment:`.

## Assumptions to verify against gc (flagged for the maintainers)

- Formulas are authored in the v2 `[[steps]]` shape with `needs`, `[vars.x]`
  tables (`default` + `required`), and `{{var}}` substitution, mirroring
  `gascity-packs/contributing-to-gascity/formulas/*.formula.toml`. No
  `[requires] formula_compiler` declaration is made (no graph-only features
  are used).
- The judge step is routed with `assignee = "kourt-judge"` on the step
  (formula-spec v2 §1.3 "Default assignee"). If a step-level assignee does
  not dispatch to a pack agent, the fallback is to sling the judge formula
  at the agent explicitly.
- Run state uses `gc bd mol current --json | jq -r .molecule_id` as the
  contributing pack does; every call is guarded so a v2 workflow without a
  molecule root degrades to printing paths on stdout.
- `$GC_PACK_DIR` resolves to this pack's directory inside a worker session
  (used to locate `skills/kourt-judge/SKILL.md`), as in the contributing
  pack's formulas.
- Orders cannot pass required vars, so `plan` defaults to `plans`.
