# PLAN fixture-must-fail

## INTENT
This plan MUST fail the gate: it expects a ReplicaSet create that never
happens (unmet EXPECT), and it forbids anything outside podinfo's ownership
subtree, which the stray configmap write trips (tripped NEVER). CI relies on
this fixture exiting non-zero.

## SUBTREE
- deployments/podinfo

## EXPECT
[deploy]
- deployments/podinfo ×2 updated

[rs] then
- replicasets/podinfo-* created

## THROUGHOUT
- pods/podinfo-* ×2 created

## NEVER
- elsewhere changed
