# PLAN podinfo-scale

## INTENT
Scaling a Deployment from 1 to 3 replicas is handled entirely inside its
own ownership subtree: the deployment controller updates the ReplicaSet,
the ReplicaSet controller creates exactly two Pods owned by that ReplicaSet,
and nothing causally unrelated moves. Chainsaw would assert "3 pods exist
eventually"; this plan asserts the causal chain and that ELSEWHERE stayed
quiet. Deployment status writes trail the pod creates, so the only hard
edge drawn is ReplicaSet-before-Pods.

## SUBTREE
- deployments/kourt-podinfo

## GIVEN
- apply manifests/podinfo-1.yaml

## WHEN
- apply manifests/podinfo-3.yaml

## EXPECT
[deploy]
- deployments/kourt-podinfo ×1+ updated
- deployments/kourt-podinfo .spec.replicas 1→3

[rs]
- replicasets/kourt-podinfo-* ×1+ updated

[pods] after rs
- pods/kourt-podinfo-* ×2 created-by replicasets/kourt-podinfo-*
- pods/kourt-podinfo-* phase Pending→Running
- pods/kourt-podinfo-* condition Ready=True

[ready] finally
- deployments/kourt-podinfo .status.availableReplicas set

## THROUGHOUT
- pods/kourt-podinfo-* ×2 created
- replicasets/kourt-podinfo-* ×0 created    # a scale is not a rollout
- pods/kourt-podinfo-* ×0 deleted

## NEVER
- elsewhere changed except nodes/*, leases/*, events/*, endpointslices/*, services/*, masterleases/*, peerserverleases/*
- pods/kourt-podinfo-* deleted
- deployments/kourt-podinfo .spec.replicas 3→1

## SETTLE
- quiescence 10s on the subtree
- timeout 3m

## CLEANUP
- delete manifests/podinfo-1.yaml

## EVIDENCE
- [pods] after rs: the latest ReplicaSet update revision versus the earliest Pod create revision.
- THROUGHOUT: the create / delete counts over the window.
- NEVER elsewhere: zero offenders, or every offender listed with its revision.
