# PLAN capi-autoscale-up

## INTENT
Four pods that cannot be scheduled make cluster-autoscaler raise the
MachineDeployment's replicas; the MachineSet controller creates exactly two
new Machines, mirage provisions their MirageMachines, the two new Nodes
register, and the pods bind. The scale is a scale, not a rollout: no new
MachineSet, no Machine deleted, the control plane untouched. Pods and Nodes
live outside the MachineDeployment's ownership subtree (a Node has no
ownerReference to its Machine), so they are named as exceptions, not hidden.

## SUBTREE
- machinedeployments/demo-md

## GIVEN
(a provisioned cluster `demo` whose MachineDeployment demo-md has 1 ready replica, with cluster-autoscaler running against it)

## WHEN
- apply manifests/pending-pods.yaml

## EXPECT
[pending]
- pods/scale-test-* ×4 created
- pods/scale-test-* condition PodScheduled=False

[scale] then
- machinedeployments/demo-md .spec.replicas 1→3
- machinedeployments/demo-md updated
- machinesets/demo-md-* updated
- machines/demo-md-* ×2 created-by machinesets/demo-md-*
- miragemachines/demo-md-* ×2 created

[nodes] after scale
- nodes/demo-md-* ×2 created

[bind] after nodes
- pods/scale-test-* .spec.nodeName set
- pods/scale-test-* condition PodScheduled=True

## THROUGHOUT
- machines/demo-md-* ×2 created
- machines/demo-md-* ×0 deleted
- machinesets/demo-md-* ×0 created
- machinedeployments/demo-md ×1+ updated

## NEVER
- elsewhere changed except deployments/scale-test, replicasets/scale-test-*, pods/scale-test-*, nodes/*, leases/*, events/*, endpointslices/*, masterleases/*, peerserverleases/*
- machinedeployments/demo-md deleted
- machines/demo-cp-* changed
- kubeadmcontrolplanes/demo-cp changed
- machinedeployments/demo-md .spec.replicas 3→1

## SETTLE
- quiescence 20s on the subtree
- timeout 10m

## EVIDENCE
- [scale]: the MachineDeployment update revision, then the two Machine create revisions, each citing the MachineSet owner edge.
- [nodes] after scale: the earliest Node create revision versus the latest [scale] revision.
- THROUGHOUT: the create / delete counts over the window.
- NEVER: zero offenders outside machinedeployments/demo-md once pods and nodes are excepted.
