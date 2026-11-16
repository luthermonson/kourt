# PLAN fixture-no-asserts

## INTENT
A plan that asserts nothing machine-checkable. Fail-closed: lint rejects it
(exit 2) and the gate fails it, so an empty plan can never go green.

## EXPECT
- the pods become Ready (prose only)
