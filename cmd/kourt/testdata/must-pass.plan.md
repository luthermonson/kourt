# PLAN fixture-must-pass

## INTENT
Scaling podinfo touches the Deployment and its ReplicaSet, then creates two Pods under the ReplicaSet. The stray configmap write is explicitly excepted. Deployment status writes trail the pods, so no edge is drawn from the deployment to the pods.

## SUBTREE
- deployments/podinfo

## EXPECT
[deploy]
- deployments/podinfo ×2 updated

[rs]
- replicasets/podinfo-* updated

[pods] after rs
- pods/podinfo-* ×2 created-by replicasets/podinfo-*
- pods/podinfo-* condition Ready=True

## THROUGHOUT
- pods/podinfo-* ×2 created
- */* ×0 deleted

## NEVER
- elsewhere changed except configmaps/stray
- pods/podinfo-* deleted
