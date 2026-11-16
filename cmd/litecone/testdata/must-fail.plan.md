# PLAN fixture-must-fail

## INTENT
This plan MUST fail the gate: it expects a ReplicaSet that is never created
(unmet EXPECT) and forbids anything outside podinfo's ownership subtree,
which the stray configmap write trips (tripped NEVER). CI relies on this
fixture exiting non-zero.

## EXPECT
1. update deployments/default/podinfo
2. create replicasets/default/podinfo-*

## THROUGHOUT
- count create pods/default/podinfo-* == 2

## NEVER
- outside deployments/default/podinfo
