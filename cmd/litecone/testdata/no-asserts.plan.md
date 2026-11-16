# PLAN fixture-no-asserts

## INTENT
A plan with nothing structural: one prose line and one field-level
assertion the gate must report SKIPPED. Fail-closed: lint rejects it
(exit 2) and the gate fails it (exit 1), so an all-SKIPPED plan can never
go green.

## SUBTREE
- deployments/podinfo

## EXPECT
- the pods become Ready (prose only)
- pods/podinfo-* condition Ready=True
