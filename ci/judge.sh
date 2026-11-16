#!/usr/bin/env bash
# ci/judge.sh — run the kourt-judge skill over each verdict in a run dir, via a
# single Anthropic API call per plan.
#
# This is the CI-appropriate shape of the judge: stateless, one call, reading
# only the committed run (run.json) and the gate's verdict (verdict.json). It
# never re-runs the gate and never changes pass/fail — the gate's exit code
# already decided that. This step only writes the narrative verdict.
# (The full `gc` orchestrator is the interactive/local driver of the same
# kourt-judge skill; in CI one stateless API call is the right tool.)
#
# Needs ANTHROPIC_API_KEY. If it is unset the script NO-OPS (exit 0), so the
# gate pipeline runs entirely tokenless and only this optional narration is
# skipped. Model defaults to the cheapest tier; override with KOURT_JUDGE_MODEL.
set -euo pipefail

RUN_DIR="${1:-.kourt}"
MODEL="${KOURT_JUDGE_MODEL:-claude-haiku-5-5}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SKILL="$HERE/../pack/skills/kourt-judge/SKILL.md"

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "[judge] ANTHROPIC_API_KEY not set — skipping narrative judge; the gate result stands."
  exit 0
fi
command -v jq   >/dev/null || { echo "[judge] jq is required"; exit 0; }
command -v curl >/dev/null || { echo "[judge] curl is required"; exit 0; }
[ -f "$SKILL" ] || { echo "[judge] kourt-judge skill not found at $SKILL"; exit 0; }

shopt -s nullglob
verdicts=("$RUN_DIR"/*.verdict.json)
if [ ${#verdicts[@]} -eq 0 ]; then
  echo "[judge] no *.verdict.json in $RUN_DIR — nothing to judge."
  exit 0
fi

rc=0
for verdict in "${verdicts[@]}"; do
  name="$(basename "$verdict" .verdict.json)"
  run="$RUN_DIR/$name.run.json"
  out="$RUN_DIR/$name.judgment.md"
  [ -f "$run" ] || { echo "[judge] $name: no run file, skipping"; continue; }
  echo "[judge] $name -> $out  (model $MODEL)"

  user="$(mktemp)"
  {
    echo "Judge this kourt run following the skill exactly. Cite \`event id rev=N\` on every"
    echo "factual claim. Never overturn the gate; never re-run it. Write only the verdict."
    echo
    echo "## verdict.json (the deterministic gate's per-line result)"
    cat "$verdict"
    echo
    echo "## run.json (the ordered, revision-stamped timeline + ownership edges)"
    cat "$run"
  } > "$user"

  payload="$(jq -n --arg model "$MODEL" --rawfile sys "$SKILL" --rawfile u "$user" \
    '{model:$model, max_tokens:4000, system:$sys, messages:[{role:"user",content:$u}]}')"
  rm -f "$user"

  resp="$(curl -sS https://api.anthropic.com/v1/messages \
    -H "x-api-key: $ANTHROPIC_API_KEY" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    -d "$payload" || true)"

  text="$(echo "$resp" | jq -r '.content[]? | select(.type=="text") | .text' 2>/dev/null)"
  if [ -n "$text" ]; then
    printf '%s\n' "$text" > "$out"
  else
    echo "[judge] $name: API returned no text; raw response saved to $out"
    echo "$resp" > "$out"
    rc=1
  fi
done
exit $rc
