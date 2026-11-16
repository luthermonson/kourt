# PLAN fixture-must-pass

## INTENT
Scaling podinfo touches the Deployment, its ReplicaSet and creates two Pods, in that order. The stray configmap write is explicitly excepted.

## EXPECT
1. update deployments/default/podinfo
2. update replicasets/default/podinfo-*
3. create pods/default/podinfo-* x2

## THROUGHOUT
- count create pods/default/podinfo-* == 2
- count delete ** == 0

## NEVER
- outside deployments/default/podinfo except configmaps/kube-system/stray
- delete pods/default/podinfo-*
